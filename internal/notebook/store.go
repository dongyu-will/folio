package notebook

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const userNotePrefix = "user-"

type Library struct {
	Books []Book `json:"books"`
}

type libraryIndex struct {
	Version int         `json:"version"`
	Books   []bookEntry `json:"books"`
}

type bookEntry struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Source       string `json:"source"`
	MediaVersion int    `json:"mediaVersion,omitempty"`
	File         string `json:"file"`
}

type Store struct {
	dir             string
	mu              sync.Mutex
	settingsMu      sync.Mutex
	drawingMu       sync.Mutex
	drawingJournals map[string]drawingJournal
	inkRenderer     string
	inFlightMu      sync.Mutex
	inFlightMedia   map[string]int
}

type ImportResult struct {
	Library Library `json:"library"`
	BookID  string  `json:"bookId"`
	Added   int     `json:"added"`
	Updated int     `json:"updated"`
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) SetInkRenderer(path string) { s.inkRenderer = path }

func (s *Store) indexPath() string  { return filepath.Join(s.dir, "index.json") }
func (s *Store) legacyPath() string { return filepath.Join(s.dir, "library.json") }
func (s *Store) booksDir() string   { return filepath.Join(s.dir, "books") }
func (s *Store) mediaDir() string   { return filepath.Join(s.dir, "media") }

func bookFileName(bookID string) string {
	return fmt.Sprintf("%x.json", sha256.Sum256([]byte(bookID)))
}

func (s *Store) MediaPath(name string) string {
	if !mediaName.MatchString(name) {
		return ""
	}
	return filepath.Join(s.mediaDir(), name)
}

func (s *Store) Read() (Library, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

func (s *Store) read() (Library, error) {
	idx, migrated, err := s.readIndex()
	if err != nil {
		return Library{}, err
	}
	if migrated != nil {
		return *migrated, nil
	}
	books := make([]Book, 0, len(idx.Books))
	for _, entry := range idx.Books {
		book, err := s.readBookFile(entry.File)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Library{}, err
		}
		books = append(books, book)
	}
	return Library{Books: books}, nil
}

// readIndex loads index.json, transparently migrating a legacy single-file
// library.json into sharded books/<hash>.json files on first access.
func (s *Store) readIndex() (libraryIndex, *Library, error) {
	data, err := os.ReadFile(s.indexPath())
	if errors.Is(err, os.ErrNotExist) {
		legacyData, legacyErr := os.ReadFile(s.legacyPath())
		if errors.Is(legacyErr, os.ErrNotExist) {
			return libraryIndex{Version: 1, Books: []bookEntry{}}, nil, nil
		}
		if legacyErr != nil {
			return libraryIndex{}, nil, legacyErr
		}
		var legacy Library
		if err := json.Unmarshal(legacyData, &legacy); err != nil {
			return libraryIndex{}, nil, fmt.Errorf("无法读取本地笔记库：%w", err)
		}
		if legacy.Books == nil {
			legacy.Books = []Book{}
		}
		if err := s.writeAllShards(legacy, nil); err != nil {
			return libraryIndex{}, nil, err
		}
		_ = os.Remove(s.legacyPath())
		return makeIndex(legacy.Books), &legacy, nil
	}
	if err != nil {
		return libraryIndex{}, nil, err
	}
	var idx libraryIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return libraryIndex{}, nil, fmt.Errorf("无法读取本地笔记索引：%w", err)
	}
	return idx, nil, nil
}

func (s *Store) readBookFile(file string) (Book, error) {
	if file == "" || file != filepath.Base(file) {
		return Book{}, errors.New("无效的笔记本索引路径")
	}
	data, err := os.ReadFile(filepath.Join(s.booksDir(), file))
	if err != nil {
		return Book{}, err
	}
	var book Book
	if err := json.Unmarshal(data, &book); err != nil {
		return Book{}, fmt.Errorf("无法读取笔记本数据：%w", err)
	}
	return book, nil
}

func makeIndex(books []Book) libraryIndex {
	entries := make([]bookEntry, 0, len(books))
	for _, b := range books {
		entries = append(entries, bookEntry{
			ID:           b.ID,
			Title:        b.Title,
			Source:       b.Source,
			MediaVersion: b.MediaVersion,
			File:         bookFileName(b.ID),
		})
	}
	return libraryIndex{Version: 1, Books: entries}
}

func (s *Store) writeIndex(idx libraryIndex) error {
	data, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	return WriteFile(s.indexPath(), data)
}

func (s *Store) writeBook(book Book) error {
	data, err := json.Marshal(book)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.booksDir(), 0700); err != nil {
		return err
	}
	return WriteFile(filepath.Join(s.booksDir(), bookFileName(book.ID)), data)
}

func (s *Store) writeAllShards(library Library, dirty map[string]bool) error {
	for _, book := range library.Books {
		if dirty != nil && !dirty[book.ID] {
			continue
		}
		if err := s.writeBook(book); err != nil {
			return err
		}
	}
	return s.writeIndex(makeIndex(library.Books))
}

func (s *Store) Import(path string) (ImportResult, error) {
	return s.ImportWithOptions(path, ImportOptions{})
}

// ImportWithPreview publishes a read-only library snapshot for a new notebook
// before loading its attachments. Only the completed import is saved.
func (s *Store) ImportWithPreview(path string, preview func(ImportResult)) (ImportResult, error) {
	return s.ImportWithOptions(path, ImportOptions{Preview: preview})
}

func (s *Store) ImportWithOptions(path string, opts ImportOptions) (ImportResult, error) {
	if detectImportFormat(path) == ".pdf" {
		return s.importPDF(path, opts)
	}
	return s.importFile(path, false, opts)
}

func (s *Store) importFile(path string, existingOnly bool, opts ImportOptions) (ImportResult, error) {
	format := detectImportFormat(path)
	sink := s.newMediaSink()
	defer sink.cleanup()

	var showText func([]Book)
	if opts.Preview != nil {
		showText = func(books []Book) {
			if len(books) == 0 {
				return
			}
			library, err := s.Read()
			if err != nil {
				return
			}
			isNew := func(book Book) bool {
				return indexBook(&library, book.ID) < 0 && indexBookByTopic(&library, book.ID) < 0
			}
			if !isNew(books[0]) {
				return
			}
			for _, book := range books {
				if isNew(book) {
					library.Books = append(library.Books, book)
				}
			}
			opts.Preview(ImportResult{Library: library, BookID: books[0].ID})
		}
	}
	books, files, configs, err := readMarginNoteWithCtx(opts.Ctx, path, s.inkRenderer, sink, showText, opts.OnProgress)
	if err != nil {
		return ImportResult{}, err
	}

	needPDFs := false
	if format == ".marginpkg" {
		if !existingOnly {
			needPDFs = true
		} else if snapshot, snapErr := s.Read(); snapErr == nil {
			needPDFs = slices.ContainsFunc(books, func(b Book) bool {
				return indexBook(&snapshot, b.ID) >= 0
			})
		}
	}
	if needPDFs {
		documents, err := s.importPackagePDFs(opts.Ctx, path, configs, opts.OnProgress)
		if err != nil {
			s.mu.Lock()
			if lib, readErr := s.read(); readErr == nil {
				s.gcDocuments(lib.Books)
			}
			s.mu.Unlock()
			return ImportResult{}, err
		}
		for i := range books {
			books[i].Documents = documents
			books[i].PDFsScanned = true
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	library, err := s.read()
	if err != nil {
		return ImportResult{}, err
	}
	result := ImportResult{BookID: books[0].ID}
	dirty := make(map[string]bool)
	var staleBookFiles []string
	changed := false
	mergedUpdate := false

	for _, book := range books {
		if i := indexBook(&library, book.ID); i >= 0 {
			target := &library.Books[i]
			updated := false
			if target.MediaVersion < book.MediaVersion {
				mergeMedia(target, book)
				updated = true
			}
			docs, docsChanged := mergeDocuments(target.Documents, book.Documents)
			if docsChanged {
				target.Documents = docs
				updated = true
			}
			if book.PDFsScanned && !target.PDFsScanned {
				target.PDFsScanned = true
				updated = true
			}
			if updated {
				dirty[target.ID] = true
				result.Updated++
				changed = true
				mergedUpdate = true
			}
			continue
		}
		if !existingOnly {
			if i := indexBookByTopic(&library, book.ID); i >= 0 {
				oldFile := bookFileName(library.Books[i].ID)
				mergeBookUpdate(&library.Books[i], book)
				if newFile := bookFileName(library.Books[i].ID); newFile != oldFile {
					staleBookFiles = append(staleBookFiles, oldFile)
				}
				dirty[library.Books[i].ID] = true
				result.Updated++
				changed = true
				mergedUpdate = true
				continue
			}
			library.Books = append(library.Books, book)
			dirty[book.ID] = true
			result.Added++
			changed = true
		}
	}

	if changed {
		if len(files) > 0 {
			if err := os.MkdirAll(s.mediaDir(), 0700); err != nil {
				return ImportResult{}, err
			}
			for name, data := range files {
				if _, err := writeImmutableFile(s.MediaPath(name), data); err != nil {
					return ImportResult{}, err
				}
			}
		}
		if err := s.writeAllShards(library, dirty); err != nil {
			return ImportResult{}, err
		}
		for _, oldFile := range staleBookFiles {
			_ = os.Remove(filepath.Join(s.booksDir(), oldFile))
		}
		sink.finalize(referencedMedia(library.Books))
		if mergedUpdate {
			s.gcMedia(library.Books)
		}
	} else {
		sink.finalize(nil)
		if needPDFs {
			s.gcDocuments(library.Books)
		}
	}
	result.Library = library
	return result, nil
}

func blocksHaveNoText(blocks []ContentBlock) bool {
	for _, b := range blocks {
		if strings.TrimSpace(b.Text) != "" {
			return false
		}
	}
	return true
}

func mergeMedia(book *Book, source Book) {
	byID := make(map[string]Note, len(source.Notes))
	for _, n := range source.Notes {
		byID[n.ID] = n
	}
	var dummyIDs []string
	for i := range book.Notes {
		n := &book.Notes[i]
		original, ok := byID[n.ID]
		if !ok {
			if !strings.HasPrefix(n.ID, userNotePrefix) && !n.TitleEdited && !n.BodyEdited &&
				strings.TrimSpace(n.Title) == "" && strings.TrimSpace(n.Body) == "" &&
				strings.TrimSpace(n.Excerpt) == "" && n.Clip == "" && blocksHaveNoText(n.Blocks) && len(n.Children) == 0 {
				dummyIDs = append(dummyIDs, n.ID)
			}
			continue
		}
		n.Clip, n.Blocks = original.Clip, original.Blocks
		n.Style, n.Collapsed = original.Style, original.Collapsed
		n.LayoutMode = original.LayoutMode
		n.HighlightRects = original.HighlightRects
		n.MaskRects = original.MaskRects
		n.BookMD5 = original.BookMD5
		n.BookTitle = original.BookTitle
		if original.CreatedAt > 0 && n.CreatedAt == 0 {
			n.CreatedAt = original.CreatedAt
		}
		if !(n.TitleEdited || n.BodyEdited || n.MetaEdited) || original.UpdatedAt > n.UpdatedAt {
			n.UpdatedAt = original.UpdatedAt
		}
		if !n.MetaEdited {
			n.Color = original.Color
			n.Tags = original.Tags
		}
		n.Links = original.Links
		if original.Page != nil {
			p := *original.Page
			n.Page = &p
		}
		n.TitleEdited = n.TitleEdited || n.Title != original.Title
		n.BodyEdited = n.BodyEdited || n.Body != original.Body
	}
	if len(dummyIDs) > 0 {
		removeNotes(book, dummyIDs)
	}
	book.MediaVersion = source.MediaVersion
}

// mergeBookUpdate updates an existing notebook from a newer MarginNote export
// of the same topic while preserving locally edited notes and user-added notes.
func mergeBookUpdate(target *Book, incoming Book) {
	if target.SourceTitle != "" {
		incoming.SourceTitle = incoming.Title
		incoming.Title = target.Title
	}
	removeNotes(&incoming, target.DeletedNotes)
	oldByID := make(map[string]Note, len(target.Notes))
	for _, n := range target.Notes {
		oldByID[n.ID] = n
	}
	inSet := make(map[string]bool, len(incoming.Notes))
	for _, n := range incoming.Notes {
		inSet[n.ID] = true
	}

	attachedUserNotes := make(map[string]bool)
	notes := make([]Note, 0, len(incoming.Notes))
	for _, n := range incoming.Notes {
		if old, ok := oldByID[n.ID]; ok {
			n.Review = old.Review
			if old.TitleEdited {
				n.Title = old.Title
				n.TitleEdited = true
			}
			if old.BodyEdited {
				n.Body = old.Body
				n.BodyEdited = true
			}
			for _, childID := range old.Children {
				if strings.HasPrefix(childID, userNotePrefix) && !inSet[childID] && !slices.Contains(n.Children, childID) {
					if _, exists := oldByID[childID]; exists {
						n.Children = append(n.Children, childID)
						attachedUserNotes[childID] = true
					}
				}
			}
		}
		notes = append(notes, n)
	}

	roots := slices.Clone(incoming.Roots)
	for _, old := range target.Notes {
		if !strings.HasPrefix(old.ID, userNotePrefix) || inSet[old.ID] {
			continue
		}
		// Keep child links of user-created notes pointing to surviving notes.
		var keptChildren []string
		for _, c := range old.Children {
			if inSet[c] || strings.HasPrefix(c, userNotePrefix) {
				keptChildren = append(keptChildren, c)
				if strings.HasPrefix(c, userNotePrefix) {
					attachedUserNotes[c] = true
				}
			}
		}
		old.Children = keptChildren
		notes = append(notes, old)
	}
	for _, old := range target.Notes {
		if strings.HasPrefix(old.ID, userNotePrefix) && !inSet[old.ID] && !attachedUserNotes[old.ID] {
			if !slices.Contains(roots, old.ID) {
				roots = append(roots, old.ID)
			}
		}
	}

	target.ID = incoming.ID
	target.Title = incoming.Title
	target.SourceTitle = incoming.SourceTitle
	target.Source = incoming.Source
	target.MediaVersion = incoming.MediaVersion
	target.Notes = notes
	target.Roots = roots
	target.Documents, _ = mergeDocuments(target.Documents, incoming.Documents)
	target.PDFsScanned = target.PDFsScanned || incoming.PDFsScanned
}

func (s *Store) RenameBook(bookID, title string) (Library, error) {
	title = strings.TrimSpace(title)
	if title == "" || len(title) > 1000 || strings.ContainsAny(title, "\r\n") {
		return Library{}, errors.New("请输入有效的笔记本名称")
	}
	return s.editBook(bookID, func(book *Book) error {
		if book.Title == title {
			return nil
		}
		if book.SourceTitle == "" {
			book.SourceTitle = book.Title
		}
		book.Title = title
		return nil
	}, false)
}

func (s *Store) DeleteNote(bookID, noteID string) (Library, error) {
	return s.editBook(bookID, func(book *Book) error {
		if noteID == "" || indexNote(book, noteID) < 0 {
			return errors.New("找不到这条笔记")
		}
		removeNotes(book, []string{noteID})
		if !strings.HasPrefix(noteID, userNotePrefix) {
			book.DeletedNotes = append(book.DeletedNotes, noteID)
		}
		return nil
	}, true)
}

func (s *Store) editBook(bookID string, edit func(*Book) error, collect bool) (Library, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	library, err := s.read()
	if err != nil {
		return Library{}, err
	}
	i := indexBook(&library, bookID)
	if i < 0 {
		return Library{}, errors.New("找不到此笔记本")
	}
	book := &library.Books[i]
	oldTitle := book.Title
	if err := edit(book); err != nil {
		return Library{}, err
	}
	if err := s.writeBook(*book); err != nil {
		return Library{}, err
	}
	if oldTitle != book.Title {
		// The shard owns the current title. Refresh the index's cached title when possible.
		_ = s.writeIndex(makeIndex(library.Books))
	}
	if collect {
		s.gcMedia(library.Books)
	}
	return library, nil
}

// removeNotes splices surviving children into the removed nodes' positions.
func removeNotes(book *Book, ids []string) {
	if len(ids) == 0 {
		return
	}
	removed := make(map[string][]string, len(ids))
	idsSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idsSet[id] = true
	}
	for _, note := range book.Notes {
		if idsSet[note.ID] {
			removed[note.ID] = note.Children
		}
	}
	splice := func(links []string, parent string) []string {
		var out []string
		seen := map[string]bool{parent: true}
		stack := slices.Clone(links)
		slices.Reverse(stack)
		for len(stack) > 0 {
			id := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[id] {
				continue
			}
			seen[id] = true
			if idsSet[id] {
				for _, child := range slices.Backward(removed[id]) {
					stack = append(stack, child)
				}
			} else {
				out = append(out, id)
			}
		}
		return out
	}
	book.Roots = splice(book.Roots, "")
	book.Notes = slices.DeleteFunc(book.Notes, func(n Note) bool { return idsSet[n.ID] })
	for i := range book.Notes {
		n := &book.Notes[i]
		if len(n.Children) > 0 {
			n.Children = splice(n.Children, n.ID)
		}
	}
}

// RecoverMedia upgrades books imported before attachments were supported.
// Only matching archive fingerprints can change an existing notebook.
func (s *Store) RecoverMedia(downloads string) error {
	library, err := s.Read()
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, book := range library.Books {
		source := book.Source
		missingPDFs := !book.PDFsScanned && strings.EqualFold(filepath.Ext(source), ".marginpkg")
		if (!missingPDFs && book.MediaVersion >= mediaVersion(s.inkRenderer)) ||
			source == "" || source != filepath.Base(source) || seen[source] {
			continue
		}
		seen[source] = true
		if _, readErr := s.importFile(filepath.Join(downloads, source), true, ImportOptions{}); !errors.Is(readErr, os.ErrNotExist) {
			err = errors.Join(err, readErr)
		}
	}
	return err
}

func mergeDocuments(previous, incoming []Document) ([]Document, bool) {
	changed := false
	for _, d := range incoming {
		idx := slices.IndexFunc(previous, func(old Document) bool { return old.ID == d.ID })
		if idx < 0 {
			previous = append(previous, d)
			changed = true
			continue
		}
		old := &previous[idx]
		if d.Title != "" && d.Title != old.Title && strings.HasPrefix(old.Title, "文档 ") {
			old.Title = d.Title
			changed = true
		}
		if d.LastPage > 0 && old.LastPage == 0 {
			old.LastPage = d.LastPage
			changed = true
		}
		if len(d.TOC) > 0 && !slices.Equal(old.TOC, d.TOC) {
			old.TOC = slices.Clone(d.TOC)
			changed = true
		}
	}
	return previous, changed
}

func validateNoteInput(title, body string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" && strings.TrimSpace(body) == "" {
		return "", errors.New("请填写标题或正文")
	}
	if len(title) > 1000 || len(body) > 1<<20 {
		return "", errors.New("标题或正文太长")
	}
	return title, nil
}

// SaveDocumentPage persists the last viewed page (1-based) for a single document.
func (s *Store) SaveDocumentPage(bookID, docID string, lastPage int) error {
	if bookID == "" || docID == "" || lastPage < 1 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, _, err := s.readIndex()
	if err != nil {
		return err
	}
	pos := slices.IndexFunc(idx.Books, func(e bookEntry) bool { return e.ID == bookID })
	if pos < 0 {
		return nil
	}
	book, err := s.readBookFile(idx.Books[pos].File)
	if err != nil {
		return err
	}
	changed := false
	for i := range book.Documents {
		if book.Documents[i].ID == docID {
			if book.Documents[i].LastPage != lastPage {
				book.Documents[i].LastPage = lastPage
				changed = true
			}
			break
		}
	}
	if !changed {
		return nil
	}
	return s.writeBook(book)
}

// NormalizeTags cleans, strips leading #, deduplicates, and preserves order.
func NormalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(tags))
	var out []string
	for _, raw := range tags {
		tag := strings.TrimSpace(raw)
		tag = strings.TrimLeft(tag, "#")
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

func cloneColorPtr(c *int) *int {
	if c == nil {
		return nil
	}
	v := *c
	return &v
}

func isDescendantNote(book Book, rootID, candidateID string) bool {
	if rootID == candidateID {
		return true
	}
	byID := make(map[string]int, len(book.Notes))
	for i, n := range book.Notes {
		byID[n.ID] = i
	}
	seen := map[string]bool{rootID: true}
	stack := []string{rootID}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		idx, ok := byID[cur]
		if !ok {
			continue
		}
		for _, cid := range book.Notes[idx].Children {
			if cid == candidateID {
				return true
			}
			if !seen[cid] {
				seen[cid] = true
				stack = append(stack, cid)
			}
		}
	}
	return false
}

// SaveNote persists edits by rewriting only the single target book shard.
func (s *Store) SaveNote(bookID, noteID, title, body string) (Library, error) {
	return s.saveNote(bookID, noteID, title, body, false, nil, nil, nil)
}

// SaveNoteWithMeta persists title/body plus optional color, tags, and parent move.
func (s *Store) SaveNoteWithMeta(bookID, noteID, title, body string, color *int, tags []string, newParentID *string) (Library, error) {
	return s.saveNote(bookID, noteID, title, body, true, color, tags, newParentID)
}

func (s *Store) saveNote(bookID, noteID, title, body string, updateMeta bool, color *int, tags []string, newParentID *string) (Library, error) {
	title, err := validateNoteInput(title, body)
	if err != nil {
		return Library{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, _, err := s.readIndex()
	if err != nil {
		return Library{}, err
	}
	pos := slices.IndexFunc(idx.Books, func(e bookEntry) bool { return e.ID == bookID })
	if pos < 0 {
		return Library{}, errors.New("找不到这条笔记")
	}
	book, err := s.readBookFile(idx.Books[pos].File)
	if err != nil {
		return Library{}, err
	}
	j := indexNote(&book, noteID)
	if j < 0 {
		return Library{}, errors.New("找不到这条笔记")
	}
	if newParentID != nil {
		target := *newParentID
		if target != "" {
			if indexNote(&book, target) < 0 {
				return Library{}, errors.New("找不到目标归属笔记")
			}
			if isDescendantNote(book, noteID, target) {
				return Library{}, errors.New("不能把笔记移动到自己或下级笔记中")
			}
		}
		book.Roots = slices.DeleteFunc(book.Roots, func(id string) bool { return id == noteID })
		for k := range book.Notes {
			book.Notes[k].Children = slices.DeleteFunc(book.Notes[k].Children, func(id string) bool { return id == noteID })
		}
		if target == "" {
			book.Roots = append(book.Roots, noteID)
		} else {
			p := indexNote(&book, target)
			if p >= 0 && !slices.Contains(book.Notes[p].Children, noteID) {
				book.Notes[p].Children = append(book.Notes[p].Children, noteID)
			}
		}
	}
	n := &book.Notes[j]
	n.TitleEdited = n.TitleEdited || n.Title != title
	n.BodyEdited = n.BodyEdited || n.Body != body
	n.Title, n.Body = title, body
	if updateMeta {
		n.Color = cloneColorPtr(color)
		n.Tags = NormalizeTags(tags)
		n.MetaEdited = true
	}
	n.UpdatedAt = time.Now().Unix()
	if err := s.writeBook(book); err != nil {
		return Library{}, err
	}
	return s.read()
}

// AddNote appends a new child note under parentID (or root if empty) and
// rewrites only that book's shard file.
func (s *Store) AddNote(bookID, parentID, title, body string) (Library, string, error) {
	return s.AddNoteWithMeta(bookID, parentID, title, body, nil, nil)
}

// AddNoteWithMeta appends a new child note with optional color and tags.
func (s *Store) AddNoteWithMeta(bookID, parentID, title, body string, color *int, tags []string) (Library, string, error) {
	title, err := validateNoteInput(title, body)
	if err != nil {
		return Library{}, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, _, err := s.readIndex()
	if err != nil {
		return Library{}, "", err
	}
	pos := slices.IndexFunc(idx.Books, func(e bookEntry) bool { return e.ID == bookID })
	if pos < 0 {
		return Library{}, "", errors.New("找不到此笔记本")
	}
	book, err := s.readBookFile(idx.Books[pos].File)
	if err != nil {
		return Library{}, "", err
	}
	now := time.Now().Unix()
	noteID := userNotePrefix + uuid.NewString()
	book.Notes = append(book.Notes, Note{
		ID:          noteID,
		Title:       title,
		Body:        body,
		Color:       cloneColorPtr(color),
		Tags:        NormalizeTags(tags),
		CreatedAt:   now,
		UpdatedAt:   now,
		TitleEdited: true,
		BodyEdited:  true,
		MetaEdited:  true,
	})
	if parentID != "" {
		if p := indexNote(&book, parentID); p >= 0 {
			book.Notes[p].Children = append(book.Notes[p].Children, noteID)
		} else {
			book.Roots = append(book.Roots, noteID)
		}
	} else {
		book.Roots = append(book.Roots, noteID)
	}
	if err := s.writeBook(book); err != nil {
		return Library{}, "", err
	}
	library, err := s.read()
	return library, noteID, err
}

// DeleteBook deletes a notebook shard and garbage-collects orphaned media files.
func (s *Store) DeleteBook(bookID string) (Library, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	library, err := s.read()
	if err != nil {
		return Library{}, err
	}
	i := indexBook(&library, bookID)
	if i < 0 {
		return Library{}, errors.New("找不到此笔记本")
	}
	removedFile := bookFileName(library.Books[i].ID)
	library.Books = slices.Delete(library.Books, i, i+1)
	if err := s.writeIndex(makeIndex(library.Books)); err != nil {
		return Library{}, err
	}
	_ = os.Remove(filepath.Join(s.booksDir(), removedFile))
	s.deleteDrawing(bookID)
	s.gcMedia(library.Books)
	s.gcDocuments(library.Books)
	return library, nil
}

func referencedMedia(books []Book) map[string]bool {
	referenced := make(map[string]bool)
	for _, b := range books {
		for _, n := range b.Notes {
			if n.Clip != "" {
				referenced[n.Clip] = true
			}
			for _, blk := range n.Blocks {
				if blk.Image != "" {
					referenced[blk.Image] = true
				}
			}
		}
	}
	return referenced
}

func (s *Store) addInFlightMedia(name string) {
	s.inFlightMu.Lock()
	if s.inFlightMedia == nil {
		s.inFlightMedia = make(map[string]int)
	}
	s.inFlightMedia[name]++
	s.inFlightMu.Unlock()
}

func (s *Store) releaseInFlightMedia(name string) int {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	if s.inFlightMedia == nil {
		return 0
	}
	n := s.inFlightMedia[name] - 1
	if n <= 0 {
		delete(s.inFlightMedia, name)
		return 0
	}
	s.inFlightMedia[name] = n
	return n
}

func (s *Store) isInFlightMedia(name string) bool {
	s.inFlightMu.Lock()
	n := s.inFlightMedia[name]
	s.inFlightMu.Unlock()
	return n > 0
}

type storeMediaSink struct {
	store     *Store
	dirOnce   sync.Once
	dirErr    error
	mu        sync.Mutex
	seen      map[string]bool
	created   map[string]bool
	inFlight  map[string]chan struct{}
	finalized bool
}

func (s *Store) newMediaSink() *storeMediaSink {
	return &storeMediaSink{
		store:    s,
		seen:     make(map[string]bool),
		created:  make(map[string]bool),
		inFlight: make(map[string]chan struct{}),
	}
}

func (w *storeMediaSink) save(name string, data []byte) error {
	path := w.store.MediaPath(name)
	if path == "" {
		return errors.New("无效的媒体文件名")
	}
	var ch chan struct{}
	for {
		w.mu.Lock()
		if w.seen[name] {
			w.mu.Unlock()
			return nil
		}
		if waitCh, busy := w.inFlight[name]; busy {
			w.mu.Unlock()
			<-waitCh
			continue
		}
		ch = make(chan struct{})
		w.inFlight[name] = ch
		w.mu.Unlock()
		break
	}

	w.dirOnce.Do(func() {
		w.dirErr = os.MkdirAll(w.store.mediaDir(), 0700)
	})
	if w.dirErr != nil {
		w.mu.Lock()
		delete(w.inFlight, name)
		close(ch)
		w.mu.Unlock()
		return w.dirErr
	}

	w.store.addInFlightMedia(name)
	created, err := writeImmutableFile(path, data)
	w.mu.Lock()
	delete(w.inFlight, name)
	if err == nil {
		w.seen[name] = true
		if created {
			w.created[name] = true
		}
	} else {
		w.store.releaseInFlightMedia(name)
	}
	close(ch)
	w.mu.Unlock()
	return err
}

func (w *storeMediaSink) finalize(referenced map[string]bool) {
	w.mu.Lock()
	if w.finalized {
		w.mu.Unlock()
		return
	}
	w.finalized = true
	seen := w.seen
	created := w.created
	w.seen = nil
	w.created = nil
	w.mu.Unlock()

	for name := range seen {
		remaining := w.store.releaseInFlightMedia(name)
		if remaining == 0 && created[name] && (referenced == nil || !referenced[name]) {
			_ = os.Remove(w.store.MediaPath(name))
		}
	}
}

func (w *storeMediaSink) cleanup() {
	w.finalize(nil)
}

func (s *Store) gcMedia(books []Book) {
	referenced := referencedMedia(books)
	entries, err := os.ReadDir(s.mediaDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if mediaName.MatchString(name) && !referenced[name] && !s.isInFlightMedia(name) {
			_ = os.Remove(filepath.Join(s.mediaDir(), name))
		}
	}
}

func (s *Store) Book(id string) (Book, error) {
	library, err := s.Read()
	if err != nil {
		return Book{}, err
	}
	if i := indexBook(&library, id); i >= 0 {
		return library.Books[i], nil
	}
	return Book{}, errors.New("找不到此笔记本")
}

func bookTopicID(bookID string) string {
	_, topic, ok := strings.Cut(bookID, "/")
	if !ok {
		return ""
	}
	return topic
}

func indexBook(library *Library, id string) int {
	return slices.IndexFunc(library.Books, func(book Book) bool { return book.ID == id })
}

func indexBookByTopic(library *Library, bookID string) int {
	topic := bookTopicID(bookID)
	if topic == "" {
		return -1
	}
	return slices.IndexFunc(library.Books, func(book Book) bool { return bookTopicID(book.ID) == topic })
}

func indexNote(book *Book, id string) int {
	return slices.IndexFunc(book.Notes, func(note Note) bool { return note.ID == id })
}

// writeImmutableFile atomically writes a content-addressed media blob without
// per-file fsync, skipping the write when the immutable target already exists.
// Returns created=true only when this call newly created the file on disk.
func writeImmutableFile(path string, data []byte) (created bool, err error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".mgnote-media-*")
	if err != nil {
		return false, err
	}
	tmpName := f.Name()
	defer os.Remove(tmpName)
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	return true, nil
}

func WriteFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".mgnote-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// CleanupStaleTempFiles removes leftover temporary files created by aborted
// imports or atomic writes older than maxAge.
func (s *Store) CleanupStaleTempFiles(maxAge time.Duration) {
	if s == nil || s.dir == "" {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	dirs := []string{
		s.dir,
		s.booksDir(),
		s.mediaDir(),
		filepath.Join(s.dir, "documents"),
	}
	prefixes := []string{".pdf-", ".mgnote-", ".mgnote-media-"}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			matched := false
			for _, prefix := range prefixes {
				if strings.HasPrefix(name, prefix) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if !info.ModTime().After(cutoff) {
				_ = os.Remove(filepath.Join(dir, name))
			}
		}
	}
}
