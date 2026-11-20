package main

import (
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
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
	preferencesOpen bool
	renameOpen      bool
	bookName        string
	storageBytes    int64

	bookID, scope, selected, query string
	bookChoice                     string
	activeTrail                    []string
	page                           screen
	editMode                       editorMode
	draftTitle, draftBody string
	preview               string
	images                map[string]*ui.Bitmap
	mediaCache            map[string]*ui.Bitmap
	mediaDims             map[string]image.Point
	cardMenuOpen          bool

	cachedQuery      string
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
	mode                                               workspaceMode
	splitRatio, splitStart, splitPointerX, marginStart float32
	splitDragging, marginDragging                      bool
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

	importMu           sync.Mutex
	postUI             func(func())
	downloadsPending   bool
	downloadsPolling   bool
	importPaths        []queuedImport
	takeDownloadFn     func(context.Context) (*pendingAndroidDownload, error)
	importFileFn       func(string) (notebook.ImportResult, error)
	openImportDialogFn func(mygo.OpenDialogOptions) ([]string, error)

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

	message        string
	messageError   bool
	messagePending bool
	messageUntil   time.Time
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
		if blk.Text != "" {
			b.WriteByte(' ')
			b.WriteString(blk.Text)
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
		return "未命名节点"
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
	return noteText(n) != "" || n.Clip != "" || len(n.Blocks) > 0
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
	a.cachedHits = nil
}

func (a *workspace) searchNotes() []string {
	if a.cachedQueryReady && a.cachedQuery == a.query {
		return a.cachedHits
	}
	words := strings.Fields(strings.ToLower(a.query))
	var ids []string
	for _, id := range a.tree.order {
		text := a.tree.search[id]
		matches := true
		for _, word := range words {
			if !strings.Contains(text, word) {
				matches = false
				break
			}
		}
		if matches {
			ids = append(ids, id)
		}
	}
	a.cachedQuery = a.query
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
		a.images, a.mediaCache, a.mediaDims = nil, nil, nil
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
		a.mediaCache, a.mediaDims = nil, nil
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
		a.scope, a.query, a.scale = "", "", float32(a.settings.GraphZoom)/100
		a.page = browse
		a.setNote(a.firstNote())
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
	n, ok := a.tree.notes[id]
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
	a.images = make(map[string]*ui.Bitmap)
	for _, ref := range noteCardImages(n) {
		if bmp := a.mediaBitmap(ref); bmp != nil {
			a.images[ref] = bmp
		}
	}
}

func (a *workspace) imageDims(ref string) (int, int) {
	if ref == "" {
		return 0, 0
	}
	if bmp := a.images[ref]; bmp != nil {
		return bmp.Size()
	}
	if bmp := a.mediaCache[ref]; bmp != nil {
		return bmp.Size()
	}
	if dim, ok := a.mediaDims[ref]; ok {
		return dim.X, dim.Y
	}
	if a.mediaDims == nil {
		a.mediaDims = make(map[string]image.Point)
	}
	if a.store == nil {
		return 0, 0
	}
	path := a.store.MediaPath(ref)
	if path == "" {
		a.mediaDims[ref] = image.Point{}
		return 0, 0
	}
	f, err := os.Open(path)
	if err != nil {
		a.mediaDims[ref] = image.Point{}
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		a.mediaDims[ref] = image.Point{}
		return 0, 0
	}
	a.mediaDims[ref] = image.Point{X: cfg.Width, Y: cfg.Height}
	return cfg.Width, cfg.Height
}

func (a *workspace) mediaBitmap(ref string) *ui.Bitmap {
	if ref == "" {
		return nil
	}
	if bmp := a.images[ref]; bmp != nil {
		return bmp
	}
	if bmp, ok := a.mediaCache[ref]; ok {
		return bmp
	}
	if a.store == nil {
		return nil
	}
	if a.mediaCache == nil {
		a.mediaCache = make(map[string]*ui.Bitmap)
	}
	path := a.store.MediaPath(ref)
	if path == "" {
		a.mediaCache[ref] = nil
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		a.mediaCache[ref] = nil
		return nil
	}
	bmp, err := ui.DecodeBitmap(data)
	if err != nil {
		a.mediaCache[ref] = nil
		return nil
	}
	if len(a.mediaCache) >= 96 {
		clear(a.mediaCache)
	}
	a.mediaCache[ref] = bmp
	if a.mediaDims == nil {
		a.mediaDims = make(map[string]image.Point)
	}
	w, h := bmp.Size()
	a.mediaDims[ref] = image.Point{X: w, Y: h}
	return bmp
}

func (a *workspace) openNote(id string) {
	if a.editing() {
		return
	}
	a.cardMenuOpen = false
	if a.scope != "" && !slices.Contains(a.tree.trail(id), a.scope) {
		a.scope = ""
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
	a.setNoteWithTrail(id, nextTrail)
	a.focusSelected = true
	a.page = detail
}

const importingNotebookMessage = "正在导入笔记本…"

func (a *workspace) showMessage(text string, failed bool) {
	a.message, a.messageError, a.messagePending = text, failed, false
	a.messageUntil = time.Now().Add(5 * time.Second)
}

func (a *workspace) showProgress(text string) {
	a.message, a.messageError, a.messagePending = text, false, true
	a.messageUntil = time.Time{}
}

func (a *workspace) clearPendingMessage() {
	if !a.messagePending {
		return
	}
	a.message, a.messageError, a.messagePending = "", false, false
	a.messageUntil = time.Time{}
}

func (a *workspace) showError(err error) {
	a.showMessage(err.Error(), true)
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
	for _, p := range paths {
		if p != "" {
			a.importPaths = append(a.importPaths, queuedImport{path: p})
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
	switch {
	case r.Added > 0:
		a.showMessage("已导入笔记本", false)
	case r.Updated > 0:
		a.showMessage("已更新本地笔记本", false)
	default:
		a.showMessage("该笔记本已在库中，已为你打开", false)
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

func (a *workspace) chooseImport() {
	win := a.window()
	openFn := a.openImportDialogFn
	if openFn == nil {
		openFn = mygo.Dialog.Open
	}
	importFn := a.importFileFn
	if importFn == nil {
		importFn = a.store.Import
	}
	a.work(func() func() {
		filters := []mygo.FileFilter{{Name: "MarginNote / PDF", Extensions: []string{"marginpkg", "marginnotes", "pdf"}}}
		if runtime.GOOS == "android" {
			// Android's document providers usually give MarginNote files a generic MIME type.
			filters = nil
		}
		paths, err := openFn(mygo.OpenDialogOptions{
			Parent: win, Title: "导入笔记或 PDF",
			Filters: filters,
		})
		var result notebook.ImportResult
		if err == nil && len(paths) > 0 {
			defer cleanupAndroidPickedFiles(paths...)
			a.updateUI(func() { a.showProgress(importingNotebookMessage) })
			result, err = importFn(paths[0])
		}
		return func() {
			if err != nil {
				a.showError(err)
			} else if len(paths) > 0 {
				a.applyImport(result)
			} else {
				a.clearPendingMessage()
			}
		}
	})
}

func takeAndroidDownload(ctx context.Context) (*pendingAndroidDownload, error) {
	var opened *pendingAndroidDownload
	err := mygo.Native(androidopen.PluginID).Call(ctx, "take", nil, &opened)
	return opened, err
}

func (a *workspace) flushImports() {
	if !a.hasUI() || a.busy || a.editing() {
		return
	}
	if item, ok := a.popImport(); ok {
		importFn := a.importFileFn
		if importFn == nil {
			importFn = a.store.Import
		}
		a.showProgress(importingNotebookMessage)
		a.work(func() func() {
			defer func() {
				if item.cleanup {
					_ = os.Remove(item.path)
					_ = os.Remove(filepath.Dir(item.path))
				} else {
					cleanupAndroidPickedFiles(item.path)
				}
			}()
			r, err := importFn(item.path)
			return func() {
				if err != nil {
					a.showError(err)
				} else {
					a.applyImport(r)
				}
			}
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

func (a *workspace) startEditNote() {
	n := a.tree.notes[a.selected]
	a.cardMenuOpen = false
	a.editMode, a.page = editNote, detail
	body := n.Body
	if body == "" && n.Clip == "" {
		body = n.Excerpt
	}
	a.draftTitle, a.draftBody = noteTitle(n), body
}

func (a *workspace) startAddChild() {
	a.cardMenuOpen = false
	a.editMode, a.page = editNewChild, detail
	a.draftTitle, a.draftBody = "", ""
}

func (a *workspace) saveEditor() {
	mode := a.editMode
	id, bookID, title, body := a.selected, a.bookID, a.draftTitle, a.draftBody
	prevTrail := slices.Clone(a.currentTrail())
	a.work(func() func() {
		if mode == editNewChild {
			library, newID, err := a.store.AddNote(bookID, id, title, body)
			return func() {
				if err != nil {
					a.showError(err)
					return
				}
				a.setLibrary(library, bookID)
				a.editMode = editNone
				nextTrail := append(prevTrail, newID)
				a.setNoteWithTrail(newID, nextTrail)
				a.page = detail
				a.showMessage("已新建子笔记", false)
			}
		}
		library, err := a.store.SaveNote(bookID, id, title, body)
		return func() {
			if err != nil {
				a.showError(err)
				return
			}
			a.setLibrary(library, bookID)
			a.editMode = editNone
			a.setNoteWithTrail(id, prevTrail)
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
	a.work(func() func() {
		tmp, err := os.CreateTemp("", "mgnote-export-*.zip")
		if err != nil {
			return func() { a.showError(err) }
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		writeErr := a.store.ExportMarkdownZIP(book.ID, tmp)
		if err := errors.Join(writeErr, tmp.Close()); err != nil {
			return func() { a.showError(err) }
		}
		saved, err := mygo.Dialog.SaveCopy(tmpPath, fileName)
		return func() {
			if err != nil {
				a.showError(err)
			} else if saved != "" {
				a.showMessage("已导出 Markdown 和图片", false)
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
	win := a.window()
	a.work(func() func() {
		res, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent: win, Type: mygo.MessageWarning, Title: "删除笔记",
			Message: "确定要删除「" + title + "」吗？", Detail: "下级笔记将保留并移到上一级。",
			Buttons: []string{"删除", "取消"}, CancelButton: 1, DestructiveButtons: []int{0},
		})
		if err != nil {
			return func() { a.showError(err) }
		}
		if res.Button != 0 {
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
			a.setNote(a.firstNote())
			a.page = browse
			a.showMessage("已删除笔记", false)
		}
	})
}

func sanitizeExportName(title string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	name := strings.TrimSpace(replacer.Replace(title))
	if name == "" {
		return "mgnote"
	}
	return name
}

func (a *workspace) confirmDeleteBook() {
	if a.bookID == "" {
		return
	}
	bookID, title := a.bookID, a.tree.book.Title
	win := a.window()
	a.work(func() func() {
		res, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent:             win,
			Type:               mygo.MessageWarning,
			Title:              "删除笔记本",
			Message:            "确定要删除「" + title + "」吗？",
			Detail:             "该笔记本及本地修改将被删除，未再使用的附图也会一并清理。",
			Buttons:            []string{"删除", "取消"},
			CancelButton:       1,
			DestructiveButtons: []int{0},
		})
		if err != nil {
			return func() { a.showError(err) }
		}
		if res.Button != 0 {
			return func() {}
		}
		a.drawingWrites.wait()
		library, err := a.store.DeleteBook(bookID)
		return func() {
			if err != nil {
				a.showError(err)
				return
			}
			a.drawingWrites.forget(bookID)
			a.setLibrary(library, "")
			a.showMessage("已删除笔记本", false)
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
	msg := strings.Join(strings.Fields(strings.TrimSpace(err.Error())), " ")
	if msg == "" {
		return "检查更新失败，请稍后重试"
	}
	runes := []rune(msg)
	if len(runes) > 100 {
		msg = string(runes[:100]) + "…"
	}
	return msg
}
