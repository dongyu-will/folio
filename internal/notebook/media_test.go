package notebook

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

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
