package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dongyu-will/folio/internal/androidopen"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/netproxy"
)

const (
	folioRepoURL         = "https://github.com/dongyu-will/folio"
	folioReleasesURL     = "https://github.com/dongyu-will/folio/releases"
	folioReleasesAPI     = "https://api.github.com/repos/dongyu-will/folio/releases/latest"
	folioFallbackVersion = "0.2.1"

	maxUpdateMetadataBytes   = 2 << 20   // 2 MiB cap for GitHub Releases JSON and SHA256SUMS
	defaultMaxUpdateAPKBytes = 512 << 20 // 512 MiB cap for Android APK downloads
	maxUpdateRedirects       = 10
)

// maxUpdateAPKBytes is a variable so unit tests can verify size-cap enforcement without allocating 512 MiB.
var maxUpdateAPKBytes int64 = defaultMaxUpdateAPKBytes

// errNoPublishedRelease indicates the repository has no published GitHub Release yet (HTTP 404).
var errNoPublishedRelease = errors.New("暂无已发布版本")

type updateInfo struct {
	Version       string
	Notes         string
	ReleaseURL    string
	APKURL        string
	APKName       string
	APKSize       int64
	SHA256SumsURL string
	UsingMyGo     bool
	mygoUpdate    *mygo.Update
}

func (u *updateInfo) canDirectInstall() bool {
	if u == nil {
		return false
	}
	if u.UsingMyGo {
		return true
	}
	return runtime.GOOS == "android" && u.APKURL != ""
}

type updateInstallResult struct {
	RelaunchRequired   bool
	PermissionRequired bool
	FallbackURL        string
}

var updateClientState struct {
	once   sync.Once
	client *http.Client
}

func defaultUpdateHTTPClient() *http.Client {
	updateClientState.once.Do(func() {
		transport := netproxy.Transport().Clone()
		transport.ResponseHeaderTimeout = 30 * time.Second
		updateClientState.client = &http.Client{Transport: transport}
	})
	return updateClientState.client
}

func httpsOnlyClient(base *http.Client) *http.Client {
	if base == nil {
		base = defaultUpdateHTTPClient()
	}
	cloned := *base
	prevCheck := base.CheckRedirect
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxUpdateRedirects {
			return fmt.Errorf("重定向次数过多（超过 %d 次）", maxUpdateRedirects)
		}
		if err := validateHTTPSURL(req.URL.String()); err != nil {
			return fmt.Errorf("拒绝非 HTTPS 重定向：%w", err)
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
	return checkGitHubRelease(ctx, defaultUpdateHTTPClient(), folioReleasesAPI, currentAppVersion())
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
	if err := validateHTTPSURL(apiURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", updateUserAgent(currentVer))
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := httpsOnlyClient(client).Do(req)
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

	var rel githubReleaseResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxUpdateMetadataBytes)).Decode(&rel); err != nil {
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

	apkAsset, sumsURL := selectReleaseAssets(rel.Assets, runtime.GOARCH)
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
			if score > bestAPKScore {
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

func scoreAPKAsset(lowerName, arch string) int {
	hasArm64 := strings.Contains(lowerName, "arm64") || strings.Contains(lowerName, "aarch64")
	hasArm32 := strings.Contains(lowerName, "armeabi") || strings.Contains(lowerName, "armv7")
	hasX8664 := strings.Contains(lowerName, "x86_64") || strings.Contains(lowerName, "amd64")
	hasX86 := !hasX8664 && (strings.Contains(lowerName, "x86") || strings.Contains(lowerName, "386"))
	hasArch := hasArm64 || hasArm32 || hasX8664 || hasX86

	switch arch {
	case "arm64":
		if hasArm64 {
			return 4
		}
	case "arm":
		if hasArm32 {
			return 4
		}
	case "amd64":
		if hasX8664 {
			return 4
		}
	case "386":
		if hasX86 {
			return 4
		}
	}
	if strings.Contains(lowerName, "universal") {
		return 3
	}
	if !hasArch {
		return 2
	}
	return 1
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
	if runtime.GOOS == "android" && info.APKURL != "" {
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
	if info == nil || strings.TrimSpace(info.APKURL) == "" {
		return updateInstallResult{}, errors.New("缺少 Android 安装包下载地址")
	}
	if err := validateHTTPSURL(info.APKURL); err != nil {
		return updateInstallResult{}, err
	}

	cacheDir, err := getCacheDir(ctx)
	if err != nil {
		return updateInstallResult{}, err
	}

	var expectedSHA256 string
	if sumsURL := strings.TrimSpace(info.SHA256SumsURL); sumsURL != "" {
		sumsText, err := fetchSHA256Sums(ctx, client, sumsURL)
		if err != nil {
			return updateInstallResult{}, err
		}
		if digest, ok := parseSHA256Sums(sumsText, info.APKName); ok {
			expectedSHA256 = digest
		}
	}

	apkName := sanitizeAPKFilename(info.APKName)
	destPath := filepath.Join(cacheDir, apkName)

	reused := false
	if expectedSHA256 != "" {
		if size, err := verifyFileSHA256(destPath, expectedSHA256); err == nil && size > 0 {
			reused = true
			cleanOldUpdateFiles(cacheDir, apkName)
			if progress != nil {
				progress(size, size)
			}
		}
	}
	if !reused {
		cleanOldUpdateFiles(cacheDir, "")
		if err := downloadAPKToFile(ctx, client, info.APKURL, destPath, expectedSHA256, progress); err != nil {
			return updateInstallResult{}, err
		}
	}
	return invokeInstaller(ctx, destPath)
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
	default:
		return updateInstallResult{}, fmt.Errorf("未知的安装器状态：%q", reply.Status)
	}
}

func fetchSHA256Sums(ctx context.Context, client *http.Client, sumsURL string) (string, error) {
	if err := validateHTTPSURL(sumsURL); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", updateUserAgent(currentAppVersion()))

	resp, err := httpsOnlyClient(client).Do(req)
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
		if strings.HasSuffix(lower, ".apk") || (strings.HasPrefix(name, ".folio-update-") && strings.HasSuffix(name, ".tmp")) {
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
	if err := validateHTTPSURL(apkURL); err != nil {
		return err
	}
	if strings.TrimSpace(destPath) == "" {
		return errors.New("无效的安装包保存路径")
	}
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	if expectedSHA256 != "" && !isValidSHA256Hex(expectedSHA256) {
		return errors.New("无效的 SHA-256 校验值")
	}

	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建更新目录失败：%w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apkURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", updateUserAgent(currentAppVersion()))

	resp, err := httpsOnlyClient(client).Do(req)
	if err != nil {
		return fmt.Errorf("下载安装包失败：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载安装包失败：HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxUpdateAPKBytes {
		return fmt.Errorf("安装包过大（超过 %d MiB 限制）", maxUpdateAPKBytes>>20)
	}

	tmpFile, err := os.CreateTemp(dir, ".folio-update-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败：%w", err)
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
		r:     io.LimitReader(resp.Body, maxUpdateAPKBytes+1),
		total: total,
		fn:    progress,
	}
	written, copyErr := io.Copy(io.MultiWriter(tmpFile, hasher), reader)
	closeErr := tmpFile.Close()
	if copyErr != nil {
		return fmt.Errorf("下载安装包失败：%w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("保存安装包失败：%w", closeErr)
	}
	if written == 0 {
		return errors.New("下载的安装包为空")
	}
	if written > maxUpdateAPKBytes {
		return fmt.Errorf("安装包过大（超过 %d MiB 限制）", maxUpdateAPKBytes>>20)
	}
	if total > 0 && written != total {
		return fmt.Errorf("安装包下载不完整（已下载 %d / %d 字节）", written, total)
	}
	if expectedSHA256 != "" {
		actual := hex.EncodeToString(hasher.Sum(nil))
		if actual != expectedSHA256 {
			return fmt.Errorf("安装包 SHA-256 校验失败：期望 %s，实际 %s", expectedSHA256, actual)
		}
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("设置安装包权限失败：%w", err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("保存安装包失败：%w", err)
	}
	success = true
	return nil
}
