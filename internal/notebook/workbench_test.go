package notebook

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
	if len(archive.File) != 2 || archive.File[0].Name != "README.md" {
		t.Fatalf("wanted Markdown and one shared image, got %d files", len(archive.File))
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
			if !strings.Contains(page, "# 本地名称") || !strings.Contains(page, "![]("+archive.File[1].Name+")") ||
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
