package notebook

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type InkPoint struct {
	X, Y     float32
	Pressure float32
}

type Stroke struct {
	Points []InkPoint
	Color  string
	Width  float32
	Marker bool `json:",omitempty"`
}

// PDFRegion uses coordinates relative to the original page, in the range 0 to 1.
type PDFRegion struct {
	Document string
	Page     int
	X, Y     float32
	W, H     float32
}

type CanvasCard struct {
	ID      string
	Title   string
	X, Y    float32
	W, H    float32
	Anchor  string     `json:",omitempty"`
	Ink     []Stroke   `json:",omitempty"`
	Excerpt *PDFRegion `json:",omitempty"`
}

type Drawing struct {
	Ink   []Stroke            `json:"ink,omitempty"`
	Pages map[string][]Stroke `json:"pages,omitempty"`
	Cards []CanvasCard        `json:"cards,omitempty"`
}

func (s *Store) canvasPath(bookID string) string {
	if topic := bookTopicID(bookID); topic != "" {
		bookID = "topic:" + topic
	}
	return filepath.Join(s.dir, "drawings", fmt.Sprintf("%x.json", sha256.Sum256([]byte(bookID))))
}

func (s *Store) ReadDrawing(bookID string) (Drawing, error) {
	s.drawingMu.Lock()
	defer s.drawingMu.Unlock()
	return s.readDrawing(s.canvasPath(bookID))
}

type drawingCheckpoint struct {
	Drawing
	Sequence uint64 `json:"journalSequence,omitempty"`
}

type drawingJournal struct {
	sequence, checkpoint uint64
	bytes                int64
}

type sliceChange[T any] struct {
	At     int
	Remove int
	Insert []T `json:",omitempty"`
}

type drawingChange struct {
	Ink   *sliceChange[Stroke]            `json:"ink,omitempty"`
	Pages map[string]*sliceChange[Stroke] `json:"pages,omitempty"`
	Cards *sliceChange[CanvasCard]        `json:"cards,omitempty"`
}

func changeSlice[T any](before, after []T, equal func(T, T) bool) *sliceChange[T] {
	if len(before) == len(after) && (len(before) == 0 || &before[0] == &after[0]) {
		return nil
	}
	first := 0
	for first < min(len(before), len(after)) && equal(before[first], after[first]) {
		first++
	}
	oldEnd, newEnd := len(before), len(after)
	if first == oldEnd && first == newEnd {
		return nil
	}
	for oldEnd > first && newEnd > first && equal(before[oldEnd-1], after[newEnd-1]) {
		oldEnd--
		newEnd--
	}
	return &sliceChange[T]{At: first, Remove: oldEnd - first, Insert: after[first:newEnd]}
}

func (c *sliceChange[T]) apply(target []T) ([]T, error) {
	if c == nil {
		return target, nil
	}
	if c.At < 0 || c.At > len(target) || c.Remove < 0 || c.Remove > len(target)-c.At {
		return nil, errors.New("手写日志位置无效")
	}
	return slices.Replace(target, c.At, c.At+c.Remove, c.Insert...), nil
}

func sameStroke(a, b Stroke) bool {
	if a.Color != b.Color || a.Width != b.Width || a.Marker != b.Marker || len(a.Points) != len(b.Points) {
		return false
	}
	return len(a.Points) == 0 || &a.Points[0] == &b.Points[0] || slices.Equal(a.Points, b.Points)
}

func sameCard(a, b CanvasCard) bool {
	if a.ID != b.ID || a.Title != b.Title || a.X != b.X || a.Y != b.Y || a.W != b.W || a.H != b.H || a.Anchor != b.Anchor {
		return false
	}
	if a.Excerpt != b.Excerpt && (a.Excerpt == nil || b.Excerpt == nil || *a.Excerpt != *b.Excerpt) {
		return false
	}
	return changeSlice(a.Ink, b.Ink, sameStroke) == nil
}

func diffDrawing(before, after Drawing) drawingChange {
	change := drawingChange{
		Ink:   changeSlice(before.Ink, after.Ink, sameStroke),
		Cards: changeSlice(before.Cards, after.Cards, sameCard),
	}
	addPage := func(key string, old, next []Stroke) {
		if patch := changeSlice(old, next, sameStroke); patch != nil {
			if change.Pages == nil {
				change.Pages = make(map[string]*sliceChange[Stroke])
			}
			change.Pages[key] = patch
		}
	}
	for key, strokes := range after.Pages {
		addPage(key, before.Pages[key], strokes)
	}
	for key, strokes := range before.Pages {
		if _, ok := after.Pages[key]; !ok {
			addPage(key, strokes, nil)
		}
	}
	return change
}

func (c drawingChange) apply(d *Drawing) error {
	var err error
	if d.Ink, err = c.Ink.apply(d.Ink); err != nil {
		return err
	}
	if d.Cards, err = c.Cards.apply(d.Cards); err != nil {
		return err
	}
	for key, change := range c.Pages {
		strokes, err := change.apply(d.Pages[key])
		if err != nil {
			return err
		}
		if len(strokes) == 0 {
			delete(d.Pages, key)
		} else {
			if d.Pages == nil {
				d.Pages = make(map[string][]Stroke)
			}
			d.Pages[key] = strokes
		}
	}
	return nil
}

func journalDir(path string) string { return strings.TrimSuffix(path, ".json") + ".journal" }

func journalSequence(name string) (uint64, bool) {
	raw, ok := strings.CutSuffix(name, ".json")
	if !ok || len(raw) != 20 {
		return 0, false
	}
	seq, err := strconv.ParseUint(raw, 10, 64)
	return seq, err == nil && seq > 0
}

func (s *Store) readDrawing(path string) (Drawing, error) {
	var base drawingCheckpoint
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &base); err != nil {
			return Drawing{}, fmt.Errorf("无法读取手写数据：%w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Drawing{}, err
	}
	state := drawingJournal{sequence: base.Sequence, checkpoint: base.Sequence}
	dir := journalDir(path)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Drawing{}, err
	}
	for _, entry := range entries {
		seq, ok := journalSequence(entry.Name())
		if !ok || seq <= base.Sequence {
			continue
		}
		if seq != state.sequence+1 {
			return Drawing{}, errors.New("手写日志有缺失的记录")
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return Drawing{}, err
		}
		var change drawingChange
		if err := json.Unmarshal(data, &change); err != nil {
			return Drawing{}, fmt.Errorf("无法读取手写日志：%w", err)
		}
		if err := change.apply(&base.Drawing); err != nil {
			return Drawing{}, err
		}
		state.sequence, state.bytes = seq, state.bytes+int64(len(data))
	}
	if s.drawingJournals == nil {
		s.drawingJournals = make(map[string]drawingJournal)
	}
	s.drawingJournals[path] = state
	return base.Drawing, nil
}

// SaveDrawingChange persists the transition from the last saved state.
// The caller keeps the before and after snapshots immutable during the call.
func (s *Store) SaveDrawingChange(bookID string, before, after Drawing) error {
	change := diffDrawing(before, after)
	if change.Ink == nil && change.Pages == nil && change.Cards == nil {
		return nil
	}
	data, err := json.Marshal(change)
	if err != nil {
		return err
	}
	s.drawingMu.Lock()
	defer s.drawingMu.Unlock()
	path := s.canvasPath(bookID)
	state, ok := s.drawingJournals[path]
	if !ok {
		if _, err := s.readDrawing(path); err != nil {
			return err
		}
		state = s.drawingJournals[path]
	}
	dir := journalDir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	seq := state.sequence + 1
	if err := WriteFile(filepath.Join(dir, fmt.Sprintf("%020d.json", seq)), data); err != nil {
		return err
	}
	state.sequence, state.bytes = seq, state.bytes+int64(len(data))
	// Each patch is already durable. A checkpoint only limits recovery work.
	if (seq-state.checkpoint >= 128 && seq%32 == 0 || state.bytes > 8<<20 && seq%8 == 0) && s.writeDrawingCheckpoint(path, after, seq) == nil {
		state.checkpoint, state.bytes = seq, 0
	}
	s.drawingJournals[path] = state
	return nil
}

func (s *Store) SaveDrawing(bookID string, drawing Drawing) error {
	s.drawingMu.Lock()
	defer s.drawingMu.Unlock()
	path := s.canvasPath(bookID)
	if _, ok := s.drawingJournals[path]; !ok {
		if _, err := s.readDrawing(path); err != nil {
			return err
		}
	}
	seq := s.drawingJournals[path].sequence
	if err := s.writeDrawingCheckpoint(path, drawing, seq); err != nil {
		return err
	}
	s.drawingJournals[path] = drawingJournal{sequence: seq, checkpoint: seq}
	return nil
}

func (s *Store) writeDrawingCheckpoint(path string, drawing Drawing, seq uint64) error {
	data, err := json.Marshal(drawingCheckpoint{Drawing: drawing, Sequence: seq})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := WriteFile(path, data); err != nil {
		return err
	}
	// Any entries left after a crash are covered by journalSequence.
	_ = os.RemoveAll(journalDir(path))
	return nil
}

func (s *Store) deleteDrawing(bookID string) {
	s.drawingMu.Lock()
	defer s.drawingMu.Unlock()
	path := s.canvasPath(bookID)
	_ = os.Remove(path)
	_ = os.RemoveAll(journalDir(path))
	delete(s.drawingJournals, path)
}
