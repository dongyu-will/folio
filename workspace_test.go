package main

import (
	"slices"
	"sync"
	"testing"

	"github.com/dongyu-will/folio/internal/notebook"
)

func TestSharedDAGNodeKeepsClickedBranchTrail(t *testing.T) {
	book := notebook.Book{
		ID:    "digest/topic",
		Title: "拓扑测试",
		Roots: []string{"b1", "b2"},
		Notes: []notebook.Note{
			{ID: "b1", Title: "分支一", Children: []string{"shared"}},
			{ID: "b2", Title: "分支二", Children: []string{"shared"}},
			{ID: "shared", Title: "共享节点", Body: "共享内容", Children: []string{"leaf"}},
			{ID: "leaf", Title: "叶子节点", Body: "叶子内容"},
		},
	}
	app := &workspace{}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)

	// Default DFS parent of "shared" is "b1".
	if got := app.tree.trail("shared"); !slices.Equal(got, []string{"", "b1", "shared"}) {
		t.Fatalf("unexpected default trail: %v", got)
	}

	// Clicking "shared" under branch 2 in the mindmap must keep branch 2 active.
	branch2Trail := []string{"", "b2", "shared"}
	app.selectGraphNode("shared", branch2Trail)
	if got := app.currentTrail(); !slices.Equal(got, branch2Trail) {
		t.Fatalf("expected active trail %v, got %v", branch2Trail, got)
	}
	if app.scope != "" {
		t.Fatalf("selecting graph node must not overwrite scope, got %q", app.scope)
	}

	nodes, picked := app.mapNodes(600)
	if !slices.Equal(mapNodeTrail(nodes, picked), branch2Trail) {
		t.Fatalf("mindmap jumped branches: picked trail=%v", mapNodeTrail(nodes, picked))
	}

	// Opening child "leaf" from reader extends the branch-2 trail.
	app.openChildNote("leaf")
	wantLeaf := []string{"", "b2", "shared", "leaf"}
	if got := app.currentTrail(); !slices.Equal(got, wantLeaf) {
		t.Fatalf("expected extended trail %v, got %v", wantLeaf, got)
	}
}

func TestSearchNotesCachingAndBlocksText(t *testing.T) {
	book := notebook.Book{
		ID:    "digest/t",
		Title: "高数",
		Roots: []string{"n1", "n2"},
		Notes: []notebook.Note{
			{ID: "n1", Title: "级数收敛", Body: "比值审敛法"},
			{ID: "n2", Title: "泰勒展开", Blocks: []notebook.ContentBlock{{Text: "拉格朗日余项"}}},
		},
	}
	app := &workspace{}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)

	app.query = "拉格朗日"
	hits1 := app.searchNotes()
	if !slices.Equal(hits1, []string{"n2"}) {
		t.Fatalf("expected block text match [n2], got %v", hits1)
	}
	if !app.cachedQueryReady {
		t.Fatal("expected search cache to be populated")
	}
	// Cached call should return identical result slice without recomputation.
	hits2 := app.searchNotes()
	if !slices.Equal(hits2, hits1) {
		t.Fatalf("cached search mismatch: %v vs %v", hits2, hits1)
	}

	// Changing query invalidates and recomputes.
	app.query = "收敛"
	hits3 := app.searchNotes()
	if !slices.Equal(hits3, []string{"n1"}) {
		t.Fatalf("expected [n1], got %v", hits3)
	}
}

func TestApplyImportFeedbackMessages(t *testing.T) {
	book := notebook.Book{ID: "d/t", Title: "Book", Roots: []string{"n1"}, Notes: []notebook.Note{{ID: "n1", Title: "N1"}}}
	lib := notebook.Library{Books: []notebook.Book{book}}
	app := &workspace{}

	app.applyImport(notebook.ImportResult{Library: lib, BookID: book.ID, Added: 1})
	if app.message != "已导入「Book」" || app.messageError {
		t.Fatalf("unexpected added message: %q (err=%v)", app.message, app.messageError)
	}

	app.applyImport(notebook.ImportResult{Library: lib, BookID: book.ID, Updated: 1})
	if app.message != "已更新「Book」" || app.messageError {
		t.Fatalf("unexpected updated message: %q (err=%v)", app.message, app.messageError)
	}

	app.applyImport(notebook.ImportResult{Library: lib, BookID: book.ID})
	if app.message != "该笔记本已在库中，已为你打开" || app.messageError {
		t.Fatalf("unexpected duplicate message: %q (err=%v)", app.message, app.messageError)
	}
}

func TestImportQueueThreadSafety(t *testing.T) {
	app := &workspace{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				app.enqueueImport("/tmp/a.marginpkg")
				app.notifyDownloadsReady()
				_, _ = app.popImportPath()
				_ = app.takeDownloadsPending()
			}
		}()
	}
	wg.Wait()
}

func TestBlocksCoverBodyAndSanitizeExportName(t *testing.T) {
	n := notebook.Note{
		Body:   "第一段 第二段",
		Blocks: []notebook.ContentBlock{{Text: "第一段"}, {Text: "第二段"}},
	}
	if !n.BlocksCoverBody(n.Body) {
		t.Fatal("expected blocks to cover unedited body")
	}
	n.BodyEdited = true
	if n.BlocksCoverBody(n.Body) {
		t.Fatal("edited note must not defer to original blocks")
	}
	if got := sanitizeExportName("高数/级数:总结*?"); got != "高数_级数_总结__" {
		t.Fatalf("unexpected sanitized filename: %q", got)
	}
	if got := sanitizeExportName("   "); got != "Folio" {
		t.Fatalf("expected fallback name Folio, got %q", got)
	}
}
