package notebook

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"howett.net/plist"
)

func TestImportRecoversFormulaExcerptWithoutOverwritingEdits(t *testing.T) {
	file := filepath.Join(t.TempDir(), "formula.marginnotes")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{
		"CREATE TABLE ZTOPIC (Z_PK INTEGER, ZTOPICID TEXT, ZTITLE TEXT, ZMINDLINKS TEXT)",
		"CREATE TABLE ZBOOKNOTE (Z_PK INTEGER, ZNOTEID TEXT, ZTOPICID TEXT, ZGROUPNOTEID TEXT, ZNOTETITLE TEXT, ZNOTES_TEXT TEXT, ZHIGHLIGHT_TEXT TEXT, ZSTARTPAGE INTEGER, ZMINDLINKS TEXT, ZHIGHLIGHT_PIC BLOB, ZNOTES BLOB)",
		"CREATE TABLE ZMEDIA (ZMD5 TEXT, ZDATA BLOB)",
		"INSERT INTO ZTOPIC VALUES (1, 'topic', '数列', 'note')",
	} {
		if _, err := db.ExecContext(context.Background(), query); err != nil {
			t.Fatal(err)
		}
	}
	archive := func(objects ...any) []byte {
		t.Helper()
		data, err := plist.Marshal(map[string]any{
			"$archiver": "NSKeyedArchiver", "$top": map[string]any{"root": plist.UID(1)},
			"$objects": append([]any{"$null"}, objects...),
		}, plist.BinaryFormat)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewRGBA(image.Rect(0, 0, 48, 8))); err != nil {
		t.Fatal(err)
	}
	clip := archive(map[string]any{
		"NS.keys": []any{plist.UID(2)}, "NS.objects": []any{plist.UID(3)},
	}, "paint", "image-key")
	wrappedImage := archive(map[string]any{"NS.data": original.Bytes()})
	if _, err := db.Exec("INSERT INTO ZMEDIA VALUES (?, ?)", "image-key", wrappedImage); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO ZBOOKNOTE VALUES (1, 'note', 'topic', NULL, '无穷大比阶', '', 'nnann\uf03d', 7, '', ?, NULL)", clip); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	store := NewStore(dir)
	result, err := store.Import(file)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := os.ReadDir(filepath.Join(dir, "media"))
	if err != nil || len(attachments) != 1 {
		t.Fatalf("formula note has no original image: %v, %v", attachments, err)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "media", attachments[0].Name()))
	if err != nil || !bytes.Equal(saved, original.Bytes()) {
		t.Fatal("stored image differs from the original")
	}
	if _, err := store.SaveNote(result.BookID, "note", "已编辑", "我的笔记"); err != nil {
		t.Fatal(err)
	}
	// Simulate the pre-attachment library format before repeating the import.
	if _, err := store.update(func(l *Library) (bool, error) {
		l.Books[0].MediaVersion = 0
		l.Books[0].Notes[0].Clip = ""
		l.Books[0].Notes[0].BodyEdited = false
		l.Books[0].Notes[0].Review = "known"
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	repeated, err := store.Import(file)
	if err != nil {
		t.Fatal(err)
	}
	note := repeated.Library.Books[0].Notes[0]
	if repeated.Added != 0 || note.Title != "已编辑" || note.Body != "我的笔记" || note.Review != "known" {
		t.Fatalf("reimport overwrote local edits: %+v", note)
	}
	if note.Clip != attachments[0].Name() || !note.BodyEdited {
		t.Fatalf("did not restore the image alongside the edited body: %+v", note)
	}
}

func TestImportMarginNoteColorsFoldAndDummyFilter(t *testing.T) {
	file := filepath.Join(t.TempDir(), "styled.marginnotes")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{
		"CREATE TABLE ZTOPIC (Z_PK INTEGER, ZTOPICID TEXT, ZTITLE TEXT, ZMINDLINKS TEXT)",
		"CREATE TABLE ZBOOKNOTE (Z_PK INTEGER, ZNOTEID TEXT, ZTOPICID TEXT, ZGROUPNOTEID TEXT, ZNOTETITLE TEXT, ZNOTES_TEXT TEXT, ZHIGHLIGHT_TEXT TEXT, ZSTARTPAGE INTEGER, ZMINDLINKS TEXT, ZHIGHLIGHT_PIC BLOB, ZNOTES BLOB, ZHIGHLIGHT_STYLE TEXT, ZMINDCLOSE INTEGER)",
		"CREATE TABLE ZMEDIA (ZMD5 TEXT, ZDATA BLOB)",
		"INSERT INTO ZTOPIC VALUES (1, 'topic', '级数', 'root')",
	} {
		if _, err := db.ExecContext(context.Background(), query); err != nil {
			t.Fatal(err)
		}
	}
	archive := func(objects ...any) []byte {
		t.Helper()
		data, err := plist.Marshal(map[string]any{
			"$archiver": "NSKeyedArchiver", "$top": map[string]any{"root": plist.UID(1)},
			"$objects": append([]any{"$null"}, objects...),
		}, plist.BinaryFormat)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	dummyBlob := archive(
		map[string]any{"NS.objects": []any{plist.UID(2)}},
		map[string]any{"NS.keys": []any{plist.UID(3)}, "NS.objects": []any{plist.UID(4)}},
		"type", "DummyNote",
	)
	if _, err := db.Exec("INSERT INTO ZBOOKNOTE VALUES (1, 'root', 'topic', NULL, '常数项级数', '', '', NULL, 'leaf', NULL, NULL, 'mbooks-annotation1c', 0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO ZBOOKNOTE VALUES (2, 'leaf', 'topic', NULL, '审敛流水线', '三步法', '', 3, '', NULL, NULL, 'mbooks-annotation17', 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO ZBOOKNOTE VALUES (3, 'dummy', 'topic', NULL, '', '', '', NULL, '', NULL, ?, NULL, 0)", dummyBlob); err != nil {
		t.Fatal(err)
	}

	store := NewStore(t.TempDir())
	res, err := store.Import(file)
	if err != nil {
		t.Fatal(err)
	}
	book := res.Library.Books[0]
	if len(book.Notes) != 2 || len(book.Roots) != 1 {
		t.Fatalf("expected DummyNote filtered out (2 notes, 1 root), got %d notes, %d roots: %+v", len(book.Notes), len(book.Roots), book)
	}
	root := book.Notes[0]
	leaf := book.Notes[1]
	if root.Color == nil || *root.Color != 12 || root.Style != 1 || root.Collapsed {
		t.Fatalf("unexpected root style metadata: %+v", root)
	}
	if leaf.Color == nil || *leaf.Color != 7 || leaf.Style != 1 || !leaf.Collapsed {
		t.Fatalf("unexpected leaf style metadata: %+v", leaf)
	}
}

func TestParseCocoaHTMLRichSpans(t *testing.T) {
	raw := `<!DOCTYPE html><html><head><style type="text/css">
p.p1 {margin: 0.0px; font: 18.0px 'PingFang SC Semibold'; color: #2000ff}
span.s1 {font: 18.0px 'PingFang SC'; background-color: #ffffff}
span.s2 {font: 18.0px 'PingFang SC'; color: #ff0000; text-decoration: underline}
span.s3 {font: 18.0px 'PingFang SC'; color: #000000; background-color: #f1faf6}
</style></head><body>
<p class="p1"><span class="s1"><b>核心：</b></span><span class="s2">无差别</span><span class="s3">正常正文</span></p>
</body></html>`
	plain, spans := parseHTMLRich(raw)
	if plain != "核心：无差别正常正文" {
		t.Fatalf("unexpected plain text: %q", plain)
	}
	if len(spans) != 3 {
		t.Fatalf("expected 3 styled spans, got %d: %+v", len(spans), spans)
	}
	if spans[0].Text != "核心：" || spans[0].Color != "#2000ff" || !spans[0].Bold || spans[0].Bg != "" {
		t.Fatalf("unexpected span 0: %+v", spans[0])
	}
	if spans[1].Text != "无差别" || spans[1].Color != "#ff0000" || !spans[1].Underline || spans[1].Bold {
		t.Fatalf("unexpected span 1: %+v", spans[1])
	}
	if spans[2].Text != "正常正文" || spans[2].Color != "" || spans[2].Bg != "#f1faf6" || spans[2].FontSize != 18 {
		t.Fatalf("unexpected span 2 (expected #000000 reset to theme ink, #f1faf6 bg, 18px font): %+v", spans[2])
	}
}

func TestParseHTMLFontSizeInheritance(t *testing.T) {
	raw := `<!DOCTYPE html><html><head><style type="text/css">
p.p1 {margin: 0.0px; font: 18.0px/24.0px 'PingFang SC'}
span.s1 {font-size: 13.00px}
span.s2 {font-size: 12pt}
</style></head><body>
<p class="p1">主句<span class="s1">小字注释<span style="font-size: 10px">极小</span>回到13</span><span class="s2">十二磅</span></p>
</body></html>`
	plain, spans := parseHTMLRich(raw)
	if plain != "主句小字注释极小回到13十二磅" {
		t.Fatalf("unexpected plain: %q", plain)
	}
	if !spansHaveStyling(spans) {
		t.Fatalf("expected font-size-only spans to count as styled: %+v", spans)
	}
	want := []struct {
		text string
		size float32
	}{
		{"主句", 18},
		{"小字注释", 13},
		{"极小", 10},
		{"回到13", 13},
		{"十二磅", 16},
	}
	if len(spans) != len(want) {
		t.Fatalf("expected %d spans (distinct font sizes must not merge), got %d: %+v", len(want), len(spans), spans)
	}
	for i, w := range want {
		if spans[i].Text != w.text || math.Abs(float64(spans[i].FontSize-w.size)) > 0.01 {
			t.Fatalf("span %d = %+v, want text=%q size=%v", i, spans[i], w.text, w.size)
		}
	}
}

func TestImportV5MetadataRectsLayoutBlocksGhostsAndRecoverUpgrade(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "v5.marginnotes")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE ZTOPIC (Z_PK INTEGER, ZTOPICID TEXT, ZTITLE TEXT, ZMINDLINKS TEXT, ZHASHTAGS TEXT)`,
		`CREATE TABLE ZBOOKCONFIG (Z_PK INTEGER, ZMD5 TEXT, ZMD5LONG TEXT, ZTITLE TEXT, ZCURRPAGE INTEGER, ZOPTIONS TEXT)`,
		`CREATE TABLE ZBOOKNOTE (
			Z_PK INTEGER, ZNOTEID TEXT, ZTOPICID TEXT, ZGROUPNOTEID TEXT,
			ZNOTETITLE TEXT, ZNOTES_TEXT TEXT, ZHIGHLIGHT_TEXT TEXT, ZSTARTPAGE INTEGER,
			ZMINDLINKS TEXT, ZHIGHLIGHT_PIC BLOB, ZNOTES BLOB, ZHIGHLIGHT_STYLE TEXT,
			ZMINDCLOSE INTEGER, ZRECOGNIZE_TEXT TEXT, ZBOOKMD5 TEXT,
			ZHIGHLIGHT_DATE REAL, ZNOTE_DATE REAL, ZTYPE INTEGER, ZZINDEX INTEGER
		)`,
		`CREATE TABLE ZMEDIA (ZMD5 TEXT, ZDATA BLOB)`,
	} {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}

	archive := func(objects ...any) []byte {
		t.Helper()
		data, err := plist.Marshal(map[string]any{
			"$archiver": "NSKeyedArchiver", "$top": map[string]any{"root": plist.UID(1)},
			"$objects": append([]any{"$null"}, objects...),
		}, plist.BinaryFormat)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	clipImg := makePNG(t, 24, 12, 7)
	sketchImg := makePNG(t, 16, 16, 9)
	if _, err := db.Exec("INSERT INTO ZMEDIA VALUES (?, ?)", "clip-md5", archive(map[string]any{"NS.data": clipImg})); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO ZMEDIA VALUES (?, ?)", "sketch-md5", archive(map[string]any{"NS.data": sketchImg})); err != nil {
		t.Fatal(err)
	}

	// ZHIGHLIGHT_PIC with NS.rectval, numeric pageNo, selLst, and maskRects (dict + direct rect string).
	highlightPic := archive(
		// 1: root dict
		map[string]any{
			"NS.keys":    []any{plist.UID(2), plist.UID(3), plist.UID(4)},
			"NS.objects": []any{plist.UID(5), plist.UID(6), plist.UID(13)},
		},
		"paint", "selLst", "maskRects", // 2, 3, 4
		"clip-md5",                                      // 5
		map[string]any{"NS.objects": []any{plist.UID(7)}}, // 6: selLst array
		// 7: selLst[0] dict
		map[string]any{
			"NS.keys":    []any{plist.UID(8), plist.UID(9)},
			"NS.objects": []any{plist.UID(10), plist.UID(11)},
		},
		"pageNo", "rect", // 8, 9
		int64(4),                                             // 10
		map[string]any{"NS.rectval": plist.UID(12)},          // 11
		"{{78.47, 180.36}, {172.95, 89.92}}",                 // 12
		map[string]any{"NS.objects": []any{plist.UID(14), plist.UID(18)}}, // 13: maskRects array
		// 14: maskRects[0] dict
		map[string]any{
			"NS.keys":    []any{plist.UID(8), plist.UID(9)},
			"NS.objects": []any{plist.UID(15), plist.UID(16)},
		},
		uint64(4),                                   // 15
		map[string]any{"NS.rectval": plist.UID(17)}, // 16
		"{{10, 20}, {30, 40}}",                      // 17
		map[string]any{"NS.rectval": plist.UID(19)}, // 18: direct NSValue rect
		"{{50, 60}, {70, 80}}",                      // 19
	)

	// Multi-block TextNote (2 blocks) + LinkNote.
	qaAndLinkBlocks := archive(
		map[string]any{"NS.objects": []any{plist.UID(2), plist.UID(7), plist.UID(10), plist.UID(14)}},
		// 2: TextNote block 0
		map[string]any{"NS.keys": []any{plist.UID(3), plist.UID(4)}, "NS.objects": []any{plist.UID(5), plist.UID(6)}},
		"type", "text", "TextNote", "【单选】下列哪项正确？\nA. 选项一\nB. 选项二 #高频",
		// 7: TextNote block 1
		map[string]any{"NS.keys": []any{plist.UID(3), plist.UID(4)}, "NS.objects": []any{plist.UID(5), plist.UID(8)}},
		"答案：A\n解析：因为显然成立。",
		"unused",
		// 10: LinkNote via noteid
		map[string]any{"NS.keys": []any{plist.UID(3), plist.UID(11), plist.UID(4)}, "NS.objects": []any{plist.UID(12), plist.UID(13), plist.UID(18)}},
		"noteid", "LinkNote", "EXCERPT-1",
		// 14: LinkNote via URL in text + self link (must be excluded)
		map[string]any{"NS.keys": []any{plist.UID(3), plist.UID(4)}, "NS.objects": []any{plist.UID(12), plist.UID(15)}},
		"参考 marginnote3app://note/TARGET-2 和 marginnote3app://note/QA-1",
		"unused2", "unused3", "",
	)

	// Canvas-level ZTYPE==4 SketchNote (must be filtered out, sketch-md5 must be pruned!).
	canvasSketchBlocks := archive(
		map[string]any{"NS.objects": []any{plist.UID(2)}},
		map[string]any{"NS.keys": []any{plist.UID(3), plist.UID(4)}, "NS.objects": []any{plist.UID(5), plist.UID(6)}},
		"type", "paint", "SketchNote", "sketch-md5",
	)

	// ZBOOKCONFIG with double-encoded AIBookTocMap JSON string.
	tocJSON, _ := json.Marshal([]map[string]any{
		{"title": "第一章 级数", "pageNo": 1, "level": 0},
		{"label": "1.1 收敛判别", "pageNo": 3, "level": 1},
		{"title": "无效项", "pageNo": 0, "level": 1},
	})
	optJSON, _ := json.Marshal(map[string]any{
		"AIBookTocMap": string(tocJSON),
	})
	bookLongMD5 := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	bookShortMD5 := bookLongMD5[:32]
	if _, err := db.Exec(
		"INSERT INTO ZBOOKCONFIG VALUES (1, ?, ?, ?, ?, ?)",
		bookShortMD5, bookLongMD5, "高等数学讲义", 12, string(optJSON),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO ZTOPIC VALUES (1, 'topic-1', '级数专题', 'ROOT-1', '#核心 #数学/级数')",
	); err != nil {
		t.Fatal(err)
	}

	// Root note (layoutMode: 100)
	if _, err := db.Exec(`INSERT INTO ZBOOKNOTE VALUES
		(1, 'ROOT-1', 'topic-1', NULL, '级数总览', '', '', NULL, 'EXCERPT-1|QA-1', NULL, NULL, 'mbooks-annotation1c', 0, '{"mapConfig":{"layoutMode":100}}', ?, 700000000.2, 800000000.8, 256, 0)`,
		bookLongMD5,
	); err != nil {
		t.Fatal(err)
	}
	// Excerpt note: ZSTARTPAGE is NULL, page=4 comes from ZHIGHLIGHT_PIC.selLst, layoutMode=0
	if _, err := db.Exec(`INSERT INTO ZBOOKNOTE VALUES
		(2, 'EXCERPT-1', 'topic-1', 'ROOT-1', '比值判别法', '', '摘录内容 #判别法', NULL, '', ?, NULL, '', 0, '{"mapConfig":{"layoutMode":0}}', ?, 710000000.0, 810000000.0, 256, 1)`,
		highlightPic, bookLongMD5,
	); err != nil {
		t.Fatal(err)
	}
	// Multi-block Q&A note with LinkNote & inline hashtag, layoutMode={} (nil)
	qaBody := "【单选】下列哪项正确？\nA. 选项一\nB. 选项二 #高频\n答案：A\n解析：因为显然成立。"
	if _, err := db.Exec(`INSERT INTO ZBOOKNOTE VALUES
		(3, 'QA-1', 'topic-1', 'ROOT-1', '习题001', ?, '', 9, '', NULL, ?, '', 0, '{"mapConfig":{}}', ?, 720000000.0, 820000000.0, 256, 2)`,
		qaBody, qaAndLinkBlocks, bookShortMD5,
	); err != nil {
		t.Fatal(err)
	}
	// Empty ghost note (ZTYPE=256, ZZINDEX=515)
	if _, err := db.Exec(`INSERT INTO ZBOOKNOTE VALUES
		(4, 'GHOST-1', 'topic-1', 'ROOT-1', '', '', '', NULL, '', NULL, NULL, '', 0, '', ?, 720000000.0, 820000000.0, 256, 515)`,
		bookLongMD5,
	); err != nil {
		t.Fatal(err)
	}
	// Canvas-level untitled ZTYPE=4 SketchNote with ZZINDEX<0
	if _, err := db.Exec(`INSERT INTO ZBOOKNOTE VALUES
		(5, 'SKETCH-BG', 'topic-1', 'ROOT-1', '', '', '', NULL, '', NULL, ?, '', 0, '', ?, 720000000.0, 820000000.0, 4, -2147475456)`,
		canvasSketchBlocks, bookLongMD5,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Package .marginnotes + matching hash-named PDF into a .marginpkg.
	dbBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(tmp, "downloads")
	if err := os.MkdirAll(pkgDir, 0700); err != nil {
		t.Fatal(err)
	}
	pkgPath := filepath.Join(pkgDir, "v5.marginpkg")
	var pkgBuf bytes.Buffer
	zw := zip.NewWriter(&pkgBuf)
	for name, data := range map[string][]byte{
		"v5.marginnotes": dbBytes,
		"VersionBackupSubset/documents/" + bookLongMD5 + ".pdf": []byte("%PDF-1.7\n%%EOF\n"),
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
	if err := os.WriteFile(pkgPath, pkgBuf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	storeDir := filepath.Join(tmp, "store")
	store := NewStore(storeDir)
	res, err := store.Import(pkgPath)
	if err != nil {
		t.Fatal(err)
	}
	book := res.Library.Books[0]
	if book.MediaVersion != 5 {
		t.Fatalf("expected MediaVersion=5, got %d", book.MediaVersion)
	}
	if len(book.Notes) != 3 || len(book.Roots) != 1 || book.Roots[0] != "ROOT-1" {
		t.Fatalf("expected 3 real notes and 1 root (ghost + canvas sketch filtered), got notes=%d roots=%v", len(book.Notes), book.Roots)
	}
	// Unused sketch-md5 PNG must have been pruned so only clip-md5 exists on disk.
	mediaEntries, err := os.ReadDir(filepath.Join(storeDir, "media"))
	if err != nil || len(mediaEntries) != 1 {
		t.Fatalf("expected exactly 1 saved media file (canvas sketch pruned), got %d (%v)", len(mediaEntries), err)
	}

	// Document metadata from ZBOOKCONFIG.
	if len(book.Documents) != 1 {
		t.Fatalf("expected 1 document, got %+v", book.Documents)
	}
	doc := book.Documents[0]
	if doc.Title != "高等数学讲义" || doc.LastPage != 12 {
		t.Fatalf("unexpected document metadata: %+v", doc)
	}
	wantTOC := []TOCEntry{
		{Title: "第一章 级数", Page: 1, Level: 0},
		{Title: "1.1 收敛判别", Page: 3, Level: 1},
	}
	if !slices.Equal(doc.TOC, wantTOC) {
		t.Fatalf("unexpected TOC: got %+v, want %+v", doc.TOC, wantTOC)
	}

	byID := make(map[string]Note, len(book.Notes))
	for _, n := range book.Notes {
		byID[n.ID] = n
	}

	root := byID["ROOT-1"]
	if root.LayoutMode == nil || *root.LayoutMode != 100 {
		t.Fatalf("expected ROOT-1 LayoutMode=100, got %v", root.LayoutMode)
	}
	if root.BookMD5 != bookLongMD5 || root.BookTitle != "高等数学讲义" {
		t.Fatalf("unexpected ROOT-1 book metadata: md5=%q title=%q", root.BookMD5, root.BookTitle)
	}
	if root.CreatedAt != 700000000+coreDataEpochOffset || root.UpdatedAt != 800000001+coreDataEpochOffset {
		t.Fatalf("unexpected ROOT-1 timestamps: created=%d updated=%d", root.CreatedAt, root.UpdatedAt)
	}
	if !slices.Equal(root.Tags, []string{"核心", "数学/级数"}) {
		t.Fatalf("expected ZTOPIC.ZHASHTAGS on root, got %v", root.Tags)
	}

	ex := byID["EXCERPT-1"]
	if ex.Page == nil || *ex.Page != 4 {
		t.Fatalf("expected EXCERPT-1 Page=4 from ZHIGHLIGHT_PIC.selLst, got %v", ex.Page)
	}
	if ex.LayoutMode == nil || *ex.LayoutMode != 0 {
		t.Fatalf("expected EXCERPT-1 LayoutMode=0, got %v", ex.LayoutMode)
	}
	if len(ex.HighlightRects) != 1 || ex.HighlightRects[0].Page != 4 ||
		math.Abs(float64(ex.HighlightRects[0].X-78.47)) > 0.01 ||
		math.Abs(float64(ex.HighlightRects[0].W-172.95)) > 0.01 {
		t.Fatalf("unexpected HighlightRects: %+v", ex.HighlightRects)
	}
	if len(ex.MaskRects) != 2 || ex.MaskRects[0].Page != 4 || ex.MaskRects[1].Page != 4 || ex.MaskRects[1].X != 50 {
		t.Fatalf("unexpected MaskRects: %+v", ex.MaskRects)
	}
	if !slices.Equal(ex.Tags, []string{"判别法"}) {
		t.Fatalf("expected inline #tag on EXCERPT-1, got %v", ex.Tags)
	}

	qa := byID["QA-1"]
	if qa.LayoutMode != nil {
		t.Fatalf("expected nil LayoutMode for empty mapConfig, got %d", *qa.LayoutMode)
	}
	if qa.BookTitle != "高等数学讲义" {
		t.Fatalf("expected short MD5 BookTitle lookup, got %q", qa.BookTitle)
	}
	if len(qa.Blocks) != 2 {
		t.Fatalf("expected 2 TextNote blocks preserved on QA-1, got %d: %+v", len(qa.Blocks), qa.Blocks)
	}
	if !qa.BlocksCoverBody(qa.Body) {
		t.Fatalf("expected BlocksCoverBody true for multi-block TextNote")
	}
	if !slices.Equal(qa.Links, []string{"EXCERPT-1", "TARGET-2"}) {
		t.Fatalf("unexpected QA-1 Links: %v", qa.Links)
	}
	if !slices.Equal(qa.Tags, []string{"高频"}) {
		t.Fatalf("unexpected QA-1 Tags: %v", qa.Tags)
	}

	// Simulate v4 -> v5 RecoverMedia upgrade while preserving local edits and cleaning stale ghosts!
	if _, err := store.SaveNote(res.BookID, "QA-1", "我修改的标题", "我修改的正文"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.update(func(l *Library) (bool, error) {
		b := &l.Books[0]
		b.MediaVersion = 4
		b.Documents[0].Title = "文档 1"
		b.Documents[0].LastPage = 0
		b.Documents[0].TOC = nil
		for i := range b.Notes {
			n := &b.Notes[i]
			if n.ID == "EXCERPT-1" {
				n.Page = nil
				n.HighlightRects = nil
				n.MaskRects = nil
				n.LayoutMode = nil
				n.BookTitle = ""
				n.CreatedAt = 0
				n.Tags = nil
			}
			if n.ID == "QA-1" {
				n.Blocks = nil
				n.Links = nil
				n.Review = "keep-me"
			}
		}
		// Inject stale v4 ghost + stale v4 untitled sketch note to verify cleanup on upgrade.
		b.Notes = append(b.Notes,
			Note{ID: "GHOST-1"},
			Note{ID: "SKETCH-BG", Blocks: []ContentBlock{{Image: "deadbeef.png"}}},
		)
		b.Roots = append(b.Roots, "GHOST-1", "SKETCH-BG")
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.RecoverMedia(pkgDir); err != nil {
		t.Fatal(err)
	}
	upgradedLib, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	ub := upgradedLib.Books[0]
	if ub.MediaVersion != 5 {
		t.Fatalf("RecoverMedia did not bump MediaVersion to 5: %d", ub.MediaVersion)
	}
	if len(ub.Notes) != 3 || len(ub.Roots) != 1 {
		t.Fatalf("RecoverMedia did not clean stale v4 ghosts/sketch: notes=%d roots=%v", len(ub.Notes), ub.Roots)
	}
	if ub.Documents[0].Title != "高等数学讲义" || ub.Documents[0].LastPage != 12 || !slices.Equal(ub.Documents[0].TOC, wantTOC) {
		t.Fatalf("RecoverMedia did not upgrade Document metadata: %+v", ub.Documents[0])
	}
	uByID := map[string]Note{}
	for _, n := range ub.Notes {
		uByID[n.ID] = n
	}
	if uByID["QA-1"].Title != "我修改的标题" || uByID["QA-1"].Body != "我修改的正文" || uByID["QA-1"].Review != "keep-me" {
		t.Fatalf("RecoverMedia clobbered local edits: %+v", uByID["QA-1"])
	}
	if len(uByID["QA-1"].Blocks) != 2 || !slices.Equal(uByID["QA-1"].Links, []string{"EXCERPT-1", "TARGET-2"}) {
		t.Fatalf("RecoverMedia did not restore QA-1 v5 metadata: %+v", uByID["QA-1"])
	}
	if uByID["EXCERPT-1"].Page == nil || *uByID["EXCERPT-1"].Page != 4 || len(uByID["EXCERPT-1"].HighlightRects) != 1 ||
		uByID["EXCERPT-1"].LayoutMode == nil || *uByID["EXCERPT-1"].LayoutMode != 0 ||
		uByID["EXCERPT-1"].BookTitle != "高等数学讲义" || uByID["EXCERPT-1"].CreatedAt == 0 ||
		!slices.Equal(uByID["EXCERPT-1"].Tags, []string{"判别法"}) {
		t.Fatalf("RecoverMedia did not backfill EXCERPT-1 v5 metadata: %+v", uByID["EXCERPT-1"])
	}
}

func TestMergedLinkNoteAndPicNoteBlocks(t *testing.T) {
	pngBytes := makePNG(t, 24, 14, 7)
	archive := func(objects ...any) []byte {
		t.Helper()
		data, err := plist.Marshal(map[string]any{
			"$archiver": "NSKeyedArchiver", "$top": map[string]any{"root": plist.UID(1)},
			"$objects": append([]any{"$null"}, objects...),
		}, plist.BinaryFormat)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	m := &mediaImport{
		sources: map[string][]byte{
			"pic-md5":    pngBytes,
			"merged-md5": pngBytes,
		},
		files: map[string][]byte{},
	}

	// Build a bplist archive containing:
	// 1. LinkNote with q_hpic (merged PDF excerpt image + selLst page/rect) and noteid
	// 2. LinkNote with q_htext (merged text excerpt without image)
	// 3. PicNote with img key (pasted image comment)
	notesBlob := archive(
		// 1: root array [2, 11, 15]
		map[string]any{"NS.objects": []any{plist.UID(2), plist.UID(11), plist.UID(15)}},
		// 2: LinkNote with q_hpic
		map[string]any{
			"NS.keys":    []any{plist.UID(3), plist.UID(4), plist.UID(5)},
			"NS.objects": []any{plist.UID(6), plist.UID(7), plist.UID(8)},
		},
		"type", "noteid", "q_hpic", "LinkNote", "MERGED-1",
		// 8: q_hpic dict {paint: "merged-md5", selLst: [9]}
		map[string]any{
			"NS.keys":    []any{plist.UID(19), plist.UID(20)},
			"NS.objects": []any{plist.UID(21), plist.UID(9)},
		},
		// 9: selLst array [10]
		map[string]any{"NS.objects": []any{plist.UID(10)}},
		// 10: sel entry {pageNo: 5, rect: "{{10, 20}, {100, 40}}"}
		map[string]any{
			"NS.keys":    []any{plist.UID(22), plist.UID(23)},
			"NS.objects": []any{plist.UID(24), plist.UID(25)},
		},
		// 11: LinkNote with q_htext only
		map[string]any{
			"NS.keys":    []any{plist.UID(3), plist.UID(12), plist.UID(4)},
			"NS.objects": []any{plist.UID(6), plist.UID(13), plist.UID(14)},
		},
		"q_htext", "合并的纯文本摘录段落", "MERGED-2",
		// 15: PicNote with img key
		map[string]any{
			"NS.keys":    []any{plist.UID(3), plist.UID(16)},
			"NS.objects": []any{plist.UID(17), plist.UID(18)},
		},
		"img", "PicNote", "pic-md5",
		"paint", "selLst", "merged-md5", "pageNo", "rect", int64(5), "{{10, 20}, {100, 40}}",
	)

	var n Note
	n.ID = "HOST-1"
	m.attach(&n, nil, notesBlob)

	if len(n.Links) != 2 || n.Links[0] != "MERGED-1" || n.Links[1] != "MERGED-2" {
		t.Fatalf("expected 2 merged links, got %v", n.Links)
	}
	if n.Page == nil || *n.Page != 5 {
		t.Fatalf("expected page 5 extracted from LinkNote q_hpic, got %v", n.Page)
	}
	if len(n.HighlightRects) != 1 || n.HighlightRects[0].Page != 5 || n.HighlightRects[0].W != 100 {
		t.Fatalf("expected highlight rect from LinkNote q_hpic, got %+v", n.HighlightRects)
	}
	if len(n.Blocks) != 3 {
		t.Fatalf("expected 3 blocks (merged image, merged text, PicNote image), got %+v", n.Blocks)
	}
	if n.Blocks[0].Image == "" || n.Blocks[1].Text != "合并的纯文本摘录段落" || n.Blocks[2].Image == "" {
		t.Fatalf("unexpected block contents: %+v", n.Blocks)
	}
}

func buildEmptyPKDrawing(t *testing.T, gzipped, tombstonedOnly bool) []byte {
	t.Helper()
	var proto []byte
	if tombstonedOnly {
		var constBuf [6]byte
		binary.LittleEndian.PutUint32(constBuf[0:4], math.Float32bits(6))
		binary.LittleEndian.PutUint16(constBuf[4:6], 32767)
		var pt [12]byte
		binary.LittleEndian.PutUint32(pt[0:4], math.Float32bits(10))
		binary.LittleEndian.PutUint32(pt[4:8], math.Float32bits(10))
		var path []byte
		path = appendTag(path, 3, 0)
		path = appendVarint(path, 1)
		path = appendTag(path, 4, 0)
		path = appendVarint(path, 3)
		path = appendTag(path, 5, 0)
		path = appendVarint(path, (1<<2)|(1<<8))
		path = appendBytesField(path, 6, constBuf[:])
		path = appendBytesField(path, 7, pt[:])
		var stroke []byte
		stroke = appendTag(stroke, 4, 0)
		stroke = appendVarint(stroke, 0)
		stroke = appendBytesField(stroke, 5, path)
		stroke = appendTag(stroke, 8, 0)
		stroke = appendVarint(stroke, 1) // tombstoned flag
		proto = appendBytesField(proto, 5, stroke)
	}
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

func writeMockInkRenderer(t *testing.T, dir, logPath, sleepSec string, pngBytes []byte) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell mock ink renderer not supported on Windows")
	}
	pngPath := filepath.Join(dir, "mock-ink-out.png")
	if err := os.WriteFile(pngPath, pngBytes, 0600); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dir, "mock-ink.sh")
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	if logPath != "" {
		script.WriteString("printf '%s %s %s\\n' \"$1\" \"$2\" \"$3\" >> '" + logPath + "'\n")
	}
	if sleepSec != "" {
		script.WriteString("sleep " + sleepSec + "\n")
	}
	script.WriteString("cat '" + pngPath + "'\n")
	if err := os.WriteFile(scriptPath, []byte(script.String()), 0700); err != nil {
		t.Fatal(err)
	}
	return scriptPath
}

func TestImportDecouplesSQLiteCursorFromSlowInkRendering(t *testing.T) {
	origTimeout := sqliteReadTimeout
	sqliteReadTimeout = 120 * time.Millisecond
	t.Cleanup(func() { sqliteReadTimeout = origTimeout })

	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "decoupled.marginnotes")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE TABLE ZTOPIC (Z_PK INTEGER, ZTOPICID TEXT, ZTITLE TEXT, ZMINDLINKS TEXT)",
		"CREATE TABLE ZBOOKNOTE (Z_PK INTEGER, ZNOTEID TEXT, ZTOPICID TEXT, ZGROUPNOTEID TEXT, ZNOTETITLE TEXT, ZNOTES_TEXT TEXT, ZHIGHLIGHT_TEXT TEXT, ZSTARTPAGE INTEGER, ZMINDLINKS TEXT, ZHIGHLIGHT_PIC BLOB, ZNOTES BLOB)",
		"CREATE TABLE ZMEDIA (ZMD5 TEXT, ZDATA BLOB)",
		"INSERT INTO ZTOPIC VALUES (1, 'topic-slow', '解耦测试', 'n1|n2')",
	} {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	archive := func(objects ...any) []byte {
		t.Helper()
		data, err := plist.Marshal(map[string]any{
			"$archiver": "NSKeyedArchiver", "$top": map[string]any{"root": plist.UID(1)},
			"$objects": append([]any{"$null"}, objects...),
		}, plist.BinaryFormat)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	pk1 := buildSyntheticPKDrawing(t, false)
	pk2 := buildSyntheticPKDrawing(t, true)
	// Place nested child key BEFORE its wrapper row to verify Pass 2 fetch too.
	nestedWrap := archive(
		map[string]any{"NS.keys": []any{plist.UID(2)}, "NS.objects": []any{plist.UID(3)}},
		"drawing2", "child-draw-key",
	)
	for _, row := range []struct {
		key  string
		data []byte
	}{
		{"unreferenced-pdf-json", []byte(`{"pages":[1,2,3]}`)},
		{"child-draw-key", archive(map[string]any{"NS.data": pk2})},
		{"draw-key-1", archive(map[string]any{"NS.data": pk1})},
		{"wrap-draw-key", nestedWrap},
	} {
		if _, err := db.Exec("INSERT INTO ZMEDIA VALUES (?, ?)", row.key, row.data); err != nil {
			t.Fatal(err)
		}
	}

	clip1 := archive(
		map[string]any{"NS.keys": []any{plist.UID(2)}, "NS.objects": []any{plist.UID(3)}},
		"drawing", "draw-key-1",
	)
	clip2 := archive(
		map[string]any{"NS.keys": []any{plist.UID(2)}, "NS.objects": []any{plist.UID(3)}},
		"drawing", "wrap-draw-key",
	)
	if _, err := db.Exec("INSERT INTO ZBOOKNOTE VALUES (1, 'n1', 'topic-slow', NULL, '笔记一', '', '', 1, '', ?, NULL)", clip1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO ZBOOKNOTE VALUES (2, 'n2', 'topic-slow', NULL, '笔记二', '', '', 2, '', ?, NULL)", clip2); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Mock ink renderer sleeps 300ms per call (> 120ms sqliteReadTimeout).
	// If media.attach ran inside the SQLite cursor loop, this would deterministically fail with context deadline exceeded.
	mockInk := writeMockInkRenderer(t, tmp, "", "0.3", makePNG(t, 32, 16, 5))
	store := NewStore(filepath.Join(tmp, "store"))
	store.SetInkRenderer(mockInk)

	res, err := store.Import(dbPath)
	if err != nil {
		t.Fatalf("expected import to succeed after SQLite closed before slow ink rendering, got: %v", err)
	}
	if len(res.Library.Books) != 1 || len(res.Library.Books[0].Notes) != 2 {
		t.Fatalf("unexpected imported books/notes: %+v", res.Library.Books)
	}
	for _, n := range res.Library.Books[0].Notes {
		if n.Clip == "" {
			t.Fatalf("expected rendered clip for note %s", n.ID)
		}
	}
}

func TestImportMemoizesDuplicatePKDrawingsAndSkipsEmptyDrawings(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "memo-empty.marginnotes")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE TABLE ZTOPIC (Z_PK INTEGER, ZTOPICID TEXT, ZTITLE TEXT, ZMINDLINKS TEXT)",
		"CREATE TABLE ZBOOKNOTE (Z_PK INTEGER, ZNOTEID TEXT, ZTOPICID TEXT, ZGROUPNOTEID TEXT, ZNOTETITLE TEXT, ZNOTES_TEXT TEXT, ZHIGHLIGHT_TEXT TEXT, ZSTARTPAGE INTEGER, ZMINDLINKS TEXT, ZHIGHLIGHT_PIC BLOB, ZNOTES BLOB)",
		"CREATE TABLE ZMEDIA (ZMD5 TEXT, ZDATA BLOB)",
		"INSERT INTO ZTOPIC VALUES (1, 'topic-memo', '缓存测试', 'd1|d2|d3|d4|e1|e2')",
	} {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	archive := func(objects ...any) []byte {
		t.Helper()
		data, err := plist.Marshal(map[string]any{
			"$archiver": "NSKeyedArchiver", "$top": map[string]any{"root": plist.UID(1)},
			"$objects": append([]any{"$null"}, objects...),
		}, plist.BinaryFormat)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	pkNonEmpty := buildSyntheticPKDrawing(t, false)
	pkEmptyRaw := buildEmptyPKDrawing(t, false, false)
	pkEmptyTombstone := buildEmptyPKDrawing(t, true, true)
	baseClip := makePNG(t, 40, 20, 11)

	for _, row := range []struct {
		key  string
		data []byte
	}{
		{"draw-dup", archive(map[string]any{"NS.data": pkNonEmpty})},
		{"draw-alias", archive(map[string]any{"NS.data": pkNonEmpty})},
		{"draw-empty-raw", archive(map[string]any{"NS.data": pkEmptyRaw})},
		{"draw-empty-tomb", archive(map[string]any{"NS.data": pkEmptyTombstone})},
		{"base-clip", archive(map[string]any{"NS.data": baseClip})},
	} {
		if _, err := db.Exec("INSERT INTO ZMEDIA VALUES (?, ?)", row.key, row.data); err != nil {
			t.Fatal(err)
		}
	}

	makeDrawClip := func(drawKey string) []byte {
		return archive(
			map[string]any{"NS.keys": []any{plist.UID(2)}, "NS.objects": []any{plist.UID(3)}},
			"drawing", drawKey,
		)
	}
	makePaintDrawClip := func(paintKey, drawKey string) []byte {
		return archive(
			map[string]any{
				"NS.keys":    []any{plist.UID(2), plist.UID(3)},
				"NS.objects": []any{plist.UID(4), plist.UID(5)},
			},
			"paint", "drawing", paintKey, drawKey,
		)
	}

	inserts := []struct {
		pk   int
		id   string
		clip []byte
	}{
		{1, "d1", makeDrawClip("draw-dup")},
		{2, "d2", makeDrawClip("draw-dup")},
		{3, "d3", makeDrawClip("draw-dup")},
		{4, "d4", makeDrawClip("draw-alias")},
		{5, "e1", makePaintDrawClip("base-clip", "draw-empty-raw")},
		{6, "e2", makePaintDrawClip("base-clip", "draw-empty-tomb")},
	}
	for _, item := range inserts {
		if _, err := db.Exec("INSERT INTO ZBOOKNOTE VALUES (?, ?, 'topic-memo', NULL, ?, '', '', 1, '', ?, NULL)", item.pk, item.id, item.id, item.clip); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(tmp, "ink-calls.log")
	mockInk := writeMockInkRenderer(t, tmp, logPath, "", makePNG(t, 24, 24, 33))
	store := NewStore(filepath.Join(tmp, "store"))
	store.SetInkRenderer(mockInk)

	res, err := store.Import(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	logRaw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("expected mock ink renderer log file: %v", err)
	}
	calls := strings.Split(strings.TrimSpace(string(logRaw)), "\n")
	if len(calls) != 1 {
		t.Fatalf("expected external ink renderer invoked EXACTLY ONCE (4 duplicates memoized, 2 empty skipped), got %d calls:\n%s", len(calls), string(logRaw))
	}

	notes := res.Library.Books[0].Notes
	if len(notes) != 6 {
		t.Fatalf("expected 6 notes, got %d", len(notes))
	}
	firstDupClip := notes[0].Clip
	if firstDupClip == "" {
		t.Fatal("expected non-empty rendered clip on d1")
	}
	for i := 1; i < 4; i++ {
		if notes[i].Clip != firstDupClip {
			t.Fatalf("note %s clip=%q != d1 clip=%q", notes[i].ID, notes[i].Clip, firstDupClip)
		}
	}
	// Empty-drawing notes must fall back cleanly to the original base clip bytes.
	for i := 4; i < 6; i++ {
		if notes[i].Clip == "" {
			t.Fatalf("empty-drawing note %s lost its base clip", notes[i].ID)
		}
		saved, err := os.ReadFile(store.MediaPath(notes[i].Clip))
		if err != nil || !bytes.Equal(saved, baseClip) {
			t.Fatalf("empty-drawing note %s clip bytes differ from base clip (err=%v)", notes[i].ID, err)
		}
	}
}

func TestDirectRGBACompositingOverBaseClip(t *testing.T) {
	pk := buildSyntheticPKDrawing(t, false)

	// 1. Direct renderPKDrawingImage API contract.
	rgba, err := renderPKDrawingImage(pk, 100, 100, 100)
	if err != nil || rgba == nil {
		t.Fatalf("renderPKDrawingImage failed: rgba=%v err=%v", rgba, err)
	}
	if rgba.Bounds().Dx() != 100 || rgba.Bounds().Dy() != 100 {
		t.Fatalf("unexpected RGBA bounds: %v", rgba.Bounds())
	}
	if emptyRGBA, err := renderPKDrawingImage(buildEmptyPKDrawing(t, false, false), 100, 100, 100); err != nil || emptyRGBA != nil {
		t.Fatalf("expected (nil, nil) for 0-stroke PKDrawing, got rgba=%v err=%v", emptyRGBA, err)
	}
	if _, err := renderPKDrawingImage([]byte("not-a-pkdrawing"), 100, 100, 100); err == nil {
		t.Fatal("expected error for invalid PKDrawing bytes")
	}

	// 2. Build a solid blue (#0080ff) 100x100 base clip PNG and composite ink directly over it
	//    via BOTH unshared in-place path and shared memoized path.
	base := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			base.SetNRGBA(x, y, color.NRGBA{R: 0, G: 0x80, B: 0xff, A: 0xff})
		}
	}
	var baseBuf bytes.Buffer
	if err := png.Encode(&baseBuf, base); err != nil {
		t.Fatal(err)
	}

	key := inkCacheKey{hash: sha256.Sum256(pk), width: 100, height: 100, pixels: 100}
	for _, tc := range []struct {
		name   string
		counts map[inkCacheKey]int
	}{
		{name: "unshared-direct-inplace", counts: map[inkCacheKey]int{key: 1}},
		{name: "shared-memoized-rgba", counts: map[inkCacheKey]int{key: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMediaImport("", nil)
			m.sources["clip"] = baseBuf.Bytes()
			m.sources["draw"] = pk
			m.inkCounts = tc.counts

			out := m.picture(map[string]any{"paint": "clip", "drawing": "draw"})
			if len(out) == 0 {
				t.Fatal("expected composited PNG bytes")
			}
			img, err := png.Decode(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("composited output is not valid PNG: %v", err)
			}
			if img.Bounds().Dx() != 100 || img.Bounds().Dy() != 100 {
				t.Fatalf("unexpected composited bounds: %v", img.Bounds())
			}
			// Background pixel (5, 5) stays solid blue (#0080ff).
			br, bg, bb, ba := img.At(5, 5).RGBA()
			if br > 0x0500 || bg < 0x7000 || bg > 0x9000 || bb < 0xf000 || ba < 0xf000 {
				t.Fatalf("background pixel (5,5) corrupted: r=%#x g=%#x b=%#x a=%#x", br, bg, bb, ba)
			}
			// Opaque black pen stroke at (40, 20) covers the blue base.
			pr, pg, pb, pa := img.At(40, 20).RGBA()
			if pr > 0x1000 || pg > 0x1000 || pb > 0x1000 || pa < 0xf000 {
				t.Fatalf("expected opaque black pen stroke at (40,20), got r=%#x g=%#x b=%#x a=%#x", pr, pg, pb, pa)
			}
			// Translucent red marker at (40, 50) blends red over blue base (both red and blue channels present).
			mr, _, mb, ma := img.At(40, 50).RGBA()
			if mr < 0x4000 || mb < 0x5000 || ma < 0xf000 {
				t.Fatalf("expected red marker blended over blue base at (40,50), got r=%#x b=%#x a=%#x", mr, mb, ma)
			}
			// Tombstoned stroke at (40, 85) leaves blue base untouched.
			tr, tg, tb, ta := img.At(40, 85).RGBA()
			if tr > 0x0500 || tg < 0x7000 || tb < 0xf000 || ta < 0xf000 {
				t.Fatalf("tombstoned stroke altered (40,85): r=%#x g=%#x b=%#x a=%#x", tr, tg, tb, ta)
			}
		})
	}
}

func TestSharedCompositedDrawingProducesSingleDeterministicMediaHash(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "shared-composite.marginnotes")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE TABLE ZTOPIC (Z_PK INTEGER, ZTOPICID TEXT, ZTITLE TEXT, ZMINDLINKS TEXT)",
		"CREATE TABLE ZBOOKNOTE (Z_PK INTEGER, ZNOTEID TEXT, ZTOPICID TEXT, ZGROUPNOTEID TEXT, ZNOTETITLE TEXT, ZNOTES_TEXT TEXT, ZHIGHLIGHT_TEXT TEXT, ZSTARTPAGE INTEGER, ZMINDLINKS TEXT, ZHIGHLIGHT_PIC BLOB, ZNOTES BLOB)",
		"CREATE TABLE ZMEDIA (ZMD5 TEXT, ZDATA BLOB)",
		"INSERT INTO ZTOPIC VALUES (1, 'topic-shared-comp', '共享合成测试', 'c1|c2|c3|c4|c5|c6')",
	} {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	archive := func(objects ...any) []byte {
		t.Helper()
		data, err := plist.Marshal(map[string]any{
			"$archiver": "NSKeyedArchiver", "$top": map[string]any{"root": plist.UID(1)},
			"$objects": append([]any{"$null"}, objects...),
		}, plist.BinaryFormat)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	// Build a solid blue 100x100 base clip and a PKDrawing with translucent
	// marker and antialiased pen edges that previously split hashes between
	// Consumer 1 (premul RGBA) and Consumers 2..N (decoded PNG NRGBA).
	base := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			base.SetNRGBA(x, y, color.NRGBA{R: 0x20, G: 0x70, B: 0xe0, A: 0xff})
		}
	}
	var baseBuf bytes.Buffer
	if err := png.Encode(&baseBuf, base); err != nil {
		t.Fatal(err)
	}
	pk := buildSyntheticPKDrawing(t, false)
	if _, err := db.Exec("INSERT INTO ZMEDIA VALUES (?, ?)", "base-clip", archive(map[string]any{"NS.data": baseBuf.Bytes()})); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO ZMEDIA VALUES (?, ?)", "shared-draw", archive(map[string]any{"NS.data": pk})); err != nil {
		t.Fatal(err)
	}
	clip := archive(
		map[string]any{
			"NS.keys":    []any{plist.UID(2), plist.UID(3)},
			"NS.objects": []any{plist.UID(4), plist.UID(5)},
		},
		"paint", "drawing", "base-clip", "shared-draw",
	)
	for i := 1; i <= 6; i++ {
		id := "c" + string(rune('0'+i))
		if _, err := db.Exec("INSERT INTO ZBOOKNOTE VALUES (?, ?, 'topic-shared-comp', NULL, ?, '', '', 1, '', ?, NULL)", i, id, id, clip); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	storeDir := filepath.Join(tmp, "store")
	store := NewStore(storeDir)
	res, err := store.Import(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	notes := res.Library.Books[0].Notes
	if len(notes) != 6 {
		t.Fatalf("expected 6 notes, got %d", len(notes))
	}
	wantClip := notes[0].Clip
	if wantClip == "" {
		t.Fatal("expected non-empty composited clip hash")
	}
	for i := 1; i < len(notes); i++ {
		if notes[i].Clip != wantClip {
			t.Fatalf("shared composited note %s got distinct hash %q != %q (non-deterministic split!)", notes[i].ID, notes[i].Clip, wantClip)
		}
	}
	entries, err := os.ReadDir(filepath.Join(storeDir, "media"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly 1 deduplicated composited media file on disk, got %d (err=%v)", len(entries), err)
	}
}

func (s *Store) update(change func(*Library) (bool, error)) (Library, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	library, err := s.read()
	if err != nil {
		return Library{}, err
	}
	oldFiles := make(map[string]bool, len(library.Books))
	for _, b := range library.Books {
		oldFiles[bookFileName(b.ID)] = true
	}
	write, err := change(&library)
	if err != nil || !write {
		return library, err
	}
	if err := s.writeAllShards(library, nil); err != nil {
		return Library{}, err
	}
	kept := make(map[string]bool, len(library.Books))
	for _, b := range library.Books {
		kept[bookFileName(b.ID)] = true
	}
	for file := range oldFiles {
		if !kept[file] {
			_ = os.Remove(filepath.Join(s.booksDir(), file))
		}
	}
	return library, nil
}
