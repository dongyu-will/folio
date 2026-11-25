package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dongyu-will/folio/internal/androidopen"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/netproxy"
)

//go:embed mygo.json
var embeddedManifestJSON []byte

var folioFallbackVersion = func() string {
	var cfg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(embeddedManifestJSON, &cfg) == nil {
		if v := normalizeVersion(cfg.Version); v != "" {
			return v
		}
	}
	return "0.2.4"
}()

const (
	folioRepoURL            = "https://github.com/dongyu-will/folio"
	folioReleasesURL        = "https://github.com/dongyu-will/folio/releases"
	folioReleasesAPI        = "https://api.github.com/repos/dongyu-will/folio/releases/latest"
	folioAndroidManifestURL = "https://github.com/dongyu-will/folio/releases/latest/download/update-android.json"

	androidConscryptCADir = "/apex/com.android.conscrypt/cacerts"
	androidSSLCertDir     = "/apex/com.android.conscrypt/cacerts:/system/etc/security/cacerts:/data/misc/keychain/certs-added"

	pendingAndroidUpdateFile = "pending.json"

	maxUpdateMetadataBytes      = 2 << 20   // 2 MiB cap for manifests, GitHub Releases JSON, and SHA256SUMS
	maxAndroidCertFileBytes     = 1 << 20   // 1 MiB cap per CA certificate file
	defaultMaxUpdateAPKBytes    = 512 << 20 // 512 MiB cap for Android APK downloads
	maxUpdateRedirects          = 10
	defaultDownloadStallTimeout = 60 * time.Second
)

// Tunable variables for unit tests so size caps, retry delays, and stall timeouts
// can be verified deterministically without long sleeps or large allocations.
var (
	maxUpdateAPKBytes          int64         = defaultMaxUpdateAPKBytes
	updateMetadataRetryBackoff time.Duration = 800 * time.Millisecond
	downloadStallTimeout       time.Duration = defaultDownloadStallTimeout
)

var (
	// errNoPublishedRelease indicates the repository has no published GitHub Release yet (HTTP 404).
	errNoPublishedRelease = errors.New("暂无已发布版本")
	errInsecureRedirect   = errors.New("拒绝非 HTTPS 重定向")
	errTooManyRedirects   = errors.New("重定向次数过多")
	errDownloadStalled    = errors.New("下载超时：长时间未收到数据，请检查网络后重试")
)

func init() {
	// Go's crypto/x509 on Android checks /system/etc/security/cacerts and
	// /data/misc/keychain/certs-added, but Android 14+ stores Mainline root
	// updates in the Conscrypt APEX directory. Set SSL_CERT_DIR before the
	// first TLS handshake when unset so system roots include Conscrypt APEX.
	if runtime.GOOS == "android" && os.Getenv("SSL_CERT_DIR") == "" {
		_ = os.Setenv("SSL_CERT_DIR", androidSSLCertDir)
	}
}

type updateInfo struct {
	Version       string
	Notes         string
	ReleaseURL    string
	APKURL        string
	APKName       string
	APKSize       int64
	SHA256        string
	SHA256SumsURL string
	PendingPath   string
	UsingMyGo     bool
	mygoUpdate    *mygo.Update
}

func (u *updateInfo) canDirectInstall() bool {
	if u == nil {
		return false
	}
	if u.UsingMyGo || u.PendingPath != "" {
		return true
	}
	return runtime.GOOS == "android" && u.APKURL != ""
}

type updateInstallResult struct {
	RelaunchRequired        bool
	PermissionRequired      bool
	DeferredUntilForeground bool
	FallbackURL             string
}

type androidManifest struct {
	Version    string                        `json:"version"`
	Notes      string                        `json:"notes"`
	ReleaseURL string                        `json:"releaseUrl"`
	APKURL     string                        `json:"apkUrl"`
	APKName    string                        `json:"apkName"`
	APKSize    int64                         `json:"apkSize"`
	SHA256     string                        `json:"sha256"`
	APKs       map[string]androidManifestAPK `json:"apks,omitempty"`
}

type androidManifestAPK struct {
	URL    string `json:"url"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type pendingAndroidUpdate struct {
	Version    string `json:"version"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	ReleaseURL string `json:"releaseUrl,omitempty"`
	Notes      string `json:"notes,omitempty"`
	APKURL     string `json:"apkUrl,omitempty"`
	APKName    string `json:"apkName,omitempty"`
	APKSize    int64  `json:"apkSize,omitempty"`
}

var updateClientState struct {
	once   sync.Once
	client *http.Client
}

func defaultUpdateHTTPClient() *http.Client {
	updateClientState.once.Do(func() {
		transport := netproxy.Transport().Clone()
		transport.ResponseHeaderTimeout = 30 * time.Second
		if runtime.GOOS == "android" {
			if pool := loadCertPoolWithExtraDir(androidConscryptCADir); pool != nil {
				if transport.TLSClientConfig == nil {
					transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
				} else {
					transport.TLSClientConfig = transport.TLSClientConfig.Clone()
				}
				transport.TLSClientConfig.RootCAs = pool
			}
			transport.Proxy = newAndroidProxyFunc(transport.Proxy, newCachedProxyLookup(5*time.Second, androidSystemProxy))
		}
		updateClientState.client = &http.Client{Transport: transport}
	})
	return updateClientState.client
}

// loadCertPoolWithExtraDir clones the system CA certificate pool and appends any
// readable PEM (or DER) certificates found in extraDir. Returns nil if extraDir
// cannot be read or contains no valid certificates so callers keep default roots.
func loadCertPoolWithExtraDir(extraDir string) *x509.CertPool {
	entries, err := os.ReadDir(extraDir)
	if err != nil || len(entries) == 0 {
		return nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	added := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		certPath := filepath.Join(extraDir, entry.Name())
		info, err := os.Stat(certPath)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxAndroidCertFileBytes {
			continue
		}
		f, err := os.Open(certPath)
		if err != nil {
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxAndroidCertFileBytes+1))
		_ = f.Close()
		if readErr != nil || len(data) == 0 || int64(len(data)) > maxAndroidCertFileBytes {
			continue
		}
		if pool.AppendCertsFromPEM(data) {
			added++
			continue
		}
		if cert, parseErr := x509.ParseCertificate(data); parseErr == nil {
			pool.AddCert(cert)
			added++
		}
	}
	if added == 0 {
		return nil
	}
	return pool
}

var proxyEnvVars = [...]string{
	"HTTP_PROXY", "http_proxy",
	"HTTPS_PROXY", "https_proxy",
	"ALL_PROXY", "all_proxy",
	"NO_PROXY", "no_proxy",
}

func hasExplicitProxyEnv() bool {
	for _, name := range proxyEnvVars {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("不支持的代理协议：%q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("无效的代理地址：%q", raw)
	}
	return u, nil
}

func newCachedProxyLookup(ttl time.Duration, fetch func(context.Context) (*url.URL, error)) func(context.Context) (*url.URL, error) {
	var (
		mu        sync.Mutex
		cached    *url.URL
		expiresAt time.Time
		hasValue  bool
	)
	return func(ctx context.Context) (*url.URL, error) {
		now := time.Now()
		mu.Lock()
		if hasValue && now.Before(expiresAt) {
			p := cached
			mu.Unlock()
			if p == nil {
				return nil, nil
			}
			cp := *p
			return &cp, nil
		}
		mu.Unlock()

		u, err := fetch(ctx)
		if err != nil {
			return nil, err
		}

		mu.Lock()
		if u != nil {
			cp := *u
			cached = &cp
		} else {
			cached = nil
		}
		expiresAt = time.Now().Add(ttl)
		hasValue = true
		mu.Unlock()

		if u == nil {
			return nil, nil
		}
		cp := *u
		return &cp, nil
	}
}

func newAndroidProxyFunc(
	baseProxy func(*http.Request) (*url.URL, error),
	nativeProxy func(context.Context) (*url.URL, error),
) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		if baseProxy != nil {
			if p, err := baseProxy(req); err != nil || p != nil {
				return p, err
			}
		}
		if req == nil || req.URL == nil || isLoopbackHost(req.URL.Hostname()) || hasExplicitProxyEnv() || nativeProxy == nil {
			return nil, nil
		}
		proxyCtx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		p, err := nativeProxy(proxyCtx)
		if err != nil || p == nil {
			return nil, nil
		}
		cp := *p
		return &cp, nil
	}
}

func androidSystemProxy(ctx context.Context) (*url.URL, error) {
	if !mygo.App.IsReady() {
		return nil, nil
	}
	var reply struct {
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if err := mygo.Native(androidopen.PluginID).Call(ctx, "systemProxy", nil, &reply); err != nil {
		return nil, err
	}
	if reply.Error != "" {
		return nil, errors.New(reply.Error)
	}
	return parseProxyURL(reply.URL)
}

func httpsOnlyClient(base *http.Client) *http.Client {
	if base == nil {
		base = defaultUpdateHTTPClient()
	}
	cloned := *base
	prevCheck := base.CheckRedirect
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxUpdateRedirects {
			return fmt.Errorf("%w（超过 %d 次）", errTooManyRedirects, maxUpdateRedirects)
		}
		if err := validateHTTPSURL(req.URL.String()); err != nil {
			return fmt.Errorf("%w：%w", errInsecureRedirect, err)
		}
		if prevCheck != nil {
			return prevCheck(req, via)
		}
		return nil
	}
	return &cloned
}

func validateHTTPSURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("更新地址为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("无效的更新地址：%w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return fmt.Errorf("更新地址必须使用 HTTPS：%s", raw)
	}
	return nil
}

func isRetryableHTTPStatus(code int) bool {
	return code == http.StatusRequestTimeout || (code >= 500 && code <= 599)
}

func isNonRetryableUpdateError(err error) bool {
	return errors.Is(err, errInsecureRedirect) || errors.Is(err, errTooManyRedirects)
}

func doUpdateMetadataRequest(ctx context.Context, client *http.Client, buildReq func() (*http.Request, error)) (*http.Response, error) {
	httpClient := httpsOnlyClient(client)
	const maxAttempts = 2
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := buildReq()
		if err != nil {
			return nil, err
		}
		resp, err := httpClient.Do(req)
		if err == nil {
			if attempt+1 < maxAttempts && isRetryableHTTPStatus(resp.StatusCode) {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
				_ = resp.Body.Close()
			} else {
				return resp, nil
			}
		} else if attempt+1 >= maxAttempts || ctx.Err() != nil || isNonRetryableUpdateError(err) {
			return nil, err
		}

		backoff := updateMetadataRetryBackoff
		if backoff > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, context.Canceled
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	return strings.TrimSpace(v)
}

func currentAppVersion() string {
	if v := normalizeVersion(mygo.App.Version()); v != "" {
		return v
	}
	return folioFallbackVersion
}

func updateUserAgent(ver string) string {
	ver = normalizeVersion(ver)
	if ver == "" {
		ver = folioFallbackVersion
	}
	var b strings.Builder
	for _, r := range ver {
		if r >= 0x21 && r <= 0x7e {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "Folio/" + folioFallbackVersion
	}
	return "Folio/" + b.String()
}

// compareSemver compares two semantic version strings (handling optional "v" prefix,
// build metadata "+...", numeric major.minor.patch, and prerelease precedence).
// Returns -1 if a < b, 0 if a == b, and +1 if a > b.
func compareSemver(a, b string) int {
	a = normalizeVersion(a)
	b = normalizeVersion(b)
	a, _, _ = strings.Cut(a, "+")
	b, _, _ = strings.Cut(b, "+")
	aCore, aPre, _ := strings.Cut(a, "-")
	bCore, bPre, _ := strings.Cut(b, "-")

	if c := compareCoreVersion(aCore, bCore); c != 0 {
		return c
	}
	return comparePrerelease(aPre, bPre)
}

func compareCoreVersion(a, b string) int {
	af := strings.Split(a, ".")
	bf := strings.Split(b, ".")
	n := max(len(af), len(bf))
	for i := 0; i < n; i++ {
		xs, ys := "0", "0"
		if i < len(af) && af[i] != "" {
			xs = af[i]
		}
		if i < len(bf) && bf[i] != "" {
			ys = bf[i]
		}
		if xs == ys {
			continue
		}
		xn, xok := parseUintStrict(xs)
		yn, yok := parseUintStrict(ys)
		switch {
		case xok && yok:
			if c := cmp.Compare(xn, yn); c != 0 {
				return c
			}
		case xok:
			return -1
		case yok:
			return 1
		default:
			if c := strings.Compare(xs, ys); c != 0 {
				return c
			}
		}
	}
	return 0
}

func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	af := strings.Split(a, ".")
	bf := strings.Split(b, ".")
	for i := 0; i < len(af) && i < len(bf); i++ {
		if af[i] == bf[i] {
			continue
		}
		xn, xok := parseUintStrict(af[i])
		yn, yok := parseUintStrict(bf[i])
		switch {
		case xok && yok:
			return cmp.Compare(xn, yn)
		case xok:
			return -1
		case yok:
			return 1
		default:
			return strings.Compare(af[i], bf[i])
		}
	}
	return cmp.Compare(len(af), len(bf))
}

func parseUintStrict(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func checkAppUpdate(ctx context.Context) (*updateInfo, error) {
	if runtime.GOOS == "darwin" && mygo.Updater.Enabled() {
		up, err := mygo.Updater.Check(ctx)
		if err == nil {
			if up == nil {
				return nil, nil
			}
			return &updateInfo{
				Version:    normalizeVersion(up.Version),
				Notes:      strings.TrimSpace(up.Notes),
				ReleaseURL: folioReleasesURL,
				UsingMyGo:  true,
				mygoUpdate: up,
			}, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Fall back to GitHub Releases API if signed manifest check fails
		// (e.g., manifest not published yet or release only has manual assets).
	}
	if runtime.GOOS == "android" {
		return checkAndroidUpdate(ctx, defaultUpdateHTTPClient(), folioAndroidManifestURL, folioReleasesAPI, currentAppVersion(), runtime.GOARCH)
	}
	return checkGitHubRelease(ctx, defaultUpdateHTTPClient(), folioReleasesAPI, currentAppVersion())
}

// checkAndroidUpdate queries the zero-quota static update-android.json manifest
// first, falling back to the GitHub Releases API if the manifest is unavailable.
func checkAndroidUpdate(ctx context.Context, client *http.Client, manifestURL, releasesAPIURL, currentVer, arch string) (*updateInfo, error) {
	info, err := checkAndroidManifest(ctx, client, manifestURL, currentVer, arch)
	if err == nil {
		return info, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return checkGitHubReleaseForArch(ctx, client, releasesAPIURL, currentVer, arch)
}

func checkAndroidManifest(ctx context.Context, client *http.Client, manifestURL, currentVer, arch string) (*updateInfo, error) {
	if err := validateHTTPSURL(manifestURL); err != nil {
		return nil, err
	}
	resp, err := doUpdateMetadataRequest(ctx, client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", updateUserAgent(currentVer))
		return req, nil
	})
	if err != nil {
		return nil, fmt.Errorf("检查更新失败：%w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errNoPublishedRelease
	default:
		return nil, fmt.Errorf("检查更新清单失败：HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpdateMetadataBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取更新清单失败：%w", err)
	}
	if int64(len(data)) > maxUpdateMetadataBytes {
		return nil, errors.New("更新清单超过最大大小限制")
	}

	var manifest androidManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("解析更新清单失败：%w", err)
	}

	remoteVer := normalizeVersion(manifest.Version)
	if remoteVer == "" {
		return nil, errors.New("更新清单缺少有效版本号")
	}
	if compareSemver(remoteVer, currentVer) <= 0 {
		return nil, nil
	}

	releaseURL := folioReleasesURL
	if validateHTTPSURL(manifest.ReleaseURL) == nil {
		releaseURL = strings.TrimSpace(manifest.ReleaseURL)
	}

	apkURL, apkName, apkSize, shaHex, err := selectAndroidManifestAPK(manifest, arch)
	if err != nil {
		return nil, err
	}
	return &updateInfo{
		Version:    remoteVer,
		Notes:      strings.TrimSpace(manifest.Notes),
		ReleaseURL: releaseURL,
		APKURL:     apkURL,
		APKName:    apkName,
		APKSize:    apkSize,
		SHA256:     shaHex,
	}, nil
}

func androidArchAliases(arch string) []string {
	switch arch {
	case "arm64":
		return []string{"arm64", "arm64-v8a", "aarch64"}
	case "arm":
		return []string{"arm", "armeabi-v7a", "armv7"}
	case "amd64":
		return []string{"amd64", "x86_64"}
	case "386":
		return []string{"386", "x86"}
	default:
		if arch == "" {
			return nil
		}
		return []string{arch}
	}
}

func selectAndroidManifestAPK(m androidManifest, arch string) (apkURL, apkName string, apkSize int64, shaHex string, err error) {
	tryEntry := func(entry androidManifestAPK, checkArchMismatch bool) (bool, error) {
		rawURL := strings.TrimSpace(entry.URL)
		if rawURL == "" || validateHTTPSURL(rawURL) != nil {
			return false, nil
		}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			if u, parseErr := url.Parse(rawURL); parseErr == nil {
				name = path.Base(u.Path)
			}
		}
		name = sanitizeAPKFilename(name)
		if checkArchMismatch && scoreAPKAsset(strings.ToLower(name), arch) < 0 {
			return false, nil
		}
		digest := strings.ToLower(strings.TrimSpace(entry.SHA256))
		if digest != "" && !isValidSHA256Hex(digest) {
			return false, errors.New("更新清单中的 SHA-256 校验值无效")
		}
		apkURL = rawURL
		apkName = name
		apkSize = entry.Size
		shaHex = digest
		return true, nil
	}

	if len(m.APKs) > 0 {
		for _, key := range androidArchAliases(arch) {
			if entry, ok := m.APKs[key]; ok {
				matched, entryErr := tryEntry(entry, false)
				if entryErr != nil {
					return "", "", 0, "", entryErr
				}
				if matched {
					return apkURL, apkName, apkSize, shaHex, nil
				}
			}
		}
		if entry, ok := m.APKs["universal"]; ok {
			matched, entryErr := tryEntry(entry, false)
			if entryErr != nil {
				return "", "", 0, "", entryErr
			}
			if matched {
				return apkURL, apkName, apkSize, shaHex, nil
			}
		}
	}

	top := androidManifestAPK{
		URL:    m.APKURL,
		Name:   m.APKName,
		Size:   m.APKSize,
		SHA256: m.SHA256,
	}
	if _, entryErr := tryEntry(top, true); entryErr != nil {
		return "", "", 0, "", entryErr
	}
	return apkURL, apkName, apkSize, shaHex, nil
}

type githubReleaseResponse struct {
	TagName    string               `json:"tag_name"`
	Name       string               `json:"name"`
	Body       string               `json:"body"`
	HTMLURL    string               `json:"html_url"`
	Draft      bool                 `json:"draft"`
	Prerelease bool                 `json:"prerelease"`
	Assets     []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func checkGitHubRelease(ctx context.Context, client *http.Client, apiURL, currentVer string) (*updateInfo, error) {
	return checkGitHubReleaseForArch(ctx, client, apiURL, currentVer, runtime.GOARCH)
}

func checkGitHubReleaseForArch(ctx context.Context, client *http.Client, apiURL, currentVer, arch string) (*updateInfo, error) {
	if err := validateHTTPSURL(apiURL); err != nil {
		return nil, err
	}
	resp, err := doUpdateMetadataRequest(ctx, client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", updateUserAgent(currentVer))
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		return req, nil
	})
	if err != nil {
		return nil, fmt.Errorf("检查更新失败：%w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errNoPublishedRelease
	case http.StatusForbidden, http.StatusTooManyRequests:
		if resp.StatusCode == http.StatusTooManyRequests || resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, fmt.Errorf("GitHub API 请求频率受限（HTTP %d），请稍后再试", resp.StatusCode)
		}
		return nil, fmt.Errorf("检查更新失败：HTTP %d", resp.StatusCode)
	default:
		return nil, fmt.Errorf("检查更新失败：HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpdateMetadataBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取版本信息失败：%w", err)
	}
	if int64(len(data)) > maxUpdateMetadataBytes {
		return nil, errors.New("版本信息超过最大大小限制")
	}

	var rel githubReleaseResponse
	if err := json.Unmarshal(data, &rel); err != nil {
		return nil, fmt.Errorf("解析版本信息失败：%w", err)
	}
	if rel.Draft || rel.Prerelease {
		return nil, nil
	}

	remoteVer := normalizeVersion(rel.TagName)
	if remoteVer == "" {
		return nil, errors.New("发布版本缺少有效版本号")
	}
	if compareSemver(remoteVer, currentVer) <= 0 {
		return nil, nil
	}

	releaseURL := folioReleasesURL
	if validateHTTPSURL(rel.HTMLURL) == nil {
		releaseURL = strings.TrimSpace(rel.HTMLURL)
	}

	apkAsset, sumsURL := selectReleaseAssets(rel.Assets, arch)
	info := &updateInfo{
		Version:       remoteVer,
		Notes:         strings.TrimSpace(rel.Body),
		ReleaseURL:    releaseURL,
		SHA256SumsURL: sumsURL,
	}
	if apkAsset != nil {
		info.APKURL = strings.TrimSpace(apkAsset.BrowserDownloadURL)
		info.APKName = strings.TrimSpace(apkAsset.Name)
		info.APKSize = apkAsset.Size
	}
	return info, nil
}

func selectReleaseAssets(assets []githubReleaseAsset, arch string) (*githubReleaseAsset, string) {
	var bestAPK *githubReleaseAsset
	bestAPKScore := -1
	var bestSumsURL string
	bestSumsScore := -1

	for i := range assets {
		a := &assets[i]
		name := strings.TrimSpace(a.Name)
		rawURL := strings.TrimSpace(a.BrowserDownloadURL)
		if name == "" || validateHTTPSURL(rawURL) != nil {
			continue
		}
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".apk") {
			score := scoreAPKAsset(lower, arch)
			if score >= 0 && score > bestAPKScore {
				bestAPK = a
				bestAPKScore = score
			}
			continue
		}
		if score := scoreChecksumsAsset(lower); score > bestSumsScore {
			bestSumsURL = rawURL
			bestSumsScore = score
		}
	}
	return bestAPK, bestSumsURL
}

func containsArchToken(lowerName, token string) bool {
	for i := 0; i < len(lowerName); {
		idx := strings.Index(lowerName[i:], token)
		if idx < 0 {
			return false
		}
		start := i + idx
		end := start + len(token)
		leftOK := start > 0 && (lowerName[start-1] == '-' || lowerName[start-1] == '_')
		rightOK := end == len(lowerName) || lowerName[end] == '.' || lowerName[end] == '-' || lowerName[end] == '_'
		if leftOK && rightOK {
			return true
		}
		i = start + 1
	}
	return false
}

// scoreAPKAsset scores an APK filename against the target Go architecture.
// Exact architecture match returns 3; universal or arch-neutral APKs return 2;
// APKs explicitly tagged for a different CPU architecture return -1 (rejected).
func scoreAPKAsset(lowerName, arch string) int {
	hasArm64 := strings.Contains(lowerName, "arm64") || strings.Contains(lowerName, "aarch64") || strings.Contains(lowerName, "armv8")
	hasArm32 := strings.Contains(lowerName, "armeabi") || strings.Contains(lowerName, "armv7") || strings.Contains(lowerName, "arm32")
	hasX8664 := strings.Contains(lowerName, "x86_64") || strings.Contains(lowerName, "x86-64") || strings.Contains(lowerName, "amd64")
	hasX86 := !hasX8664 && (strings.Contains(lowerName, "x86") || strings.Contains(lowerName, "i386") || strings.Contains(lowerName, "i686") || containsArchToken(lowerName, "386"))
	hasArch := hasArm64 || hasArm32 || hasX8664 || hasX86

	switch arch {
	case "arm64":
		if hasArm64 {
			return 3
		}
	case "arm":
		if hasArm32 {
			return 3
		}
	case "amd64":
		if hasX8664 {
			return 3
		}
	case "386":
		if hasX86 {
			return 3
		}
	}
	if hasArch {
		return -1
	}
	return 2
}

func scoreChecksumsAsset(lowerName string) int {
	switch lowerName {
	case "sha256sums":
		return 3
	case "sha256sums.txt":
		return 2
	case "checksums.txt", "sha256sum.txt":
		return 1
	default:
		return -1
	}
}

func installAppUpdate(ctx context.Context, info *updateInfo, progress func(downloaded, total int64)) (updateInstallResult, error) {
	if info == nil {
		return updateInstallResult{}, errors.New("没有可用的更新信息")
	}
	if info.UsingMyGo && info.mygoUpdate != nil {
		if err := info.mygoUpdate.Install(ctx, progress); err != nil {
			return updateInstallResult{}, fmt.Errorf("安装更新失败：%w", err)
		}
		return updateInstallResult{RelaunchRequired: true}, nil
	}
	if runtime.GOOS == "android" && (info.APKURL != "" || info.PendingPath != "") {
		return installAndroidAPK(ctx, defaultUpdateHTTPClient(), info, androidUpdateCacheDir, androidInstallAPK, progress)
	}
	fallback := strings.TrimSpace(info.ReleaseURL)
	if validateHTTPSURL(fallback) != nil {
		fallback = folioReleasesURL
	}
	return updateInstallResult{FallbackURL: fallback}, nil
}

func installAndroidAPK(
	ctx context.Context,
	client *http.Client,
	info *updateInfo,
	getCacheDir func(context.Context) (string, error),
	invokeInstaller func(context.Context, string) (updateInstallResult, error),
	progress func(downloaded, total int64),
) (updateInstallResult, error) {
	if info == nil {
		return updateInstallResult{}, errors.New("缺少 Android 安装包下载地址")
	}
	apkURL := strings.TrimSpace(info.APKURL)
	pendingHint := strings.TrimSpace(info.PendingPath)
	if apkURL == "" && pendingHint == "" {
		return updateInstallResult{}, errors.New("缺少 Android 安装包下载地址")
	}
	if apkURL != "" {
		if err := validateHTTPSURL(apkURL); err != nil {
			return updateInstallResult{}, err
		}
	}

	cacheDir, err := getCacheDir(ctx)
	if err != nil {
		return updateInstallResult{}, err
	}

	expectedSHA256 := strings.ToLower(strings.TrimSpace(info.SHA256))
	if expectedSHA256 != "" && !isValidSHA256Hex(expectedSHA256) {
		return updateInstallResult{}, errors.New("无效的 SHA-256 校验值")
	}
	if expectedSHA256 == "" {
		if sumsURL := strings.TrimSpace(info.SHA256SumsURL); sumsURL != "" {
			sumsText, err := fetchSHA256Sums(ctx, client, sumsURL)
			if err != nil {
				return updateInstallResult{}, err
			}
			if digest, ok := parseSHA256Sums(sumsText, info.APKName); ok {
				expectedSHA256 = digest
			}
		}
	}

	rawAPKName := strings.TrimSpace(info.APKName)
	if rawAPKName == "" && pendingHint != "" {
		rawAPKName = filepath.Base(pendingHint)
	}
	if rawAPKName == "" && apkURL != "" {
		if u, parseErr := url.Parse(apkURL); parseErr == nil {
			rawAPKName = path.Base(u.Path)
		}
	}
	apkName := sanitizeAPKFilename(rawAPKName)
	destPath := filepath.Join(cacheDir, apkName)

	var (
		reused       bool
		actualSHA256 string
		apkSize      int64
	)

	// 1. Try reusing an already-verified cached APK when we know the expected digest.
	if expectedSHA256 != "" {
		candidates := make([]string, 0, 2)
		if pendingHint != "" {
			candidates = append(candidates, pendingHint)
		}
		candidates = append(candidates, destPath)
		for _, cand := range candidates {
			resolved, ok := resolveAPKWithinDir(cacheDir, cand)
			if !ok {
				continue
			}
			if size, verifyErr := verifyFileSHA256(resolved, expectedSHA256); verifyErr == nil && size > 0 {
				reused = true
				destPath = resolved
				apkName = filepath.Base(resolved)
				actualSHA256 = expectedSHA256
				apkSize = size
				break
			}
		}
	}

	// 2. Even without SHA256SUMS / manifest sha256, reuse a matching APK recorded
	// in pending.json after verifying its locally-recorded SHA-256 digest.
	if !reused && expectedSHA256 == "" {
		if pending, loadErr := loadPendingAndroidUpdate(cacheDir); loadErr == nil && pending != nil && isValidSHA256Hex(pending.SHA256) {
			infoVer := normalizeVersion(info.Version)
			pendingVer := normalizeVersion(pending.Version)
			versionMatches := (infoVer != "" && pendingVer == infoVer) ||
				(infoVer == "" && pendingVer == "" && sanitizeAPKFilename(pending.APKName) == apkName)
			if versionMatches {
				if resolved, ok := resolveAPKWithinDir(cacheDir, pending.Path); ok {
					if size, verifyErr := verifyFileSHA256(resolved, pending.SHA256); verifyErr == nil && size > 0 {
						reused = true
						destPath = resolved
						apkName = filepath.Base(resolved)
						actualSHA256 = strings.ToLower(strings.TrimSpace(pending.SHA256))
						apkSize = size
					}
				}
			}
		}
	}

	if reused {
		cleanOldUpdateFiles(cacheDir, apkName)
		if progress != nil {
			progress(apkSize, apkSize)
		}
	} else {
		if apkURL == "" {
			return updateInstallResult{}, errors.New("更新安装包已失效，请重新检查更新")
		}
		clearPendingAndroidUpdateDir(cacheDir)
		digest, written, dlErr := downloadAPKToFileWithDigest(ctx, client, apkURL, destPath, expectedSHA256, progress)
		if dlErr != nil {
			return updateInstallResult{}, dlErr
		}
		actualSHA256 = digest
		apkSize = written
	}

	// Persist pending.json BEFORE invoking Android installAPK because granting
	// "Allow from this source" on Android 11-16 force-kills and restarts the app.
	releaseURL := folioReleasesURL
	if validateHTTPSURL(info.ReleaseURL) == nil {
		releaseURL = strings.TrimSpace(info.ReleaseURL)
	}
	pending := pendingAndroidUpdate{
		Version:    normalizeVersion(info.Version),
		Path:       destPath,
		SHA256:     actualSHA256,
		ReleaseURL: releaseURL,
		Notes:      strings.TrimSpace(info.Notes),
		APKURL:     apkURL,
		APKName:    apkName,
		APKSize:    apkSize,
	}
	_ = savePendingAndroidUpdate(cacheDir, pending)

	return invokeInstaller(ctx, destPath)
}

func savePendingAndroidUpdate(cacheDir string, pending pendingAndroidUpdate) error {
	if strings.TrimSpace(cacheDir) == "" {
		return errors.New("更新缓存目录为空")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(cacheDir, ".pending-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(cacheDir, pendingAndroidUpdateFile)); err != nil {
		return err
	}
	ok = true
	return nil
}

func loadPendingAndroidUpdate(cacheDir string) (*pendingAndroidUpdate, error) {
	pendingPath := filepath.Join(cacheDir, pendingAndroidUpdateFile)
	f, err := os.Open(pendingPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxUpdateMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxUpdateMetadataBytes {
		return nil, errors.New("pending.json 超过最大大小限制")
	}
	var p pendingAndroidUpdate
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func resolveAPKWithinDir(dir, candidate string) (string, bool) {
	dir = strings.TrimSpace(dir)
	candidate = strings.TrimSpace(candidate)
	if dir == "" || candidate == "" {
		return "", false
	}
	canonDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", false
	}
	canonTarget, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(canonDir, canonTarget)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	info, err := os.Stat(canonTarget)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return "", false
	}
	if !strings.HasSuffix(strings.ToLower(info.Name()), ".apk") {
		return "", false
	}
	return filepath.Join(dir, rel), true
}

func clearPendingAndroidUpdateDir(cacheDir string) {
	if strings.TrimSpace(cacheDir) == "" {
		return
	}
	_ = os.Remove(filepath.Join(cacheDir, pendingAndroidUpdateFile))
	cleanOldUpdateFiles(cacheDir, "")
}

// checkPendingAndroidUpdate checks whether an already-downloaded Android update
// APK is waiting in <updateCacheDir>/pending.json (e.g. after Android restarted
// the app when the user granted "Allow from this source"). If the pending record
// is obsolete (version <= currentAppVersion()) or corrupted, it cleans up
// pending.json and stale APK files and returns (nil, false).
func checkPendingAndroidUpdate() (*updateInfo, bool) {
	if runtime.GOOS != "android" {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return checkPendingAndroidUpdateWith(ctx, androidUpdateCacheDir, androidCanInstallAPK, currentAppVersion())
}

func checkPendingAndroidUpdateWith(
	ctx context.Context,
	getCacheDir func(context.Context) (string, error),
	canInstallFn func(context.Context) (bool, error),
	currentVer string,
) (*updateInfo, bool) {
	if getCacheDir == nil {
		return nil, false
	}
	cacheDir, err := getCacheDir(ctx)
	if err != nil || strings.TrimSpace(cacheDir) == "" {
		return nil, false
	}
	pending, err := loadPendingAndroidUpdate(cacheDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			clearPendingAndroidUpdateDir(cacheDir)
		}
		return nil, false
	}

	ver := normalizeVersion(pending.Version)
	if ver == "" || compareSemver(ver, currentVer) <= 0 {
		clearPendingAndroidUpdateDir(cacheDir)
		return nil, false
	}

	resolvedAPK, ok := resolveAPKWithinDir(cacheDir, pending.Path)
	if !ok {
		clearPendingAndroidUpdateDir(cacheDir)
		return nil, false
	}
	size, err := verifyFileSHA256(resolvedAPK, pending.SHA256)
	if err != nil || size <= 0 {
		clearPendingAndroidUpdateDir(cacheDir)
		return nil, false
	}

	canInstallNow := false
	if canInstallFn != nil {
		if allowed, permErr := canInstallFn(ctx); permErr == nil {
			canInstallNow = allowed
		}
	}

	apkName := strings.TrimSpace(pending.APKName)
	if apkName == "" {
		apkName = filepath.Base(resolvedAPK)
	}
	releaseURL := folioReleasesURL
	if validateHTTPSURL(pending.ReleaseURL) == nil {
		releaseURL = strings.TrimSpace(pending.ReleaseURL)
	}
	apkURL := ""
	if validateHTTPSURL(pending.APKURL) == nil {
		apkURL = strings.TrimSpace(pending.APKURL)
	}

	info := &updateInfo{
		Version:     ver,
		Notes:       strings.TrimSpace(pending.Notes),
		ReleaseURL:  releaseURL,
		APKURL:      apkURL,
		APKName:     apkName,
		APKSize:     size,
		SHA256:      strings.ToLower(strings.TrimSpace(pending.SHA256)),
		PendingPath: resolvedAPK,
	}
	return info, canInstallNow
}

// clearPendingAndroidUpdate removes pending.json and cached APK files from
// Android's update cache directory. It is a no-op on non-Android platforms.
func clearPendingAndroidUpdate() {
	if runtime.GOOS != "android" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cacheDir, err := androidUpdateCacheDir(ctx)
	if err != nil || strings.TrimSpace(cacheDir) == "" {
		return
	}
	clearPendingAndroidUpdateDir(cacheDir)
}

func androidUpdateCacheDir(ctx context.Context) (string, error) {
	var reply struct {
		Dir   string `json:"dir"`
		Error string `json:"error"`
	}
	if err := mygo.Native(androidopen.PluginID).Call(ctx, "updateCacheDir", nil, &reply); err != nil {
		return "", fmt.Errorf("无法获取更新缓存目录：%w", err)
	}
	if reply.Error != "" {
		return "", errors.New(reply.Error)
	}
	dir := strings.TrimSpace(reply.Dir)
	if dir == "" {
		return "", errors.New("更新缓存目录为空")
	}
	return dir, nil
}

func androidCanInstallAPK(ctx context.Context) (bool, error) {
	if !mygo.App.IsReady() {
		return false, nil
	}
	var reply struct {
		Allowed bool   `json:"allowed"`
		Error   string `json:"error"`
	}
	if err := mygo.Native(androidopen.PluginID).Call(ctx, "canInstallAPK", nil, &reply); err != nil {
		return false, err
	}
	if reply.Error != "" {
		return false, errors.New(reply.Error)
	}
	return reply.Allowed, nil
}

func androidInstallAPK(ctx context.Context, apkPath string) (updateInstallResult, error) {
	var reply struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := mygo.Native(androidopen.PluginID).Call(ctx, "installAPK", map[string]any{"path": apkPath}, &reply); err != nil {
		return updateInstallResult{}, fmt.Errorf("无法启动系统安装器：%w", err)
	}
	if reply.Error != "" {
		return updateInstallResult{}, errors.New(reply.Error)
	}
	switch reply.Status {
	case "launched":
		return updateInstallResult{}, nil
	case "permission_required":
		return updateInstallResult{PermissionRequired: true}, nil
	case "deferred_until_foreground":
		return updateInstallResult{DeferredUntilForeground: true}, nil
	default:
		return updateInstallResult{}, fmt.Errorf("未知的安装器状态：%q", reply.Status)
	}
}

func fetchSHA256Sums(ctx context.Context, client *http.Client, sumsURL string) (string, error) {
	if err := validateHTTPSURL(sumsURL); err != nil {
		return "", err
	}
	resp, err := doUpdateMetadataRequest(ctx, client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", updateUserAgent(currentAppVersion()))
		return req, nil
	})
	if err != nil {
		return "", fmt.Errorf("下载校验文件失败：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载校验文件失败：HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpdateMetadataBytes+1))
	if err != nil {
		return "", fmt.Errorf("读取校验文件失败：%w", err)
	}
	if int64(len(data)) > maxUpdateMetadataBytes {
		return "", errors.New("校验文件超过最大大小限制")
	}
	return string(data), nil
}

// parseSHA256Sums finds the lowercase 64-hex SHA-256 digest for filename in a
// standard SHA256SUMS file (supporting both two-space text and " *" binary formats).
func parseSHA256Sums(content, filename string) (string, bool) {
	target := normalizeChecksumPath(filename)
	if target == "" {
		return "", false
	}
	targetBase := path.Base(target)

	var baseMatch string
	for _, rawLine := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexAny(line, " \t")
		if idx <= 0 {
			continue
		}
		digest := strings.ToLower(line[:idx])
		if !isValidSHA256Hex(digest) {
			continue
		}
		rest := strings.TrimLeft(line[idx:], " \t")
		rest = strings.TrimPrefix(rest, "*")
		entry := normalizeChecksumPath(rest)
		if entry == "" {
			continue
		}
		if entry == target {
			return digest, true
		}
		if baseMatch == "" && path.Base(entry) == targetBase {
			baseMatch = digest
		}
	}
	if baseMatch != "" {
		return baseMatch, true
	}
	return "", false
}

func normalizeChecksumPath(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	for strings.HasPrefix(name, "./") {
		name = strings.TrimPrefix(name, "./")
	}
	return name
}

func isValidSHA256Hex(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func sanitizeAPKFilename(name string) string {
	name = strings.TrimSpace(name)
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == ".." || !strings.HasSuffix(strings.ToLower(name), ".apk") {
		return "folio-update.apk"
	}
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	cleaned := strings.TrimLeft(b.String(), ".")
	if cleaned == "" || !strings.HasSuffix(strings.ToLower(cleaned), ".apk") {
		return "folio-update.apk"
	}
	return cleaned
}

func cleanOldUpdateFiles(dir, keepName string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if keepName != "" && name == keepName {
			continue
		}
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".apk") ||
			(strings.HasPrefix(name, ".folio-update-") && strings.HasSuffix(name, ".tmp")) ||
			(strings.HasPrefix(name, ".pending-") && strings.HasSuffix(name, ".tmp")) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

func verifyFileSHA256(filePath, expectedHex string) (int64, error) {
	expectedHex = strings.ToLower(strings.TrimSpace(expectedHex))
	if !isValidSHA256Hex(expectedHex) {
		return 0, errors.New("无效的 SHA-256 校验值")
	}
	f, err := os.Open(filePath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	hasher := sha256.New()
	n, err := io.Copy(hasher, io.LimitReader(f, maxUpdateAPKBytes+1))
	if err != nil {
		return 0, err
	}
	if n > maxUpdateAPKBytes {
		return 0, errors.New("安装包超过最大大小限制")
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != expectedHex {
		return 0, fmt.Errorf("安装包 SHA-256 校验失败：期望 %s，实际 %s", expectedHex, actual)
	}
	return n, nil
}

type stallWatchReader struct {
	body    io.ReadCloser
	cancel  context.CancelFunc
	timeout time.Duration

	lastNano atomic.Int64
	stalled  atomic.Bool
	closed   atomic.Bool

	mu    sync.Mutex
	timer *time.Timer
}

func newStallWatchReader(body io.ReadCloser, cancel context.CancelFunc, timeout time.Duration) *stallWatchReader {
	if timeout <= 0 {
		timeout = defaultDownloadStallTimeout
	}
	w := &stallWatchReader{
		body:    body,
		cancel:  cancel,
		timeout: timeout,
	}
	w.lastNano.Store(time.Now().UnixNano())
	interval := timeout / 4
	if interval < 5*time.Millisecond {
		interval = 5 * time.Millisecond
	}
	w.mu.Lock()
	w.timer = time.AfterFunc(interval, w.check)
	w.mu.Unlock()
	return w
}

func (w *stallWatchReader) check() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed.Load() {
		return
	}
	elapsed := time.Since(time.Unix(0, w.lastNano.Load()))
	if elapsed >= w.timeout {
		w.stalled.Store(true)
		w.cancel()
		return
	}
	next := w.timeout - elapsed
	quarter := w.timeout / 4
	if quarter < 5*time.Millisecond {
		quarter = 5 * time.Millisecond
	}
	if next > quarter {
		next = quarter
	}
	if next < time.Millisecond {
		next = time.Millisecond
	}
	w.timer.Reset(next)
}

func (w *stallWatchReader) Read(p []byte) (int, error) {
	n, err := w.body.Read(p)
	if n > 0 {
		w.lastNano.Store(time.Now().UnixNano())
	}
	if err != nil && w.stalled.Load() {
		return n, errDownloadStalled
	}
	return n, err
}

func (w *stallWatchReader) Close() error {
	w.closed.Store(true)
	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()
	w.cancel()
	return w.body.Close()
}

type updateProgressReader struct {
	r          io.Reader
	total      int64
	downloaded int64
	fn         func(downloaded, total int64)
}

func (p *updateProgressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 {
		p.downloaded += int64(n)
		if p.fn != nil {
			p.fn(p.downloaded, p.total)
		}
	}
	return n, err
}

func downloadAPKToFile(
	ctx context.Context,
	client *http.Client,
	apkURL, destPath, expectedSHA256 string,
	progress func(downloaded, total int64),
) error {
	_, _, err := downloadAPKToFileWithDigest(ctx, client, apkURL, destPath, expectedSHA256, progress)
	return err
}

func downloadAPKToFileWithDigest(
	ctx context.Context,
	client *http.Client,
	apkURL, destPath, expectedSHA256 string,
	progress func(downloaded, total int64),
) (string, int64, error) {
	if err := validateHTTPSURL(apkURL); err != nil {
		return "", 0, err
	}
	if strings.TrimSpace(destPath) == "" {
		return "", 0, errors.New("无效的安装包保存路径")
	}
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	if expectedSHA256 != "" && !isValidSHA256Hex(expectedSHA256) {
		return "", 0, errors.New("无效的 SHA-256 校验值")
	}

	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, fmt.Errorf("创建更新目录失败：%w", err)
	}

	reqCtx, reqCancel := context.WithCancel(ctx)
	defer reqCancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, apkURL, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", updateUserAgent(currentAppVersion()))

	resp, err := httpsOnlyClient(client).Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("下载安装包失败：%w", err)
	}
	stallReader := newStallWatchReader(resp.Body, reqCancel, downloadStallTimeout)
	defer stallReader.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("下载安装包失败：HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxUpdateAPKBytes {
		return "", 0, fmt.Errorf("安装包过大（超过 %d MiB 限制）", maxUpdateAPKBytes>>20)
	}

	tmpFile, err := os.CreateTemp(dir, ".folio-update-*.tmp")
	if err != nil {
		return "", 0, fmt.Errorf("创建临时文件失败：%w", err)
	}
	tmpPath := tmpFile.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	total := resp.ContentLength
	if progress != nil {
		progress(0, total)
	}

	hasher := sha256.New()
	reader := &updateProgressReader{
		r:     io.LimitReader(stallReader, maxUpdateAPKBytes+1),
		total: total,
		fn:    progress,
	}
	written, copyErr := io.Copy(io.MultiWriter(tmpFile, hasher), reader)
	closeErr := tmpFile.Close()
	if copyErr != nil {
		return "", 0, fmt.Errorf("下载安装包失败：%w", copyErr)
	}
	if closeErr != nil {
		return "", 0, fmt.Errorf("保存安装包失败：%w", closeErr)
	}
	if written == 0 {
		return "", 0, errors.New("下载的安装包为空")
	}
	if written > maxUpdateAPKBytes {
		return "", 0, fmt.Errorf("安装包过大（超过 %d MiB 限制）", maxUpdateAPKBytes>>20)
	}
	if total > 0 && written != total {
		return "", 0, fmt.Errorf("安装包下载不完整（已下载 %d / %d 字节）", written, total)
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if expectedSHA256 != "" && actual != expectedSHA256 {
		return "", 0, fmt.Errorf("安装包 SHA-256 校验失败：期望 %s，实际 %s", expectedSHA256, actual)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return "", 0, fmt.Errorf("设置安装包权限失败：%w", err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return "", 0, fmt.Errorf("保存安装包失败：%w", err)
	}
	success = true
	return actual, written, nil
}
