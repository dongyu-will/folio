package main

import (
	"errors"
	"sync"

	"github.com/dongyu-will/folio/internal/notebook"
)

type drawingSave struct {
	bookID        string
	before, after notebook.Drawing
}

type drawingWriter struct {
	mu      sync.Mutex
	waiting map[string]drawingSave
	failed  map[string]drawingSave
	active  *drawingSave
	idle    chan struct{}
}

func drawingKey(bookID string) string {
	if topic := bookTopicKey(bookID); topic != "" {
		return "topic:" + topic
	}
	return "book:" + bookID
}

func (w *drawingWriter) enqueue(store *notebook.Store, job drawingSave, report func(error)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := drawingKey(job.bookID)
	if older, ok := w.waiting[key]; ok {
		job.before = older.before
	} else if older, ok := w.failed[key]; ok {
		job.before = older.before
		delete(w.failed, key)
	}
	if w.waiting == nil {
		w.waiting = make(map[string]drawingSave)
	}
	w.waiting[key] = job
	if w.idle == nil {
		w.idle = make(chan struct{})
		go w.run(store, report)
	}
}

func (w *drawingWriter) run(store *notebook.Store, report func(error)) {
	for {
		w.mu.Lock()
		var key string
		var job drawingSave
		for key, job = range w.waiting {
			break
		}
		if key == "" {
			close(w.idle)
			w.idle = nil
			w.mu.Unlock()
			return
		}
		delete(w.waiting, key)
		w.active = &job
		w.mu.Unlock()

		err := store.SaveDrawingChange(job.bookID, job.before, job.after)
		w.mu.Lock()
		w.active = nil
		if err != nil {
			if next, ok := w.waiting[key]; ok {
				next.before = job.before
				w.waiting[key] = next
			} else {
				if w.failed == nil {
					w.failed = make(map[string]drawingSave)
				}
				w.failed[key] = job
			}
		}
		w.mu.Unlock()
		if err != nil {
			report(err)
		}
	}
}

func (w *drawingWriter) latest(bookID string) (notebook.Drawing, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := drawingKey(bookID)
	if job, ok := w.waiting[key]; ok {
		return job.after, true
	}
	if w.active != nil && drawingKey(w.active.bookID) == key {
		return w.active.after, true
	}
	job, ok := w.failed[key]
	return job.after, ok
}

func (w *drawingWriter) wait() {
	w.mu.Lock()
	idle := w.idle
	w.mu.Unlock()
	if idle != nil {
		<-idle
	}
}

func (w *drawingWriter) forget(bookID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.failed, drawingKey(bookID))
}

func (w *drawingWriter) flush(store *notebook.Store) error {
	w.wait()
	w.mu.Lock()
	failed := w.failed
	w.failed = nil
	w.mu.Unlock()
	var errs []error
	for _, job := range failed {
		if err := store.SaveDrawingChange(job.bookID, job.before, job.after); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
