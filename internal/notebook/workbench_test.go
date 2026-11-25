package notebook

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type fixtureNote = struct{ id, title, body, parent, links string }

func TestWorkbookRenameDeleteAndImageExport(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "library"))
	db := filepath.Join(dir, "original.marginnotes")
	picture := makePNG(t, 12, 12, 4)
	notes := []fixtureNote{
		{id: "root", title: "原名", links: "remove|keep"},
		{id: "remove", title: "要删除的笔记", links: "child"},
		{id: "keep", title: "共享节点", links: "remove|child"},
		{id: "child", title: "保留的子笔记", body: "正文"},
	}
	createTestMarginDB(t, db, "topic", "原名", notes, picture)
	original, err := store.Import(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RenameBook(original.BookID, "  本地名称  "); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteNote(original.BookID, "remove"); err != nil {
		t.Fatal(err)
	}

	db = filepath.Join(dir, "updated.marginnotes")
	notes[3].body = "更新的正文"
	createTestMarginDB(t, db, "topic", "来源的新名称", notes, picture)
	updated, err := store.Import(db)
	if err != nil {
		t.Fatal(err)
	}
	book := updated.Library.Books[0]
	if book.Title != "本地名称" || book.SourceTitle != "来源的新名称" || indexNote(&book, "remove") >= 0 {
		t.Fatalf("local changes were lost on reimport: %+v", book)
	}
	if root := book.Notes[indexNote(&book, "root")]; !slices.Equal(root.Children, []string{"child", "keep"}) {
		t.Fatalf("children did not replace the removed node: %v", root.Children)
	}
	if note := book.Notes[indexNote(&book, "keep")]; !slices.Equal(note.Children, []string{"child"}) {
		t.Fatalf("multiple parents introduced duplicate links: %v", note.Children)
	}

	var output bytes.Buffer
	if err := store.ExportMarkdownZIP(updated.BookID, &output); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 2 || archive.File[0].Name != "本地名称/本地名称.md" || archive.File[1].Name != "本地名称/images/"+strings.TrimPrefix(archive.File[1].Name, "本地名称/images/") {
		t.Fatalf("wanted Markdown and one shared image, got %d files: %+v", len(archive.File), archive.File)
	}
	for i, entry := range archive.File {
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			page := string(data)
			imgRel := strings.TrimPrefix(archive.File[1].Name, "本地名称/")
			if !strings.Contains(page, "# 本地名称") || !strings.Contains(page, "![]("+imgRel+")") ||
				!strings.Contains(page, "更新的正文") || strings.Contains(page, "要删除") {
				t.Fatalf("unexpected Markdown: %s", page)
			}
		} else if !bytes.Equal(data, picture) {
			t.Fatal("ZIP image bytes differ from the imported attachment")
		}
	}
}

func TestPackageAndStandalonePDFShareStorage(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "library"))
	db := filepath.Join(dir, "notes.marginnotes")
	createTestMarginDB(t, db, "topic", "笔记", []fixtureNote{{id: "note", title: "笔记"}}, nil)
	source, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture exercises PDF storage and import routing. Platform renderers
	// are exercised separately with complete PDFs.
	pdf := []byte("%PDF-1.7\n%%EOF\n")
	pkg := filepath.Join(dir, "notes.marginpkg")
	var packageData bytes.Buffer
	z := zip.NewWriter(&packageData)
	for name, contents := range map[string][]byte{
		"notes.marginnotes": source, "VersionBackupSubset/documents/原文.pdf": pdf, "__MACOSX/._ignored.pdf": []byte("ignored"),
	} {
		entry, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pkg, packageData.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	imported, err := store.Import(pkg)
	if err != nil {
		t.Fatal(err)
	}
	docs := imported.Library.Books[0].Documents
	if len(docs) != 1 || docs[0].Title != "原文" {
		t.Fatalf("embedded PDF list: %+v", docs)
	}
	pdfPath := filepath.Join(dir, "standalone.pdf")
	if err := os.WriteFile(pdfPath, pdf, 0600); err != nil {
		t.Fatal(err)
	}
	standalone, err := store.Import(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	second := standalone.Library.Books[1]
	if len(second.Documents) != 1 || second.Documents[0].ID != docs[0].ID {
		t.Fatalf("PDF was not deduplicated: %+v", second)
	}
	if _, err := store.DeleteBook(imported.BookID); err != nil {
		t.Fatal(err)
	}
	if saved, err := os.ReadFile(store.DocumentPath(docs[0].ID)); err != nil || !bytes.Equal(saved, pdf) {
		t.Fatalf("shared PDF was removed or changed: %v", err)
	}
	if _, err := store.DeleteBook(standalone.BookID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.DocumentPath(docs[0].ID)); !os.IsNotExist(err) {
		t.Fatalf("orphaned PDF still exists: %v", err)
	}
}

type zeroPadPDFReader struct {
	header []byte
	offset int64
	total  int64
}

func (r *zeroPadPDFReader) Read(p []byte) (int, error) {
	if r.offset >= r.total {
		return 0, io.EOF
	}
	n := 0
	if r.offset < int64(len(r.header)) {
		copied := copy(p, r.header[r.offset:])
		r.offset += int64(copied)
		n += copied
	}
	if n < len(p) && r.offset < r.total {
		rem := r.total - r.offset
		fill := min(int64(len(p)-n), rem)
		clear(p[n : n+int(fill)])
		r.offset += fill
		n += int(fill)
	}
	return n, nil
}

func TestLargePDFOver512MBImportAndProgress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping >512MB streaming write in -short mode")
	}
	const size = 520 << 20
	header := []byte("%PDF-1.7\n% large synthetic payload\n")
	origSpace := availableSpaceFn
	availableSpaceFn = func(string) (int64, error) { return size + (128 << 20), nil }
	t.Cleanup(func() { availableSpaceFn = origSpace })

	store := NewStore(filepath.Join(t.TempDir(), "library"))
	var lastDone, lastTotal int64
	var calls int
	doc, _, err := store.saveDocument(context.Background(), &zeroPadPDFReader{header: header, total: size}, "LargeBook", size, func(done, total int64) {
		calls++
		lastDone, lastTotal = done, total
	})
	if err != nil {
		t.Fatalf("expected >512MB PDF save to succeed, got: %v", err)
	}
	if calls == 0 || lastDone != size || lastTotal != size {
		t.Fatalf("unexpected progress callbacks: calls=%d done=%d total=%d", calls, lastDone, lastTotal)
	}
	info, err := os.Stat(store.DocumentPath(doc.ID))
	if err != nil || info.Size() != size {
		t.Fatalf("saved PDF stat mismatch: info=%v err=%v", info, err)
	}
}

func TestImportRejectsInsufficientDiskSpace(t *testing.T) {
	origSpace := availableSpaceFn
	availableSpaceFn = func(string) (int64, error) { return 10 << 20, nil }
	t.Cleanup(func() { availableSpaceFn = origSpace })

	store := NewStore(filepath.Join(t.TempDir(), "library"))
	_, _, err := store.saveDocument(context.Background(), bytes.NewReader([]byte("%PDF-1.7\n%%EOF\n")), "TightSpace", 50<<20, nil)
	if !errors.Is(err, errNoDiskSpace) {
		t.Fatalf("expected errNoDiskSpace, got: %v", err)
	}
}

func TestSaveDocumentFromPathMovesAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "library"))
	pdf := []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF\n")
	wantID := fmt.Sprintf("%x", sha256.Sum256(pdf))

	src1 := filepath.Join(dir, "picked1.pdf")
	if err := os.WriteFile(src1, pdf, 0600); err != nil {
		t.Fatal(err)
	}
	res1, err := store.ImportWithOptions(src1, ImportOptions{CanMoveSource: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src1); !os.IsNotExist(err) {
		t.Fatalf("expected moved source file to be gone, stat err=%v", err)
	}
	if len(res1.Library.Books) != 1 || res1.Library.Books[0].Documents[0].ID != wantID {
		t.Fatalf("unexpected moved PDF import result: %+v", res1)
	}

	// Second import of identical bytes with CanMoveSource=true deduplicates cleanly.
	src2 := filepath.Join(dir, "picked2.pdf")
	if err := os.WriteFile(src2, pdf, 0600); err != nil {
		t.Fatal(err)
	}
	res2, err := store.ImportWithOptions(src2, ImportOptions{CanMoveSource: true})
	if err != nil {
		t.Fatal(err)
	}
	if res2.BookID != res1.BookID || len(res2.Library.Books) != 1 {
		t.Fatalf("expected deduplicated book, got: %+v", res2)
	}
}

func TestCleanupStaleTempFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	for _, sub := range []string{"documents", "books", "media"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	oldTmp := filepath.Join(dir, "documents", ".pdf-stale")
	freshTmp := filepath.Join(dir, "documents", ".pdf-fresh")
	realPDF := filepath.Join(dir, "documents", strings.Repeat("a", 64)+".pdf")
	oldMetaTmp := filepath.Join(dir, "books", ".mgnote-old")
	for _, p := range []string{oldTmp, freshTmp, realPDF, oldMetaTmp} {
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	staleTime := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{oldTmp, realPDF, oldMetaTmp} {
		if err := os.Chtimes(p, staleTime, staleTime); err != nil {
			t.Fatal(err)
		}
	}

	store.CleanupStaleTempFiles(time.Hour)

	if _, err := os.Stat(oldTmp); !os.IsNotExist(err) {
		t.Fatalf("expected stale .pdf-* temp removed, err=%v", err)
	}
	if _, err := os.Stat(oldMetaTmp); !os.IsNotExist(err) {
		t.Fatalf("expected stale .mgnote-* temp removed, err=%v", err)
	}
	if _, err := os.Stat(freshTmp); err != nil {
		t.Fatalf("expected fresh temp preserved, err=%v", err)
	}
	if _, err := os.Stat(realPDF); err != nil {
		t.Fatalf("expected real PDF preserved, err=%v", err)
	}
}

func TestExportMarkdownZIPSkipsMissingImages(t *testing.T) {
	store := NewStore(t.TempDir())
	book := Book{
		ID:    "export-missing",
		Title: "///",
		Roots: []string{"n1"},
		Notes: []Note{{
			ID:     "n1",
			Title:  "附图节点",
			Body:   "正文",
			Clip:   "missing-image.png",
			Blocks: []ContentBlock{{Image: "missing-image.png"}},
		}},
	}
	if _, err := store.update(func(l *Library) (bool, error) {
		l.Books = []Book{book}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	missing, err := store.ExportMarkdownZIPWithStats(book.ID, &out)
	if err != nil {
		t.Fatalf("expected export to succeed skipping missing media, got err=%v", err)
	}
	if missing != 1 {
		t.Fatalf("expected missing=1, got %d", missing)
	}
	archive, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 1 || archive.File[0].Name != "Folio/Folio.md" {
		t.Fatalf("expected fallback Folio/Folio.md entry, got %+v", archive.File)
	}
}

func TestSaveAndAddNoteWithMetaAndReparent(t *testing.T) {
	store := NewStore(t.TempDir())
	c0 := 2
	book := Book{
		ID:    "meta-book",
		Title: "分类测试",
		Roots: []string{"p1", "p2"},
		Notes: []Note{
			{ID: "p1", Title: "父节点一", Children: []string{"c1"}},
			{ID: "c1", Title: "子节点", Body: "正文", Color: &c0, Children: []string{"g1"}},
			{ID: "g1", Title: "孙节点"},
			{ID: "p2", Title: "父节点二"},
		},
	}
	if _, err := store.update(func(l *Library) (bool, error) {
		l.Books = []Book{book}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	// NormalizeTags strips # and deduplicates.
	if got := NormalizeTags([]string{" #重点", "重点", "#错题", ""}); !slices.Equal(got, []string{"重点", "错题"}) {
		t.Fatalf("NormalizeTags unexpected: %v", got)
	}

	// Reject cycle (moving c1 into its descendant g1).
	badTarget := "g1"
	if _, err := store.SaveNoteWithMeta(book.ID, "c1", "子节点", "正文", &c0, []string{"重点"}, &badTarget); err == nil {
		t.Fatal("expected cycle move error")
	}

	// Move c1 under p2 with updated color 7 and tags.
	c7 := 7
	newParent := "p2"
	lib, err := store.SaveNoteWithMeta(book.ID, "c1", "子节点-改", "正文-改", &c7, []string{"#重点", "高数", "重点"}, &newParent)
	if err != nil {
		t.Fatalf("SaveNoteWithMeta failed: %v", err)
	}
	b := lib.Books[0]
	n := b.Notes[indexNote(&b, "c1")]
	if !n.MetaEdited || n.Color == nil || *n.Color != 7 || !slices.Equal(n.Tags, []string{"重点", "高数"}) {
		t.Fatalf("unexpected note meta: %+v", n)
	}
	if p1 := b.Notes[indexNote(&b, "p1")]; slices.Contains(p1.Children, "c1") {
		t.Fatalf("p1 should no longer contain c1: %v", p1.Children)
	}
	if p2 := b.Notes[indexNote(&b, "p2")]; !slices.Contains(p2.Children, "c1") {
		t.Fatalf("p2 should contain moved c1: %v", p2.Children)
	}

	// Add child with meta under p2.
	c3 := 3
	lib2, newID, err := store.AddNoteWithMeta(book.ID, "p2", "新增卡片", "内容", &c3, []string{"新增"})
	if err != nil {
		t.Fatal(err)
	}
	b2 := lib2.Books[0]
	added := b2.Notes[indexNote(&b2, newID)]
	if added.Color == nil || *added.Color != 3 || !slices.Equal(added.Tags, []string{"新增"}) {
		t.Fatalf("unexpected added note meta: %+v", added)
	}
}
