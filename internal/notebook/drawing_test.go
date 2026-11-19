package notebook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestDrawingJournalReplayAndCheckpoint(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	id := "digest/topic"
	original := Stroke{Color: "#1e2530", Points: []InkPoint{{X: 2, Y: 1, Pressure: .3}}}
	legacy := Drawing{Ink: []Stroke{original}, Pages: map[string][]Stroke{"pdf:1": {original}}}
	path := store.canvasPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	oldJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, oldJSON, 0600); err != nil {
		t.Fatal(err)
	}

	current, err := store.ReadDrawing(id)
	if err != nil || !reflect.DeepEqual(current, legacy) {
		t.Fatalf("legacy drawing: %+v, %v", current, err)
	}
	pdfRegion := PDFRegion{Document: "pdf", Page: 1, X: .2, Y: .3, W: .1, H: .2}
	card := CanvasCard{ID: "excerpt", Excerpt: &pdfRegion}
	next := Drawing{Cards: []CanvasCard{card}, Pages: current.Pages}
	if err := store.SaveDrawingChange(id, current, next); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journalDir(path), ".mgnote-interrupted"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	reopen := NewStore(dir)
	read, err := reopen.ReadDrawing(id)
	if err != nil || len(read.Ink) != 0 || len(read.Cards) != 1 || !reflect.DeepEqual(read.Cards[0], card) || len(read.Pages["pdf:1"]) != 1 {
		t.Fatalf("replay across a legacy snapshot: %+v, %v", read, err)
	}
	if rewrite, err := os.ReadFile(path); err != nil || string(rewrite) != string(oldJSON) {
		t.Fatal("one edit rewrote the legacy snapshot", err)
	}
	firstPatch := filepath.Join(journalDir(path), "00000000000000000001.json")
	first, err := os.ReadFile(firstPatch)
	if err != nil {
		t.Fatal(err)
	}

	current = next
	for i := 1; i < 130; i++ {
		next = current
		next.Ink = append(slices.Clone(current.Ink), Stroke{Points: []InkPoint{{X: float32(i)}}})
		if err := reopen.SaveDrawingChange(id, current, next); err != nil {
			t.Fatal(err)
		}
		current = next
	}
	var checkpoint drawingCheckpoint
	checkpointJSON, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(checkpointJSON, &checkpoint); err != nil || checkpoint.Sequence != 128 {
		t.Fatalf("checkpoint sequence = %d, %v", checkpoint.Sequence, err)
	}
	// Emulate a process that wrote the checkpoint and stopped before cleanup.
	if err := os.WriteFile(firstPatch, first, 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewStore(dir).ReadDrawing("new-digest/topic")
	if err != nil || !reflect.DeepEqual(recovered, current) {
		t.Fatalf("checkpoint contents or remaining records did not recover, %v", err)
	}
}
