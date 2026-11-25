package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dongyu-will/folio/internal/androidopen"
	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type screen uint8

const (
	browse screen = iota
	detail
	libraryScreen
)

type editorMode uint8

const (
	editNone editorMode = iota
	editNote
	editNewChild
)

type workspace struct {
	store           *notebook.Store
	win             *mygo.Window
	books           []notebook.Book
	tree            noteTree
	settings        notebook.Settings
	preferencesOpen      bool
	renameOpen           bool
	bookName             string
	storageBytes         int64
	webdavURL            string
	webdavUser           string
	webdavPassword       string
	webdavPasswordLoaded bool
	webdavBusy           bool
	webdavStatus         string

	bookID, scope, selected, query string
	bookChoice                     string
	activeTrail                    []string
	page, prevPage                 screen
	editMode                       editorMode
	draftTitle, draftBody          string
	origTitle, origBody            string
	draftColorSet, origColorSet    bool
	draftColor, origColor          int
	draftTags, origTags            string
	draftParent, origParent        string
	discardPrompt                  bool
	preview                        string
	previewZoom                    float32
	focusSearch                    bool
	filterColorSet                 bool
	filterColor                    int
	filterTag                      string
	filterHasImage                 bool
	filterHasPage                  bool
	filterPopoverOpen              bool
	reciteMode                     bool
	revealedMasks                  map[string]bool
	revealedNotes                  map[string]bool
	reviewOpen                     bool
	reviewQueue                    []string
	reviewIndex                    int
	reviewRevealed                 bool
	reviewKnown                    map[string]bool
	media                          mediaViewState
	mediaDims                      map[string]image.Point
	cardMenuOpen                   bool

	cachedQuery      string
	cachedFilterSig  string
	cachedQueryReady bool
	cachedHits       []string

	readerScroll                                       ui.ScrollState
	outlineScroll                                      ui.ScrollState
	settingsScroll                                     ui.ScrollState
	expanded                                           map[string]bool
	scale, panX, panY                                  float32
	lastZoomX, lastZoomY                               float32
	lastZoomAt                                         time.Time
	recenter, focusSelected, canvasReady               bool
	canvasPointer                                      canvasPointer
	drawing                                            drawingState
	drawingWrites                                      drawingWriter
	pdf                                                pdfState
	pdfDensity                                         float32
	mode                                               workspaceMode
	splitRatio, splitStart, splitPointerX, marginStart float32
	splitDragging, marginDragging, minimapDragging     bool
	canvasWidth, canvasHeight                          float32
	lastViewW                                          float32
	lastViewWide, lastViewSidebar                      bool
	sidebarHiddenForSplit                              bool
	forceTouchUI                                       bool
	jumpToCard                                         string

	pdfHighlightNoteID string
	pdfTOCOpen         bool

	androidInk          androidInk
	androidFramePending atomic.Bool
	androidFrameReadyFn func()

	busy bool

	importMu            sync.Mutex
	postUI              func(func())
	downloadsPending    bool
	downloadsPolling    bool
	importPaths         []queuedImport
	importCancel        context.CancelFunc
	importCanceled      bool
	importBatchActive   bool
	importBatchTotal    int
	importBatchDone     int
	importBatchFailed   int
	importBatchBooks    int
	importBatchFirstErr error
	takeDownloadFn      func(context.Context) (*pendingAndroidDownload, error)
	importFileFn        func(string, func(notebook.ImportResult)) (notebook.ImportResult, error)
	openImportDialogFn  func(mygo.OpenDialogOptions) ([]string, error)

	updateMu                    sync.Mutex
	update                      updateUIState
	updateBannerDismissed       bool
	updateCancel                context.CancelFunc
	updateDownloadGen           uint64
	updateCanceledGen           uint64
	autoCheckRunning            bool
	checkUpdateFn               func(context.Context) (*updateInfo, error)
	installUpdateFn             func(context.Context, *updateInfo, func(downloaded, total int64)) (updateInstallResult, error)
	relaunchFn                  func()
	checkPendingAndroidUpdateFn func() (*updateInfo, bool)
	isOnlineFn                  func() bool

	message          string
	messageError     bool
	messagePending   bool
	messageUntil     time.Time
	toastActionLabel string
	toastActionFn    func()

	camAnim         cameraSpring
	pageDir         int8
	reviewDir       int8
	reviewStep      uint64
	previewDir      int8
	toastShakeRun   uint64
	discardShakeRun uint64
	pdfPageShakeRun uint64
	maskFades       fadeTracker
	shapeSnapAt     time.Time
	shapeSnapKey    string

	pdfPageSaveMu    sync.Mutex
	pdfPageSaveTimer *time.Timer
	pendingPDFSave   struct {
		bookID string
		docID  string
		page   int
	}
}

type queuedImport struct {
	path    string
	cleanup bool
}

type pendingAndroidDownload struct {
	Path  string
	Error string
}

type updatePhase uint8

const (
	updateIdle updatePhase = iota
	updateChecking
	updateUpToDate
	updateNoRelease
	updateAvailable
	updateDownloading
	updateRelaunchRequired
	updatePermissionRequired
	updateDeferredForeground
	updateInstallerLaunched
	updateError
)

const (
	autoUpdateCheckDelay    = 2 * time.Second
	autoUpdateCheckInterval = 12 * time.Hour
)

type updateUIState struct {
	phase      updatePhase
	info       *updateInfo
	downloaded int64
	total      int64
	errText    string
}

func (st updateUIState) hasAttention() bool {
	switch st.phase {
	case updateAvailable, updateRelaunchRequired, updateDeferredForeground:
		return true
	case updatePermissionRequired:
		return st.info != nil && st.info.PendingPath != ""
	default:
		return false
	}
}

func (st updateUIState) showsBanner() bool {
	switch st.phase {
	case updateAvailable, updateRelaunchRequired:
		return true
	case updatePermissionRequired:
		return st.info != nil && st.info.PendingPath != ""
	default:
		return false
	}
}

type noteTree struct {
	book   notebook.Book
	root   string
	notes  map[string]notebook.Note
	kids   map[string][]string
	parent map[string]string
	search map[string]string
	order  []string
}

func buildTree(book notebook.Book) noteTree {
	tree := noteTree{
		book:   book,
		notes:  map[string]notebook.Note{},
		kids:   map[string][]string{},
		parent: map[string]string{},
		search: map[string]string{},
	}
	for _, n := range book.Notes {
		tree.notes[n.ID] = n
		tree.kids[n.ID] = n.Children
		tree.search[n.ID] = buildSearchText(n)
	}
	tree.notes[""] = notebook.Note{Title: book.Title}
	tree.kids[""] = book.Roots
	if len(book.Roots) == 1 {
		n := tree.notes[book.Roots[0]]
		if len(n.Children) > 0 {
			tree.root = n.ID
			if strings.TrimSpace(n.Title) == "" {
				n.Title = book.Title
				tree.notes[n.ID] = n
			}
			tree.kids[n.ID] = slices.Clone(n.Children)
		}
	} else if len(book.Roots) > 1 {
		n := tree.notes[book.Roots[0]]
		if (n.Title == book.Title || book.SourceTitle != "" && n.Title == book.SourceTitle) && len(n.Children) > 0 {
			tree.root = n.ID
			n.Title = book.Title
			tree.notes[n.ID] = n
			tree.kids[n.ID] = append(slices.Clone(n.Children), book.Roots[1:]...)
		}
	}
	seen := map[string]bool{tree.root: true}
	type entry struct{ id, parent string }
	stack := []entry{}
	push := func(id string) {
		for _, child := range slices.Backward(tree.kids[id]) {
			if _, exists := tree.notes[child]; exists {
				stack = append(stack, entry{child, id})
			}
		}
	}
	push(tree.root)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !seen[n.id] {
			seen[n.id] = true
			tree.parent[n.id] = n.parent
			tree.order = append(tree.order, n.id)
			push(n.id)
		}
	}
	return tree
}

func buildSearchText(n notebook.Note) string {
	var b strings.Builder
	b.WriteString(n.Title)
	b.WriteByte(' ')
	b.WriteString(n.Body)
	b.WriteByte(' ')
	b.WriteString(n.Excerpt)
	for _, blk := range n.Blocks {
		if txt := blockPlainText(blk); txt != "" {
			b.WriteByte(' ')
			b.WriteString(txt)
		}
	}
	for _, tag := range n.Tags {
		if tag != "" {
			b.WriteByte(' ')
			b.WriteString(tag)
		}
	}
	return strings.ToLower(b.String())
}

func (a *workspace) searchResultSubtitle(id, query string) string {
	n := a.tree.notes[id]
	words := strings.Fields(strings.ToLower(query))
	var sources []string
	if strings.TrimSpace(n.Body) != "" {
		sources = append(sources, n.Body)
	}
	if strings.TrimSpace(n.Excerpt) != "" {
		sources = append(sources, n.Excerpt)
	}
	for _, blk := range n.Blocks {
		if txt := blockPlainText(blk); txt != "" {
			sources = append(sources, txt)
		}
	}
	for _, src := range sources {
		line := strings.Join(strings.Fields(src), " ")
		if line == "" || line == strings.TrimSpace(n.Title) {
			continue
		}
		low := strings.ToLower(line)
		for _, w := range words {
			if w != "" && strings.Contains(low, w) {
				runes := []rune(line)
				if len(runes) > 42 {
					idx := strings.Index(low, w)
					runeIdx := len([]rune(line[:max(0, idx)]))
					start := max(0, runeIdx-10)
					end := min(len(runes), start+40)
					snippet := string(runes[start:end])
					if start > 0 {
						snippet = "…" + snippet
					}
					if end < len(runes) {
						snippet += "…"
					}
					return snippet
				}
				return line
			}
		}
	}
	if pid := a.tree.parent[id]; pid != "" {
		if pNote, ok := a.tree.notes[pid]; ok && pNote.ID != a.tree.root {
			return noteTitle(pNote)
		}
	}
	for _, src := range sources {
		line := strings.Join(strings.Fields(src), " ")
		if line != "" && line != strings.TrimSpace(n.Title) {
			runes := []rune(line)
			if len(runes) > 40 {
				return string(runes[:40]) + "…"
			}
			return line
		}
	}
	return ""
}

func (t noteTree) trail(id string) []string {
	path := []string{id}
	for id != t.root {
		p, ok := t.parent[id]
		if !ok {
			break
		}
		path = append(path, p)
		id = p
	}
	slices.Reverse(path)
	return path
}

func (t noteTree) validTrail(path []string, target string) bool {
	if len(path) == 0 || path[0] != t.root || path[len(path)-1] != target {
		return false
	}
	for i := 1; i < len(path); i++ {
		if _, ok := t.notes[path[i]]; !ok {
			return false
		}
		if !slices.Contains(t.kids[path[i-1]], path[i]) {
			return false
		}
	}
	return true
}

func (a *workspace) editing() bool { return a.editMode != editNone }

func (a *workspace) currentTrail() []string {
	if a.tree.validTrail(a.activeTrail, a.selected) {
		return a.activeTrail
	}
	return a.tree.trail(a.selected)
}

func noteTitle(n notebook.Note) string {
	if n.Title != "" {
		return n.Title
	}
	s, _, _ := strings.Cut(strings.TrimSpace(noteText(n)), "\n")
	if s == "" {
		return "未命名笔记"
	}
	runes := []rune(s)
	if len(runes) > 40 {
		return string(runes[:40]) + "…"
	}
	return s
}

func noteText(n notebook.Note) string {
	if n.Body != "" {
		return n.Body
	}
	return n.Excerpt
}

func noteHasImage(n notebook.Note) bool {
	if n.Clip != "" {
		return true
	}
	for _, b := range n.Blocks {
		if b.Image != "" {
			return true
		}
	}
	return false
}

// noteCardText returns the readable multi-line body to display inline on cards.
// When a clean PDF excerpt clip image is present, omit raw PDF OCR excerpt text
// just like MarginNote and the note detail reader do.
func noteCardText(n notebook.Note) string {
	var text string
	if n.Clip != "" {
		text = strings.TrimSpace(n.Body)
	} else {
		text = strings.TrimSpace(noteText(n))
	}
	if text == "" {
		return ""
	}
	title := strings.TrimSpace(n.Title)
	if title != "" && text == title {
		return ""
	}
	return cleanCardBody(text)
}

// cleanCardBody normalizes line breaks so blank spacer lines never produce orphaned "..." lines on cards.
func cleanCardBody(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, ln := range lines {
		trimmed := strings.TrimRight(ln, " \t")
		if strings.TrimSpace(trimmed) == "" {
			if len(out) > 0 && !prevBlank {
				out = append(out, "")
				prevBlank = true
			}
			continue
		}
		out = append(out, trimmed)
		prevBlank = false
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

func noteCardImages(n notebook.Note) []string {
	var refs []string
	if n.Clip != "" {
		refs = append(refs, n.Clip)
	}
	for _, b := range n.Blocks {
		if b.Image != "" && !slices.Contains(refs, b.Image) {
			refs = append(refs, b.Image)
		}
	}
	return refs
}

func hasNoteContent(n notebook.Note) bool {
	return noteText(n) != "" || n.Clip != "" || len(n.Blocks) > 0 || n.PendingMedia
}

func blockPlainText(blk notebook.ContentBlock) string {
	if s := strings.TrimSpace(blk.Text); s != "" {
		return s
	}
	if len(blk.Spans) == 0 {
		return ""
	}
	var b strings.Builder
	for _, sp := range blk.Spans {
		b.WriteString(sp.Text)
	}
	return strings.TrimSpace(b.String())
}

func (a *workspace) touchUI() bool {
	return runtime.GOOS == "android" || runtime.GOOS == "ios" || a.forceTouchUI
}

func (a *workspace) enterSplitMode() {
	a.mode = splitMode
	a.drawing.dockOpen = true
	if a.lastViewW > 0 && a.lastViewW < 1040 && !a.settings.SidebarCollapsed {
		a.sidebarHiddenForSplit = true
	}
}

func (a *workspace) jumpToNotePDF(n notebook.Note) {
	if n.Page == nil || *n.Page <= 0 {
		return
	}
	docs := a.tree.book.Documents
	if len(docs) == 0 {
		a.showMessage("当前笔记本未附带 PDF 原文", false)
		return
	}
	targetDoc := a.pdf.document
	if n.BookMD5 != "" || n.BookTitle != "" {
		for _, d := range docs {
			if (n.BookMD5 != "" && d.ID == n.BookMD5) || (n.BookTitle != "" && d.Title == n.BookTitle) {
				targetDoc = d.ID
				break
			}
		}
	}
	if targetDoc == "" || !slices.ContainsFunc(docs, func(d notebook.Document) bool { return d.ID == targetDoc }) {
		targetDoc = docs[0].ID
	}
	if a.pdf.document != targetDoc {
		a.setPDFDocument(targetDoc)
	}
	a.setPDFPage(int(*n.Page) - 1)
	a.pdfHighlightNoteID = n.ID
	a.pdfTOCOpen = false
	if a.mode == canvasMode {
		a.enterSplitMode()
		a.recenter = true
	}
	a.cardMenuOpen = false
	a.page = browse
}

func formatNoteTime(unixSec int64) string {
	if unixSec <= 0 {
		return ""
	}
	return time.Unix(unixSec, 0).Local().Format("2006-01-02 15:04")
}

func (a *workspace) invalidateSearch() {
	a.cachedQueryReady = false
	a.cachedFilterSig = ""
	a.cachedHits = nil
}

func (a *workspace) hasActiveFilter() bool {
	return a.filterColorSet || a.filterTag != "" || a.filterHasImage || a.filterHasPage
}

func (a *workspace) activeFilterCount() int {
	n := 0
	if a.filterColorSet {
		n++
	}
	if a.filterTag != "" {
		n++
	}
	if a.filterHasImage {
		n++
	}
	if a.filterHasPage {
		n++
	}
	return n
}

func (a *workspace) clearFilters() {
	a.filterColorSet, a.filterColor = false, 0
	a.filterTag = ""
	a.filterHasImage, a.filterHasPage = false, false
	a.filterPopoverOpen = false
	a.invalidateSearch()
}

func (a *workspace) filterSignature() string {
	return fmt.Sprintf("%v:%d|%s|%v|%v", a.filterColorSet, a.filterColor, a.filterTag, a.filterHasImage, a.filterHasPage)
}

func (a *workspace) bookUsedColors() []int {
	seen := make(map[int]bool)
	var colors []int
	for _, id := range a.tree.order {
		n := a.tree.notes[id]
		if n.Color != nil && *n.Color >= 0 && *n.Color < 16 && !seen[*n.Color] {
			seen[*n.Color] = true
			colors = append(colors, *n.Color)
		}
	}
	slices.Sort(colors)
	return colors
}

func (a *workspace) bookUsedTags() []string {
	seen := make(map[string]bool)
	var tags []string
	for _, id := range a.tree.order {
		for _, raw := range a.tree.notes[id].Tags {
			tag := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(raw), "#"))
			if tag != "" && !seen[tag] {
				seen[tag] = true
				tags = append(tags, tag)
			}
		}
	}
	slices.Sort(tags)
	return tags
}

func (a *workspace) noteMatchesFilter(n notebook.Note) bool {
	if a.filterColorSet && (n.Color == nil || *n.Color != a.filterColor) {
		return false
	}
	if a.filterTag != "" {
		found := false
		for _, raw := range n.Tags {
			tag := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(raw), "#"))
			if tag == a.filterTag {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if a.filterHasImage && !noteHasImage(n) {
		return false
	}
	if a.filterHasPage && (n.Page == nil || *n.Page <= 0) {
		return false
	}
	return true
}

func (a *workspace) searchNotes() []string {
	sig := a.filterSignature()
	if a.cachedQueryReady && a.cachedQuery == a.query && a.cachedFilterSig == sig {
		return a.cachedHits
	}
	words := strings.Fields(strings.ToLower(a.query))
	filtering := a.hasActiveFilter()
	var ids []string
	for _, id := range a.tree.order {
		if filtering && !a.noteMatchesFilter(a.tree.notes[id]) {
			continue
		}
		if len(words) > 0 {
			text := a.tree.search[id]
			matches := true
			for _, word := range words {
				if !strings.Contains(text, word) {
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
		}
		ids = append(ids, id)
	}
	a.cachedQuery = a.query
	a.cachedFilterSig = sig
	a.cachedHits = ids
	a.cachedQueryReady = true
	return ids
}

func (a *workspace) setLibrary(library notebook.Library, preferred string) {
	a.books = library.Books
	a.invalidateSearch()
	if preferred == "" {
		preferred = a.bookID
	}
	i := slices.IndexFunc(a.books, func(b notebook.Book) bool { return b.ID == preferred })
	if i < 0 && len(a.books) > 0 {
		i = 0
	}
	if i < 0 {
		a.bookID, a.bookChoice, a.scope, a.selected, a.query = "", "", "", "", ""
		a.activeTrail = nil
		a.clearMediaCache()
		a.cardMenuOpen = false
		a.pdfHighlightNoteID = ""
		a.pdfTOCOpen = false
		a.preview = ""
		a.tree = noteTree{}
		a.loadDrawing("")
		a.resetPDF(nil)
		a.page = browse
		return
	}
	book := a.books[i]
	a.bookChoice = book.ID
	a.tree = buildTree(book)
	if a.bookID != book.ID {
		prevSelected := a.selected
		prevTrail := a.activeTrail
		sameTopic := bookTopicKey(a.bookID) != "" && bookTopicKey(a.bookID) == bookTopicKey(book.ID)
		a.bookID = book.ID
		a.canvasReady = false
		a.expanded = nil
		a.clearMediaCache()
		a.cardMenuOpen = false
		a.pdfHighlightNoteID = ""
		a.pdfTOCOpen = false
		a.outlineScroll = ui.ScrollState{}
		if !sameTopic {
			a.loadDrawing(book.ID)
		}
		a.resetPDF(book.Documents)
		if sameTopic && prevSelected != "" {
			if _, ok := a.tree.notes[prevSelected]; ok {
				if a.tree.validTrail(prevTrail, prevSelected) {
					a.setNoteWithTrail(prevSelected, prevTrail)
				} else {
					a.setNote(prevSelected)
				}
				return
			}
		}
		a.clearFilters()
		a.reciteMode, a.revealedMasks, a.revealedNotes = false, nil, nil
		a.reviewOpen, a.reviewQueue, a.reviewIndex, a.reviewRevealed, a.reviewKnown = false, nil, 0, false, nil
		a.scope, a.query, a.scale = "", "", float32(a.settings.GraphZoom)/100
		a.page = browse
		a.setNote(a.firstNote())
		if a.store != nil && a.settings.LastBookID != book.ID && !slices.ContainsFunc(book.Notes, func(n notebook.Note) bool { return n.PendingMedia }) {
			a.settings.LastBookID = book.ID
			_ = a.store.SaveSettings(a.settings)
		}
	}
}

func bookTopicKey(id string) string {
	_, topic, ok := strings.Cut(id, "/")
	if !ok {
		return ""
	}
	return topic
}

func (a *workspace) firstNote() string {
	for _, id := range a.tree.order {
		if hasNoteContent(a.tree.notes[id]) && (a.scope == "" || slices.Contains(a.tree.trail(id), a.scope)) {
			return id
		}
	}
	if a.scope != "" {
		return a.scope
	}
	return a.tree.root
}

func (a *workspace) setNote(id string) {
	a.setNoteWithTrail(id, nil)
}

func (a *workspace) setNoteWithTrail(id string, trail []string) {
	a.setNoteAt(id, trail, true)
}

func (a *workspace) setNoteAt(id string, trail []string, recenter bool) {
	_, ok := a.tree.notes[id]
	if !ok {
		return
	}
	a.selected = id
	if recenter {
		a.recenter = true
	}
	if a.tree.validTrail(trail, id) {
		a.activeTrail = slices.Clone(trail)
	} else {
		a.activeTrail = a.tree.trail(id)
	}
	a.readerScroll.Y = 0
}

func (a *workspace) openNote(id string) {
	if a.editing() {
		return
	}
	a.cardMenuOpen = false
	if a.scope != "" && !slices.Contains(a.tree.trail(id), a.scope) {
		a.scope = ""
	}
	if a.page != detail {
		a.prevPage = a.page
	}
	a.setNote(id)
	a.focusSelected = true
	a.page = detail
}

func (a *workspace) openChildNote(id string) {
	if a.editing() {
		return
	}
	a.cardMenuOpen = false
	cur := a.currentTrail()
	var nextTrail []string
	if !slices.Contains(cur, id) && slices.Contains(a.tree.kids[a.selected], id) {
		nextTrail = append(slices.Clone(cur), id)
	}
	if a.scope != "" {
		check := nextTrail
		if len(check) == 0 {
			check = a.tree.trail(id)
		}
		if !slices.Contains(check, a.scope) {
			a.scope = ""
		}
	}
	if a.page != detail {
		a.prevPage = a.page
	}
	a.setNoteWithTrail(id, nextTrail)
	a.focusSelected = true
	a.page = detail
}

const (
	importingNotebookMessage    = "正在导入笔记本…"
	importingAttachmentsMessage = "正在加载笔记附件…"
)

func (a *workspace) showMessage(text string, failed bool) {
	a.message, a.messageError, a.messagePending = text, failed, false
	a.toastActionLabel, a.toastActionFn = "", nil
	if failed {
		a.messageUntil = time.Time{}
		a.toastShakeRun++
	} else {
		a.messageUntil = time.Now().Add(4 * time.Second)
	}
}

func (a *workspace) showActionMessage(text, actionLabel string, actionFn func()) {
	a.message, a.messageError, a.messagePending = text, false, false
	a.toastActionLabel, a.toastActionFn = actionLabel, actionFn
	a.messageUntil = time.Now().Add(5 * time.Second)
}

func (a *workspace) dismissMessage() {
	a.message, a.messageError, a.messagePending = "", false, false
	a.toastActionLabel, a.toastActionFn = "", nil
	a.messageUntil = time.Time{}
}

func (a *workspace) showProgress(text string) {
	a.showProgressAction(text, "", nil)
}

func (a *workspace) showProgressAction(text, actionLabel string, actionFn func()) {
	a.message, a.messageError, a.messagePending = text, false, true
	a.toastActionLabel, a.toastActionFn = actionLabel, actionFn
	a.messageUntil = time.Time{}
}

func (a *workspace) cancelImport() {
	a.importMu.Lock()
	cancel := a.importCancel
	a.importCanceled = true
	a.importPaths = nil
	a.importBatchActive = false
	a.importBatchTotal = 0
	a.importMu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.showProgress("正在取消导入…")
}

func (a *workspace) clearPendingMessage() {
	if !a.messagePending {
		return
	}
	a.dismissMessage()
}

func (a *workspace) showError(err error) {
	a.showMessage(friendlyError(err), true)
}

func (a *workspace) setWindow(win *mygo.Window) {
	a.importMu.Lock()
	a.win = win
	a.importMu.Unlock()
}

func (a *workspace) window() *mygo.Window {
	a.importMu.Lock()
	defer a.importMu.Unlock()
	return a.win
}

func (a *workspace) hasUI() bool {
	a.importMu.Lock()
	defer a.importMu.Unlock()
	return a.win != nil || a.postUI != nil
}

func (a *workspace) updateUI(fn func()) {
	a.importMu.Lock()
	win := a.win
	post := a.postUI
	a.importMu.Unlock()
	if win != nil {
		win.Update(fn)
		return
	}
	if post != nil {
		post(fn)
	}
}

func (a *workspace) enqueueImport(paths ...string) {
	a.importMu.Lock()
	added := 0
	for _, p := range paths {
		if p != "" {
			a.importPaths = append(a.importPaths, queuedImport{path: p})
			added++
		}
	}
	if added > 0 {
		a.importCanceled = false
		if !a.importBatchActive {
			queued := len(a.importPaths)
			if queued > 1 {
				a.importBatchActive = true
				a.importBatchTotal = queued
				a.importBatchDone = 0
				a.importBatchFailed = 0
				a.importBatchBooks = 0
				a.importBatchFirstErr = nil
			}
		} else {
			a.importBatchTotal += added
		}
	}
	hasUI := a.win != nil || a.postUI != nil
	a.importMu.Unlock()
	if hasUI {
		a.updateUI(func() { a.flushImports() })
	}
}

func (a *workspace) notifyDownloadsReady() {
	a.importMu.Lock()
	a.downloadsPending = true
	hasUI := a.win != nil || a.postUI != nil
	a.importMu.Unlock()
	if hasUI {
		a.updateUI(func() { a.flushImports() })
	}
}

func (a *workspace) popImport() (queuedImport, bool) {
	a.importMu.Lock()
	defer a.importMu.Unlock()
	if len(a.importPaths) == 0 {
		return queuedImport{}, false
	}
	item := a.importPaths[0]
	a.importPaths = a.importPaths[1:]
	return item, true
}

func (a *workspace) popImportPath() (string, bool) {
	item, ok := a.popImport()
	return item.path, ok
}

func (a *workspace) takeDownloadsPending() bool {
	a.importMu.Lock()
	defer a.importMu.Unlock()
	if !a.downloadsPending {
		return false
	}
	a.downloadsPending = false
	return true
}

func (a *workspace) startDownloadPoll() bool {
	a.importMu.Lock()
	defer a.importMu.Unlock()
	if !a.downloadsPending || a.downloadsPolling {
		return false
	}
	a.downloadsPending = false
	a.downloadsPolling = true
	return true
}

// work serializes imports, exports, deletes, and saves while the UI stays on MyGo's UI thread.
func (a *workspace) work(task func() func()) {
	if a.busy || !a.hasUI() {
		return
	}
	a.busy = true
	go func() {
		finish := task()
		a.updateUI(func() {
			a.busy = false
			finish()
			if a.preferencesOpen {
				a.refreshStorageBytes()
			}
			a.flushImports()
		})
	}()
}

func (a *workspace) applyImport(r notebook.ImportResult) {
	if a.bookID == r.BookID {
		trail := slices.Clone(a.currentTrail())
		a.setLibrary(r.Library, r.BookID)
		a.setNoteWithTrail(a.selected, trail)
		if a.pdf.document == "" && len(a.tree.book.Documents) > 0 {
			a.resetPDF(a.tree.book.Documents)
		}
	} else {
		a.setLibrary(r.Library, r.BookID)
	}
	a.showImportResult(r)
}

func (a *workspace) showImportResult(r notebook.ImportResult) {
	var targetTitle string
	var isPDF bool
	for _, b := range r.Library.Books {
		if b.ID == r.BookID {
			targetTitle = b.Title
			isPDF = strings.HasPrefix(b.ID, "pdf-") && len(b.Notes) == 0 && len(b.Documents) > 0
			break
		}
	}
	switch {
	case r.Added > 1:
		a.showMessage(fmt.Sprintf("已导入 %d 本笔记本", r.Added), false)
	case r.Added == 1:
		switch {
		case isPDF && targetTitle != "":
			a.showMessage("已导入 PDF「"+targetTitle+"」", false)
		case targetTitle != "":
			a.showMessage("已导入「"+targetTitle+"」", false)
		default:
			a.showMessage("已导入笔记本", false)
		}
	case r.Updated > 0:
		if targetTitle != "" {
			a.showMessage("已更新「"+targetTitle+"」", false)
		} else {
			a.showMessage("已更新本地笔记本", false)
		}
	default:
		a.showMessage("该笔记本已在库中，已为你打开", false)
	}
}

func (a *workspace) importPath(path string) func() {
	canMove := isAndroidPickedCacheFile(runtime.GOOS, path, androidCacheDirs())
	return a.importPathWithOptions(path, canMove)
}

func formatByteSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", max(int64(0), n))
	}
}

func formatImportProgress(p notebook.ImportProgress) string {
	var prefix string
	switch p.Stage {
	case notebook.ImportStageExtractDB:
		prefix = "正在读取笔记数据库…"
	case notebook.ImportStageLoadAttachments:
		return importingAttachmentsMessage
	case notebook.ImportStageImportPDF:
		if p.Count > 1 && p.Index > 0 {
			prefix = fmt.Sprintf("正在导入 PDF (%d/%d)…", p.Index, p.Count)
		} else {
			prefix = "正在导入 PDF…"
		}
	default:
		return importingNotebookMessage
	}
	if p.Done <= 0 {
		return prefix
	}
	if p.Total > 0 {
		done := min(p.Done, p.Total)
		return fmt.Sprintf("%s %s / %s", prefix, formatByteSize(done), formatByteSize(p.Total))
	}
	return fmt.Sprintf("%s %s", prefix, formatByteSize(p.Done))
}

func (a *workspace) onAndroidImportProgress(raw []byte) {
	if !a.hasUI() {
		return
	}
	var payload struct {
		Copied int64 `json:"copied"`
		Total  int64 `json:"total"`
		Done   bool  `json:"done"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	if payload.Done {
		a.updateUI(func() {
			if !a.busy && strings.HasPrefix(a.message, "正在接收文件") {
				a.clearPendingMessage()
			}
		})
		return
	}
	var msg string
	switch {
	case payload.Total > 0:
		copied := min(max(int64(0), payload.Copied), payload.Total)
		msg = fmt.Sprintf("正在接收文件… %s / %s", formatByteSize(copied), formatByteSize(payload.Total))
	case payload.Copied > 0:
		msg = fmt.Sprintf("正在接收文件… %s", formatByteSize(payload.Copied))
	default:
		msg = "正在接收文件…"
	}
	a.updateUI(func() {
		if !a.busy {
			a.showProgress(msg)
		}
	})
}

func (a *workspace) importPathWithOptions(path string, canMove bool) func() {
	var before notebook.Library
	var beforeBook string
	previewed := false
	previewCb := func(preview notebook.ImportResult) {
		a.updateUI(func() {
			before, beforeBook = notebook.Library{Books: a.books}, a.bookID
			previewed = true
			a.setLibrary(preview.Library, preview.BookID)
			a.showProgressAction(importingAttachmentsMessage, "取消", a.cancelImport)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.importMu.Lock()
	a.importCancel = cancel
	canceledAlready := a.importCanceled
	a.importMu.Unlock()
	if canceledAlready {
		cancel()
	}
	var result notebook.ImportResult
	var err error
	if a.importFileFn != nil {
		result, err = a.importFileFn(path, previewCb)
	} else if a.store != nil {
		var progMu sync.Mutex
		var lastReport time.Time
		var lastStage notebook.ImportStage
		var hasReported bool
		result, err = a.store.ImportWithOptions(path, notebook.ImportOptions{
			Ctx:           ctx,
			Preview:       previewCb,
			CanMoveSource: canMove,
			OnProgress: func(p notebook.ImportProgress) {
				progMu.Lock()
				now := time.Now()
				stageChanged := !hasReported || p.Stage != lastStage
				if !stageChanged && now.Sub(lastReport) < 120*time.Millisecond && (p.Total <= 0 || p.Done < p.Total) {
					progMu.Unlock()
					return
				}
				hasReported = true
				lastStage = p.Stage
				lastReport = now
				progMu.Unlock()
				msg := formatImportProgress(p)
				a.updateUI(func() {
					a.showProgressAction(msg, "取消", a.cancelImport)
				})
			},
		})
	}
	cancel()
	a.importMu.Lock()
	a.importCancel = nil
	wasCanceled := a.importCanceled || errors.Is(err, context.Canceled)
	batchActive := a.importBatchActive
	moreQueued := len(a.importPaths) > 0
	if wasCanceled {
		a.importPaths = nil
		a.importBatchActive = false
		a.importBatchTotal = 0
		a.importCanceled = false
	} else if batchActive {
		if err != nil {
			a.importBatchFailed++
			if a.importBatchFirstErr == nil {
				a.importBatchFirstErr = err
			}
		} else {
			a.importBatchDone++
			a.importBatchBooks += result.Added + result.Updated
		}
	}
	doneCount := a.importBatchDone
	failedCount := a.importBatchFailed
	booksCount := a.importBatchBooks
	firstErr := a.importBatchFirstErr
	if batchActive && !moreQueued {
		a.importBatchActive = false
		a.importBatchTotal = 0
		a.importBatchDone = 0
		a.importBatchFailed = 0
		a.importBatchBooks = 0
		a.importBatchFirstErr = nil
	}
	a.importMu.Unlock()

	return func() {
		if wasCanceled {
			if previewed {
				a.replaceImportPreview(before, beforeBook)
			}
			a.showMessage("已取消导入", false)
			return
		}
		if err != nil {
			if previewed {
				a.replaceImportPreview(before, beforeBook)
			}
			if batchActive && !moreQueued {
				a.showBatchSummary(doneCount, failedCount, booksCount, firstErr)
			} else if !batchActive {
				a.showError(err)
			}
			return
		}
		if previewed {
			a.replaceImportPreview(result.Library, result.BookID)
			a.showImportResult(result)
		} else {
			a.applyImport(result)
		}
		if batchActive && !moreQueued {
			a.showBatchSummary(doneCount, failedCount, booksCount, firstErr)
		}
	}
}

func (a *workspace) showBatchSummary(done, failed, books int, firstErr error) {
	switch {
	case failed > 0 && done > 0:
		msg := fmt.Sprintf("批量导入完成：成功 %d 个，失败 %d 个", done, failed)
		if firstErr != nil {
			msg += "（" + friendlyError(firstErr) + "）"
		}
		a.showMessage(msg, true)
	case failed > 0:
		if firstErr != nil {
			a.showError(firstErr)
		} else {
			a.showMessage("批量导入失败", true)
		}
	case books > 0:
		a.showMessage(fmt.Sprintf("已导入 %d 个文件（新增或更新 %d 本笔记本）", done, books), false)
	default:
		a.showMessage(fmt.Sprintf("已检查 %d 个文件，相关笔记本均已在库中", done), false)
	}
}

func (a *workspace) replaceImportPreview(library notebook.Library, fallback string) {
	preferred := a.bookChoice
	if !slices.ContainsFunc(library.Books, func(book notebook.Book) bool { return book.ID == preferred }) {
		preferred = fallback
	}
	a.setLibrary(library, preferred)
	if _, ok := a.tree.notes[a.scope]; !ok {
		a.scope = ""
	}
	if _, ok := a.tree.notes[a.selected]; !ok {
		a.cardMenuOpen = false
		a.setNote(a.firstNote())
	} else if !a.tree.validTrail(a.activeTrail, a.selected) {
		a.activeTrail = a.tree.trail(a.selected)
	}
	if a.pdf.document == "" && len(a.tree.book.Documents) > 0 {
		a.resetPDF(a.tree.book.Documents)
	}
}

// androidCacheDirs returns the app's Android cache directory roots.
// MyGo's Android host sets both XDG_CACHE_HOME and TMPDIR to Context.cacheDir.
func androidCacheDirs() []string {
	var dirs []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" || !filepath.IsAbs(dir) {
			return
		}
		cleaned := filepath.Clean(dir)
		if filepath.Dir(cleaned) == cleaned || cleaned == "/tmp" {
			return
		}
		if !slices.Contains(dirs, cleaned) {
			dirs = append(dirs, cleaned)
		}
	}
	if dir, err := os.UserCacheDir(); err == nil {
		add(dir)
	}
	add(os.TempDir())
	return dirs
}

func isPathStrictlyWithinDir(root, target string) bool {
	if root == "" || target == "" || !filepath.IsAbs(root) || !filepath.IsAbs(target) {
		return false
	}
	cleanRoot := filepath.Clean(root)
	if filepath.Dir(cleanRoot) == cleanRoot {
		return false
	}
	cleanTarget := filepath.Clean(target)
	rel, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil || rel == "" || rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// isAndroidPickedCacheFile reports whether filePath is strictly inside one of
// Android's MyGo picker temporary cache directories (<cacheDir>/picked/...).
// On non-Android platforms it always returns false so user files are never matched.
func isAndroidPickedCacheFile(goos, filePath string, cacheDirs []string) bool {
	if goos != "android" || strings.TrimSpace(filePath) == "" || !filepath.IsAbs(filePath) {
		return false
	}
	cleanPath := filepath.Clean(filePath)
	for _, cacheDir := range cacheDirs {
		if strings.TrimSpace(cacheDir) == "" || !filepath.IsAbs(cacheDir) {
			continue
		}
		cleanCache := filepath.Clean(cacheDir)
		if filepath.Dir(cleanCache) == cleanCache {
			continue
		}
		pickedDir := filepath.Join(cleanCache, "picked")
		if isPathStrictlyWithinDir(pickedDir, cleanPath) {
			return true
		}
	}
	return false
}

// cleanupAndroidPickedFile deletes filePath (and any now-empty per-pick subdirectory)
// only on Android and only when the resolved regular file lives strictly inside
// <cacheDir>/picked/. It never removes symlinks, directories, or <cacheDir>/picked itself.
func cleanupAndroidPickedFile(goos, filePath string, cacheDirs []string) bool {
	if goos != "android" || strings.TrimSpace(filePath) == "" || !filepath.IsAbs(filePath) {
		return false
	}
	cleanPath := filepath.Clean(filePath)
	info, err := os.Lstat(cleanPath)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	parentDir, err := filepath.EvalSymlinks(filepath.Dir(cleanPath))
	if err != nil {
		return false
	}
	realTarget := filepath.Join(parentDir, filepath.Base(cleanPath))

	var matchedPickedRoot string
	for _, cacheDir := range cacheDirs {
		if strings.TrimSpace(cacheDir) == "" || !filepath.IsAbs(cacheDir) {
			continue
		}
		cleanCache := filepath.Clean(cacheDir)
		if filepath.Dir(cleanCache) == cleanCache {
			continue
		}
		pickedDir := filepath.Join(cleanCache, "picked")
		realPickedDir, err := filepath.EvalSymlinks(pickedDir)
		if err != nil {
			continue
		}
		if isPathStrictlyWithinDir(realPickedDir, realTarget) {
			matchedPickedRoot = realPickedDir
			break
		}
	}
	if matchedPickedRoot == "" {
		return false
	}
	if err := os.Remove(realTarget); err != nil {
		return false
	}
	for dir := parentDir; isPathStrictlyWithinDir(matchedPickedRoot, dir); {
		if err := os.Remove(dir); err != nil {
			break
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
	return true
}

func cleanupAndroidPickedFiles(paths ...string) {
	if runtime.GOOS != "android" || len(paths) == 0 {
		return
	}
	cacheDirs := androidCacheDirs()
	for _, p := range paths {
		cleanupAndroidPickedFile(runtime.GOOS, p, cacheDirs)
	}
}

func cleanupStaleAndroidPicked(cacheDirs []string, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	for _, cacheDir := range cacheDirs {
		if strings.TrimSpace(cacheDir) == "" || !filepath.IsAbs(cacheDir) {
			continue
		}
		pickedDir := filepath.Join(filepath.Clean(cacheDir), "picked")
		realPicked, err := filepath.EvalSymlinks(pickedDir)
		if err != nil {
			continue
		}
		_ = filepath.WalkDir(realPicked, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || p == realPicked {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.ModTime().After(cutoff) {
				return nil
			}
			if os.Remove(p) == nil {
				parent := filepath.Dir(p)
				for isPathStrictlyWithinDir(realPicked, parent) {
					if os.Remove(parent) != nil {
						break
					}
					next := filepath.Dir(parent)
					if next == parent {
						break
					}
					parent = next
				}
			}
			return nil
		})
	}
}

func (a *workspace) chooseImport() {
	win := a.window()
	openFn := a.openImportDialogFn
	if openFn == nil {
		openFn = mygo.Dialog.Open
	}
	a.work(func() func() {
		filters := []mygo.FileFilter{{Name: "MarginNote / PDF", Extensions: []string{"marginpkg", "marginnotes", "pdf"}}}
		if runtime.GOOS == "android" {
			// Android's document providers usually give MarginNote files a generic MIME type.
			filters = nil
		}
		paths, err := openFn(mygo.OpenDialogOptions{
			Parent: win, Title: "导入笔记或 PDF",
			Filters: filters, Multiple: true,
		})
		if err != nil {
			return func() { a.showError(err) }
		}
		if len(paths) == 0 {
			return a.clearPendingMessage
		}
		if len(paths) > 1 {
			a.enqueueImport(paths[1:]...)
		}
		first := paths[0]
		defer cleanupAndroidPickedFiles(first)
		a.updateUI(func() {
			a.preferencesOpen = false
			a.renameOpen = false
			a.showProgressAction(importingNotebookMessage, "取消", a.cancelImport)
		})
		return a.importPath(first)
	})
}

func takeAndroidDownload(ctx context.Context) (*pendingAndroidDownload, error) {
	var opened *pendingAndroidDownload
	err := mygo.Native(androidopen.PluginID).Call(ctx, "take", nil, &opened)
	return opened, err
}

func (a *workspace) flushImports() {
	if !a.hasUI() || a.busy {
		return
	}
	if a.editing() {
		a.importMu.Lock()
		pending := len(a.importPaths)
		a.importMu.Unlock()
		if pending > 0 && a.message == "" {
			a.showMessage("正在编辑笔记，请先保存或关闭后再导入", false)
		}
		return
	}
	if item, ok := a.popImport(); ok {
		a.showProgressAction(importingNotebookMessage, "取消", a.cancelImport)
		a.work(func() func() {
			defer func() {
				if item.cleanup {
					_ = os.Remove(item.path)
					_ = os.Remove(filepath.Dir(item.path))
				} else {
					cleanupAndroidPickedFiles(item.path)
				}
			}()
			return a.importPathWithOptions(item.path, item.cleanup || isAndroidPickedCacheFile(runtime.GOOS, item.path, androidCacheDirs()))
		})
		return
	}
	takeFn := a.takeDownloadFn
	if takeFn == nil {
		if runtime.GOOS != "android" {
			return
		}
		takeFn = takeAndroidDownload
	}
	if !a.startDownloadPoll() {
		return
	}
	// Poll Android's pending-open queue off the UI thread without setting
	// a.busy = true unless a non-empty file path is actually returned.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		opened, err := takeFn(ctx)
		cancel()

		if err == nil && (opened == nil || (opened.Path == "" && opened.Error == "")) {
			a.importMu.Lock()
			a.downloadsPolling = false
			more := a.downloadsPending
			a.importMu.Unlock()
			if more {
				a.updateUI(func() { a.flushImports() })
			}
			return
		}

		a.importMu.Lock()
		a.downloadsPolling = false
		if opened != nil {
			a.downloadsPending = true
			if err == nil && opened.Error == "" && opened.Path != "" {
				a.importPaths = append(a.importPaths, queuedImport{path: opened.Path, cleanup: true})
			}
		}
		a.importMu.Unlock()

		a.updateUI(func() {
			if err != nil {
				a.showError(err)
			} else if opened != nil && opened.Error != "" {
				a.showError(errors.New(opened.Error))
			}
			a.flushImports()
		})
	}()
}

func parseTagsInput(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == ' ' || r == '\t' || r == '\n'
	})
	return notebook.NormalizeTags(fields)
}

func formatTagsInput(tags []string) string {
	norm := notebook.NormalizeTags(tags)
	return strings.Join(norm, " ")
}

func (a *workspace) isDescendantOf(rootID, candidateID string) bool {
	if rootID == candidateID {
		return true
	}
	seen := map[string]bool{rootID: true}
	stack := []string{rootID}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, cid := range a.tree.kids[cur] {
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

func (a *workspace) validParentCandidates(noteID string) []string {
	var out []string
	for _, id := range a.tree.order {
		if id == "" || id == a.tree.root || a.isDescendantOf(noteID, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}

func (a *workspace) startEditNote() {
	n := a.tree.notes[a.selected]
	a.cardMenuOpen = false
	if a.page != detail {
		a.prevPage = a.page
	}
	a.editMode, a.page = editNote, detail
	body := n.Body
	if body == "" && n.Clip == "" {
		body = n.Excerpt
	}
	a.draftTitle, a.draftBody = n.Title, body
	a.origTitle, a.origBody = a.draftTitle, a.draftBody
	a.draftColorSet, a.draftColor = false, 0
	if n.Color != nil {
		a.draftColorSet, a.draftColor = true, *n.Color
	}
	a.origColorSet, a.origColor = a.draftColorSet, a.draftColor
	a.draftTags = formatTagsInput(n.Tags)
	a.origTags = a.draftTags
	parent := a.tree.parent[n.ID]
	if parent == a.tree.root {
		parent = ""
	}
	a.draftParent, a.origParent = parent, parent
	a.discardPrompt = false
}

func (a *workspace) startAddChild() {
	a.cardMenuOpen = false
	if a.page != detail {
		a.prevPage = a.page
	}
	a.editMode, a.page = editNewChild, detail
	a.draftTitle, a.draftBody = "", ""
	a.origTitle, a.origBody = "", ""
	a.draftColorSet, a.draftColor = false, 0
	a.origColorSet, a.origColor = false, 0
	a.draftTags, a.origTags = "", ""
	parent := a.selected
	if parent == a.tree.root {
		parent = ""
	}
	a.draftParent, a.origParent = parent, parent
	a.discardPrompt = false
}

func (a *workspace) editorDirty() bool {
	return a.draftTitle != a.origTitle || a.draftBody != a.origBody ||
		a.draftColorSet != a.origColorSet || (a.draftColorSet && a.draftColor != a.origColor) ||
		a.draftTags != a.origTags || a.draftParent != a.origParent
}

func (a *workspace) canSaveEditor() bool {
	return strings.TrimSpace(a.draftTitle) != "" || strings.TrimSpace(a.draftBody) != ""
}

func (a *workspace) requestCancelEdit() {
	if !a.editing() {
		return
	}
	if !a.editorDirty() || a.discardPrompt {
		a.editMode = editNone
		a.discardPrompt = false
		a.flushImports()
		return
	}
	a.discardPrompt = true
	a.discardShakeRun++
}

func (a *workspace) saveEditor() {
	if !a.canSaveEditor() {
		a.showMessage("请先填写标题或正文", true)
		return
	}
	mode := a.editMode
	id, bookID, title, body := a.selected, a.bookID, a.draftTitle, a.draftBody
	var colorPtr *int
	if a.draftColorSet {
		v := a.draftColor
		colorPtr = &v
	}
	tags := parseTagsInput(a.draftTags)
	var moveParent *string
	if mode == editNote && a.draftParent != a.origParent {
		p := a.draftParent
		if p == "" && a.tree.root != "" {
			p = a.tree.root
		}
		moveParent = &p
	}
	prevTrail := slices.Clone(a.currentTrail())
	a.work(func() func() {
		if mode == editNewChild {
			library, newID, err := a.store.AddNoteWithMeta(bookID, id, title, body, colorPtr, tags)
			return func() {
				if err != nil {
					a.showError(err)
					return
				}
				a.setLibrary(library, bookID)
				a.editMode = editNone
				a.discardPrompt = false
				nextTrail := append(prevTrail, newID)
				a.setNoteWithTrail(newID, nextTrail)
				a.page = detail
				a.showMessage("已新建子笔记", false)
			}
		}
		library, err := a.store.SaveNoteWithMeta(bookID, id, title, body, colorPtr, tags, moveParent)
		return func() {
			if err != nil {
				a.showError(err)
				return
			}
			a.setLibrary(library, bookID)
			a.editMode = editNone
			a.discardPrompt = false
			if moveParent != nil {
				a.setNote(id)
			} else {
				a.setNoteWithTrail(id, prevTrail)
			}
			a.page = detail
			a.showMessage("已保存", false)
		}
	})
}

func (a *workspace) exportMarkdown() {
	if a.bookID == "" {
		return
	}
	book := a.tree.book
	fileName := sanitizeExportName(book.Title) + ".zip"
	a.showProgress("正在导出 Markdown…")
	a.work(func() func() {
		tmp, err := os.CreateTemp("", "mgnote-export-*.zip")
		if err != nil {
			return func() { a.showError(err) }
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		missingImages, writeErr := a.store.ExportMarkdownZIPWithStats(book.ID, tmp)
		if err := errors.Join(writeErr, tmp.Close()); err != nil {
			return func() { a.showError(err) }
		}
		saved, err := mygo.Dialog.SaveCopy(tmpPath, fileName)
		return func() {
			if err != nil {
				a.showError(err)
			} else if saved != "" {
				if missingImages > 0 {
					a.showMessage(fmt.Sprintf("已导出 Markdown（跳过 %d 张缺失图片）", missingImages), false)
				} else {
					a.showMessage("已导出 Markdown 和图片", false)
				}
			} else {
				a.clearPendingMessage()
			}
		}
	})
}

func (a *workspace) confirmDeleteNote() {
	bookID, id := a.bookID, a.selected
	if id == "" {
		return
	}
	title := noteTitle(a.tree.notes[id])
	parentID := a.tree.parent[id]
	var fallbackID string
	if kids := a.tree.kids[parentID]; len(kids) > 1 {
		for idx, k := range kids {
			if k == id {
				if idx+1 < len(kids) {
					fallbackID = kids[idx+1]
				} else if idx > 0 {
					fallbackID = kids[idx-1]
				}
				break
			}
		}
	}
	if fallbackID == "" {
		fallbackID = parentID
	}
	hapticWarning()
	win := a.window()
	a.work(func() func() {
		res, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent: win, Type: mygo.MessageWarning, Title: "删除笔记",
			Message: "确定要删除「" + title + "」吗？", Detail: "下级笔记将保留并移到上一级，删除后无法撤销。",
			Buttons: []string{"取消", "删除"}, CancelButton: 0, DestructiveButtons: []int{1},
		})
		if err != nil {
			return func() { a.showError(err) }
		}
		if res.Button != 1 {
			return func() {}
		}
		library, err := a.store.DeleteNote(bookID, id)
		return func() {
			if err != nil {
				a.showError(err)
				return
			}
			a.setLibrary(library, a.bookID)
			if a.scope == id {
				a.scope = ""
			}
			if _, ok := a.tree.notes[fallbackID]; ok && fallbackID != "" {
				a.setNote(fallbackID)
			} else {
				a.setNote(a.firstNote())
			}
			a.page = browse
			a.showMessage("已删除笔记", false)
		}
	})
}

func sanitizeExportName(title string) string {
	return notebook.SanitizeName(title)
}

const webdavSecretKey = "folio.webdav.password"

func (a *workspace) loadWebDAVDraft() {
	if a.webdavURL == "" && a.settings.WebDAVURL != "" {
		a.webdavURL = a.settings.WebDAVURL
	}
	if a.webdavUser == "" && a.settings.WebDAVUsername != "" {
		a.webdavUser = a.settings.WebDAVUsername
	}
	if a.webdavPasswordLoaded {
		return
	}
	a.webdavPasswordLoaded = true
	if raw, err := mygo.Secrets.Get(webdavSecretKey); err == nil {
		a.webdavPassword = string(raw)
	}
}

func (a *workspace) saveWebDAVDraft() (notebook.WebDAVConfig, bool) {
	urlStr := strings.TrimSpace(a.webdavURL)
	user := strings.TrimSpace(a.webdavUser)
	pw := a.webdavPassword
	next := a.settings
	if next.WebDAVURL != urlStr || next.WebDAVUsername != user {
		next.WebDAVURL = urlStr
		next.WebDAVUsername = user
		if !a.saveSettings(next) {
			return notebook.WebDAVConfig{}, false
		}
	}
	if pw != "" {
		if err := mygo.Secrets.Set(webdavSecretKey, []byte(pw)); err != nil && !errors.Is(err, errors.ErrUnsupported) {
			// Keep the password in memory for the current session even if keychain storage fails.
			a.webdavStatus = "密码仅在本次运行期间有效（系统凭据库不可用）"
		}
	}
	return notebook.WebDAVConfig{
		BaseURL:  urlStr,
		Username: user,
		Password: pw,
	}, true
}

func (a *workspace) exportBackup() {
	if a.busy || a.store == nil {
		return
	}
	fileName := "folio-backup-" + time.Now().Format("20060102-1504") + ".zip"
	a.showProgress("正在打包整库备份…")
	a.work(func() func() {
		a.drawingWrites.wait()
		tmp, err := os.CreateTemp("", "folio-backup-*.zip")
		if err != nil {
			return func() { a.showError(err) }
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		writeErr := a.store.ExportBackupZIP(tmp)
		if err := errors.Join(writeErr, tmp.Close()); err != nil {
			return func() { a.showError(err) }
		}
		saved, err := mygo.Dialog.SaveCopy(tmpPath, fileName)
		return func() {
			if err != nil {
				a.showError(err)
			} else if saved != "" {
				a.showMessage("已导出整库备份压缩包", false)
			} else {
				a.clearPendingMessage()
			}
		}
	})
}

func (a *workspace) restoreBackup() {
	if a.busy || a.store == nil {
		return
	}
	win := a.window()
	a.work(func() func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:  win,
			Title:   "选择整库备份压缩包",
			Filters: []mygo.FileFilter{{Name: "Folio Backup ZIP", Extensions: []string{"zip"}}},
		})
		if err != nil {
			return func() { a.showError(err) }
		}
		if len(paths) == 0 {
			return func() {}
		}
		chosen := paths[0]
		defer cleanupAndroidPickedFiles(chosen)
		res, msgErr := mygo.Dialog.Message(mygo.MessageOptions{
			Parent:             win,
			Type:               mygo.MessageWarning,
			Title:              "恢复整库备份",
			Message:            "确定要从备份恢复全部笔记本与设置吗？",
			Detail:             "当前本地的所有笔记本、手写批注、PDF 与设置将被备份内容替换，此操作无法撤销。",
			Buttons:            []string{"取消", "确认恢复"},
			CancelButton:       0,
			DestructiveButtons: []int{1},
		})
		if msgErr != nil {
			return func() { a.showError(msgErr) }
		}
		if res.Button != 1 {
			return func() {}
		}
		a.updateUI(func() {
			a.showProgress("正在恢复整库备份…")
		})
		a.drawingWrites.wait()
		closePDFRendererCache()
		lib, restoredSettings, err := a.store.RestoreBackupZIP(chosen)
		return func() {
			if err != nil {
				a.showError(err)
				return
			}
			a.releaseMemoryCaches()
			a.settings = restoredSettings
			syncAppTheme(restoredSettings.Appearance)
			a.webdavURL = restoredSettings.WebDAVURL
			a.webdavUser = restoredSettings.WebDAVUsername
			a.preferencesOpen = false
			a.renameOpen = false
			targetID := restoredSettings.LastBookID
			if targetID == "" && len(lib.Books) > 0 {
				targetID = lib.Books[0].ID
			}
			a.setLibrary(lib, targetID)
			a.refreshStorageBytes()
			a.showMessage("已从备份恢复笔记库", false)
		}
	})
}

func (a *workspace) testWebDAVConnection() {
	if a.webdavBusy || a.busy {
		return
	}
	cfg, ok := a.saveWebDAVDraft()
	if !ok {
		return
	}
	a.webdavBusy = true
	a.webdavStatus = "正在测试 WebDAV 连接…"
	a.work(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := notebook.TestWebDAV(ctx, cfg)
		return func() {
			a.webdavBusy = false
			if err != nil {
				a.webdavStatus = err.Error()
				a.showError(err)
				return
			}
			a.webdavStatus = "连接成功，目录可访问"
			a.showMessage("WebDAV 连接测试成功", false)
		}
	})
}

func (a *workspace) syncWebDAV() {
	if a.webdavBusy || a.busy || a.store == nil {
		return
	}
	cfg, ok := a.saveWebDAVDraft()
	if !ok {
		return
	}
	a.webdavBusy = true
	a.webdavStatus = "正在与 WebDAV 双向同步…"
	a.showProgress("正在与 WebDAV 同步…")
	a.work(func() func() {
		a.drawingWrites.wait()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := a.store.SyncWebDAV(ctx, cfg)
		return func() {
			a.webdavBusy = false
			if err != nil {
				a.webdavStatus = err.Error()
				a.showError(err)
				return
			}
			next := a.settings
			next.WebDAVLastSyncUnix = res.SyncedAt
			a.saveSettings(next)
			if a.bookID != "" || len(res.Library.Books) > 0 {
				a.setLibrary(res.Library, a.bookID)
				if a.bookID != "" {
					a.loadDrawing(a.bookID)
				}
			}
			a.refreshStorageBytes()
			summary := fmt.Sprintf("同步完成（上传 %d 本/%d 个文件，拉取 %d 本/%d 个文件）",
				res.UploadedBooks, res.UploadedFiles, res.DownloadedBooks, res.DownloadedFiles)
			a.webdavStatus = summary
			a.showMessage(summary, false)
		}
	})
}

func (a *workspace) confirmDeleteBook() {
	if a.bookID == "" {
		return
	}
	bookID, title := a.bookID, a.tree.book.Title
	noteCount := len(a.tree.book.Notes)
	docCount := len(a.tree.book.Documents)
	detailText := fmt.Sprintf("将永久删除「%s」中的 %d 条笔记、全部手写批注及未再使用的附图，此操作无法撤销。", title, noteCount)
	if docCount > 0 {
		detailText = fmt.Sprintf("将永久删除「%s」中的 %d 条笔记、全部手写批注，以及未再使用的 %d 份 PDF 与附图，此操作无法撤销。", title, noteCount, docCount)
	}
	hapticWarning()
	win := a.window()
	a.work(func() func() {
		res, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent:             win,
			Type:               mygo.MessageWarning,
			Title:              "删除笔记本",
			Message:            "确定要永久删除「" + title + "」吗？",
			Detail:             detailText,
			Buttons:            []string{"取消", "永久删除"},
			CancelButton:       0,
			DestructiveButtons: []int{1},
		})
		if err != nil {
			return func() { a.showError(err) }
		}
		if res.Button != 1 {
			return func() {}
		}
		a.drawingWrites.wait()
		closePDFRendererCache()
		library, err := a.store.DeleteBook(bookID)
		return func() {
			if err != nil {
				a.showError(err)
				return
			}
			a.drawingWrites.forget(bookID)
			a.preferencesOpen = false
			a.renameOpen = false
			a.setLibrary(library, "")
			a.showMessage("已删除笔记本「"+title+"」", false)
		}
	})
}

func (a *workspace) updateSnapshot() updateUIState {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	st := a.update
	if st.info != nil {
		cp := *st.info
		st.info = &cp
	}
	return st
}

func (a *workspace) updateViewState() (updateUIState, bool) {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	st := a.update
	if st.info != nil {
		cp := *st.info
		st.info = &cp
	}
	return st, a.updateBannerDismissed
}

func (a *workspace) hasUpdateBadge() bool {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	return a.update.hasAttention()
}

func (a *workspace) dismissUpdateBanner() {
	a.updateMu.Lock()
	a.updateBannerDismissed = true
	a.updateMu.Unlock()
}

func (a *workspace) setUpdateState(services ui.Services, fn func(*updateUIState)) {
	if win := a.window(); win != nil {
		win.Update(func() {
			a.updateMu.Lock()
			fn(&a.update)
			a.updateMu.Unlock()
		})
		return
	}
	a.updateMu.Lock()
	fn(&a.update)
	a.updateMu.Unlock()
	services.Invalidate()
}

func (a *workspace) scheduleAutoCheckUpdates() {
	go func() {
		time.Sleep(autoUpdateCheckDelay)
		a.maybeAutoCheckUpdates()
	}()
}

func (a *workspace) readLastUpdateCheckUnix() int64 {
	if a.window() != nil && a.store != nil {
		if s, err := a.store.ReadSettings(); err == nil {
			return s.LastUpdateCheckUnix
		}
		return 0
	}
	return a.settings.LastUpdateCheckUnix
}

func (a *workspace) recordUpdateCheckUnix(nowUnix int64) {
	apply := func() {
		if a.settings.LastUpdateCheckUnix >= nowUnix {
			return
		}
		next := a.settings
		next.LastUpdateCheckUnix = nowUnix
		a.settings = next
		if a.store != nil {
			_ = a.store.SaveSettings(next)
		}
	}
	if a.hasUI() {
		a.updateUI(apply)
		return
	}
	apply()
}

func (a *workspace) isNetworkOnline() bool {
	if a.isOnlineFn != nil {
		return a.isOnlineFn()
	}
	// mygo.Network.Status() requires App.Run to have started; skip it in headless tests.
	if a.window() == nil {
		return true
	}
	return mygo.Network.Status().Online
}

func (a *workspace) maybeAutoCheckUpdates() {
	a.updateMu.Lock()
	if a.update.phase != updateIdle || a.autoCheckRunning {
		a.updateMu.Unlock()
		return
	}
	a.autoCheckRunning = true
	pendingFn := a.checkPendingAndroidUpdateFn
	if pendingFn == nil && runtime.GOOS == "android" {
		pendingFn = checkPendingAndroidUpdate
	}
	checkFn := a.checkUpdateFn
	if checkFn == nil {
		checkFn = checkAppUpdate
	}
	a.updateMu.Unlock()

	defer func() {
		a.updateMu.Lock()
		a.autoCheckRunning = false
		a.updateMu.Unlock()
	}()

	// 1. On Android, check first for an already-downloaded verified pending APK
	// (e.g. after Android restarted the app when granting Unknown Sources permission).
	if pendingFn != nil {
		if pendingInfo, canInstallNow := pendingFn(); pendingInfo != nil {
			cp := *pendingInfo
			targetPhase := updateAvailable
			if !canInstallNow {
				targetPhase = updatePermissionRequired
			}
			a.setUpdateState(ui.Services{}, func(st *updateUIState) {
				if st.phase != updateIdle {
					return
				}
				*st = updateUIState{
					phase: targetPhase,
					info:  &cp,
				}
				a.updateBannerDismissed = false
			})
			return
		}
	}

	// 2. Throttle automatic background checks to once every 12 hours.
	lastCheckUnix := a.readLastUpdateCheckUnix()
	if lastCheckUnix > 0 && time.Since(time.Unix(lastCheckUnix, 0)) < autoUpdateCheckInterval {
		return
	}

	// 3. Avoid unnecessary requests while offline.
	if !a.isNetworkOnline() {
		return
	}

	// 4. Run a silent check: persist timestamp on success (including up-to-date
	// and errNoPublishedRelease), stay completely silent on network errors.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	info, err := checkFn(ctx)
	if err != nil && !errors.Is(err, errNoPublishedRelease) {
		return
	}
	a.recordUpdateCheckUnix(time.Now().Unix())
	if info == nil {
		return
	}
	cp := *info
	a.setUpdateState(ui.Services{}, func(st *updateUIState) {
		if st.phase != updateIdle && st.phase != updateUpToDate && st.phase != updateNoRelease {
			return
		}
		*st = updateUIState{
			phase: updateAvailable,
			info:  &cp,
		}
		a.updateBannerDismissed = false
	})
}

func (a *workspace) checkForUpdates(services ui.Services) {
	a.updateMu.Lock()
	if a.update.phase == updateChecking || a.update.phase == updateDownloading {
		a.updateMu.Unlock()
		return
	}
	a.update = updateUIState{phase: updateChecking}
	checkFn := a.checkUpdateFn
	if checkFn == nil {
		checkFn = checkAppUpdate
	}
	a.updateMu.Unlock()
	services.Invalidate()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		info, err := checkFn(ctx)
		a.setUpdateState(services, func(st *updateUIState) {
			switch {
			case errors.Is(err, errNoPublishedRelease):
				*st = updateUIState{phase: updateNoRelease}
			case err != nil:
				*st = updateUIState{
					phase:   updateError,
					errText: summarizeUpdateError(err),
				}
			case info == nil:
				*st = updateUIState{phase: updateUpToDate}
			default:
				cp := *info
				*st = updateUIState{
					phase: updateAvailable,
					info:  &cp,
				}
				a.updateBannerDismissed = false
			}
		})
		if (err == nil || errors.Is(err, errNoPublishedRelease)) && a.window() != nil {
			a.recordUpdateCheckUnix(time.Now().Unix())
		}
	}()
}

func (a *workspace) isDownloadSuperseded(gen uint64) bool {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	return a.updateDownloadGen != gen || a.updateCanceledGen == gen
}

func (a *workspace) cancelUpdateDownload() {
	a.updateMu.Lock()
	if a.update.phase != updateDownloading {
		a.updateMu.Unlock()
		return
	}
	cancel := a.updateCancel
	a.updateCancel = nil
	a.updateCanceledGen = a.updateDownloadGen
	info := a.update.info
	a.update = updateUIState{
		phase: updateAvailable,
		info:  info,
	}
	a.updateMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if win := a.window(); win != nil {
		win.Update(func() {})
	}
}

func (a *workspace) installUpdate(services ui.Services) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	a.updateMu.Lock()
	if a.update.phase == updateChecking || a.update.phase == updateDownloading || a.update.info == nil {
		a.updateMu.Unlock()
		cancel()
		return
	}
	a.updateDownloadGen++
	gen := a.updateDownloadGen
	a.updateCancel = cancel
	infoCopy := *a.update.info
	a.update = updateUIState{
		phase: updateDownloading,
		info:  &infoCopy,
	}
	installFn := a.installUpdateFn
	if installFn == nil {
		installFn = installAppUpdate
	}
	a.updateMu.Unlock()
	services.Invalidate()

	go func() {
		defer cancel()

		var (
			progMu     sync.Mutex
			lastReport time.Time
			lastPct    = -1
		)
		progress := func(downloaded, total int64) {
			progMu.Lock()
			now := time.Now()
			pct := -1
			if total > 0 {
				pct = int(min(100, max(0, downloaded*100/total)))
			}
			shouldPublish := lastReport.IsZero() ||
				(pct >= 0 && pct != lastPct) ||
				(total > 0 && downloaded >= total) ||
				now.Sub(lastReport) >= 120*time.Millisecond
			if !shouldPublish {
				progMu.Unlock()
				return
			}
			lastReport = now
			lastPct = pct
			progMu.Unlock()

			a.setUpdateState(services, func(st *updateUIState) {
				if st.phase != updateDownloading || a.updateDownloadGen != gen || a.updateCanceledGen == gen {
					return
				}
				st.downloaded = downloaded
				st.total = total
			})
		}

		res, err := installFn(ctx, &infoCopy, progress)
		a.setUpdateState(services, func(st *updateUIState) {
			if a.updateDownloadGen == gen {
				a.updateCancel = nil
			}
			if a.updateDownloadGen != gen || a.updateCanceledGen == gen {
				return
			}
			if err != nil {
				*st = updateUIState{
					phase:   updateError,
					info:    &infoCopy,
					errText: summarizeUpdateError(err),
				}
				return
			}
			switch {
			case res.RelaunchRequired:
				st.phase = updateRelaunchRequired
			case res.PermissionRequired:
				st.phase = updatePermissionRequired
			case res.DeferredUntilForeground:
				st.phase = updateDeferredForeground
			case res.FallbackURL != "":
				st.phase = updateAvailable
			default:
				st.phase = updateInstallerLaunched
			}
		})
		if err == nil && res.FallbackURL != "" && ui.SafeToOpen(res.FallbackURL) && !a.isDownloadSuperseded(gen) {
			if win := a.window(); win != nil {
				urlToOpen := res.FallbackURL
				win.Update(func() { services.OpenURL(urlToOpen) })
			}
		}
	}()
}

func (a *workspace) relaunchApp() {
	if a.relaunchFn != nil {
		a.relaunchFn()
		return
	}
	mygo.App.Relaunch()
}

func summarizeUpdateError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errDownloadStalled) {
		return errDownloadStalled.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "连接超时，请检查网络连接后重试"
	}
	if errors.Is(err, context.Canceled) {
		return "已取消更新操作"
	}
	return friendlyError(err)
}

func (a *workspace) onAndroidInstallState(raw []byte) {
	var payload struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return
	}
	a.updateMu.Lock()
	switch payload.Status {
	case "permission_required":
		a.update.phase = updatePermissionRequired
	case "launched":
		a.update.phase = updateInstallerLaunched
	default:
		a.updateMu.Unlock()
		return
	}
	a.updateMu.Unlock()
	if win := a.window(); win != nil {
		win.Update(func() {})
	}
}

type savedWorkspaceState struct {
	BookID      string        `json:"bookID,omitempty"`
	Scope       string        `json:"scope,omitempty"`
	Selected    string        `json:"selected,omitempty"`
	ActiveTrail []string      `json:"activeTrail,omitempty"`
	Mode        workspaceMode `json:"mode,omitempty"`
	PDFDocument string        `json:"pdfDocument,omitempty"`
	PDFPage     int           `json:"pdfPage,omitempty"`
	Query       string        `json:"query,omitempty"`
}

func (a *workspace) marshalSessionState() []byte {
	if a.bookID == "" {
		return nil
	}
	state := savedWorkspaceState{
		BookID:      a.bookID,
		Scope:       a.scope,
		Selected:    a.selected,
		ActiveTrail: slices.Clone(a.activeTrail),
		Mode:        a.mode,
		PDFDocument: a.pdf.document,
		PDFPage:     a.pdf.page,
		Query:       a.query,
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil
	}
	return raw
}

func (a *workspace) restoreSessionState(raw []byte) {
	if len(raw) == 0 {
		return
	}
	var state savedWorkspaceState
	if err := json.Unmarshal(raw, &state); err != nil || state.BookID == "" {
		return
	}
	if !slices.ContainsFunc(a.books, func(b notebook.Book) bool { return b.ID == state.BookID }) {
		return
	}
	a.setLibrary(notebook.Library{Books: a.books}, state.BookID)
	if state.Scope != "" {
		if _, ok := a.tree.notes[state.Scope]; ok {
			a.scope = state.Scope
		}
	}
	if state.Selected != "" {
		if _, ok := a.tree.notes[state.Selected]; ok {
			if a.tree.validTrail(state.ActiveTrail, state.Selected) {
				a.setNoteWithTrail(state.Selected, state.ActiveTrail)
			} else {
				a.setNote(state.Selected)
			}
		}
	}
	if state.Query != "" {
		a.query = state.Query
		a.invalidateSearch()
	}
	if state.PDFDocument != "" && slices.ContainsFunc(a.tree.book.Documents, func(d notebook.Document) bool { return d.ID == state.PDFDocument }) {
		a.setPDFDocument(state.PDFDocument)
		if state.PDFPage >= 0 {
			a.setPDFPage(state.PDFPage)
		}
	}
	switch state.Mode {
	case splitMode:
		a.setMode(splitMode)
	case pdfMode:
		a.setMode(pdfMode)
	case canvasMode:
		a.setMode(canvasMode)
	}
}
