package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo/ui"
)

func TestThumbnailCacheEvictsLeastRecentlyUsedByBytes(t *testing.T) {
	var cache mediaThumbnailCache
	bitmap := ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 10, 10)))
	keys := []mediaThumbnailKey{{"a", 256}, {"b", 256}, {"c", 256}}
	const budget = 1100
	cache.put(keys[0], bitmap, budget)
	cache.put(keys[1], bitmap, budget)
	cache.get(keys[0])
	cache.put(keys[2], bitmap, budget)
	if _, ok := cache.get(keys[1]); ok {
		t.Fatal("least recently used thumbnail was kept")
	}
	for _, key := range []mediaThumbnailKey{keys[0], keys[2]} {
		if got, ok := cache.get(key); !ok || got != bitmap {
			t.Fatalf("recent thumbnail %v was evicted", key)
		}
	}
	if cache.bytes > budget {
		t.Fatalf("cache uses %d bytes, budget is %d", cache.bytes, budget)
	}
}

func TestMediaThumbnailAndOriginalUseSeparateSizes(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	ref := strings.Repeat("a", 64) + ".png"
	path := app.store.MediaPath(ref)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 1200, 600))); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	thumbnail := app.mediaThumbnail(ref, 0)
	if thumbnail == nil {
		t.Fatal("thumbnail failed to load")
	}
	if w, h := thumbnail.Size(); w != 256 || h != 128 {
		t.Fatalf("thumbnail dimensions = %dx%d", w, h)
	}
	original, ready := app.originalMedia(ref)
	if !ready || original == nil {
		t.Fatal("original failed to load")
	}
	if w, h := original.Size(); w != 1200 || h != 600 {
		t.Fatalf("original dimensions = %dx%d", w, h)
	}
	if got := app.mediaThumbnail(ref, 0); got != thumbnail {
		t.Fatal("opening the original replaced the thumbnail")
	}
}
