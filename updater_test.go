package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
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
