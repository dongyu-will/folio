package notebook

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func seedStoreForBackupTest(t *testing.T, dir string) (*Store, Book, string, string) {
	t.Helper()
	store := NewStore(dir)

	mediaSum := sha256.Sum256([]byte("png-payload"))
	mediaFile := hex.EncodeToString(mediaSum[:]) + ".png"
	if err := os.MkdirAll(store.mediaDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.MediaPath(mediaFile), []byte("png-payload"), 0600); err != nil {
		t.Fatal(err)
	}

	docSum := sha256.Sum256([]byte("%PDF-1.4\n%fake"))
	docID := hex.EncodeToString(docSum[:])
	if err := os.MkdirAll(filepath.Join(dir, "documents"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.DocumentPath(docID), []byte("%PDF-1.4\n%fake"), 0600); err != nil {
		t.Fatal(err)
	}

	col := 2
	book := Book{
		ID:     "book-backup-1",
		Title:  "备份测试笔记本",
		Source: "test",
		Roots:  []string{"n1"},
		Documents: []Document{{
			ID:       docID,
			Title:    "讲义.pdf",
			LastPage: 2,
		}},
		Notes: []Note{{
			ID:        "n1",
			Title:     "牛顿第二定律",
			Body:      "F = ma",
			Clip:      mediaFile,
			Color:     &col,
			Tags:      []string{"物理", "重点"},
			UpdatedAt: 1700000100,
			MaskRects: []HighlightRect{{Page: 1, X: 0.1, Y: 0.2, W: 0.3, H: 0.1}},
		}},
	}
	store.mu.Lock()
	err := store.writeAllShards(Library{Books: []Book{book}}, nil)
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	if err := store.SaveSettings(Settings{
		Appearance:     "dark",
		LastBookID:     book.ID,
		WebDAVURL:      "https://dav.example.com/dav",
		WebDAVUsername: "alice",
	}); err != nil {
		t.Fatal(err)
	}

	// Write an initial checkpoint plus an uncheckpointed incremental journal stroke.
	d0 := Drawing{}
	d1 := Drawing{
		Ink: []Stroke{{
			Color:  "#ff0000",
			Width:  2.5,
			Points: []InkPoint{{X: 10, Y: 20, Pressure: 0.5}, {X: 30, Y: 40, Pressure: 0.8}},
		}},
	}
	if err := store.SaveDrawing(book.ID, d0); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDrawingChange(book.ID, d0, d1); err != nil {
		t.Fatal(err)
	}
	return store, book, mediaFile, docID
}

func TestExportAndRestoreBackupZIP(t *testing.T) {
	srcDir := t.TempDir()
	src, book, mediaFile, docID := seedStoreForBackupTest(t, srcDir)

	backupZip := filepath.Join(t.TempDir(), "folio-backup.zip")
	f, err := os.Create(backupZip)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.ExportBackupZIP(f); err != nil {
		_ = f.Close()
		t.Fatalf("ExportBackupZIP failed: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	dstDir := t.TempDir()
	dst := NewStore(dstDir)
	lib, settings, err := dst.RestoreBackupZIP(backupZip)
	if err != nil {
		t.Fatalf("RestoreBackupZIP failed: %v", err)
	}
	if len(lib.Books) != 1 || lib.Books[0].ID != book.ID || len(lib.Books[0].Notes) != 1 {
		t.Fatalf("unexpected restored library: %+v", lib.Books)
	}
	if settings.Appearance != "dark" || settings.WebDAVUsername != "alice" {
		t.Fatalf("unexpected restored settings: %+v", settings)
	}
	drawing, err := dst.ReadDrawing(book.ID)
	if err != nil || len(drawing.Ink) != 1 || len(drawing.Ink[0].Points) != 2 {
		t.Fatalf("expected uncheckpointed stroke in restored drawing, got %+v err=%v", drawing, err)
	}
	if data, err := os.ReadFile(dst.MediaPath(mediaFile)); err != nil || string(data) != "png-payload" {
		t.Fatalf("media missing after restore: err=%v data=%q", err, data)
	}
	if data, err := os.ReadFile(dst.DocumentPath(docID)); err != nil || !strings.HasPrefix(string(data), "%PDF-1.4") {
		t.Fatalf("document missing after restore: err=%v data=%q", err, data)
	}
}

func TestRestoreBackupZIPRejectsZipSlip(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
	}{
		{
			name:  "traversal",
			files: map[string]string{"index.json": `{"version":1,"books":[]}`, "../evil.txt": "boom"},
		},
		{
			name:  "absolute-path",
			files: map[string]string{"index.json": `{"version":1,"books":[]}`, "/tmp/evil.txt": "boom"},
		},
		{
			name:  "nested-subdir",
			files: map[string]string{"index.json": `{"version":1,"books":[]}`, "books/sub/a.json": `{}`},
		},
		{
			name:  "missing-index",
			files: map[string]string{"settings.json": `{}`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storeDir := t.TempDir()
			sentinel := filepath.Join(storeDir, "settings.json")
			if err := os.WriteFile(sentinel, []byte(`{"appearance":"light"}`), 0600); err != nil {
				t.Fatal(err)
			}
			zipPath := filepath.Join(t.TempDir(), tc.name+".zip")
			zf, err := os.Create(zipPath)
			if err != nil {
				t.Fatal(err)
			}
			zw := zip.NewWriter(zf)
			for name, body := range tc.files {
				w, err := zw.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write([]byte(body))
			}
			_ = zw.Close()
			_ = zf.Close()

			store := NewStore(storeDir)
			if _, _, err := store.RestoreBackupZIP(zipPath); err == nil {
				t.Fatalf("expected error for invalid archive %s", tc.name)
			}
			// Existing store content must remain untouched.
			got, err := os.ReadFile(sentinel)
			if err != nil || string(got) != `{"appearance":"light"}` {
				t.Fatalf("existing settings modified on rejected restore: err=%v got=%q", err, got)
			}
		})
	}
}

type mockWebDAVServer struct {
	mu       sync.Mutex
	dirs     map[string]bool
	files    map[string][]byte
	putCount map[string]int
}

func newMockWebDAVServer() (*httptest.Server, *mockWebDAVServer) {
	state := &mockWebDAVServer{
		dirs:     map[string]bool{"/dav/": true},
		files:    map[string][]byte{},
		putCount: map[string]int{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "alice" || pass != "secret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="dav"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()

		p := r.URL.Path
		switch r.Method {
		case "PROPFIND", http.MethodOptions:
			w.WriteHeader(http.StatusMultiStatus)
		case "MKCOL":
			state.dirs[strings.TrimRight(p, "/")+"/"] = true
			w.WriteHeader(http.StatusCreated)
		case http.MethodHead:
			if _, exists := state.files[p]; !exists {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			data, exists := state.files[p]
			if !exists {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
		case http.MethodPut:
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(r.Body)
			state.files[p] = buf.Bytes()
			state.putCount[p]++
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	return srv, state
}

func TestWebDAVSyncBidirectionalAndHeadSkip(t *testing.T) {
	srv, dav := newMockWebDAVServer()
	defer srv.Close()

	cfg := WebDAVConfig{
		BaseURL:  srv.URL + "/dav",
		Username: "alice",
		Password: "secret",
		Client:   srv.Client(),
	}
	ctx := context.Background()

	if err := TestWebDAV(ctx, WebDAVConfig{
		BaseURL:  srv.URL + "/dav",
		Username: "alice",
		Password: "wrong",
		Client:   srv.Client(),
	}); err == nil {
		t.Fatal("expected auth failure on wrong password")
	}
	if err := TestWebDAV(ctx, cfg); err != nil {
		t.Fatalf("TestWebDAV failed: %v", err)
	}

	storeA, book1, mediaFile, docID := seedStoreForBackupTest(t, t.TempDir())
	res1, err := storeA.SyncWebDAV(ctx, cfg)
	if err != nil {
		t.Fatalf("storeA initial sync failed: %v", err)
	}
	if res1.UploadedBooks != 1 || res1.UploadedFiles < 3 {
		t.Fatalf("unexpected initial upload counts: %+v", res1)
	}

	// Second sync on storeA must skip re-uploading unchanged media/doc thanks to HEAD check.
	res2, err := storeA.SyncWebDAV(ctx, cfg)
	if err != nil {
		t.Fatalf("storeA repeat sync failed: %v", err)
	}
	if res2.UploadedBooks != 0 || res2.UploadedFiles != 0 {
		t.Fatalf("expected no uploads on clean repeat sync, got %+v", res2)
	}
	dav.mu.Lock()
	mediaPuts := dav.putCount["/dav/FolioSync/media/"+mediaFile]
	docPuts := dav.putCount["/dav/FolioSync/documents/"+docID+".pdf"]
	dav.mu.Unlock()
	if mediaPuts != 1 || docPuts != 1 {
		t.Fatalf("expected single PUT for media/doc across two syncs, got media=%d doc=%d", mediaPuts, docPuts)
	}

	// Store B pulls Book 1, adds Book 2 and edits Note n1 with newer timestamp.
	storeB := NewStore(t.TempDir())
	resB1, err := storeB.SyncWebDAV(ctx, cfg)
	if err != nil {
		t.Fatalf("storeB initial pull failed: %v", err)
	}
	if len(resB1.Library.Books) != 1 || resB1.DownloadedBooks != 1 {
		t.Fatalf("unexpected storeB pull result: %+v", resB1)
	}
	if _, err := os.ReadFile(storeB.MediaPath(mediaFile)); err != nil {
		t.Fatalf("expected media downloaded to storeB: %v", err)
	}
	if _, err := os.ReadFile(storeB.DocumentPath(docID)); err != nil {
		t.Fatalf("expected document downloaded to storeB: %v", err)
	}

	// Edit n1 in storeB and sync up.
	if _, err := storeB.SaveNoteWithMeta(book1.ID, "n1", "牛顿第二定律（修订）", "F = ma 重点公式", nil, []string{"物理", "必考"}, nil); err != nil {
		t.Fatalf("storeB SaveNoteWithMeta failed: %v", err)
	}
	// Sync back to storeA.
	if _, err := storeB.SyncWebDAV(ctx, cfg); err != nil {
		t.Fatalf("storeB push failed: %v", err)
	}
	resA3, err := storeA.SyncWebDAV(ctx, cfg)
	if err != nil {
		t.Fatalf("storeA sync after storeB edit failed: %v", err)
	}
	if len(resA3.Library.Books) != 1 || resA3.Library.Books[0].Notes[0].Title != "牛顿第二定律（修订）" {
		t.Fatalf("expected storeA to receive updated note from storeB, got %+v", resA3.Library.Books[0].Notes[0])
	}
}
