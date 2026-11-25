package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo/ui"
)

func newBenchWorkspace(b *testing.B, noteCount int) *workspace {
	b.Helper()
	const branches = 10
	roots := make([]string, 0, branches)
	notes := make([]notebook.Note, 0, noteCount)
	perBranch := max(1, noteCount/branches)
	idx := 0
	for br := 0; br < branches && idx < noteCount; br++ {
		rootID := fmt.Sprintf("root-%d", br)
		roots = append(roots, rootID)
		idx++
		var kids []string
		for c := 1; c < perBranch && idx < noteCount; c++ {
			cid := fmt.Sprintf("note-%d", idx)
			idx++
			kids = append(kids, cid)
			color := idx % 8
			notes = append(notes, notebook.Note{
				ID:    cid,
				Title: fmt.Sprintf("笔记节点 %d", idx),
				Body:  "用于基准测试的示例笔记正文内容",
				Color: &color,
			})
		}
		color := br % 8
		notes = append(notes, notebook.Note{
			ID:       rootID,
			Title:    fmt.Sprintf("主题分支 %d", br),
			Body:     "主题分支摘要",
			Color:    &color,
			Children: kids,
		})
	}
	book := notebook.Book{
		ID:    "bench/book",
		Title: "性能基准测试笔记本",
		Roots: roots,
		Notes: notes,
	}
	app := &workspace{store: notebook.NewStore(b.TempDir())}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)
	return app
}

func BenchmarkMindmap200Notes(b *testing.B) {
	app := newBenchWorkspace(b, 200)
	tt := ui.NewTester(app.view, 1280, 800)
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.panX = float32(i % 20)
		tt.Frame()
	}
}

func BenchmarkOutline200Notes(b *testing.B) {
	app := newBenchWorkspace(b, 200)
	app.settings.GraphLayout = notebook.LayoutOutline
	app.setAllExpanded(true)
	tt := ui.NewTester(app.view, 1280, 800)
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.selected = app.tree.order[i%len(app.tree.order)]
		tt.Frame()
	}
}

func BenchmarkModalOpenClose(b *testing.B) {
	app := newBenchWorkspace(b, 20)
	tt := ui.NewTester(app.view, 1280, 800)
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.preferencesOpen = true
		tt.Frame()
		tt.Advance(350 * time.Millisecond)
		app.preferencesOpen = false
		tt.Frame()
		tt.Advance(350 * time.Millisecond)
	}
}

func BenchmarkIdleFrameCost(b *testing.B) {
	app := newBenchWorkspace(b, 50)
	tt := ui.NewTester(app.view, 1280, 800)
	tt.Frame()
	tt.Advance(500 * time.Millisecond)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tt.Frame()
	}
}
