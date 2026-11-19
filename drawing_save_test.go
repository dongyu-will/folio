package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dongyu-will/folio/internal/notebook"
)

func TestDrawingWriterRecoversFailedSaveAndPreservesOrder(t *testing.T) {
	dir := t.TempDir()
	store := notebook.NewStore(dir)
	writer := &drawingWriter{}
	drawingsDir := filepath.Join(dir, "drawings")
	if err := os.WriteFile(drawingsDir, []byte("blocked directory"), 0600); err != nil {
		t.Fatal(err)
	}
	const id = "source/topic"
	errs := make(chan error, 1)
	start := notebook.Drawing{Ink: []inkStroke{{Points: []inkPoint{{X: 1}}}}}
	writer.enqueue(store, drawingSave{bookID: id, after: start}, func(err error) { errs <- err })
	writer.wait()
	select {
	case <-errs:
	default:
		t.Fatal("expected the worker to report the disk error")
	}
	if latest, ok := writer.latest(id); !ok || len(latest.Ink) != 1 {
		t.Fatal("work still waiting for disk should be visible when switching notebooks")
	}
	if err := os.Remove(drawingsDir); err != nil {
		t.Fatal(err)
	}
	before := start
	for i := 2; i <= 32; i++ {
		next := notebook.Drawing{Ink: append(slices.Clone(before.Ink), inkStroke{Points: []inkPoint{{X: float32(i)}}})}
		writer.enqueue(store, drawingSave{bookID: "updated/topic", before: before, after: next}, func(err error) {
			t.Error(err)
		})
		before = next
	}
	if err := writer.flush(store); err != nil {
		t.Fatal(err)
	}
	got, err := notebook.NewStore(dir).ReadDrawing(id)
	if err != nil || len(got.Ink) != 32 {
		t.Fatalf("reopened notebook contains %d strokes, %v", len(got.Ink), err)
	}
	for i, stroke := range got.Ink {
		if stroke.Points[0].X != float32(i+1) {
			t.Fatalf("stroke %d was replayed out of order", i)
		}
	}
}
