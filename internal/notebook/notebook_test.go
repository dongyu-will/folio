package notebook

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"howett.net/plist"
)

func appendVarint(b []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(b, tmp[:n]...)
}

func appendTag(b []byte, field uint64, wire uint8) []byte {
	return appendVarint(b, (field<<3)|uint64(wire))
}

func appendBytesField(b []byte, field uint64, data []byte) []byte {
	b = appendTag(b, field, 2)
	b = appendVarint(b, uint64(len(data)))
	return append(b, data...)
}

func appendFloat32Field(b []byte, field uint64, v float32) []byte {
	b = appendTag(b, field, 5)
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], math.Float32bits(v))
	return append(b, tmp[:]...)
}

func buildSyntheticPKDrawing(t *testing.T, gzipped bool) []byte {
	t.Helper()
	makeColor := func(r, g, b, a float32) []byte {
		var out []byte
		out = appendFloat32Field(out, 1, r)
		out = appendFloat32Field(out, 2, g)
		out = appendFloat32Field(out, 3, b)
		out = appendFloat32Field(out, 4, a)
		return out
	}
	makeInk := func(kind string, r, g, b, a float32) []byte {
		var out []byte
		out = appendBytesField(out, 1, makeColor(r, g, b, a))
		out = appendBytesField(out, 2, []byte(kind))
		return out
	}
	makeTransform := func(scale, tx, ty float32) []byte {
		var out []byte
		out = appendFloat32Field(out, 1, scale)
		out = appendFloat32Field(out, 2, 0)
		out = appendFloat32Field(out, 3, 0)
		out = appendFloat32Field(out, 4, scale)
		out = appendFloat32Field(out, 5, tx)
		out = appendFloat32Field(out, 6, ty)
		return out
	}
	makeStroke := func(inkIdx uint64, flags uint64, radius float32, opacity float64, pts [][2]float32, tf []byte) []byte {
		// pointMask: bit 0 (location = 8B) | bit 1 (time = 4B) = 3
		// constMask: bit 2 (radius = 4B) | bit 8 (opacity = 2B) = 260
		var constBuf [6]byte
		binary.LittleEndian.PutUint32(constBuf[0:4], math.Float32bits(radius))
		binary.LittleEndian.PutUint16(constBuf[4:6], uint16(math.Round(opacity*32767)))

		ptsBuf := make([]byte, 0, len(pts)*12)
		for i, p := range pts {
			var rec [12]byte
			binary.LittleEndian.PutUint32(rec[0:4], math.Float32bits(p[0]))
			binary.LittleEndian.PutUint32(rec[4:8], math.Float32bits(p[1]))
			binary.LittleEndian.PutUint32(rec[8:12], math.Float32bits(float32(i)*0.02))
			ptsBuf = append(ptsBuf, rec[:]...)
		}

		var path []byte
		path = appendTag(path, 3, 0)
		path = appendVarint(path, uint64(len(pts)))
		path = appendTag(path, 4, 0)
		path = appendVarint(path, 3)
		path = appendTag(path, 5, 0)
		path = appendVarint(path, (1<<2)|(1<<8))
		path = appendBytesField(path, 6, constBuf[:])
		path = appendBytesField(path, 7, ptsBuf)

		var stroke []byte
		stroke = appendTag(stroke, 4, 0)
		stroke = appendVarint(stroke, inkIdx)
		stroke = appendBytesField(stroke, 5, path)
		if len(tf) > 0 {
			stroke = appendBytesField(stroke, 7, tf)
		}
		stroke = appendTag(stroke, 8, 0)
		stroke = appendVarint(stroke, flags)
		return stroke
	}

	var proto []byte
	// Ink 0: black pen, Ink 1: red marker (alpha 0.4)
	proto = appendBytesField(proto, 4, makeInk("com.apple.ink.pen", 0, 0, 0, 1))
	proto = appendBytesField(proto, 4, makeInk("com.apple.ink.marker", 1, 0, 0, 0.4))
	// Stroke 0: visible black horizontal pen line at y=20 (scaled 0.5 from y=40)
	proto = appendBytesField(proto, 5, makeStroke(0, 0, 8, 1.0, [][2]float32{{20, 40}, {160, 40}}, makeTransform(0.5, 0, 0)))
	// Stroke 1: visible translucent marker line at y=50 with self-overlapping vertex
	proto = appendBytesField(proto, 5, makeStroke(1, 0, 14, 0.4, [][2]float32{{10, 50}, {40, 50}, {70, 50}}, nil))
	// Stroke 2: tombstoned/erased stroke at y=85 (flags bit 0 = 1) must NOT draw
	proto = appendBytesField(proto, 5, makeStroke(0, 1, 12, 1.0, [][2]float32{{10, 85}, {80, 85}}, nil))

	raw := append([]byte{'w', 'r', 'd', 0xf0, 0x02, 0x00}, proto...)
	if !gzipped {
		return raw
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPureGoPKDrawingRendering(t *testing.T) {
	for _, gzipped := range []bool{false, true} {
		data := buildSyntheticPKDrawing(t, gzipped)
		out := renderPKDrawing(data, 100, 100, 100)
		if len(out) == 0 {
			t.Fatalf("gzipped=%v: expected rendered PNG bytes", gzipped)
		}
		img, err := png.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("gzipped=%v: invalid PNG: %v", gzipped, err)
		}
		if img.Bounds().Dx() != 100 || img.Bounds().Dy() != 100 {
			t.Fatalf("unexpected bounds: %v", img.Bounds())
		}
		// Pen line at (40, 20) should be opaque black.
		_, _, _, a := img.At(40, 20).RGBA()
		if a < 0xf000 {
			t.Fatalf("expected opaque pen stroke at (40,20), got alpha=%#x", a)
		}
		// Marker joint at (40, 50) should stay translucent (~40%), not double-darkened.
		r, _, _, ma := img.At(40, 50).RGBA()
		if ma < 0x4000 || ma > 0x9000 || r < ma/2 {
			t.Fatalf("expected uniform translucent red marker at (40,50), got r=%#x a=%#x", r, ma)
		}
		// Erased stroke at (40, 85) must remain fully transparent.
		_, _, _, ea := img.At(40, 85).RGBA()
		if ea != 0 {
			t.Fatalf("expected tombstoned stroke at (40,85) to be skipped, got alpha=%#x", ea)
		}
	}
}

func TestCleanHTMLNoteAndBody(t *testing.T) {
	cocoaHTML := `<!DOCTYPE html PUBLIC "-//W3C//DTD HTML 4.01//EN">
<html><head><meta charset="utf-8"><style type="text/css">p.p1 {margin: 0.0px; font: 12.0px PingFangSC}</style><title>ignored</title></head>
<body><p class="p1">极限与连续 &amp; 导数</p><p>第二段<br>换行内容</p><ul><li>条目A</li><li>条目B</li></ul></body></html>`

	got := cleanHTMLNote(map[string]any{"type": "HtmlNote", "text": "", "html": cocoaHTML})
	if strings.Contains(got, "margin:") || strings.Contains(got, "<p") || strings.Contains(got, "ignored") {
		t.Fatalf("HTML noise leaked into cleaned text: %q", got)
	}
	for _, want := range []string{"极限与连续 & 导数", "第二段", "换行内容", "• 条目A", "• 条目B"} {
		if !strings.Contains(got, want) {
			t.Fatalf("cleaned text missing %q: %q", want, got)
		}
	}

	plain := cleanHTMLNote(map[string]any{"type": "HtmlNote", "text": "x < y 时成立", "html": cocoaHTML})
	if plain != "x < y 时成立" {
		t.Fatalf("plain text with '<' should stay intact, got %q", plain)
	}
}

func createTestMarginDB(t *testing.T, path, topicID, topicTitle string, notes []struct{ id, title, body, parent, links string }, imageBytes []byte) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		"CREATE TABLE ZTOPIC (Z_PK INTEGER, ZTOPICID TEXT, ZTITLE TEXT, ZMINDLINKS TEXT)",
		"CREATE TABLE ZBOOKNOTE (Z_PK INTEGER, ZNOTEID TEXT, ZTOPICID TEXT, ZGROUPNOTEID TEXT, ZNOTETITLE TEXT, ZNOTES_TEXT TEXT, ZHIGHLIGHT_TEXT TEXT, ZSTARTPAGE INTEGER, ZMINDLINKS TEXT, ZHIGHLIGHT_PIC BLOB, ZNOTES BLOB)",
		"CREATE TABLE ZMEDIA (ZMD5 TEXT, ZDATA BLOB)",
	} {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	var clip []byte
	if len(imageBytes) > 0 {
		archive := func(objects ...any) []byte {
			data, err := plist.Marshal(map[string]any{
				"$archiver": "NSKeyedArchiver", "$top": map[string]any{"root": plist.UID(1)},
				"$objects": append([]any{"$null"}, objects...),
			}, plist.BinaryFormat)
			if err != nil {
				t.Fatal(err)
			}
			return data
		}
		clip = archive(map[string]any{
			"NS.keys": []any{plist.UID(2)}, "NS.objects": []any{plist.UID(3)},
		}, "paint", "img-key")
		if _, err := db.Exec("INSERT INTO ZMEDIA VALUES (?, ?)", "img-key", archive(map[string]any{"NS.data": imageBytes})); err != nil {
			t.Fatal(err)
		}
	}
	var rootID string
	if len(notes) > 0 {
		rootID = notes[0].id
	}
	if topicID != "" {
		if _, err := db.Exec("INSERT INTO ZTOPIC VALUES (1, ?, ?, ?)", topicID, topicTitle, rootID); err != nil {
			t.Fatal(err)
		}
	}
	for i, n := range notes {
		var noteClip []byte
		if i == 0 {
			noteClip = clip
		}
		if _, err := db.Exec(
			"INSERT INTO ZBOOKNOTE VALUES (?, ?, ?, ?, ?, ?, '', 1, ?, ?, NULL)",
			i+1, n.id, topicID, n.parent, n.title, n.body, n.links, noteClip,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func makePNG(t *testing.T, w, h int, seed byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Pix[0] = seed
	img.Pix[3] = 255
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestShardedStoreMigrationAndSingleShardSave(t *testing.T) {
	dir := t.TempDir()
	legacy := Library{Books: []Book{
		{ID: "hash1/topic1", Title: "Book 1", Roots: []string{"n1"}, Notes: []Note{{ID: "n1", Title: "T1", Body: "B1"}}},
		{ID: "hash2/topic2", Title: "Book 2", Roots: []string{"n2"}, Notes: []Note{{ID: "n2", Title: "T2", Body: "B2"}}},
	}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "library.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}

	store := NewStore(dir)
	lib, err := store.Read()
	if err != nil || len(lib.Books) != 2 {
		t.Fatalf("migration read failed: %v, books=%d", err, len(lib.Books))
	}
	if _, err := os.Stat(filepath.Join(dir, "library.json")); !os.IsNotExist(err) {
		t.Fatalf("expected legacy library.json to be removed after migration")
	}
	if _, err := os.Stat(filepath.Join(dir, "index.json")); err != nil {
		t.Fatalf("expected index.json after migration: %v", err)
	}

	book2Path := filepath.Join(dir, "books", bookFileName("hash2/topic2"))
	oldTime := time.Now().Add(-time.Minute).Truncate(time.Second)
	if err := os.Chtimes(book2Path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	updated, err := store.SaveNote("hash1/topic1", "n1", "T1-edited", "B1-edited")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Books[0].Notes[0].Title != "T1-edited" || !updated.Books[0].Notes[0].TitleEdited {
		t.Fatalf("save did not persist edited title: %+v", updated.Books[0].Notes[0])
	}
	info, err := os.Stat(book2Path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(oldTime) {
		t.Fatalf("saving Book 1 should not rewrite Book 2 shard (was %v, now %v)", oldTime, info.ModTime())
	}
}

func TestTopicUpdatePreservesEditsAndUserNotes(t *testing.T) {
	tmp := t.TempDir()
	dbV1 := filepath.Join(tmp, "v1.marginnotes")
	dbV2 := filepath.Join(tmp, "v2.marginnotes")

	createTestMarginDB(t, dbV1, "TOPIC-UUID-1", "高等数学", []struct{ id, title, body, parent, links string }{
		{id: "n1", title: "第一章", body: "旧正文1", links: "n2"},
		{id: "n2", title: "旧标题2", body: "旧正文2"},
	}, makePNG(t, 8, 8, 1))

	createTestMarginDB(t, dbV2, "TOPIC-UUID-1", "高等数学（修订版）", []struct{ id, title, body, parent, links string }{
		{id: "n1", title: "第一章（新）", body: "新正文1", links: "n2|n3"},
		{id: "n2", title: "新标题2", body: "新正文2"},
		{id: "n3", title: "新增节点", body: "来自 MarginNote"},
	}, makePNG(t, 8, 8, 2))

	store := NewStore(filepath.Join(tmp, "data"))
	r1, err := store.Import(dbV1)
	if err != nil || r1.Added != 1 {
		t.Fatalf("v1 import failed: %+v, %v", r1, err)
	}
	// Locally edit n2 and add a custom child note under n1.
	if _, err := store.SaveNote(r1.BookID, "n2", "我的标题2", "我的笔记2"); err != nil {
		t.Fatal(err)
	}
	_, userNoteID, err := store.AddNote(r1.BookID, "n1", "自建补充", "手写总结")
	if err != nil {
		t.Fatal(err)
	}

	// Re-importing the exact same v1 DB should report neither added nor updated.
	rDup, err := store.Import(dbV1)
	if err != nil || rDup.Added != 0 || rDup.Updated != 0 {
		t.Fatalf("duplicate import should be no-op: added=%d updated=%d err=%v", rDup.Added, rDup.Updated, err)
	}

	// Importing updated v2 DB with same topic UUID must update in place.
	r2, err := store.Import(dbV2)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Added != 0 || r2.Updated != 1 || len(r2.Library.Books) != 1 {
		t.Fatalf("expected in-place update without duplicate book: added=%d updated=%d books=%d", r2.Added, r2.Updated, len(r2.Library.Books))
	}
	book := r2.Library.Books[0]
	if book.Title != "高等数学（修订版）" {
		t.Fatalf("expected updated title, got %q", book.Title)
	}
	byID := map[string]Note{}
	for _, n := range book.Notes {
		byID[n.ID] = n
	}
	if byID["n1"].Body != "新正文1" {
		t.Fatalf("unedited note n1 should be updated, got %q", byID["n1"].Body)
	}
	if byID["n2"].Title != "我的标题2" || byID["n2"].Body != "我的笔记2" {
		t.Fatalf("edited note n2 should retain local edits, got %+v", byID["n2"])
	}
	if byID["n3"].Title != "新增节点" {
		t.Fatalf("new note n3 missing: %+v", byID["n3"])
	}
	if byID[userNoteID].Title != "自建补充" {
		t.Fatalf("user-created note %s lost on merge", userNoteID)
	}
	// Old image should have been garbage-collected after v2 replaced it.
	mediaEntries, _ := os.ReadDir(filepath.Join(tmp, "data", "media"))
	if len(mediaEntries) != 1 {
		t.Fatalf("expected 1 active media file after update GC, got %d", len(mediaEntries))
	}
}

func TestDeleteBookAndMediaGC(t *testing.T) {
	tmp := t.TempDir()
	sharedImg := makePNG(t, 6, 6, 42)
	exclusiveImg := makePNG(t, 6, 6, 99)

	db1 := filepath.Join(tmp, "b1.marginnotes")
	db2 := filepath.Join(tmp, "b2.marginnotes")
	createTestMarginDB(t, db1, "T1", "Book1", []struct{ id, title, body, parent, links string }{{id: "n1", title: "N1", body: "B1"}}, sharedImg)
	createTestMarginDB(t, db2, "T2", "Book2", []struct{ id, title, body, parent, links string }{{id: "n2", title: "N2", body: "B2"}}, exclusiveImg)

	store := NewStore(filepath.Join(tmp, "store"))
	r1, err := store.Import(db1)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := store.Import(db2)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(filepath.Join(tmp, "store", "media"))
	if len(before) != 2 {
		t.Fatalf("expected 2 media files before delete, got %d", len(before))
	}

	lib, err := store.DeleteBook(r2.BookID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Books) != 1 || lib.Books[0].ID != r1.BookID {
		t.Fatalf("unexpected remaining books: %+v", lib.Books)
	}
	after, _ := os.ReadDir(filepath.Join(tmp, "store", "media"))
	if len(after) != 1 {
		t.Fatalf("expected exclusive media file to be GC'd, remaining=%d", len(after))
	}
}

func TestConnectNotesCycleAndZipValidationAndMarkdown(t *testing.T) {
	// Cycle + ZGROUPNOTEID fallback + disconnected cycle reachability.
	notes, roots := connectNotes([]sourceNote{
		{Note: Note{ID: "a", Title: "Root A", Children: []string{"b"}}},
		{Note: Note{ID: "b", Title: "Child B", Children: []string{"a"}}}, // cycle back to a
		{Note: Note{ID: "c", Title: "Grouped C"}, parent: "a"},            // linked via parent fallback
		{Note: Note{ID: "x", Title: "Cycle X", Children: []string{"y"}}},
		{Note: Note{ID: "y", Title: "Cycle Y", Children: []string{"x"}}},
	}, []string{"a"})
	if len(notes) != 5 {
		t.Fatalf("expected 5 notes, got %d", len(notes))
	}
	if len(roots) < 2 || roots[0] != "a" {
		t.Fatalf("expected disconnected cycle to be added to roots, got %v", roots)
	}

	// Markdown export includes title, body, and child links.
	book := Book{
		Title: "测试书",
		Roots: []string{"a"},
		Notes: []Note{
			{ID: "a", Title: "节点[1]", Body: "正文内容", Children: []string{"b"}},
			{ID: "b", Title: "子节点", Blocks: []ContentBlock{{Text: "块正文"}}},
		},
	}
	md := string(book.Markdown())
	for _, want := range []string{"# 测试书", "## 节点[1]", "正文内容", "- [子节点](#note-2)", "块正文"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}

	// Zip validation: ignore __MACOSX and reject multiple .marginnotes entries.
	tmp := t.TempDir()
	badZip := filepath.Join(tmp, "multi.marginpkg")
	f, err := os.Create(badZip)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, name := range []string{"__MACOSX/._a.marginnotes", "one.marginnotes", "two.marginnotes"} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte("dummy"))
	}
	_ = zw.Close()
	_ = f.Close()
	if _, err := openPayload(badZip); err == nil || !strings.Contains(err.Error(), "多个笔记数据库") {
		t.Fatalf("expected multi-db zip rejection, got %v", err)
	}
}

func TestImportExtensionlessMagicByteFallback(t *testing.T) {
	tmp := t.TempDir()
	img := makePNG(t, 12, 12, 21)

	// 1. Extensionless SQLite (.marginnotes) file.
	extlessDB := filepath.Join(tmp, "picked_sqlite_blob")
	createTestMarginDB(t, extlessDB, "TOPIC-MAGIC-DB", "无后缀数据库", []struct{ id, title, body, parent, links string }{
		{id: "n-db", title: "SQLite节点", body: "魔数识别"},
	}, img)

	// 2. Extensionless ZIP (.marginpkg) file with embedded PDF, plus unrecognized ".bin" extension.
	dbRaw, err := os.ReadFile(extlessDB)
	if err != nil {
		t.Fatal(err)
	}
	pdfBytes := []byte("%PDF-1.7\n%%EOF\n")
	var pkgBuf bytes.Buffer
	zw := zip.NewWriter(&pkgBuf)
	for name, data := range map[string][]byte{
		"inner.marginnotes":                 dbRaw,
		"VersionBackupSubset/docs/讲义原文.pdf": pdfBytes,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	extlessPkg := filepath.Join(tmp, "picked_package_blob")
	if err := os.WriteFile(extlessPkg, pkgBuf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	binPkg := filepath.Join(tmp, "picked_package.bin")
	if err := os.WriteFile(binPkg, pkgBuf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	// 3. Extensionless 15-byte PDF file (shorter than 16-byte sniff buffer).
	extlessPDF := filepath.Join(tmp, "picked_pdf_blob")
	if err := os.WriteFile(extlessPDF, pdfBytes, 0600); err != nil {
		t.Fatal(err)
	}

	// 4. Extensionless unrecognized plain text file.
	extlessBad := filepath.Join(tmp, "picked_plain_text")
	if err := os.WriteFile(extlessBad, []byte("plain text without magic"), 0600); err != nil {
		t.Fatal(err)
	}

	if got := detectImportFormat(extlessPkg); got != ".marginpkg" {
		t.Fatalf("detectImportFormat(extlessPkg) = %q, want .marginpkg", got)
	}
	if got := detectImportFormat(binPkg); got != ".marginpkg" {
		t.Fatalf("detectImportFormat(binPkg) = %q, want .marginpkg", got)
	}
	if got := detectImportFormat(extlessDB); got != ".marginnotes" {
		t.Fatalf("detectImportFormat(extlessDB) = %q, want .marginnotes", got)
	}
	if got := detectImportFormat(extlessPDF); got != ".pdf" {
		t.Fatalf("detectImportFormat(extlessPDF) = %q, want .pdf", got)
	}
	if got := detectImportFormat(extlessBad); got != "" {
		t.Fatalf("detectImportFormat(extlessBad) = %q, want empty", got)
	}

	// Import extensionless .marginpkg -> must import notes AND embedded PDF documents.
	storePkg := NewStore(filepath.Join(tmp, "store-pkg"))
	resPkg, err := storePkg.Import(extlessPkg)
	if err != nil {
		t.Fatalf("extensionless .marginpkg import failed: %v", err)
	}
	if resPkg.Added != 1 || len(resPkg.Library.Books) != 1 || len(resPkg.Library.Books[0].Notes) != 1 {
		t.Fatalf("unexpected extensionless .marginpkg books: %+v", resPkg.Library.Books)
	}
	if len(resPkg.Library.Books[0].Documents) != 1 || resPkg.Library.Books[0].Documents[0].Title != "讲义原文" {
		t.Fatalf("extensionless .marginpkg did not import embedded PDF: %+v", resPkg.Library.Books[0].Documents)
	}

	// Import unrecognized .bin .marginpkg.
	storeBin := NewStore(filepath.Join(tmp, "store-bin"))
	resBin, err := storeBin.Import(binPkg)
	if err != nil || resBin.Added != 1 || len(resBin.Library.Books[0].Documents) != 1 {
		t.Fatalf(".bin .marginpkg import failed: res=%+v err=%v", resBin, err)
	}

	// Import extensionless .marginnotes.
	storeDB := NewStore(filepath.Join(tmp, "store-db"))
	resDB, err := storeDB.Import(extlessDB)
	if err != nil || resDB.Added != 1 || len(resDB.Library.Books[0].Notes) != 1 {
		t.Fatalf("extensionless .marginnotes import failed: res=%+v err=%v", resDB, err)
	}

	// Import extensionless .pdf.
	resPDF, err := storeDB.Import(extlessPDF)
	if err != nil || resPDF.Added != 1 || !strings.HasPrefix(resPDF.BookID, "pdf-") {
		t.Fatalf("extensionless .pdf import failed: res=%+v err=%v", resPDF, err)
	}

	// Invalid extensionless file still fails cleanly.
	if _, err := storeDB.Import(extlessBad); err == nil || !strings.Contains(err.Error(), ".marginpkg") {
		t.Fatalf("expected clean rejection for invalid extensionless file, got %v", err)
	}
}

func TestReimportSkipsExistingMediaFilesOnDisk(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "reimport.marginnotes")
	img := makePNG(t, 16, 16, 77)
	createTestMarginDB(t, dbPath, "TOPIC-SKIP", "跳过已有媒体", []struct{ id, title, body, parent, links string }{
		{id: "n1", title: "节点1", body: "正文"},
	}, img)

	store := NewStore(filepath.Join(tmp, "store"))
	first, err := store.Import(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	clipName := first.Library.Books[0].Notes[0].Clip
	if clipName == "" {
		t.Fatal("expected saved clip on first import")
	}
	mediaPath := store.MediaPath(clipName)
	oldTime := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	if err := os.Chtimes(mediaPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// 1. No-op re-import must skip rewriting the existing media file.
	second, err := store.Import(dbPath)
	if err != nil || second.Added != 0 || second.Updated != 0 {
		t.Fatalf("unexpected no-op re-import result: added=%d updated=%d err=%v", second.Added, second.Updated, err)
	}
	info, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(oldTime) {
		t.Fatalf("no-op re-import rewrote existing media file (was %v, now %v)", oldTime, info.ModTime())
	}

	// 2. Active MediaVersion upgrade re-import must also skip rewriting existing media on disk.
	if _, err := store.update(func(l *Library) (bool, error) {
		l.Books[0].MediaVersion = 0
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	upgraded, err := store.Import(dbPath)
	if err != nil || upgraded.Updated != 1 {
		t.Fatalf("unexpected upgrade re-import result: updated=%d err=%v", upgraded.Updated, err)
	}
	info2, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info2.ModTime().Equal(oldTime) {
		t.Fatalf("upgrade re-import rewrote existing media file (was %v, now %v)", oldTime, info2.ModTime())
	}
	saved, err := os.ReadFile(mediaPath)
	if err != nil || !bytes.Equal(saved, img) {
		t.Fatalf("existing media file content corrupted (err=%v)", err)
	}
}
