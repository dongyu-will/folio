package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dongyu-will/folio/internal/notebook"
)

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.2.1", "0.2.1", 0},
		{"v0.2.1", "0.2.1", 0},
		{"V1.2.0", "1.2", 0},
		{"1.0.0+build.1", "1.0.0+build.99", 0},
		{"0.2.1", "0.2.2", -1},
		{"0.2.2", "0.2.10", -1},
		{"0.2.9", "0.10.0", -1},
		{"0.9.9", "1.0.0", -1},
		{"2.0.0", "1.9.9", 1},
		{"v0.3.0", "v0.2.1", 1},
		// SemVer 2.0.0 prerelease precedence chain
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-alpha.beta", "1.0.0-beta", -1},
		{"1.0.0-beta", "1.0.0-beta.2", -1},
		{"1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"1.0.0-beta.11", "1.0.0-rc.1", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0-rc.1", "1.0.0-rc.1", 0},
	}

	for _, tc := range cases {
		if got := compareSemver(tc.a, tc.b); got != tc.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := compareSemver(tc.b, tc.a); got != -tc.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d (symmetry)", tc.b, tc.a, got, -tc.want)
		}
	}
}

func TestCurrentAppVersionFallback(t *testing.T) {
	if got := currentAppVersion(); got != folioFallbackVersion {
		t.Fatalf("currentAppVersion() = %q, want %q", got, folioFallbackVersion)
	}
}

func TestCheckGitHubRelease(t *testing.T) {
	t.Run("newer release with apk and sha256sums", func(t *testing.T) {
		var gotAccept, gotUA string
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAccept = r.Header.Get("Accept")
			gotUA = r.Header.Get("User-Agent")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name":   "v0.3.0",
				"body":       "## 0.3.0\n- 新增自动更新\n",
				"html_url":   "https://github.com/dongyu-will/folio/releases/tag/v0.3.0",
				"draft":      false,
				"prerelease": false,
				"assets": []map[string]any{
					{
						"name":                 "Folio-0.3.0-android.apk",
						"size":                 123456,
						"browser_download_url": "https://github.com/dongyu-will/folio/releases/download/v0.3.0/Folio-0.3.0-android.apk",
					},
					{
						"name":                 "SHA256SUMS",
						"size":                 256,
						"browser_download_url": "https://github.com/dongyu-will/folio/releases/download/v0.3.0/SHA256SUMS",
					},
				},
			})
		}))
		defer srv.Close()

		info, err := checkGitHubRelease(context.Background(), srv.Client(), srv.URL, "0.2.1")
		if err != nil {
			t.Fatalf("checkGitHubRelease() unexpected error: %v", err)
		}
		if info == nil {
			t.Fatal("checkGitHubRelease() = nil, want updateInfo")
		}
		if gotAccept != "application/vnd.github+json" {
			t.Errorf("Accept header = %q, want application/vnd.github+json", gotAccept)
		}
		if gotUA != "Folio/0.2.1" {
			t.Errorf("User-Agent header = %q, want Folio/0.2.1", gotUA)
		}
		if info.Version != "0.3.0" {
			t.Errorf("Version = %q, want 0.3.0", info.Version)
		}
		if info.Notes != "## 0.3.0\n- 新增自动更新" {
			t.Errorf("Notes = %q", info.Notes)
		}
		if info.ReleaseURL != "https://github.com/dongyu-will/folio/releases/tag/v0.3.0" {
			t.Errorf("ReleaseURL = %q", info.ReleaseURL)
		}
		if info.APKName != "Folio-0.3.0-android.apk" || info.APKSize != 123456 {
			t.Errorf("APK metadata = (%q, %d)", info.APKName, info.APKSize)
		}
		if info.APKURL != "https://github.com/dongyu-will/folio/releases/download/v0.3.0/Folio-0.3.0-android.apk" {
			t.Errorf("APKURL = %q", info.APKURL)
		}
		if info.SHA256SumsURL != "https://github.com/dongyu-will/folio/releases/download/v0.3.0/SHA256SUMS" {
			t.Errorf("SHA256SumsURL = %q", info.SHA256SumsURL)
		}
		if info.UsingMyGo {
			t.Error("UsingMyGo = true, want false")
		}
	})

	t.Run("already up to date", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.2.1",
				"html_url": "https://github.com/dongyu-will/folio/releases/tag/v0.2.1",
			})
		}))
		defer srv.Close()

		info, err := checkGitHubRelease(context.Background(), srv.Client(), srv.URL, "0.2.1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info != nil {
			t.Fatalf("got %+v, want nil when up to date", info)
		}
	})

	t.Run("http 404 returns errNoPublishedRelease", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()

		info, err := checkGitHubRelease(context.Background(), srv.Client(), srv.URL, "0.2.1")
		if info != nil {
			t.Fatalf("got %+v, want nil", info)
		}
		if !errors.Is(err, errNoPublishedRelease) {
			t.Fatalf("err = %v, want errNoPublishedRelease", err)
		}
	})

	t.Run("non-HTTPS asset and api URLs rejected", func(t *testing.T) {
		if _, err := checkGitHubRelease(context.Background(), http.DefaultClient, "http://api.github.com/repos/dongyu-will/folio/releases/latest", "0.2.1"); err == nil {
			t.Fatal("expected error for plain http apiURL, got nil")
		}

		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.3.0",
				"html_url": "http://insecure.example/releases/v0.3.0",
				"assets": []map[string]any{
					{
						"name":                 "Folio-0.3.0.apk",
						"size":                 1024,
						"browser_download_url": "http://insecure.example/Folio-0.3.0.apk",
					},
					{
						"name":                 "SHA256SUMS",
						"size":                 100,
						"browser_download_url": "http://insecure.example/SHA256SUMS",
					},
				},
			})
		}))
		defer srv.Close()

		info, err := checkGitHubRelease(context.Background(), srv.Client(), srv.URL, "0.2.1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info == nil {
			t.Fatal("expected updateInfo for newer release")
		}
		if info.APKURL != "" || info.APKName != "" {
			t.Errorf("non-HTTPS APK asset must be rejected, got URL=%q Name=%q", info.APKURL, info.APKName)
		}
		if info.SHA256SumsURL != "" {
			t.Errorf("non-HTTPS SHA256SUMS asset must be rejected, got %q", info.SHA256SumsURL)
		}
		if info.ReleaseURL != folioReleasesURL {
			t.Errorf("non-HTTPS html_url should fall back to %q, got %q", folioReleasesURL, info.ReleaseURL)
		}
	})

	t.Run("draft and prerelease skipped", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			draft      bool
			prerelease bool
		}{
			{"draft", true, false},
			{"prerelease", false, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"tag_name":   "v0.9.0",
						"draft":      tc.draft,
						"prerelease": tc.prerelease,
					})
				}))
				defer srv.Close()

				info, err := checkGitHubRelease(context.Background(), srv.Client(), srv.URL, "0.2.1")
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if info != nil {
					t.Fatalf("got %+v, want nil for %s release", info, tc.name)
				}
			})
		}
	})

	t.Run("prefers matching arch over generic apk", func(t *testing.T) {
		archTag := runtime.GOARCH
		if archTag == "amd64" {
			archTag = "x86_64"
		}
		wantName := fmt.Sprintf("Folio-0.3.0-android-%s.apk", archTag)
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.3.0",
				"assets": []map[string]any{
					{
						"name":                 "Folio-0.3.0-android-universal.apk",
						"size":                 200,
						"browser_download_url": "https://github.com/dongyu-will/folio/releases/download/v0.3.0/Folio-0.3.0-android-universal.apk",
					},
					{
						"name":                 wantName,
						"size":                 150,
						"browser_download_url": "https://github.com/dongyu-will/folio/releases/download/v0.3.0/" + wantName,
					},
				},
			})
		}))
		defer srv.Close()

		info, err := checkGitHubRelease(context.Background(), srv.Client(), srv.URL, "0.2.1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info == nil || info.APKName != wantName {
			t.Fatalf("APKName = %v, want %q", info, wantName)
		}
	})
}

func TestParseSHA256Sums(t *testing.T) {
	const (
		hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		hashB = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
		hashC = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)
	content := strings.Join([]string{
		"# checksums",
		"",
		"not-a-valid-hash  Folio-0.3.0.apk",
		hashA + "  Folio-0.3.0-darwin.tar.gz",
		hashB + " *./Folio-0.3.0-android.apk",
		hashC + "  dist/Folio-0.3.0-arm64.apk",
	}, "\r\n")

	if got, ok := parseSHA256Sums(content, "Folio-0.3.0-darwin.tar.gz"); !ok || got != hashA {
		t.Errorf("parseSHA256Sums(darwin) = (%q, %v), want (%q, true)", got, ok, hashA)
	}
	if got, ok := parseSHA256Sums(content, "Folio-0.3.0-android.apk"); !ok || got != strings.ToLower(hashB) {
		t.Errorf("parseSHA256Sums(android) = (%q, %v), want (%q, true)", got, ok, strings.ToLower(hashB))
	}
	if got, ok := parseSHA256Sums(content, "Folio-0.3.0-arm64.apk"); !ok || got != hashC {
		t.Errorf("parseSHA256Sums(arm64 basename fallback) = (%q, %v), want (%q, true)", got, ok, hashC)
	}
	if got, ok := parseSHA256Sums(content, "Folio-0.3.0.apk"); ok || got != "" {
		t.Errorf("parseSHA256Sums(invalid hash entry) = (%q, %v), want (\"\", false)", got, ok)
	}
	if got, ok := parseSHA256Sums(content, "missing.apk"); ok || got != "" {
		t.Errorf("parseSHA256Sums(missing) = (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestDownloadAPKToFile(t *testing.T) {
	payload := bytes.Repeat([]byte("FOLIO-APK-CONTENT-"), 4096)
	sum := sha256.Sum256(payload)
	validHash := hex.EncodeToString(sum[:])

	t.Run("valid sha256 and progress callback", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.android.package-archive")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload)
		}))
		defer srv.Close()

		dir := t.TempDir()
		dest := filepath.Join(dir, "folio-0.3.0.apk")

		var calls int
		var lastDownloaded, lastTotal int64
		err := downloadAPKToFile(context.Background(), srv.Client(), srv.URL+"/folio.apk", dest, validHash, func(downloaded, total int64) {
			calls++
			if downloaded < lastDownloaded {
				t.Errorf("progress went backwards: %d < %d", downloaded, lastDownloaded)
			}
			lastDownloaded = downloaded
			lastTotal = total
		})
		if err != nil {
			t.Fatalf("downloadAPKToFile() error = %v", err)
		}
		if calls < 2 {
			t.Errorf("progress called %d times, want >= 2", calls)
		}
		if lastDownloaded != int64(len(payload)) || lastTotal != int64(len(payload)) {
			t.Errorf("final progress = (%d, %d), want (%d, %d)", lastDownloaded, lastTotal, len(payload), len(payload))
		}
		gotBytes, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("ReadFile(dest): %v", err)
		}
		if !bytes.Equal(gotBytes, payload) {
			t.Fatal("downloaded file content mismatch")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 || entries[0].Name() != "folio-0.3.0.apk" {
			t.Errorf("unexpected files in dir: %v", entries)
		}
	})

	t.Run("corrupted sha256 fails and removes temp file", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(payload)
		}))
		defer srv.Close()

		dir := t.TempDir()
		dest := filepath.Join(dir, "folio-0.3.0.apk")
		wrongHash := strings.Repeat("0", 64)

		err := downloadAPKToFile(context.Background(), srv.Client(), srv.URL+"/folio.apk", dest, wrongHash, nil)
		if err == nil {
			t.Fatal("expected SHA-256 mismatch error, got nil")
		}
		if !strings.Contains(err.Error(), "SHA-256") {
			t.Errorf("error = %q, want SHA-256 mention", err.Error())
		}
		if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
			t.Errorf("dest file should not exist after checksum failure, statErr=%v", statErr)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Errorf("temp file leaked after checksum failure: %v", entries)
		}
	})

	t.Run("non-HTTPS URL rejected", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "folio.apk")
		err := downloadAPKToFile(context.Background(), http.DefaultClient, "http://example.com/folio.apk", dest, "", nil)
		if err == nil {
			t.Fatal("expected error for non-HTTPS URL, got nil")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Errorf("unexpected files created: %v", entries)
		}
	})

	t.Run("HTTPS redirect to HTTP rejected", func(t *testing.T) {
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(payload)
		}))
		defer plain.Close()

		tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL+"/evil.apk", http.StatusFound)
		}))
		defer tlsSrv.Close()

		dir := t.TempDir()
		dest := filepath.Join(dir, "folio.apk")
		err := downloadAPKToFile(context.Background(), tlsSrv.Client(), tlsSrv.URL+"/redirect", dest, "", nil)
		if err == nil {
			t.Fatal("expected error on HTTPS->HTTP downgrade redirect, got nil")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Errorf("unexpected files created on rejected redirect: %v", entries)
		}
	})

	t.Run("enforces max APK size limit and cleans temp file", func(t *testing.T) {
		prevMax := maxUpdateAPKBytes
		maxUpdateAPKBytes = 1024
		t.Cleanup(func() { maxUpdateAPKBytes = prevMax })

		// Stream more than maxUpdateAPKBytes without setting Content-Length.
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			flusher, _ := w.(http.Flusher)
			chunk := bytes.Repeat([]byte("x"), 512)
			for i := 0; i < 4; i++ {
				_, _ = w.Write(chunk)
				if flusher != nil {
					flusher.Flush()
				}
			}
		}))
		defer srv.Close()

		dir := t.TempDir()
		dest := filepath.Join(dir, "oversized.apk")
		err := downloadAPKToFile(context.Background(), srv.Client(), srv.URL+"/big.apk", dest, "", nil)
		if err == nil {
			t.Fatal("expected oversized download error, got nil")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Errorf("temp file leaked after size-cap failure: %v", entries)
		}
	})
}

func TestInstallAndroidAPKFlow(t *testing.T) {
	apkBytes := []byte("PK\x03\x04dummy-apk-payload-for-folio-0.3.0")
	sum := sha256.Sum256(apkBytes)
	apkHash := hex.EncodeToString(sum[:])

	var apkDownloads atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/SHA256SUMS":
			_, _ = fmt.Fprintf(w, "%s  Folio-0.3.0.apk\n", apkHash)
		case "/Folio-0.3.0.apk":
			apkDownloads.Add(1)
			_, _ = w.Write(apkBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	// Seed a stale APK that should be cleaned up.
	staleAPK := filepath.Join(cacheDir, "Folio-0.2.0.apk")
	if err := os.WriteFile(staleAPK, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	info := &updateInfo{
		Version:       "0.3.0",
		APKName:       "../../Folio-0.3.0.apk", // unsafe path should be sanitized to basename
		APKURL:        srv.URL + "/Folio-0.3.0.apk",
		SHA256SumsURL: srv.URL + "/SHA256SUMS",
	}

	getDir := func(context.Context) (string, error) { return cacheDir, nil }

	var installedPath string
	res, err := installAndroidAPK(
		context.Background(),
		srv.Client(),
		info,
		getDir,
		func(_ context.Context, p string) (updateInstallResult, error) {
			installedPath = p
			return updateInstallResult{PermissionRequired: true}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("first installAndroidAPK() error = %v", err)
	}
	if !res.PermissionRequired {
		t.Fatal("expected PermissionRequired == true")
	}
	wantPath := filepath.Join(cacheDir, "Folio-0.3.0.apk")
	if installedPath != wantPath {
		t.Fatalf("installer path = %q, want %q", installedPath, wantPath)
	}
	if _, err := os.Stat(staleAPK); !os.IsNotExist(err) {
		t.Fatalf("stale APK should have been cleaned up, stat err = %v", err)
	}
	if apkDownloads.Load() != 1 {
		t.Fatalf("apkDownloads = %d, want 1", apkDownloads.Load())
	}

	// Second attempt after user grants permission reuses the verified cached APK.
	var progressCalled bool
	res2, err := installAndroidAPK(
		context.Background(),
		srv.Client(),
		info,
		getDir,
		func(_ context.Context, p string) (updateInstallResult, error) {
			if p != wantPath {
				t.Errorf("second installer path = %q, want %q", p, wantPath)
			}
			return updateInstallResult{}, nil
		},
		func(downloaded, total int64) {
			progressCalled = downloaded == int64(len(apkBytes)) && total == int64(len(apkBytes))
		},
	)
	if err != nil {
		t.Fatalf("second installAndroidAPK() error = %v", err)
	}
	if res2.PermissionRequired || res2.RelaunchRequired || res2.FallbackURL != "" {
		t.Fatalf("unexpected second install result: %+v", res2)
	}
	if apkDownloads.Load() != 1 {
		t.Fatalf("expected verified cached APK reuse without re-downloading, downloads=%d", apkDownloads.Load())
	}
	if !progressCalled {
		t.Error("expected progress callback on cached APK reuse")
	}
}

func TestInstallAppUpdateFallback(t *testing.T) {
	res, err := installAppUpdate(context.Background(), &updateInfo{
		Version:    "0.3.0",
		ReleaseURL: "https://github.com/dongyu-will/folio/releases/tag/v0.3.0",
	}, nil)
	if err != nil {
		t.Fatalf("installAppUpdate() error = %v", err)
	}
	if res.FallbackURL != "https://github.com/dongyu-will/folio/releases/tag/v0.3.0" {
		t.Fatalf("FallbackURL = %q", res.FallbackURL)
	}

	res2, err := installAppUpdate(context.Background(), &updateInfo{
		Version:    "0.3.0",
		ReleaseURL: "http://evil.example/release",
	}, nil)
	if err != nil {
		t.Fatalf("installAppUpdate() error = %v", err)
	}
	if res2.FallbackURL != folioReleasesURL {
		t.Fatalf("FallbackURL for non-HTTPS ReleaseURL = %q, want %q", res2.FallbackURL, folioReleasesURL)
	}
}

func TestLoadCertPoolWithExtraDir(t *testing.T) {
	if pool := loadCertPoolWithExtraDir(filepath.Join(t.TempDir(), "missing")); pool != nil {
		t.Fatal("expected nil pool for missing cert dir")
	}

	emptyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(emptyDir, "junk.txt"), []byte("not a cert"), 0o644); err != nil {
		t.Fatal(err)
	}
	if pool := loadCertPoolWithExtraDir(emptyDir); pool != nil {
		t.Fatal("expected nil pool when no valid certificates exist")
	}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: srv.Certificate().Raw,
	})
	pemDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pemDir, "9a5ba575.0"), append([]byte("Android Conscrypt PEM\n"), pemBytes...), 0o644); err != nil {
		t.Fatal(err)
	}

	pool := loadCertPoolWithExtraDir(pemDir)
	if pool == nil {
		t.Fatal("expected non-nil cert pool for valid PEM cert dir")
	}
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    pool,
			},
		},
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("TLS request using loaded extra CA pool failed: %v", err)
	}
	_ = resp.Body.Close()

	derDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(derDir, "root.der"), srv.Certificate().Raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if derPool := loadCertPoolWithExtraDir(derDir); derPool == nil {
		t.Fatal("expected non-nil cert pool for valid DER cert dir")
	}
}

func TestAndroidProxyFunc(t *testing.T) {
	for _, k := range proxyEnvVars {
		t.Setenv(k, "")
	}

	t.Run("consults native proxy when base returns nil", func(t *testing.T) {
		var nativeCalls atomic.Int32
		proxyFn := newAndroidProxyFunc(
			func(*http.Request) (*url.URL, error) { return nil, nil },
			func(context.Context) (*url.URL, error) {
				nativeCalls.Add(1)
				return parseProxyURL("http://127.0.0.1:7890")
			},
		)

		req, _ := http.NewRequest(http.MethodGet, "https://github.com/dongyu-will/folio", nil)
		got, err := proxyFn(req)
		if err != nil {
			t.Fatalf("proxyFn() error = %v", err)
		}
		if got == nil || got.String() != "http://127.0.0.1:7890" {
			t.Fatalf("proxyFn() = %v, want http://127.0.0.1:7890", got)
		}
		if nativeCalls.Load() != 1 {
			t.Fatalf("nativeCalls = %d, want 1", nativeCalls.Load())
		}

		// Loopback target must bypass native proxy.
		localReq, _ := http.NewRequest(http.MethodGet, "https://127.0.0.1:8443/update-android.json", nil)
		localProxy, err := proxyFn(localReq)
		if err != nil || localProxy != nil {
			t.Fatalf("loopback proxy = (%v, %v), want (nil, nil)", localProxy, err)
		}
		if nativeCalls.Load() != 1 {
			t.Fatalf("loopback request should not call native proxy, calls=%d", nativeCalls.Load())
		}
	})

	t.Run("supports bracketed IPv6 proxy and respects base proxy", func(t *testing.T) {
		baseURL, _ := url.Parse("http://proxy.internal:8080")
		var nativeCalls atomic.Int32
		proxyWithBase := newAndroidProxyFunc(
			func(*http.Request) (*url.URL, error) { return baseURL, nil },
			func(context.Context) (*url.URL, error) {
				nativeCalls.Add(1)
				return parseProxyURL("http://[::1]:7890")
			},
		)
		req, _ := http.NewRequest(http.MethodGet, "https://github.com", nil)
		got, err := proxyWithBase(req)
		if err != nil || got == nil || got.String() != "http://proxy.internal:8080" {
			t.Fatalf("proxyWithBase() = (%v, %v)", got, err)
		}
		if nativeCalls.Load() != 0 {
			t.Fatalf("native proxy should not run when base proxy matched, calls=%d", nativeCalls.Load())
		}

		ipv6Proxy, err := parseProxyURL("http://[::1]:7890")
		if err != nil || ipv6Proxy == nil || ipv6Proxy.Host != "[::1]:7890" {
			t.Fatalf("parseProxyURL(ipv6) = (%v, %v)", ipv6Proxy, err)
		}
		if p, err := parseProxyURL(""); err != nil || p != nil {
			t.Fatalf("parseProxyURL(empty) = (%v, %v), want (nil, nil)", p, err)
		}
	})

	t.Run("skips native proxy when explicit NO_PROXY env is set", func(t *testing.T) {
		t.Setenv("NO_PROXY", "*")
		var nativeCalls atomic.Int32
		proxyFn := newAndroidProxyFunc(
			func(*http.Request) (*url.URL, error) { return nil, nil },
			func(context.Context) (*url.URL, error) {
				nativeCalls.Add(1)
				return parseProxyURL("http://127.0.0.1:7890")
			},
		)
		req, _ := http.NewRequest(http.MethodGet, "https://github.com", nil)
		got, err := proxyFn(req)
		if err != nil || got != nil {
			t.Fatalf("proxyFn() with NO_PROXY = (%v, %v), want (nil, nil)", got, err)
		}
		if nativeCalls.Load() != 0 {
			t.Fatalf("expected 0 nativeCalls when NO_PROXY set, got %d", nativeCalls.Load())
		}
	})

	t.Run("caches proxy lookup within TTL and fails open on error", func(t *testing.T) {
		var calls atomic.Int32
		lookup := newCachedProxyLookup(50*time.Millisecond, func(context.Context) (*url.URL, error) {
			calls.Add(1)
			return parseProxyURL("http://127.0.0.1:1080")
		})
		for i := 0; i < 3; i++ {
			u, err := lookup(context.Background())
			if err != nil || u == nil || u.String() != "http://127.0.0.1:1080" {
				t.Fatalf("call %d: (%v, %v)", i, u, err)
			}
		}
		if calls.Load() != 1 {
			t.Fatalf("cached lookup calls = %d, want 1", calls.Load())
		}

		failOpen := newAndroidProxyFunc(nil, func(context.Context) (*url.URL, error) {
			return nil, errors.New("native unavailable")
		})
		req, _ := http.NewRequest(http.MethodGet, "https://github.com", nil)
		if got, err := failOpen(req); err != nil || got != nil {
			t.Fatalf("failOpen = (%v, %v), want (nil, nil)", got, err)
		}
	})
}

func TestScoreAPKAsset(t *testing.T) {
	cases := []struct {
		name string
		arch string
		want int
	}{
		{"folio-0.3.0-android-arm64.apk", "arm64", 3},
		{"folio-0.3.0-android-arm64-v8a.apk", "arm64", 3},
		{"folio-0.3.0-android-aarch64.apk", "arm64", 3},
		{"folio-0.3.0-android-armeabi-v7a.apk", "arm", 3},
		{"folio-0.3.0-android-armv7.apk", "arm", 3},
		{"folio-0.3.0-android-x86_64.apk", "amd64", 3},
		{"folio-0.3.0-android-amd64.apk", "amd64", 3},
		{"folio-0.3.0-android-x86.apk", "386", 3},
		{"folio-0.3.0-android-386.apk", "386", 3},
		{"folio-0.3.0-android-universal.apk", "arm64", 2},
		{"folio-0.3.0-android.apk", "arm64", 2},
		{"folio-0.386.0-android.apk", "arm64", 2},
		// Explicitly mismatched CPU architectures must be rejected (-1).
		{"folio-0.3.0-android-x86_64.apk", "arm64", -1},
		{"folio-0.3.0-android-amd64.apk", "arm64", -1},
		{"folio-0.3.0-android-armeabi-v7a.apk", "arm64", -1},
		{"folio-0.3.0-android-armv7.apk", "arm64", -1},
		{"folio-0.3.0-android-x86.apk", "arm64", -1},
		{"folio-0.3.0-android-arm64.apk", "amd64", -1},
		{"folio-0.3.0-android-arm64.apk", "arm", -1},
	}
	for _, tc := range cases {
		if got := scoreAPKAsset(tc.name, tc.arch); got != tc.want {
			t.Errorf("scoreAPKAsset(%q, %q) = %d, want %d", tc.name, tc.arch, got, tc.want)
		}
	}

	// selectReleaseAssets must reject when only mismatched-ABI APKs exist.
	apk, _ := selectReleaseAssets([]githubReleaseAsset{
		{
			Name:               "folio-0.3.0-android-x86_64.apk",
			Size:               1000,
			BrowserDownloadURL: "https://github.com/dongyu-will/folio/releases/download/v0.3.0/folio-0.3.0-android-x86_64.apk",
		},
		{
			Name:               "folio-0.3.0-android-armeabi-v7a.apk",
			Size:               900,
			BrowserDownloadURL: "https://github.com/dongyu-will/folio/releases/download/v0.3.0/folio-0.3.0-android-armeabi-v7a.apk",
		},
	}, "arm64")
	if apk != nil {
		t.Fatalf("selectReleaseAssets() selected mismatched ABI %+v, want nil", apk)
	}
}

func TestCheckAndroidManifestAndFallback(t *testing.T) {
	prevBackoff := updateMetadataRetryBackoff
	updateMetadataRetryBackoff = 5 * time.Millisecond
	t.Cleanup(func() { updateMetadataRetryBackoff = prevBackoff })

	const (
		arm64Hash = "1111111111111111111111111111111111111111111111111111111111111111"
		univHash  = "2222222222222222222222222222222222222222222222222222222222222222"
		topHash   = "3333333333333333333333333333333333333333333333333333333333333333"
	)

	t.Run("selects exact arch from apks without touching GitHub API", func(t *testing.T) {
		var manifestHits, apiHits atomic.Int32
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/update-android.json":
				manifestHits.Add(1)
				_ = json.NewEncoder(w).Encode(androidManifest{
					Version:    "0.3.0",
					Notes:      "  Android 0.3.0 release  ",
					ReleaseURL: "https://github.com/dongyu-will/folio/releases/tag/v0.3.0",
					APKURL:     "https://github.com/dongyu-will/folio/releases/download/v0.3.0/folio-0.3.0-top.apk",
					APKName:    "folio-0.3.0-top.apk",
					APKSize:    500,
					SHA256:     topHash,
					APKs: map[string]androidManifestAPK{
						"arm64": {
							URL:    "https://github.com/dongyu-will/folio/releases/download/v0.3.0/folio-0.3.0-android-arm64.apk",
							Name:   "folio-0.3.0-android-arm64.apk",
							Size:   20987787,
							SHA256: strings.ToUpper(arm64Hash),
						},
						"universal": {
							URL:    "https://github.com/dongyu-will/folio/releases/download/v0.3.0/folio-0.3.0-android-universal.apk",
							Name:   "folio-0.3.0-android-universal.apk",
							Size:   60107932,
							SHA256: univHash,
						},
					},
				})
			case "/releases/latest":
				apiHits.Add(1)
				http.Error(w, "should not be called", http.StatusInternalServerError)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		info, err := checkAndroidUpdate(context.Background(), srv.Client(), srv.URL+"/update-android.json", srv.URL+"/releases/latest", "0.2.1", "arm64")
		if err != nil {
			t.Fatalf("checkAndroidUpdate() error = %v", err)
		}
		if info == nil {
			t.Fatal("checkAndroidUpdate() = nil, want updateInfo")
		}
		if manifestHits.Load() != 1 || apiHits.Load() != 0 {
			t.Fatalf("hits manifest=%d api=%d, want 1 and 0", manifestHits.Load(), apiHits.Load())
		}
		if info.Version != "0.3.0" || info.Notes != "Android 0.3.0 release" {
			t.Errorf("version/notes = (%q, %q)", info.Version, info.Notes)
		}
		if info.APKName != "folio-0.3.0-android-arm64.apk" || info.APKSize != 20987787 || info.SHA256 != arm64Hash {
			t.Errorf("arm64 selection = (%q, %d, %q)", info.APKName, info.APKSize, info.SHA256)
		}

		// Unknown arch falls back to "universal" inside apks.
		infoUniv, err := checkAndroidUpdate(context.Background(), srv.Client(), srv.URL+"/update-android.json", srv.URL+"/releases/latest", "0.2.1", "amd64")
		if err != nil || infoUniv == nil {
			t.Fatalf("universal fallback = (%+v, %v)", infoUniv, err)
		}
		if infoUniv.APKName != "folio-0.3.0-android-universal.apk" || infoUniv.SHA256 != univHash {
			t.Errorf("universal selection = (%q, %q)", infoUniv.APKName, infoUniv.SHA256)
		}

		// Already up-to-date returns (nil, nil) without calling GitHub API.
		infoSame, err := checkAndroidUpdate(context.Background(), srv.Client(), srv.URL+"/update-android.json", srv.URL+"/releases/latest", "0.3.0", "arm64")
		if err != nil || infoSame != nil {
			t.Fatalf("up-to-date = (%+v, %v), want (nil, nil)", infoSame, err)
		}
		if apiHits.Load() != 0 {
			t.Fatalf("GitHub API should not be called when manifest succeeds, apiHits=%d", apiHits.Load())
		}
	})

	t.Run("top-level apkUrl fallback and HTTPS enforcement", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/top.json":
				_ = json.NewEncoder(w).Encode(androidManifest{
					Version:    "0.3.0",
					ReleaseURL: "http://insecure.example/tag",
					APKURL:     "https://github.com/dongyu-will/folio/releases/download/v0.3.0/folio-0.3.0-android-arm64.apk",
					APKName:    "folio-0.3.0-android-arm64.apk",
					APKSize:    1234,
					SHA256:     topHash,
				})
			case "/insecure.json":
				_ = json.NewEncoder(w).Encode(androidManifest{
					Version: "0.3.0",
					APKURL:  "http://insecure.example/folio-0.3.0.apk",
					APKs: map[string]androidManifestAPK{
						"arm64": {URL: "http://insecure.example/folio-arm64.apk", Name: "folio-arm64.apk"},
					},
				})
			case "/badsha.json":
				_ = json.NewEncoder(w).Encode(androidManifest{
					Version: "0.3.0",
					APKURL:  "https://github.com/dongyu-will/folio/releases/download/v0.3.0/folio-0.3.0.apk",
					SHA256:  "not-a-sha256",
				})
			}
		}))
		defer srv.Close()

		info, err := checkAndroidManifest(context.Background(), srv.Client(), srv.URL+"/top.json", "0.2.1", "arm64")
		if err != nil || info == nil {
			t.Fatalf("top.json = (%+v, %v)", info, err)
		}
		if info.ReleaseURL != folioReleasesURL {
			t.Errorf("non-HTTPS releaseUrl should fall back to %q, got %q", folioReleasesURL, info.ReleaseURL)
		}
		if info.APKName != "folio-0.3.0-android-arm64.apk" || info.SHA256 != topHash {
			t.Errorf("top-level APK = (%q, %q)", info.APKName, info.SHA256)
		}

		// Top-level arm64 APK must be rejected on amd64.
		infoMismatch, err := checkAndroidManifest(context.Background(), srv.Client(), srv.URL+"/top.json", "0.2.1", "amd64")
		if err != nil || infoMismatch == nil {
			t.Fatalf("top.json mismatch = (%+v, %v)", infoMismatch, err)
		}
		if infoMismatch.APKURL != "" {
			t.Errorf("expected mismatched top-level APK to be rejected, got %q", infoMismatch.APKURL)
		}

		// Non-HTTPS URLs rejected.
		infoInsecure, err := checkAndroidManifest(context.Background(), srv.Client(), srv.URL+"/insecure.json", "0.2.1", "arm64")
		if err != nil || infoInsecure == nil {
			t.Fatalf("insecure.json = (%+v, %v)", infoInsecure, err)
		}
		if infoInsecure.APKURL != "" {
			t.Errorf("expected non-HTTPS APK URLs to be rejected, got %q", infoInsecure.APKURL)
		}

		// Malformed SHA-256 rejected with error.
		if _, err := checkAndroidManifest(context.Background(), srv.Client(), srv.URL+"/badsha.json", "0.2.1", "arm64"); err == nil {
			t.Fatal("expected error for malformed SHA-256 in manifest")
		}
	})

	t.Run("404 manifest falls back to GitHub Releases API", func(t *testing.T) {
		var manifestHits, apiHits atomic.Int32
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/update-android.json":
				manifestHits.Add(1)
				http.NotFound(w, r)
			case "/releases/latest":
				apiHits.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"tag_name": "v0.3.1",
					"body":     "Fallback from GitHub API",
					"html_url": "https://github.com/dongyu-will/folio/releases/tag/v0.3.1",
					"assets": []map[string]any{
						{
							"name":                 "folio-0.3.1-android-arm64.apk",
							"size":                 4096,
							"browser_download_url": "https://github.com/dongyu-will/folio/releases/download/v0.3.1/folio-0.3.1-android-arm64.apk",
						},
					},
				})
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		info, err := checkAndroidUpdate(context.Background(), srv.Client(), srv.URL+"/update-android.json", srv.URL+"/releases/latest", "0.2.1", "arm64")
		if err != nil || info == nil {
			t.Fatalf("fallback checkAndroidUpdate() = (%+v, %v)", info, err)
		}
		if manifestHits.Load() != 1 || apiHits.Load() != 1 {
			t.Fatalf("hits manifest=%d api=%d, want 1 and 1", manifestHits.Load(), apiHits.Load())
		}
		if info.Version != "0.3.1" || info.APKName != "folio-0.3.1-android-arm64.apk" {
			t.Errorf("fallback info = %+v", info)
		}

		// Both 404 -> errNoPublishedRelease.
		_, err = checkAndroidUpdate(context.Background(), srv.Client(), srv.URL+"/missing-manifest", srv.URL+"/missing-api", "0.2.1", "arm64")
		if !errors.Is(err, errNoPublishedRelease) {
			t.Fatalf("both 404 err = %v, want errNoPublishedRelease", err)
		}
	})
}

func TestMetadataRetryOnTransientErrors(t *testing.T) {
	prevBackoff := updateMetadataRetryBackoff
	updateMetadataRetryBackoff = 5 * time.Millisecond
	t.Cleanup(func() { updateMetadataRetryBackoff = prevBackoff })

	t.Run("retries once on HTTP 503", func(t *testing.T) {
		var attempts atomic.Int32
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if attempts.Add(1) == 1 {
				http.Error(w, "temporary outage", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(androidManifest{
				Version: "0.3.0",
				APKURL:  "https://github.com/dongyu-will/folio/releases/download/v0.3.0/folio-0.3.0-android-arm64.apk",
			})
		}))
		defer srv.Close()

		info, err := checkAndroidManifest(context.Background(), srv.Client(), srv.URL, "0.2.1", "arm64")
		if err != nil || info == nil || info.Version != "0.3.0" {
			t.Fatalf("retry on 503 = (%+v, %v)", info, err)
		}
		if attempts.Load() != 2 {
			t.Fatalf("attempts = %d, want 2", attempts.Load())
		}
	})

	t.Run("retries once on transient connection drop", func(t *testing.T) {
		var attempts atomic.Int32
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if attempts.Add(1) == 1 {
				if hj, ok := w.(http.Hijacker); ok {
					conn, _, err := hj.Hijack()
					if err == nil {
						_ = conn.Close()
						return
					}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v0.3.2",
			})
		}))
		defer srv.Close()

		info, err := checkGitHubRelease(context.Background(), srv.Client(), srv.URL, "0.2.1")
		if err != nil || info == nil || info.Version != "0.3.2" {
			t.Fatalf("retry on drop = (%+v, %v)", info, err)
		}
		if attempts.Load() != 2 {
			t.Fatalf("attempts = %d, want 2", attempts.Load())
		}
	})

	t.Run("does not retry 404 or insecure redirect", func(t *testing.T) {
		var notFoundAttempts atomic.Int32
		srv404 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			notFoundAttempts.Add(1)
			http.NotFound(w, r)
		}))
		defer srv404.Close()

		_, err := checkAndroidManifest(context.Background(), srv404.Client(), srv404.URL, "0.2.1", "arm64")
		if !errors.Is(err, errNoPublishedRelease) {
			t.Fatalf("404 err = %v, want errNoPublishedRelease", err)
		}
		if notFoundAttempts.Load() != 1 {
			t.Fatalf("404 attempts = %d, want 1", notFoundAttempts.Load())
		}

		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"version":"0.9.0"}`))
		}))
		defer plain.Close()

		var redirectAttempts atomic.Int32
		srvRedir := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			redirectAttempts.Add(1)
			http.Redirect(w, r, plain.URL+"/manifest.json", http.StatusFound)
		}))
		defer srvRedir.Close()

		_, err = checkAndroidManifest(context.Background(), srvRedir.Client(), srvRedir.URL, "0.2.1", "arm64")
		if !errors.Is(err, errInsecureRedirect) {
			t.Fatalf("redirect err = %v, want errInsecureRedirect", err)
		}
		if redirectAttempts.Load() != 1 {
			t.Fatalf("insecure redirect attempts = %d, want 1", redirectAttempts.Load())
		}
	})
}

func TestDownloadAPKStallTimeoutAndCancel(t *testing.T) {
	prevStall := downloadStallTimeout
	downloadStallTimeout = 80 * time.Millisecond
	t.Cleanup(func() { downloadStallTimeout = prevStall })

	t.Run("stalled download fails cleanly and cleans temp file", func(t *testing.T) {
		releaseServer := make(chan struct{})
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte("partial-apk-chunk"))
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
			case <-releaseServer:
			}
		}))
		defer func() {
			close(releaseServer)
			srv.Close()
		}()

		dir := t.TempDir()
		dest := filepath.Join(dir, "stalled.apk")
		start := time.Now()
		err := downloadAPKToFile(context.Background(), srv.Client(), srv.URL+"/stalled.apk", dest, "", nil)
		elapsed := time.Since(start)
		if !errors.Is(err, errDownloadStalled) {
			t.Fatalf("stalled download err = %v, want errDownloadStalled", err)
		}
		if elapsed > 2*time.Second {
			t.Errorf("stall timeout took too long: %v", elapsed)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Errorf("temp file leaked after stall timeout: %v", entries)
		}
	})

	t.Run("active trickling download completes without false stall", func(t *testing.T) {
		chunks := [][]byte{
			[]byte("chunk-1-"),
			[]byte("chunk-2-"),
			[]byte("chunk-3-"),
			[]byte("chunk-4-"),
			[]byte("chunk-5"),
		}
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			flusher, _ := w.(http.Flusher)
			for _, ch := range chunks {
				_, _ = w.Write(ch)
				if flusher != nil {
					flusher.Flush()
				}
				time.Sleep(25 * time.Millisecond)
			}
		}))
		defer srv.Close()

		dir := t.TempDir()
		dest := filepath.Join(dir, "trickle.apk")
		if err := downloadAPKToFile(context.Background(), srv.Client(), srv.URL+"/trickle.apk", dest, "", nil); err != nil {
			t.Fatalf("active trickling download failed: %v", err)
		}
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "chunk-1-chunk-2-chunk-3-chunk-4-chunk-5" {
			t.Fatalf("downloaded bytes = %q", string(got))
		}
	})

	t.Run("context cancellation aborts download cleanly", func(t *testing.T) {
		started := make(chan struct{})
		releaseServer := make(chan struct{})
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte("start"))
			if flusher != nil {
				flusher.Flush()
			}
			close(started)
			select {
			case <-r.Context().Done():
			case <-releaseServer:
			}
		}))
		defer func() {
			close(releaseServer)
			srv.Close()
		}()

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-started
			cancel()
		}()

		dir := t.TempDir()
		dest := filepath.Join(dir, "canceled.apk")
		err := downloadAPKToFile(ctx, srv.Client(), srv.URL+"/canceled.apk", dest, "", nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled download err = %v, want context.Canceled", err)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Errorf("temp file leaked after cancel: %v", entries)
		}
	})
}

func TestPendingAndroidUpdateLifecycle(t *testing.T) {
	apkBytes := []byte("PK\x03\x04pending-apk-no-sha256sums-test-payload")
	sum := sha256.Sum256(apkBytes)
	apkHash := hex.EncodeToString(sum[:])

	var downloads atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		_, _ = w.Write(apkBytes)
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	getDir := func(context.Context) (string, error) { return cacheDir, nil }

	info := &updateInfo{
		Version:    "0.3.0",
		Notes:      "修复若干问题",
		ReleaseURL: "https://github.com/dongyu-will/folio/releases/tag/v0.3.0",
		APKName:    "folio-0.3.0-android-arm64.apk",
		APKURL:     srv.URL + "/folio-0.3.0-android-arm64.apk",
		// Intentionally no SHA256 or SHA256SumsURL!
	}

	// 1. First install downloads APK and persists pending.json BEFORE invoking installer.
	var pendingSeenBeforeInstaller bool
	res, err := installAndroidAPK(
		context.Background(),
		srv.Client(),
		info,
		getDir,
		func(_ context.Context, apkPath string) (updateInstallResult, error) {
			p, loadErr := loadPendingAndroidUpdate(cacheDir)
			if loadErr == nil && p != nil && p.Version == "0.3.0" && p.SHA256 == apkHash && p.Path == apkPath {
				pendingSeenBeforeInstaller = true
			}
			return updateInstallResult{PermissionRequired: true}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("first installAndroidAPK() error = %v", err)
	}
	if !res.PermissionRequired {
		t.Fatal("expected PermissionRequired == true")
	}
	if !pendingSeenBeforeInstaller {
		t.Fatal("expected pending.json with computed SHA-256 to be written BEFORE invoking installer")
	}
	if downloads.Load() != 1 {
		t.Fatalf("downloads = %d, want 1", downloads.Load())
	}

	// 2. Retrying install WITHOUT SHA256SUMS reuses verified cached APK via pending.json.
	res2, err := installAndroidAPK(
		context.Background(),
		srv.Client(),
		info,
		getDir,
		func(_ context.Context, apkPath string) (updateInstallResult, error) {
			return updateInstallResult{DeferredUntilForeground: true}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("second installAndroidAPK() error = %v", err)
	}
	if !res2.DeferredUntilForeground {
		t.Fatal("expected DeferredUntilForeground == true")
	}
	if downloads.Load() != 1 {
		t.Fatalf("expected cache reuse via pending.json without re-downloading, downloads=%d", downloads.Load())
	}

	// 3. Simulated process restart: checkPendingAndroidUpdateWith returns valid info + permission status.
	pendingInfo, canInstall := checkPendingAndroidUpdateWith(
		context.Background(),
		getDir,
		func(context.Context) (bool, error) { return true, nil },
		"0.2.1",
	)
	if pendingInfo == nil || !canInstall {
		t.Fatalf("checkPendingAndroidUpdateWith() = (%+v, %v), want non-nil info and canInstall=true", pendingInfo, canInstall)
	}
	if pendingInfo.Version != "0.3.0" || pendingInfo.SHA256 != apkHash || pendingInfo.Notes != "修复若干问题" || !pendingInfo.canDirectInstall() {
		t.Fatalf("unexpected pendingInfo = %+v", pendingInfo)
	}
	if pendingInfo.PendingPath == "" {
		t.Fatal("expected PendingPath to be set")
	}

	// 4. Offline install directly from PendingPath (APKURL cleared) works with zero network hits!
	offlineInfo := *pendingInfo
	offlineInfo.APKURL = ""
	var offlineInstalledPath string
	resOffline, err := installAndroidAPK(
		context.Background(),
		srv.Client(),
		&offlineInfo,
		getDir,
		func(_ context.Context, p string) (updateInstallResult, error) {
			offlineInstalledPath = p
			return updateInstallResult{}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("offline installAndroidAPK() error = %v", err)
	}
	if resOffline.PermissionRequired || resOffline.DeferredUntilForeground || resOffline.FallbackURL != "" {
		t.Fatalf("unexpected offline result: %+v", resOffline)
	}
	if offlineInstalledPath != pendingInfo.PendingPath {
		t.Fatalf("offline installer path = %q, want %q", offlineInstalledPath, pendingInfo.PendingPath)
	}
	if downloads.Load() != 1 {
		t.Fatalf("offline pending install must not re-download, downloads=%d", downloads.Load())
	}

	// 5. After upgrading to 0.3.0, checkPendingAndroidUpdateWith cleans up pending.json and APK.
	staleInfo, staleCanInstall := checkPendingAndroidUpdateWith(
		context.Background(),
		getDir,
		func(context.Context) (bool, error) { return true, nil },
		"0.3.0",
	)
	if staleInfo != nil || staleCanInstall {
		t.Fatalf("expected (nil, false) after upgrade, got (%+v, %v)", staleInfo, staleCanInstall)
	}
	entries, _ := os.ReadDir(cacheDir)
	if len(entries) != 0 {
		t.Fatalf("expected cacheDir to be cleaned after upgrade, remaining=%v", entries)
	}

	// 6. Corrupted APK digest triggers cleanup and returns (nil, false).
	corruptAPK := filepath.Join(cacheDir, "folio-0.4.0.apk")
	if err := os.WriteFile(corruptAPK, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := savePendingAndroidUpdate(cacheDir, pendingAndroidUpdate{
		Version: "0.4.0",
		Path:    corruptAPK,
		SHA256:  apkHash,
	}); err != nil {
		t.Fatal(err)
	}
	if gotInfo, gotCan := checkPendingAndroidUpdateWith(context.Background(), getDir, nil, "0.3.0"); gotInfo != nil || gotCan {
		t.Fatalf("corrupt pending APK should return (nil, false), got (%+v, %v)", gotInfo, gotCan)
	}
	entries, _ = os.ReadDir(cacheDir)
	if len(entries) != 0 {
		t.Fatalf("corrupt pending files should be cleaned, remaining=%v", entries)
	}

	// 7. Path traversal outside cacheDir is rejected and external file is NOT deleted.
	outsideDir := t.TempDir()
	outsideAPK := filepath.Join(outsideDir, "outside.apk")
	if err := os.WriteFile(outsideAPK, apkBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := savePendingAndroidUpdate(cacheDir, pendingAndroidUpdate{
		Version: "0.5.0",
		Path:    outsideAPK,
		SHA256:  apkHash,
	}); err != nil {
		t.Fatal(err)
	}
	if gotInfo, gotCan := checkPendingAndroidUpdateWith(context.Background(), getDir, nil, "0.3.0"); gotInfo != nil || gotCan {
		t.Fatalf("outside pending APK should return (nil, false), got (%+v, %v)", gotInfo, gotCan)
	}
	if _, err := os.Stat(outsideAPK); err != nil {
		t.Fatalf("outside file must not be deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, pendingAndroidUpdateFile)); !os.IsNotExist(err) {
		t.Fatalf("invalid pending.json should be removed, err=%v", err)
	}
}

func TestSettingsLastUpdateCheckUnixPersistence(t *testing.T) {
	store := notebook.NewStore(t.TempDir())
	want := notebook.Settings{
		Appearance:          "dark",
		LastUpdateCheckUnix: 1763560000,
	}
	if err := store.SaveSettings(want); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got, err := store.ReadSettings()
	if err != nil {
		t.Fatalf("ReadSettings: %v", err)
	}
	if got.LastUpdateCheckUnix != want.LastUpdateCheckUnix {
		t.Fatalf("LastUpdateCheckUnix = %d, want %d", got.LastUpdateCheckUnix, want.LastUpdateCheckUnix)
	}
}
