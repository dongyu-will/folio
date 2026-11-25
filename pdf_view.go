package main

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo/ui"
	"github.com/google/uuid"
)

type workspaceMode uint8

const (
	canvasMode workspaceMode = iota
	pdfMode
	splitMode
)

type pdfState struct {
	document    string
	page        int
	displayed   pdfDisplay
	cache       map[pageRef]renderedPDFPage
	recent      []pageRef
	thumbs      map[pageRef]renderedPDFPage
	thumbRecent []pageRef
	foreground  *pageRef
	background  *pdfBackground
	nightWork   bool
	prefetchRef pageRef
	prefetchAt  time.Time
	qualityRef  pageRef
	qualitySide int
	qualityAt   time.Time
	counts      map[string]int
	serial      int

	scroll                   ui.ScrollState
	zoom                     float32
	lastZoomVPX, lastZoomVPY float32
	lastZoomAt               time.Time
	margin                   float32
	marginOpen               bool
	highlight                *notebook.PDFRegion
	focusHighlight           bool
	aspect                   float32
	jumpPageText             string
	pagePts                  map[string][2]float32
	focusedHighlightNoteID   string
	highlightSelectedAt      string

	thumbsOpen   bool
	thumbScroll  ui.ScrollState
	searchOpen   bool
	searchQuery  string
	searchHits   []pdfSearchHit
	searchIndex  int
	searchBusy   bool
	searchDoc    string
	searchDone   bool
	searchErr    string
	searchSeq    uint64
	searchScroll ui.ScrollState
	now          time.Time
}

type pdfDisplay struct {
	ref    pageRef
	page   renderedPDFPage
	scroll ui.ScrollState
}

func (a *workspace) resetPDF(documents []notebook.Document) {
	a.pdf = pdfState{
		serial:     a.pdf.serial + 1,
		marginOpen: a.settings.PDFMarginOpen,
		margin:     a.settings.PDFMargin,
	}
	a.maskFades.clear()
	a.pdfHighlightNoteID = ""
	a.pdfTOCOpen = false
	a.mode = canvasMode
	if len(documents) > 0 {
		a.pdf.document = documents[0].ID
		if documents[0].LastPage > 0 {
			a.pdf.page = documents[0].LastPage - 1
			a.pdf.jumpPageText = strconv.Itoa(documents[0].LastPage)
		}
		if len(a.tree.book.Notes) == 0 {
			a.mode = pdfMode
		}
	}
}

// releaseMemoryCaches drops in-memory PDF bitmaps, renderer handles, ink layers,
// and image thumbnails without touching on-disk files or showing a toast.
func (a *workspace) releaseMemoryCaches() {
	closePDFRendererCache()
	a.pdf.cache, a.pdf.recent = nil, nil
	a.pdf.displayed = pdfDisplay{}
	a.pdf.thumbs, a.pdf.thumbRecent = nil, nil
	a.pdf.serial++
	a.pdf.foreground, a.pdf.background, a.pdf.nightWork = nil, nil, false
	a.pdf.prefetchAt = time.Time{}
	a.drawing.pdf, a.drawing.canvas = inkLayers{}, inkLayers{}
	a.clearMediaCache()
}

func (a *workspace) clearRenderCache() {
	a.releaseMemoryCaches()
	if a.store != nil {
		a.store.CleanupStaleTempFiles(0)
	}
	a.refreshStorageBytes()
	a.showMessage("已清理临时与显示缓存", false)
}

type pdfRequest uint8

const (
	pdfVisible pdfRequest = iota
	pdfPrefetch
	pdfThumbnail
	pdfUpgrade
)

type pdfBackground struct {
	ref     pageRef
	request pdfRequest
}

func (a *workspace) requestPDF(ref pageRef, request pdfRequest, dark bool, side int) (renderedPDFPage, bool) {
	p := &a.pdf
	if item, ok := p.cache[ref]; ok {
		if request != pdfPrefetch {
			p.recent = touchPDFPage(p.recent, ref)
		}
		if request == pdfVisible && item.side > 0 && side > max(item.side, item.failedSide) {
			// Sharpening an existing page must leave the foreground renderer free
			// to handle a turn to a different page.
			_ = a.loadPDFPage(ref, pdfUpgrade, dark, side)
		}
		return a.requirePDFNight(ref, item, p.cache, dark)
	}
	if request == pdfThumbnail {
		if item, ok := p.thumbs[ref]; ok {
			p.thumbRecent = touchPDFPage(p.thumbRecent, ref)
			return a.requirePDFNight(ref, item, p.thumbs, dark)
		}
	}
	if err := a.loadPDFPage(ref, request, dark, side); err != nil {
		return renderedPDFPage{err: err}, true
	}
	return renderedPDFPage{}, false
}

func (a *workspace) loadPDFPage(ref pageRef, request pdfRequest, dark bool, side int) error {
	p := &a.pdf
	win := a.window()
	if win == nil || a.store == nil || p.foreground != nil {
		return nil
	}
	if p.background != nil && (request != pdfVisible || p.background.ref == ref && p.background.request == pdfPrefetch) {
		return nil
	}
	path := a.store.DocumentPath(ref.document)
	if path == "" {
		return fmt.Errorf("找不到所选 PDF")
	}
	background := request != pdfVisible
	if background {
		p.background = &pdfBackground{ref: ref, request: request}
	} else {
		p.foreground = &ref
	}
	serial := p.serial
	go func() {
		item, err := renderPDF(path, ref.page, side, background)
		item.err = err
		if err == nil && dark && (request == pdfVisible || request == pdfUpgrade) {
			item.night = ui.NewBitmap(nightPDF(item.raster))
		}
		win.Update(func() {
			if serial != a.pdf.serial {
				return
			}
			p := &a.pdf
			if background {
				p.background = nil
			} else {
				p.foreground = nil
			}
			if item.count > 0 {
				if p.counts == nil {
					p.counts = make(map[string]int)
				}
				p.counts[ref.document] = item.count
			}
			cache, recent, limit := &p.cache, &p.recent, 3
			if request == pdfThumbnail {
				cache, recent, limit = &p.thumbs, &p.thumbRecent, 24
			}
			if *cache == nil {
				*cache = make(map[pageRef]renderedPDFPage)
			}
			if old := (*cache)[ref]; old.light != nil {
				if err != nil {
					old.failedSide = max(old.failedSide, side)
					(*cache)[ref] = old
					return
				}
				if old.side >= item.side {
					return
				}
			}
			(*cache)[ref] = item
			*recent = touchPDFPage(*recent, ref)
			if request != pdfThumbnail && len(*recent) > limit {
				// A late prefetch must not evict the reader's current page.
				current := pageRef{p.document, p.page}
				if _, ok := (*cache)[current]; ok && current != ref {
					*recent = touchPDFPage(*recent, current)
				}
			}
			if len(*recent) > limit {
				delete(*cache, (*recent)[0])
				*recent = (*recent)[1:]
			}
		})
	}()
	return nil
}

func touchPDFPage(recent []pageRef, ref pageRef) []pageRef {
	if i := slices.Index(recent, ref); i >= 0 {
		copy(recent[i:], recent[i+1:])
		recent[len(recent)-1] = ref
		return recent
	}
	return append(recent, ref)
}

func (a *workspace) requirePDFNight(ref pageRef, item renderedPDFPage, cache map[pageRef]renderedPDFPage, dark bool) (renderedPDFPage, bool) {
	if !dark || item.err != nil || item.night != nil || item.raster == nil {
		return item, true
	}
	if win := a.window(); win != nil && !a.pdf.nightWork {
		a.pdf.nightWork = true
		serial := a.pdf.serial
		go func() {
			bitmap := ui.NewBitmap(nightPDF(item.raster))
			win.Update(func() {
				if a.pdf.serial != serial {
					return
				}
				a.pdf.nightWork = false
				if current := cache[ref]; current.light == item.light {
					current.night = bitmap
					cache[ref] = current
				}
			})
		}()
	}
	return item, false
}

func (a *workspace) prefetchPDF(c *ui.Context, ref pageRef, count, side int) {
	if count < 2 || a.drawing.gesture.stroke.Points != nil || a.splitDragging || a.marginDragging {
		return
	}
	p := &a.pdf
	if p.prefetchRef != ref || p.prefetchAt.IsZero() {
		p.prefetchRef = ref
		p.prefetchAt = c.Now().Add(150 * time.Millisecond)
	}
	if remaining := p.prefetchAt.Sub(c.Now()); remaining > 0 {
		c.After(remaining)
		return
	}
	next := ref.page + 1
	if next >= count {
		next = ref.page - 1
	}
	a.requestPDF(pageRef{ref.document, next}, pdfPrefetch, false, side)
}

func (a *workspace) setPDFPage(page int) {
	if a.pdf.displayed.ref == (pageRef{a.pdf.document, a.pdf.page}) {
		a.pdf.displayed.scroll = ui.ScrollState{X: a.pdf.scroll.X, Y: a.pdf.scroll.Y}
	}
	nextPage := max(0, page)
	a.pdf.page = nextPage
	a.androidInk.pdf = penZone{}
	a.pdf.scroll = ui.ScrollState{}
	a.pdf.highlight = nil
	a.pdf.focusHighlight = false
	a.pdf.focusedHighlightNoteID = ""
	a.pdfHighlightNoteID = ""
	a.pdf.jumpPageText = strconv.Itoa(a.pdf.page + 1)
	a.recordDocumentPage(a.bookID, a.pdf.document, nextPage+1)
}

func (a *workspace) recordDocumentPage(bookID, docID string, pageNo int) {
	if bookID == "" || docID == "" || pageNo < 1 {
		return
	}
	for i := range a.tree.book.Documents {
		if a.tree.book.Documents[i].ID == docID {
			if a.tree.book.Documents[i].LastPage == pageNo {
				return
			}
			a.tree.book.Documents[i].LastPage = pageNo
			break
		}
	}
	for bi := range a.books {
		if a.books[bi].ID == bookID {
			for di := range a.books[bi].Documents {
				if a.books[bi].Documents[di].ID == docID {
					a.books[bi].Documents[di].LastPage = pageNo
					break
				}
			}
			break
		}
	}
	if a.store == nil {
		return
	}
	a.pdfPageSaveMu.Lock()
	a.pendingPDFSave.bookID = bookID
	a.pendingPDFSave.docID = docID
	a.pendingPDFSave.page = pageNo
	if a.pdfPageSaveTimer != nil {
		a.pdfPageSaveTimer.Stop()
	}
	if a.window() == nil {
		a.pdfPageSaveMu.Unlock()
		a.flushPendingPDFPageSave()
		return
	}
	a.pdfPageSaveTimer = time.AfterFunc(600*time.Millisecond, a.flushPendingPDFPageSave)
	a.pdfPageSaveMu.Unlock()
}

func (a *workspace) flushPendingPDFPageSave() {
	a.pdfPageSaveMu.Lock()
	if a.pdfPageSaveTimer != nil {
		a.pdfPageSaveTimer.Stop()
		a.pdfPageSaveTimer = nil
	}
	pending := a.pendingPDFSave
	a.pendingPDFSave = struct {
		bookID string
		docID  string
		page   int
	}{}
	a.pdfPageSaveMu.Unlock()
	if a.store == nil || pending.bookID == "" || pending.docID == "" || pending.page < 1 {
		return
	}
	_ = a.store.SaveDocumentPage(pending.bookID, pending.docID, pending.page)
}

func (a *workspace) setPDFDocument(id string) {
	a.flushPendingPDFPageSave()
	if a.pdf.document != id {
		a.pdf.displayed = pdfDisplay{}
		a.clearPDFSearch()
	}
	a.pdf.document = id
	a.pdfTOCOpen = false
	initialPage := 0
	for _, doc := range a.tree.book.Documents {
		if doc.ID == id && doc.LastPage > 0 {
			initialPage = doc.LastPage - 1
			break
		}
	}
	a.setPDFPage(initialPage)
}

func (a *workspace) activePDFDocument() (notebook.Document, bool) {
	for _, doc := range a.tree.book.Documents {
		if doc.ID == a.pdf.document {
			return doc, true
		}
	}
	return notebook.Document{}, false
}

func (a *workspace) jumpToPDF(region notebook.PDFRegion) {
	if !slices.ContainsFunc(a.tree.book.Documents, func(d notebook.Document) bool { return d.ID == region.Document }) {
		a.showMessage("找不到摘录对应的 PDF", true)
		return
	}
	a.setPDFDocument(region.Document)
	a.setPDFPage(region.Page)
	a.pdf.highlight, a.pdf.focusHighlight = &region, true
	if a.mode == canvasMode {
		a.enterSplitMode()
	}
	a.page = browse
}

func (a *workspace) setMode(mode workspaceMode) {
	if mode != canvasMode && a.pdf.document == "" {
		return
	}
	prev := a.mode
	if mode == splitMode {
		a.enterSplitMode()
	} else {
		a.mode = mode
		a.sidebarHiddenForSplit = false
	}
	if prev != mode {
		a.drawing.gesture = inkGesture{}
		a.canvasPointer = canvasPointer{}
		a.recenter = prev == pdfMode || mode == splitMode
	}
}

func (a *workspace) documentWorkspace(c *ui.Context, p palette, width, height float32) {
	barH := float32(48)
	if a.touchUI() {
		barH = 52
	}
	ui.Column(c).Fill().Children(func() {
		a.workspaceToolbar(c, p, width, barH)
		horizontalRule(c, p)
		bodyHeight := max(1, height-barH-1)
		showNotesPane := func(w, h float32) {
			if a.settings.LayoutMode() == notebook.LayoutOutline {
				a.outlineView(c, p, w, h)
				return
			}
			a.graph(c, p, w, h)
		}
		if a.mode == canvasMode {
			showNotesPane(width, bodyHeight)
			return
		}
		gripSize := float32(7)
		if a.touchUI() {
			gripSize = 16
		}
		verticalSplit := width < 480 || (height > width*1.12 && width < 900)
		if a.mode == splitMode && verticalSplit {
			if a.splitRatio == 0 {
				if a.settings.SplitRatioV >= .2 && a.settings.SplitRatioV <= .8 {
					a.splitRatio = a.settings.SplitRatioV
				} else {
					a.splitRatio = .48
				}
			}
			minPane := min(float32(180), max(90, (bodyHeight-gripSize)*.3))
			pdfHeight := clamp((bodyHeight-gripSize)*a.splitRatio, minPane, max(minPane, bodyHeight-gripSize-minPane))
			ui.Box(c).FillWidth().Height(pdfHeight).Shrink(0).Children(func() { a.pdfPane(c, p, width, pdfHeight) })
			a.splitGrip(c, p, true, bodyHeight, pdfHeight, gripSize)
			ui.Box(c).Grow(1).FillWidth().MinHeight(0).Children(func() { showNotesPane(width, max(1, bodyHeight-pdfHeight-gripSize)) })
			return
		}
		pdfWidth := width
		if a.mode == splitMode {
			if a.splitRatio == 0 {
				if a.settings.SplitRatioH >= .2 && a.settings.SplitRatioH <= .8 {
					a.splitRatio = a.settings.SplitRatioH
				} else {
					a.splitRatio = .425
				}
			}
			minPane := min(float32(195), max(120, (width-gripSize)*.3))
			pdfWidth = clamp((width-gripSize)*a.splitRatio, minPane, max(minPane, width-gripSize-minPane))
		}
		ui.Row(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
			ui.Box(c).Width(pdfWidth).FillHeight().Shrink(0).Children(func() { a.pdfPane(c, p, pdfWidth, bodyHeight) })
			if a.mode == splitMode {
				a.splitGrip(c, p, false, width, pdfWidth, gripSize)
				ui.Box(c).Grow(1).MinWidth(0).FillHeight().Children(func() {
					showNotesPane(max(1, width-pdfWidth-gripSize), bodyHeight)
				})
			}
		})
	})
}

func (a *workspace) workspaceToolbar(c *ui.Context, p palette, width, barH float32) {
	compact := width < 660
	touch := a.touchUI()
	btnSize := float32(30)
	if touch {
		btnSize = 36
	}
	ui.Row(c).Height(barH).FillWidth().Shrink(0).PaddingX(12).Gap(8).
		AlignItems(ui.Center).Background(p.surface).Children(func() {
		if a.lastViewWide && (a.settings.SidebarCollapsed || a.sidebarHiddenForSplit || !a.lastViewSidebar) {
			designToolbarButton(c, p, icExpand, "展开侧边栏", btnSize, true, a.editing(), a.toggleSidebar)
			ui.Box(c).Width(1).Height(16).Background(p.rule)
		}
		title := a.tree.book.Title
		if a.mode == canvasMode && a.scope != "" {
			title = noteTitle(a.tree.notes[a.scope])
		}
		ui.Text(c, title).FontSize(14).FontWeight(650).TextColor(p.ink).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1).
			ContentTransition(ctText)
		if !compact {
			badgePill(c, p, fmt.Sprintf("%d 条笔记", len(a.tree.order)))
		}
		if a.scope != "" && a.mode != pdfMode {
			pillButton(c, p, "返回全部", icArrowLeft, false, a.editing(), func() { a.selectScope("") }).Height(btnSize)
		}
		ui.Box(c).Grow(1).MinWidth(0)
		if a.pdf.document != "" {
			segmentsSized(c, p, touch, []segment[workspaceMode]{{"PDF", pdfMode}, {"分屏", splitMode}, {"画布", canvasMode}}, a.mode, a.setMode)
			ui.Box(c).Grow(1).MinWidth(0)
		}
		if a.mode != pdfMode {
			a.layoutSwitcher(c, p, btnSize)
			if len(a.tree.order) > 0 && !compact {
				if a.mode == canvasMode {
					pillButton(c, p, "背诵", icBook, a.reciteMode, a.editing() || a.reviewOpen, a.toggleReciteMode).
						Height(btnSize).Tooltip("开启或退出背诵遮罩模式")
				}
				pillButton(c, p, "抽卡", icLayers, a.reviewOpen, a.editing() || a.reviewOpen, a.startReview).
					Height(btnSize).Tooltip("抽卡背诵当前范围笔记")
			}
		}
		if a.mode != canvasMode {
			marginAction := func() {
				a.pdf.marginOpen = !a.pdf.marginOpen
				next := a.settings
				next.PDFMarginOpen = a.pdf.marginOpen
				if a.pdf.margin > 0 {
					next.PDFMargin = a.pdf.margin
				} else if next.PDFMargin <= 0 {
					next.PDFMargin = .32
				}
				a.saveSettings(next)
			}
			if compact || (touch && width < 860) {
				designToolbarButton(c, p, icMargin, "批注留白", btnSize, a.pdf.marginOpen, false, marginAction)
			} else {
				pillButton(c, p, "批注留白", icMargin, a.pdf.marginOpen, false, marginAction).Height(btnSize)
			}
		}
		if (!compact || a.mode == canvasMode) && a.mode != pdfMode {
			if layout := a.settings.LayoutMode(); layout != notebook.LayoutOutline {
				showFoldAll := !compact && (!touch || a.mode == canvasMode || width >= 960)
				if showFoldAll && (layout == notebook.LayoutCards || layout == notebook.LayoutBoard) && a.hasExpandableBranches() {
					collapsed := a.hasCollapsedBranches()
					label := "全部收起"
					if collapsed {
						label = "全部展开"
					}
					pillButton(c, p, label, icLayers, false, a.editing(), func() {
						a.setAllExpanded(collapsed)
					}).Height(btnSize)
				}
				a.zoomControl(c, p, max(.10, a.scale), func(factor float32) {
					switch factor {
					case 0:
						a.zoom(0)
					case -1:
						a.zoomAt(1, a.canvasWidth/2, a.canvasHeight/2)
					default:
						a.zoomAt(a.scale*factor, a.canvasWidth/2, a.canvasHeight/2)
					}
				})
			}
		}
		if a.lastViewWide && (a.settings.SidebarCollapsed || a.sidebarHiddenForSplit || a.mode != canvasMode) {
			ui.Box(c).Shrink(0).Children(func() {
				designToolbarButton(c, p, icSettings, "设置", btnSize, true, a.editing(), a.openPreferences)
				if a.hasUpdateBadge() {
					ui.Box(c.Key("toolbar-update-dot")).Absolute().Top(4).Right(4).Size(6, 6).
						Radius(99).Background(p.accent).IgnoreInput(true)
				}
			})
		}
	})
}

func (a *workspace) layoutLabel(mode string) string {
	switch mode {
	case notebook.LayoutBoard:
		return "专题框架"
	case notebook.LayoutOutline:
		return "大纲流"
	case notebook.LayoutCompact:
		return "精简树"
	default:
		return "卡片树"
	}
}

func (a *workspace) layoutSwitcher(c *ui.Context, p palette, btnSize float32) {
	mode := a.settings.LayoutMode()
	label := a.layoutLabel(mode)
	b := ui.ButtonBase(c.Key("layout-switcher")).Height(btnSize).Padding(0, 10).Gap(5).
		Center().Radius(8).Shrink(0).Background(p.elevated).Border(1, p.border).
		Shadow(0, 1, 2, 0, p.shadowSoft).Transition(trColorMicro).
		Label("视图样式：" + label).Tooltip("切换视图样式").Disabled(a.editing()).FocusRingColor(p.accent)
	fg := p.ink
	switch {
	case a.editing():
		fg = p.quiet.Alpha(.55)
	case b.Pressed():
		b.Background(p.pressed)
	case b.Hovered():
		b.Background(p.hover)
	}
	applyPressPose(b, b.Pressed(), a.editing())
	b.Children(func() {
		ui.Icon(c, icLayers).Size(13, 13).TextColor(p.accent).Shrink(0)
		ui.Text(c, label).FontSize(12).FontWeight(550).TextColor(fg).SingleLine().ContentTransition(ctText)
		ui.Icon(c, icChevronDown).Size(11, 11).TextColor(p.quiet).Shrink(0)
	})
	options := []segment[string]{
		{"卡片树", notebook.LayoutCards},
		{"专题框架", notebook.LayoutBoard},
		{"大纲流", notebook.LayoutOutline},
		{"精简树", notebook.LayoutCompact},
	}
	b.PopoverMenuButton(func(m *ui.Menu) {
		for _, opt := range options {
			if m.Item(opt.title).ID("layout-" + opt.value).Radio(opt.value == mode).Chosen() {
				next := a.settings
				next.GraphLayout = opt.value
				if a.saveSettings(next) {
					a.canvasReady = false
					a.focusSelected = true
					a.cardMenuOpen = false
					a.outlineScroll = ui.ScrollState{}
				}
			}
		}
	})
}

func applyHoverPressBg(b ui.Element, p palette, active bool, baseFg, activeFg ui.Color) ui.Color {
	fg := baseFg
	switch {
	case active:
		fg = activeFg
	case b.Pressed():
		b.Background(p.pressed)
		fg = p.ink
	case b.Hovered():
		b.Background(p.hover)
		fg = p.ink
	}
	applyPressPose(b, b.Pressed(), false)
	return fg
}

func hoverPressSurface(b ui.Element, p palette, active bool, baseBg, baseBorder, activeBg, activeBorder ui.Color) (bg, border ui.Color) {
	bg, border = baseBg, baseBorder
	switch {
	case active:
		bg, border = activeBg, activeBorder
	case b.Pressed():
		bg = p.pressed
	case b.Hovered():
		bg = p.hover
	}
	applyPressPose(b, b.Pressed(), false)
	return bg, border
}

func (a *workspace) zoomControl(c *ui.Context, p palette, scale float32, action func(float32)) {
	a.zoomControlWithLabels(c, p, scale, "", "", action)
}

func (a *workspace) zoomControlWithLabels(c *ui.Context, p palette, scale float32, keyPrefix, suffix string, action func(float32)) {
	touch := a.touchUI()
	rowH, btnSz, pctW, icSz, fontSz := float32(32), float32(26), float32(46), float32(13.5), float32(11.5)
	if touch {
		rowH, btnSz, pctW, icSz, fontSz = 36, 30, 52, 15, 12
	}
	ui.Row(c).Height(rowH).Padding(2).Gap(1).Radius(9).AlignItems(ui.Center).Shrink(0).
		Background(p.elevated).Border(1, p.border).
		InsetShadow(0, 1, 0, 0, p.specular).
		Shadow(0, 1, 2, 0, p.shadowSoft).Children(func() {
		zoomBtn := func(key string, icon *ui.SVG, label string, onClick func()) {
			b := ui.ButtonBase(c.Key(keyPrefix+key)).Size(btnSz, btnSz).Radius(6).Center().
				Label(label).Tooltip(label).FocusRingColor(p.accent).Transition(trColorMicro).OnClick(onClick)
			fg := applyHoverPressBg(b, p, false, p.secondary, p.ink)
			b.Children(func() {
				ui.Icon(c, icon).Size(icSz, icSz).TextColor(fg)
			})
		}

		zoomBtn("zoom-out", icMinus, "缩小"+suffix, func() { action(1 / 1.18) })
		resetLabel := "重置为 100%" + suffix
		pct := ui.ButtonBase(c.Key(keyPrefix + "zoom-reset")).Height(btnSz).MinWidth(pctW).PaddingX(5).Radius(6).Center().
			Label(resetLabel).Tooltip(resetLabel).FocusRingColor(p.accent).Transition(trColorMicro).OnClick(func() { action(-1) })
		prevScale := ui.Local(pct, "prevScale", func() float32 { return scale })
		down := scale < *prevScale
		*prevScale = scale
		pctColor := applyHoverPressBg(pct, p, false, p.secondary, p.ink)
		pct.Children(func() {
			ui.Text(c, fmt.Sprintf("%d%%", int(scale*100+.5))).FontSize(fontSz).FontWeight(600).
				FontFeatures("tnum").TextColor(pctColor).SingleLine().ContentTransition(ctNumeric(down))
		})
		zoomBtn("zoom-in", icPlus, "放大"+suffix, func() { action(1.18) })
		ui.Box(c).Width(1).Height(14).Background(p.rule).Margin(0, 2)
		zoomBtn("zoom-fit", icFit, "适应视图"+suffix, func() { action(0) })
	})
}

func (a *workspace) splitGrip(c *ui.Context, p palette, vertical bool, total, pos, gripSize float32) {
	// The retained gesture lives on the workspace because layout rebuilds on each move.
	avail := max(1, total-gripSize)
	maxMin, floorMin := float32(195), float32(120)
	key, pillKey, label, axis := "split-grip", "split-grip-pill", "拖动调整分屏宽度", ui.Horizontal
	handleW, handleH := float32(3), float32(38)
	if a.touchUI() {
		handleW, handleH = 4, 44
	}
	if vertical {
		maxMin, floorMin = 180, 90
		key, pillKey, label, axis = "split-grip-v", "split-grip-v-pill", "拖动调整分屏高度", ui.Vertical
		handleW, handleH = 44, 4
	}
	minPane := min(maxMin, max(floorMin, avail*.3))
	minR, maxR := minPane/avail, max(minPane, avail-minPane)/avail

	grip := ui.Box(c.Key(key)).Shrink(0).Center().
		Background(p.shell).Label(label).Role(ui.RoleSeparator).
		Draggable(nil, ui.DraggableOptions{Axis: axis, Bounds: &ui.Rect{}, Rigid: true})
	if vertical {
		grip.FillWidth().Height(gripSize)
	} else {
		grip.Width(gripSize).FillHeight()
	}
	grip.HandleInput(func(ev ui.InputEvent) bool {
		delta := ev.X
		if vertical {
			delta = ev.Y
		}
		switch ev.Kind {
		case ui.InputPointerDown:
			a.splitDragging = ev.Button == 0
			a.splitStart, a.splitPointerX = a.splitRatio, pos+delta
			return a.splitDragging
		case ui.InputPointerMove:
			if a.splitDragging {
				a.splitRatio = clamp(a.splitStart+(pos+delta-a.splitPointerX)/avail, minR, maxR)
				return true
			}
		case ui.InputPointerUp:
			if a.splitDragging {
				a.splitDragging = false
				if ev.Canceled {
					a.splitRatio = a.splitStart
				} else {
					next := a.settings
					if vertical {
						next.SplitRatioV = a.splitRatio
					} else {
						next.SplitRatioH = a.splitRatio
					}
					a.saveSettings(next)
				}
				return true
			}
		}
		return false
	})
	grip.Children(func() {
		col := p.border
		pillScale := float32(1)
		if a.splitDragging {
			col = p.accent
			pillScale = 1.35
		} else if grip.Hovered() {
			col = p.secondary
			pillScale = 1.15
		}
		ui.Box(c.Key(pillKey)).Width(handleW).Height(handleH).Radius(min(handleW, handleH)/2).
			Background(col).Transition(trColorMicro).
			AnimatePose(ui.Pose{Scale: pillScale}, animControl)
	})
}

func (a *workspace) zoomPDF(factor float32) {
	a.pdf.lastZoomAt = time.Time{}
	a.pdf.qualityAt = time.Time{}
	if factor <= 0 {
		a.pdf.zoom = 1
		a.pdf.scroll = ui.ScrollState{}
		return
	}
	a.pdf.zoom = clamp(max(1, a.pdf.zoom)*factor, 1, 4)
}

// zoomPDFAt scales the PDF page while locking the pinched point (localX, localY on the sheet)
// under the user's fingers and applying simultaneous two-finger midpoint panning.
func (a *workspace) zoomPDFAt(factor, localX, localY, viewW, pad, baseFit, ratio float32, evTime time.Time) {
	d := &a.pdf
	if factor <= 0 || baseFit <= 0 {
		a.zoomPDF(factor)
		return
	}
	oldZoom := max(1, d.zoom)
	oldPaperW := baseFit * oldZoom
	oldPadX := max(pad, (viewW-oldPaperW*(1+ratio))/2)
	vpX := oldPadX + localX - d.scroll.X
	vpY := pad + localY - d.scroll.Y

	var panDX, panDY float32
	if !d.lastZoomAt.IsZero() && !evTime.IsZero() {
		dt := evTime.Sub(d.lastZoomAt)
		if dt >= 0 && dt < 150*time.Millisecond {
			dx, dy := vpX-d.lastZoomVPX, vpY-d.lastZoomVPY
			if abs(dx) < 160 && abs(dy) < 160 {
				panDX, panDY = dx, dy
			}
		}
	}

	u := localX / oldPaperW
	v := localY / oldPaperW
	newZoom := clamp(oldZoom*factor, 1, 4)
	if newZoom != oldZoom {
		d.qualityAt = time.Time{}
	}
	newPaperW := baseFit * newZoom
	newPadX := max(pad, (viewW-newPaperW*(1+ratio))/2)

	d.zoom = newZoom
	d.scroll.X = max(0, newPadX+u*newPaperW-vpX-panDX)
	d.scroll.Y = max(0, pad+v*newPaperW-vpY-panDY)
	d.lastZoomVPX, d.lastZoomVPY, d.lastZoomAt = vpX, vpY, evTime
}

const pdfPaperPadding = float32(16)

func (d *pdfState) marginRatio() float32 {
	if !d.marginOpen {
		return 0
	}
	if d.margin == 0 {
		d.margin = .32
	}
	return d.margin
}

func pdfFitWidth(width, height, aspect, margin float32) float32 {
	fitWidth := max(72, width-2*pdfPaperPadding) / (1 + margin)
	fitHeight := max(72, height-2*pdfPaperPadding-24) / aspect
	return min(fitWidth, fitHeight)
}

func pdfRasterSide(width, height, aspect, margin, zoom, density float32) int {
	pixels := pdfFitWidth(width, height, aspect, margin) * max(1, aspect) * max(1, zoom) * max(1, density)
	const step = 128
	return clampInt(int(math.Ceil(float64(pixels)/step))*step, 512, 2400)
}

func (a *workspace) pdfResolution(c *ui.Context, ref pageRef, width, height float32) (fitSide, renderSide int) {
	d := &a.pdf
	page := d.cache[ref]
	aspect := page.aspectRatio()
	if page.light == nil && d.displayed.ref.document == ref.document {
		aspect = d.displayed.page.aspectRatio()
	}
	margin := d.marginRatio()
	fitSide = pdfRasterSide(width, height, aspect, margin, 1, a.pdfDensity)
	if page.light == nil || page.side == 0 {
		return fitSide, fitSide
	}
	side := pdfRasterSide(width, height, aspect, margin, d.zoom, a.pdfDensity)
	if side <= max(page.side, page.failedSide) {
		d.qualityAt = time.Time{}
		return fitSide, page.side
	}
	now := c.Now()
	interacting := a.splitDragging || a.marginDragging || len(a.drawing.gesture.stroke.Points) > 0
	if d.qualityRef != ref || d.qualitySide != side || d.qualityAt.IsZero() || interacting {
		d.qualityRef, d.qualitySide = ref, side
		d.qualityAt = now.Add(180 * time.Millisecond)
	}
	if interacting {
		return fitSide, page.side
	}
	if remaining := d.qualityAt.Sub(now); remaining > 0 {
		c.After(remaining)
		return fitSide, page.side
	}
	return fitSide, side
}

func (a *workspace) clearPDFSearch() {
	a.pdf.searchSeq++
	a.pdf.searchHits = nil
	a.pdf.searchIndex = 0
	a.pdf.searchBusy = false
	a.pdf.searchDoc = ""
	a.pdf.searchDone = false
	a.pdf.searchErr = ""
	a.pdf.searchScroll = ui.ScrollState{}
}

func (a *workspace) togglePDFSearch() {
	a.pdf.searchOpen = !a.pdf.searchOpen
	if !a.pdf.searchOpen {
		a.pdf.searchBusy = false
		a.pdf.searchSeq++
	}
}

func (a *workspace) startPDFSearch() {
	query := strings.TrimSpace(a.pdf.searchQuery)
	if query == "" {
		a.clearPDFSearch()
		return
	}
	if a.store == nil || a.pdf.document == "" {
		return
	}
	docID := a.pdf.document
	docPath := a.store.DocumentPath(docID)
	if docPath == "" {
		return
	}
	a.pdf.searchSeq++
	seq := a.pdf.searchSeq
	a.pdf.searchBusy = true
	a.pdf.searchDone = false
	a.pdf.searchErr = ""
	a.pdf.searchDoc = docID
	a.pdf.searchHits = nil
	a.pdf.searchIndex = 0
	a.pdf.searchScroll = ui.ScrollState{}

	win := a.window()
	if win == nil {
		hits, err := searchPDFText(docPath, query, 200)
		a.applyPDFSearchResult(seq, docID, hits, err)
		return
	}
	go func() {
		hits, err := searchPDFText(docPath, query, 200)
		win.Update(func() {
			a.applyPDFSearchResult(seq, docID, hits, err)
		})
	}()
}

func (a *workspace) applyPDFSearchResult(seq uint64, docID string, hits []pdfSearchHit, err error) {
	if seq != a.pdf.searchSeq || a.pdf.document != docID {
		return
	}
	a.pdf.searchBusy = false
	a.pdf.searchDone = true
	if err != nil {
		a.pdf.searchErr = err.Error()
		a.pdf.searchHits = nil
		return
	}
	a.pdf.searchErr = ""
	a.pdf.searchHits = hits
	a.pdf.searchIndex = 0
	if len(hits) > 0 {
		a.jumpPDFSearchHit(0)
	}
}

func (a *workspace) jumpPDFSearchHit(idx int) {
	if len(a.pdf.searchHits) == 0 {
		return
	}
	n := len(a.pdf.searchHits)
	idx = ((idx % n) + n) % n
	a.pdf.searchIndex = idx
	hit := a.pdf.searchHits[idx]
	if hit.Page >= 0 {
		a.setPDFPage(hit.Page)
	}
}

func (a *workspace) pdfPane(c *ui.Context, p palette, width, height float32) {
	navH := float32(38)
	if a.touchUI() {
		navH = 46
	}
	searchH := float32(0)
	if a.pdf.searchOpen {
		searchH = 42
		if len(a.pdf.searchHits) > 0 || a.pdf.searchDone || a.pdf.searchErr != "" {
			if len(a.pdf.searchHits) > 0 {
				searchH += min(116, float32(len(a.pdf.searchHits))*32+8)
			} else {
				searchH += 30
			}
		}
	}
	thumbRailW := float32(0)
	refDoc := pageRef{a.pdf.document, a.pdf.page}
	knownCount := a.pdf.counts[refDoc.document]
	if a.pdf.thumbsOpen && width >= 360 && (knownCount == 0 || knownCount > 0) {
		thumbRailW = 108
	}
	sheetWidth := max(1, width-thumbRailW)
	sheetHeight := max(1, height-navH-searchH)
	ref := refDoc
	fitSide, renderSide := a.pdfResolution(c, ref, sheetWidth, sheetHeight)
	page, ready := a.requestPDF(ref, pdfVisible, p.dark && a.settings.PDFNight, renderSide)
	count := max(page.count, a.pdf.counts[ref.document])
	if ready && page.err == nil {
		a.pdf.displayed.ref, a.pdf.displayed.page = ref, page
		a.prefetchPDF(c, ref, count, fitSide)
	} else {
		a.androidInk.pdf = penZone{}
		a.publishPenZones()
	}
	ui.Column(c).Fill().Background(p.shell).Children(func() {
		a.pdfNavigation(c, p, count, navH, width)
		if a.pdf.searchOpen {
			a.pdfSearchPanel(c, p, width)
		}
		ui.Row(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
			if thumbRailW > 0 && count > 0 {
				a.pdfThumbRail(c, p, thumbRailW, sheetHeight, count)
			}
			ui.Column(c).Grow(1).MinWidth(0).FillHeight().Children(func() {
				if ready && page.err == nil {
					a.pdfSheet(c, p, sheetWidth, sheetHeight, ref, page, false)
					return
				}
				shown := &a.pdf.displayed
				if !ready && shown.ref.document == ref.document && shown.page.light != nil {
					a.pdfSheet(c, p, sheetWidth, sheetHeight, shown.ref, shown.page, true)
					return
				}
				ui.Box(c).Grow(1).FillWidth().Center().Children(func() {
					if page.err != nil {
						msg := friendlyError(page.err)
						if msg == "" {
							msg = "这一页暂时无法显示"
						}
						ui.Column(c).Center().Gap(10).Padding(16).Children(func() {
							ui.Text(c, msg).TextColor(p.danger).FontSize(13).LineHeight(1.5).Center()
							pillButton(c, p, "重新加载", icUpdateRefresh, true, false, func() {
								delete(a.pdf.cache, ref)
								if a.pdf.foreground != nil && *a.pdf.foreground == ref {
									a.pdf.foreground = nil
								}
								_ = a.loadPDFPage(ref, pdfVisible, p.dark && a.settings.PDFNight, renderSide)
							})
						})
					} else {
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Spinner(c).Size(18, 18).TextColor(p.quiet)
							ui.Text(c, "正在渲染 PDF…").TextColor(p.quiet).FontSize(13)
						})
					}
				})
			})
		})
	})
}

func (a *workspace) pdfNavigation(c *ui.Context, p palette, count int, navH, width float32) {
	activeDoc, hasDoc := a.activePDFDocument()
	hasTOC := hasDoc && len(activeDoc.TOC) > 0
	touch := a.touchUI()
	btnH, navBtnSz, inputW, inputH, tocRowH := float32(26), float32(24), float32(44), float32(23), float32(30)
	if touch {
		btnH, navBtnSz, inputW, inputH, tocRowH = 34, 36, 54, 32, 38
	}
	ui.Row(c).Height(navH).PaddingX(8).Gap(6).AlignItems(ui.Center).Shrink(0).FillWidth().
		Background(p.surface).BorderColor(p.rule).BorderWidth(0, 0, 1, 0).Children(func() {
		if hasTOC {
			tocBtn := ui.ButtonBase(c.Key("pdf-toc-btn")).Height(btnH).PaddingX(10).Radius(7).Center().
				Gap(4).Shrink(0).Label("目录").Selected(a.pdfTOCOpen).FocusRingColor(p.accent).
				Transition(trColorMicro).OnClick(func() { a.pdfTOCOpen = !a.pdfTOCOpen })
			fg, bg, bdr := p.secondary, p.elevated, p.rule
			switch {
			case a.pdfTOCOpen:
				fg, bg, bdr = p.accent, p.tint, p.tintBorder
			case tocBtn.Pressed():
				fg, bg, bdr = p.ink, p.pressed, p.border
			case tocBtn.Hovered():
				fg, bg, bdr = p.ink, p.hover, p.border
			}
			tocBtn.Background(bg).Border(1, bdr)
			applyPressPose(tocBtn, tocBtn.Pressed(), false)
			tocBtn.Children(func() {
				ui.Icon(c, icLayers).Size(12.5, 12.5).TextColor(fg)
				ui.Text(c, "目录").FontSize(12).FontWeight(550).TextColor(fg).SingleLine()
			})
			tocW := min(300, max(180, width-24))
			ui.PopoverBase(c.Key("pdf-toc-popover"), tocBtn, &a.pdfTOCOpen, func(panel ui.Element) {
				panel.Width(tocW).MaxHeight(400).Radius(10).Clip().
					Background(p.elevated).Border(1, p.border).
					InsetShadow(0, 1, 0, 0, p.specular).
					Shadow(0, 12, 32, -6, p.shadow).
					Transition(trPopover)
				ui.Column(c).FillWidth().MaxHeight(400).Children(func() {
					ui.Row(c).FillWidth().Height(36).PaddingX(12).AlignItems(ui.Center).
						Background(p.shell).BorderColor(p.rule).BorderWidth(0, 0, 1, 0).Shrink(0).Children(func() {
						ui.Text(c, "文档目录").FontSize(12).FontWeight(600).TextColor(p.secondary).Grow(1).SingleLine()
						ui.Text(c, fmt.Sprintf("%d 项", len(activeDoc.TOC))).FontSize(11).
							FontFeatures("tnum").TextColor(p.quiet).SingleLine()
					})
					ui.Scroll(c.Key("pdf-toc-scroll")).FillWidth().Shrink(1).Padding(6).Gap(2).Children(func() {
						curPage := a.pdf.page + 1
						for idx, entry := range activeDoc.TOC {
							entry := entry
							indent := float32(min(max(0, entry.Level), 4)) * 14
							active := entry.Page > 0 && entry.Page == curPage
							row := ui.ButtonBase(c.Key(fmt.Sprintf("toc-%d", idx))).
								FillWidth().MinHeight(tocRowH).Padding(4, 8, 4, 8+indent).Gap(8).
								Justify(ui.SpaceBetween).AlignItems(ui.Center).Radius(6).
								Label(entry.Title).FocusRingColor(p.accent).Transition(trColorMicro).
								OnClick(func() {
									if entry.Page > 0 {
										a.setPDFPage(entry.Page - 1)
									}
									a.pdfTOCOpen = false
								})
							fg, weight := p.secondary, 500
							if entry.Level == 0 {
								fg, weight = p.ink, 600
							}
							if active {
								row.Background(p.tint)
								weight = 600
							}
							fg = applyHoverPressBg(row, p, active, fg, p.accent)
							row.Children(func() {
								ui.Text(c, entry.Title).FontSize(12.5).FontWeight(weight).
									TextColor(fg).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
								if entry.Page > 0 {
									ui.Text(c, fmt.Sprintf("P.%d", entry.Page)).FontSize(11).
										FontWeight(550).FontFeatures("tnum").TextColor(p.quiet).Shrink(0)
								}
							})
						}
					})
				})
			})
		}
		if docs := a.tree.book.Documents; len(docs) > 1 {
			b := ui.ButtonBase(c).Height(btnH).PaddingX(8).Center().Label("切换 PDF").MinWidth(0).Shrink(1)
			title := "PDF"
			for _, doc := range docs {
				if doc.ID == a.pdf.document {
					title = doc.Title
				}
			}
			b.Children(func() {
				ui.Text(c, title+" ▾").SingleLine().Ellipsis("…").MaxWidth(160).FontSize(12).TextColor(p.secondary)
			})
			b.PopoverMenuButton(func(m *ui.Menu) {
				for _, doc := range docs {
					if m.Item(doc.Title).ID(doc.ID).Radio(doc.ID == a.pdf.document).Chosen() {
						a.setPDFDocument(doc.ID)
					}
				}
			})
		}
		if len(a.tree.book.Documents) <= 1 {
			ui.Box(c).Grow(1).MinWidth(0)
		}
		designToolbarButton(c, p, icChevronLeft, "上一页", navBtnSz, false, a.pdf.page == 0, func() { a.setPDFPage(a.pdf.page - 1) })
		pageNum := strconv.Itoa(a.pdf.page + 1)
		if a.pdf.jumpPageText == "" {
			a.pdf.jumpPageText = pageNum
		}
		inputFont := float32(11)
		if touch {
			inputFont = 13
		}
		field := ui.TextInput(c.Key("pdf-page-number"), &a.pdf.jumpPageText).Size(inputW, inputH).
			PaddingX(6).Radius(6).FontSize(inputFont).Label("页码")
		if field.Submitted() {
			if n, err := strconv.Atoi(strings.TrimSpace(a.pdf.jumpPageText)); err == nil && count > 0 {
				clamped := max(1, min(count, n))
				a.setPDFPage(clamped - 1)
			} else {
				a.pdf.jumpPageText = pageNum
				a.pdfPageShakeRun++
			}
		}
		applyErrorShake(field, a.pdfPageShakeRun)
		ui.Text(c, fmt.Sprintf("/ %d", count)).SingleLine().FontSize(inputFont).TextColor(p.quiet).Shrink(0).
			ContentTransition(ctNumeric(false))
		designToolbarButton(c, p, icChevronRight, "下一页", navBtnSz, false, count == 0 || a.pdf.page+1 >= count, func() { a.setPDFPage(a.pdf.page + 1) })
		ui.Box(c).Grow(1).MinWidth(0)
		if width >= 300 {
			designToolbarButton(c, p, icSidebar, "缩略图", navBtnSz, a.pdf.thumbsOpen, count == 0, func() {
				a.pdf.thumbsOpen = !a.pdf.thumbsOpen
			})
		}
		if width >= 320 {
			designToolbarButton(c, p, icPDFSearch, "搜全文", navBtnSz, a.pdf.searchOpen, a.pdf.document == "", a.togglePDFSearch)
		}
		if width >= 340 {
			pillButton(c, p, "背诵", icBook, a.reciteMode, a.editing(), a.toggleReciteMode).
				Height(btnH).Tooltip("遮盖并点按揭晓 PDF 挖空区")
		}
		if p.dark && width >= 360 {
			nightLabel := "原色"
			if a.settings.PDFNight {
				nightLabel = "反色"
			}
			pillButton(c, p, nightLabel, nil, a.settings.PDFNight, false, func() {
				next := a.settings
				next.PDFNight = !next.PDFNight
				a.saveSettings(next)
			}).Height(btnH).Tooltip("切换深色模式下 PDF 反色显示")
		}
		if width >= 380 {
			a.zoomControlWithLabels(c, p, max(1, a.pdf.zoom), "pdf-", " PDF", a.zoomPDF)
		}
	})
}

func (a *workspace) pdfSearchPanel(c *ui.Context, p palette, width float32) {
	ui.Column(c.Key("pdf-search-panel")).FillWidth().Shrink(0).Background(p.surface).
		BorderColor(p.rule).BorderWidth(0, 0, 1, 0).Transition(trPanelSlideV).Children(func() {
		ui.Row(c).Height(42).PaddingX(10).Gap(6).AlignItems(ui.Center).FillWidth().Children(func() {
			field := ui.TextInput(c.Key("pdf-search-input"), &a.pdf.searchQuery).
				Grow(1).MinWidth(80).Height(28).PaddingX(8).Radius(7).FontSize(12.5).
				Background(p.inputBg).Border(1, p.border).TextColor(p.ink).
				Placeholder("搜索 PDF 全文关键词…").Label("搜索 PDF 全文")
			if field.Submitted() {
				a.startPDFSearch()
			}
			searchLabel := "搜索"
			if a.pdf.searchBusy {
				searchLabel = "搜索中…"
				ui.Spinner(c).Size(13, 13).TextColor(p.accent)
			}
			pillButton(c, p, searchLabel, nil, false, a.pdf.searchBusy || strings.TrimSpace(a.pdf.searchQuery) == "", a.startPDFSearch).Height(26)
			if len(a.pdf.searchHits) > 0 {
				hitCounter := ui.Text(c, fmt.Sprintf("%d/%d", a.pdf.searchIndex+1, len(a.pdf.searchHits))).
					FontSize(11.5).TextColor(p.quiet).SingleLine().Shrink(0)
				prevHit := ui.Local(hitCounter, "prevHit", func() int { return a.pdf.searchIndex })
				down := a.pdf.searchIndex < *prevHit
				*prevHit = a.pdf.searchIndex
				hitCounter.ContentTransition(ctNumeric(down))
				designToolbarButton(c, p, icChevronLeft, "上一处", 24, false, false, func() { a.jumpPDFSearchHit(a.pdf.searchIndex - 1) })
				designToolbarButton(c, p, icChevronRight, "下一处", 24, false, false, func() { a.jumpPDFSearchHit(a.pdf.searchIndex + 1) })
			}
			designToolbarButton(c, p, icClose, "关闭全文搜索", 24, false, false, a.togglePDFSearch)
		})
		if a.pdf.searchErr != "" {
			ui.Text(c, a.pdf.searchErr).Padding(4, 12, 8).FontSize(11.5).TextColor(p.danger)
		} else if a.pdf.searchDone && len(a.pdf.searchHits) == 0 {
			ui.Text(c, "未找到匹配内容（纯图片扫描版 PDF 暂不支持全文检索）").
				Padding(4, 12, 8).FontSize(11.5).TextColor(p.quiet)
		} else if len(a.pdf.searchHits) > 0 {
			listH := min(116, float32(len(a.pdf.searchHits))*32+8)
			ui.Scroll(c.Key("pdf-search-hits")).FillWidth().Height(listH).Padding(2, 10, 6).Gap(4).
				TrackScroll(&a.pdf.searchScroll).Children(func() {
				for idx, hit := range a.pdf.searchHits {
					if idx >= 80 {
						break
					}
					active := idx == a.pdf.searchIndex
					row := ui.ButtonBase(c.Key(fmt.Sprintf("pdf-hit-%d", idx))).
						FillWidth().Height(28).PaddingX(8).Radius(6).Gap(8).AlignItems(ui.Center).
						Transition(trColorMicro).OnClick(func() { a.jumpPDFSearchHit(idx) })
					bg, border := hoverPressSurface(row, p, active, p.canvas, p.rule, p.selectedBg, p.accent.Alpha(.45))
					row.Background(bg).Border(1, border)
					row.Children(func() {
						ui.Text(c, fmt.Sprintf("P.%d", hit.Page+1)).FontSize(11).FontWeight(600).
							TextColor(p.accent).SingleLine().Shrink(0)
						ui.Text(c, hit.Snippet).FontSize(11.5).TextColor(p.ink).
							SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
					})
				}
			})
		}
	})
}

func (a *workspace) pdfThumbRail(c *ui.Context, p palette, railW, railH float32, count int) {
	const (
		itemH   = float32(132)
		itemGap = float32(6)
		stride  = itemH + itemGap
	)
	ui.Scroll(c.Key("pdf-thumb-rail")).Width(railW).Height(railH).Shrink(0).
		Background(p.surface).BorderColor(p.rule).BorderWidth(0, 1, 0, 0).
		Padding(6, 6).Gap(itemGap).TrackScroll(&a.pdf.thumbScroll).Label("PDF 缩略图").
		Transition(trPanelSlideH).
		Children(func() {
			start := max(0, int(a.pdf.thumbScroll.Y/stride)-2)
			visibleCount := max(6, int(railH/stride)+4)
			end := min(count, start+visibleCount)
			if start > 0 {
				ui.Box(c).FillWidth().Height(float32(start)*stride - itemGap).Shrink(0)
			}
			dark := p.dark && a.settings.PDFNight
			for idx := start; idx < end; idx++ {
				pageIdx := idx
				ref := pageRef{a.pdf.document, pageIdx}
				thumb, ready := a.requestPDF(ref, pdfThumbnail, dark, 160)
				active := pageIdx == a.pdf.page
				cell := ui.ButtonBase(c.Key(fmt.Sprintf("thumb-%d", pageIdx))).
					FillWidth().Height(itemH).Shrink(0).Padding(4).Radius(8).
					Label(fmt.Sprintf("第 %d 页", pageIdx+1)).Transition(trColorMicro).
					OnClick(func() { a.setPDFPage(pageIdx) })
				bg, border := hoverPressSurface(cell, p, active, p.canvas, p.rule, p.selectedBg, p.accent)
				cell.Background(bg).Border(1.5, border)
				cell.Children(func() {
					ui.Column(c).Fill().Center().Gap(3).Children(func() {
						ui.Box(c).Grow(1).MinHeight(0).FillWidth().Radius(5).Clip().
							Background(ui.Hex("#ffffff")).Center().Children(func() {
							if ready && thumb.err == nil && thumb.light != nil {
								img := thumb.light
								if dark && thumb.night != nil {
									img = thumb.night
								}
								ui.Image(c, img).Fill().Fit(ui.Contain)
							} else {
								ui.Text(c, fmt.Sprintf("%d", pageIdx+1)).FontSize(12).TextColor(p.quiet)
							}
						})
						numColor := p.quiet
						if active {
							numColor = p.accent
						}
						ui.Text(c, fmt.Sprintf("%d", pageIdx+1)).FontSize(10.5).FontWeight(600).TextColor(numColor).SingleLine()
					})
				})
			}
			if tail := count - end; tail > 0 {
				ui.Box(c).FillWidth().Height(float32(tail)*stride - itemGap).Shrink(0)
			}
		})
}

func (a *workspace) pdfSheet(c *ui.Context, p palette, width, height float32, ref pageRef, page renderedPDFPage, pending bool) {
	d := &a.pdf
	d.now = c.Now()
	image := page.light
	paper, marginPaper := ui.Hex("#ffffff"), ui.Hex("#f6f7fb")
	if p.dark && a.settings.PDFNight && page.night != nil {
		image = page.night
		paper, marginPaper = ui.Hex("#181818"), p.elevated
	}
	aspect := page.aspectRatio()
	sheetHost := ui.Box(c.Key("pdf-sheet-host")).Grow(1).MinHeight(0).FillWidth().Clip()
	targetRatio := d.marginRatio()
	var ratio float32
	if a.marginDragging || c.Instant() {
		ratio = sheetHost.Jump("pdf-margin", targetRatio)
	} else {
		ratio = sheetHost.AnimateTo("pdf-margin", targetRatio, animLayout)
	}
	const pad = pdfPaperPadding
	baseFit := pdfFitWidth(width, height, aspect, ratio)
	paperW := baseFit * max(1, d.zoom)
	paperH, addedW := paperW*aspect, paperW*ratio
	key := ref.document + ":" + strconv.Itoa(ref.page)
	scroll := &d.scroll
	if pending {
		scroll = &d.displayed.scroll
	} else {
		d.aspect = aspect
		a.checkShape(c)
		a.focusPDFSheet(c, ref, paperW, paperH)
	}
	sheetHost.Children(func() {
		horizontalPadding := max(pad, (width-paperW-addedW)/2)
		bottomPad := float32(70)
		if a.touchUI() && a.mode == pdfMode {
			bottomPad = 92
		}
		ui.ScrollBoth(c.Key("pdf-scroll")).Fill().Padding(pad, horizontalPadding, bottomPad).
			TrackScroll(scroll).Label("PDF 页面").IgnoreInput(pending).
			HandleInput(func(ev ui.InputEvent) bool {
				if pending {
					return false
				}
				switch {
				case ev.Kind == ui.InputZoom && ev.Scale > 0:
					lx := clamp(ev.X+d.scroll.X-horizontalPadding, 0, paperW+addedW)
					ly := clamp(ev.Y+d.scroll.Y-pad, 0, paperH)
					a.zoomPDFAt(ev.Scale, lx, ly, width, pad, baseFit, ratio, ev.Time)
					return true
				case ev.Kind == ui.InputScroll && ev.Mods&(ui.Ctrl|ui.Super) != 0:
					lx := clamp(ev.X+d.scroll.X-horizontalPadding, 0, paperW+addedW)
					ly := clamp(ev.Y+d.scroll.Y-pad, 0, paperH)
					a.zoomPDFAt(float32(math.Exp(float64(-ev.DY)/300)), lx, ly, width, pad, baseFit, ratio, ev.Time)
					return true
				}
				return false
			}).Children(func() {
			sheet := ui.Box(c.Key(key)).Size(paperW+addedW, paperH).Shrink(0).
				Radius(9).Clip().Background(paper).Border(1, p.rule).Label("PDF 原页与留白").
				Shadow(0, 6, 16, -5, p.shadow).IgnoreInput(pending).
				HandleInput(func(ev ui.InputEvent) bool {
					if pending {
						return false
					}
					switch {
					case ev.Kind == ui.InputZoom && ev.Scale > 0:
						a.zoomPDFAt(ev.Scale, ev.X, ev.Y, width, pad, baseFit, ratio, ev.Time)
						return true
					case ev.Kind == ui.InputScroll && ev.Mods&(ui.Ctrl|ui.Super) != 0:
						a.zoomPDFAt(float32(math.Exp(float64(-ev.DY)/300)), ev.X, ev.Y, width, pad, baseFit, ratio, ev.Time)
						return true
					case ev.Kind == ui.InputPointerDown:
						d.lastZoomAt = time.Time{}
						d.now = c.Now()
						if ev.Button == 0 && a.drawing.tool == panTool && a.reciteMode && a.toggleMaskAt(ref.document, ref.page, ui.Rect{W: paperW, H: paperH}, ev.X, ev.Y) {
							return true
						}
					}
					point := inkPoint{X: ev.X / paperW, Y: ev.Y / paperW, Pressure: ev.Pressure}
					accept := a.drawing.tool != excerptTool || ev.X <= paperW
					return a.inkInput(ev, key, point, paperW, accept)
				})
			if a.drawing.tool != panTool && a.drawing.tool != lassoTool {
				sheet.Cursor(ui.CursorCrosshair)
			}
			if !pending && a.drawing.tool == excerptTool && !a.splitDragging && !a.marginDragging {
				// A touch drag selects a region; keep the sheet fixed within the scroll view.
				sheet.Draggable(nil, ui.DraggableOptions{Bounds: &ui.Rect{}, Rigid: true})
			}
			sheet.Draw(func(paint *ui.Painter, rect ui.Rect) {
				original := ui.Rect{X: rect.X, Y: rect.Y, W: paperW, H: paperH}
				paint.Image(image, original, ui.Cover)
				if addedW > 0 {
					gutter := ui.Rect{X: rect.X + paperW, Y: rect.Y, W: addedW, H: rect.H}
					paint.Fill(gutter, marginPaper, 0)
					paint.Line(gutter.X, gutter.Y, gutter.X, gutter.Y+gutter.H, 1, p.edge)
					if addedW >= 56 {
						paint.Text(gutter.X+8, gutter.Y+14, "留白批注区", 11, p.quiet)
					}
				}
				// Keep the ink render target to the visible portion at high page zoom.
				vp := paint.Visible()
				vis := intersect(rect, vp)
				if !pending {
					a.pdfPenZone(key, vis, rect.X, rect.Y, paperW, vp.Y+vp.H)
				}
				a.drawing.drawInk(paint, vis, p, key, rect.X, rect.Y, paperW, &a.drawing.pdf)
				a.pdfHighlights(paint, p, original, ref, key)
			})
			if !pending && (addedW > 0 || d.marginOpen) {
				marginGripW := float32(7)
				if a.touchUI() {
					marginGripW = 20
				}
				gripLeft := min(paperW-marginGripW/2, max(0, paperW+addedW-2-marginGripW))
				sheet.Children(func() {
					grip := ui.Box(c.Key("pdf-margin-grip")).Absolute().Top(0).Bottom(0).Left(gripLeft).Width(marginGripW).
						Center().
						Label("拖动调整留白宽度").Role(ui.RoleSeparator).
						Draggable(nil, ui.DraggableOptions{Axis: ui.Horizontal, Bounds: &ui.Rect{}, Rigid: true}).
						HandleInput(func(ev ui.InputEvent) bool {
							if ev.Pointer == ui.PointerPen && a.drawing.tool != panTool {
								return false
							}
							switch ev.Kind {
							case ui.InputPointerDown:
								if ev.Button != 0 {
									return false
								}
								a.marginDragging, a.marginStart = true, d.margin
							case ui.InputPointerMove:
								if !a.marginDragging {
									return false
								}
								d.margin = clamp(d.margin-(ev.X-marginGripW/2)/max(120, paperW), .18, .85)
							case ui.InputPointerUp:
								if !a.marginDragging {
									return false
								}
								a.marginDragging = false
								if ev.Canceled {
									d.margin = a.marginStart
								} else {
									next := a.settings
									next.PDFMargin = d.margin
									next.PDFMarginOpen = true
									a.saveSettings(next)
								}
							default:
								return false
							}
							return true
						})
					if a.touchUI() || a.marginDragging || grip.Hovered() {
						grip.Children(func() {
							col := p.border
							pillScale := float32(1)
							if a.marginDragging {
								col = p.accent
								pillScale = 1.35
							} else if grip.Hovered() {
								col = p.secondary
								pillScale = 1.15
							}
							ui.Box(c.Key("pdf-margin-grip-pill")).Width(3.5).Height(36).Radius(2).
								Background(col).Transition(trColorMicro).
								AnimatePose(ui.Pose{Scale: pillScale}, animControl)
						})
					}
				})
			}
		})
		if a.mode == pdfMode || (a.mode == splitMode && a.settings.LayoutMode() == notebook.LayoutOutline) {
			a.inkDock(c, p, pdfMode, width)
		} else if !pending && width > 235 && a.touchUI() {
			hint := "手指导航 · 触控笔书写"
			if !a.stylusDrawsOnPDF() {
				hint = "手指导航 · 触控笔选择"
			}
			ui.Row(c).Absolute().Bottom(16).Left(16).Height(26).PaddingX(10).AlignItems(ui.Center).
				Radius(13).Background(p.elevated).Border(1, p.rule).Children(func() {
				ui.Text(c, hint).FontSize(11).TextColor(p.quiet).SingleLine()
			})
		}
		if pending {
			ui.Row(c.Key("pdf-loading")).Absolute().Top(8).Left(0).Right(0).Center().ZIndex(3).IgnoreInput(true).
				Transition(trFloatingBarStatic).Children(func() {
				ui.Row(c).Padding(6, 12).Radius(12).Gap(6).AlignItems(ui.Center).
					Background(p.elevated).Border(1, p.rule).Shadow(0, 2, 8, -2, p.shadowSoft).Children(func() {
					ui.Spinner(c).Size(13, 13).TextColor(p.secondary)
					ui.Text(c, fmt.Sprintf("正在加载第 %d 页…", d.page+1)).TextColor(p.secondary).FontSize(12)
				})
			})
		} else if a.pdfHighlightNoteID != "" {
			if hlNote, ok := a.tree.notes[a.pdfHighlightNoteID]; ok && noteHasPageHighlight(hlNote, ref.page+1) {
				clearH := float32(26)
				if a.touchUI() {
					clearH = 34
				}
				clearBtn := ui.ButtonBase(c.Key("pdf-clear-hl")).Absolute().Top(10).Right(14).
					Height(clearH).Padding(0, 10).Radius(99).AlignItems(ui.Center).Gap(5).
					Background(p.elevated).Border(1, p.tintBorder).
					Shadow(0, 2, 8, -2, p.shadowSoft).
					Transition(trFloatingBarStatic).
					Label("清除高亮定位").FocusRingColor(p.accent).
					OnClick(func() {
						a.pdfHighlightNoteID = ""
						d.focusedHighlightNoteID = ""
						d.highlight = nil
					})
				if clearBtn.Pressed() {
					clearBtn.Background(p.pressed)
				} else if clearBtn.Hovered() {
					clearBtn.Background(p.tint)
				}
				applyPressPose(clearBtn, clearBtn.Pressed(), false)
				clearBtn.Children(func() {
					ui.Box(c).Size(6.5, 6.5).Radius(99).Background(p.accent).Shrink(0)
					ui.Text(c, noteTitle(hlNote)).FontSize(11).FontWeight(550).
						TextColor(p.ink).SingleLine().Ellipsis("…").MaxWidth(150)
					ui.Icon(c, icClose).Size(11, 11).TextColor(p.quiet).Shrink(0)
				})
			}
		}
	})
}

func (a *workspace) focusPDFSheet(c *ui.Context, ref pageRef, paperW, paperH float32) {
	d := &a.pdf
	scrollTo := func(tx, ty float32) {
		if !c.Instant() && (d.scroll.MaxX > 0 || d.scroll.MaxY > 0) {
			d.scroll.ScrollTo(tx, ty, animCamera)
		} else {
			d.scroll.X = tx
			d.scroll.Y = ty
		}
	}
	if a.pdfHighlightNoteID == "" {
		d.focusedHighlightNoteID = ""
		d.highlightSelectedAt = ""
	} else {
		if d.focusedHighlightNoteID == a.pdfHighlightNoteID && a.selected != d.highlightSelectedAt && a.selected != a.pdfHighlightNoteID {
			// User switched to a different note on the canvas/outline; clear stale PDF highlight.
			a.pdfHighlightNoteID = ""
			d.focusedHighlightNoteID = ""
			d.highlightSelectedAt = ""
		} else if d.focusedHighlightNoteID == a.pdfHighlightNoteID && a.selected == a.pdfHighlightNoteID {
			d.highlightSelectedAt = a.selected
		} else if d.focusedHighlightNoteID != a.pdfHighlightNoteID {
			d.focusedHighlightNoteID = a.pdfHighlightNoteID
			d.highlightSelectedAt = a.selected
			if hlNote, ok := a.tree.notes[a.pdfHighlightNoteID]; ok {
				unit := ui.Rect{W: paperW, H: paperH}
				for _, hr := range hlNote.HighlightRects {
					if hr.Page == ref.page+1 {
						if r, valid := a.mapHighlightRect(hr, unit, ref.document); valid {
							scrollTo(max(0, r.X-32), max(0, r.Y-48))
							break
						}
					}
				}
			}
		}
	}
	if d.focusHighlight && d.highlight != nil {
		h := d.highlight
		scrollTo(max(0, h.X*paperW-32), max(0, h.Y*paperH-40))
		d.focusHighlight = false
	}
}

func intersect(a, b ui.Rect) ui.Rect {
	x, y := max(a.X, b.X), max(a.Y, b.Y)
	return ui.Rect{X: x, Y: y, W: max(0, min(a.X+a.W, b.X+b.W)-x), H: max(0, min(a.Y+a.H, b.Y+b.H)-y)}
}

func noteHasPageHighlight(n notebook.Note, pageNo int) bool {
	for _, hr := range n.HighlightRects {
		if hr.Page == pageNo {
			return true
		}
	}
	for _, mr := range n.MaskRects {
		if mr.Page == pageNo {
			return true
		}
	}
	return false
}

var pdfBoxPattern = regexp.MustCompile(`/(CropBox|MediaBox)\s*\[\s*([+-]?\d*\.?\d+)\s+([+-]?\d*\.?\d+)\s+([+-]?\d*\.?\d+)\s+([+-]?\d*\.?\d+)\s*\]`)

func scanPDFPageBox(path string, aspect float32) (float32, float32, bool) {
	if path == "" {
		return 0, 0, false
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= 0 {
		return 0, 0, false
	}
	const headLimit = 2 << 20
	const tailLimit = 512 << 10
	var chunks [][]byte
	if info.Size() <= headLimit+tailLimit {
		data, err := io.ReadAll(io.LimitReader(f, headLimit+tailLimit))
		if err != nil {
			return 0, 0, false
		}
		chunks = append(chunks, data)
	} else {
		head := make([]byte, headLimit)
		if n, _ := io.ReadFull(f, head); n > 0 {
			chunks = append(chunks, head[:n])
		}
		tail := make([]byte, tailLimit)
		if n, _ := f.ReadAt(tail, info.Size()-tailLimit); n > 0 {
			chunks = append(chunks, tail[:n])
		}
	}
	var bestW, bestH float32
	bestScore := float32(math.MaxFloat32)
	for _, chunk := range chunks {
		if !bytes.Contains(chunk, []byte("Box")) {
			continue
		}
		matches := pdfBoxPattern.FindAllSubmatch(chunk, 32)
		for _, m := range matches {
			x0, err0 := strconv.ParseFloat(string(m[2]), 32)
			y0, err1 := strconv.ParseFloat(string(m[3]), 32)
			x1, err2 := strconv.ParseFloat(string(m[4]), 32)
			y1, err3 := strconv.ParseFloat(string(m[5]), 32)
			if err0 != nil || err1 != nil || err2 != nil || err3 != nil {
				continue
			}
			w := float32(math.Abs(x1 - x0))
			h := float32(math.Abs(y1 - y0))
			if w < 36 || h < 36 {
				continue
			}
			if aspect > 0 && (h > w) != (aspect > 1) {
				w, h = h, w
			}
			score := float32(0)
			if aspect > 0 {
				score = float32(math.Abs(float64(h/w - aspect)))
			}
			if string(m[1]) == "MediaBox" {
				score += 0.01
			}
			if score < bestScore {
				bestScore = score
				bestW, bestH = w, h
			}
		}
	}
	if bestW > 0 && bestH > 0 {
		return bestW, bestH, true
	}
	return 0, 0, false
}

func (a *workspace) pdfPagePoints(docID string, aspect float32) (float32, float32) {
	if pt, ok := a.pdf.pagePts[docID]; ok && pt[0] > 0 && pt[1] > 0 {
		w, h := pt[0], pt[1]
		if aspect > 0 && (h > w) != (aspect > 1) {
			w, h = h, w
		}
		return w, h
	}
	if a.store != nil && docID != "" {
		if w, h, ok := scanPDFPageBox(a.store.DocumentPath(docID), aspect); ok {
			if a.pdf.pagePts == nil {
				a.pdf.pagePts = make(map[string][2]float32)
			}
			a.pdf.pagePts[docID] = [2]float32{w, h}
			return w, h
		}
	}
	// Fallback to standard ISO A4 / US Letter point dimensions matching rendered aspect ratio.
	w := float32(595.32)
	if aspect > 0 && aspect < 1.354 {
		w = 612
	}
	h := float32(841.92)
	if aspect > 0 {
		h = w * aspect
	}
	if docID != "" {
		if a.pdf.pagePts == nil {
			a.pdf.pagePts = make(map[string][2]float32)
		}
		a.pdf.pagePts[docID] = [2]float32{w, h}
	}
	return w, h
}

func (a *workspace) mapHighlightRect(hr notebook.HighlightRect, original ui.Rect, docID string) (ui.Rect, bool) {
	if hr.W <= 0 || hr.H <= 0 || original.W <= 0 || original.H <= 0 {
		return ui.Rect{}, false
	}
	var nx, ny, nw, nh float32
	if hr.X >= 0 && hr.Y >= 0 && hr.X+hr.W <= 1.01 && hr.Y+hr.H <= 1.01 {
		nx, ny, nw, nh = hr.X, hr.Y, hr.W, hr.H
	} else {
		// MarginNote ZHIGHLIGHT_PIC.selLst[].rect uses Quartz PDF points (bottom-left origin).
		ptW, ptH := a.pdfPagePoints(docID, a.pdf.aspect)
		if hr.X+hr.W > ptW {
			ptW = hr.X + hr.W
		}
		if hr.Y+hr.H > ptH {
			ptH = hr.Y + hr.H
		}
		nx = hr.X / ptW
		ny = 1 - (hr.Y+hr.H)/ptH
		nw = hr.W / ptW
		nh = hr.H / ptH
	}
	x0 := clamp(nx, 0, 1)
	y0 := clamp(ny, 0, 1)
	x1 := clamp(nx+nw, 0, 1)
	y1 := clamp(ny+nh, 0, 1)
	if x1 <= x0 || y1 <= y0 {
		return ui.Rect{}, false
	}
	return ui.Rect{
		X: original.X + x0*original.W,
		Y: original.Y + y0*original.H,
		W: (x1 - x0) * original.W,
		H: (y1 - y0) * original.H,
	}, true
}

func (a *workspace) toggleMaskAt(docID string, pageZeroBased int, original ui.Rect, x, y float32) bool {
	pageNo := pageZeroBased + 1
	for _, n := range a.tree.book.Notes {
		for mi, mr := range n.MaskRects {
			if mr.Page != pageNo {
				continue
			}
			r, valid := a.mapHighlightRect(mr, original, docID)
			if !valid {
				continue
			}
			if x >= r.X && x <= r.X+r.W && y >= r.Y && y <= r.Y+r.H {
				key := fmt.Sprintf("%s:%d:%d", n.ID, mr.Page, mi)
				a.setMaskRevealed(key, !a.revealedMasks[key])
				if !a.pdf.now.IsZero() {
					a.maskFades.start(key, a.pdf.now)
				}
				hapticLight()
				return true
			}
		}
	}
	return false
}

func (a *workspace) pdfHighlights(paint *ui.Painter, p palette, original ui.Rect, ref pageRef, key string) {
	show := func(region notebook.PDFRegion, active bool) {
		r := ui.Rect{X: original.X + region.X*original.W, Y: original.Y + region.Y*original.H,
			W: region.W * original.W, H: region.H * original.H}
		alpha := float32(.06)
		if active {
			alpha = .18
		}
		paint.Fill(r, p.accent.Alpha(alpha), 3)
		paint.StrokePath(rectPath(r).Dash(0, 5, 4), 1.5, p.accent)
	}
	for _, card := range a.drawing.data.Cards {
		r := card.Excerpt
		if r != nil && r.Document == ref.document && r.Page == ref.page {
			show(*r, a.pdf.highlight != nil && *r == *a.pdf.highlight)
		}
	}
	amber := ui.Hex("#f59e0b")
	if p.dark {
		amber = ui.Hex("#b45309")
	}
	if a.reciteMode {
		pageNo := ref.page + 1
		for _, n := range a.tree.book.Notes {
			for mi, mr := range n.MaskRects {
				if mr.Page != pageNo {
					continue
				}
				r, valid := a.mapHighlightRect(mr, original, ref.document)
				if !valid {
					continue
				}
				maskKey := fmt.Sprintf("%s:%d:%d", n.ID, mr.Page, mi)
				revealed := a.revealedMasks[maskKey]
				prog, active := a.maskFades.progress(maskKey, paint.Now(), animFade.Duration)
				if active {
					paint.AnimationFrame()
				}
				fillAlpha := float32(1)
				if revealed {
					fillAlpha = 1 + (0.12-1)*prog
				} else if active {
					fillAlpha = 0.12 + (1-0.12)*prog
				}
				if revealed && !active {
					paint.Fill(r, amber.Alpha(.12), 4)
					paint.StrokePath(rectPath(r).Dash(0, 4, 3), 1.25, amber.Alpha(.7))
				} else if !revealed && !active {
					paint.Fill(r, amber, 4)
					paint.StrokePath(rectPath(r), 1.25, amber.Alpha(.9))
					if r.W >= 54 && r.H >= 18 {
						paint.Text(r.X+6, r.Y+4, "点击揭晓", 10, ui.Hex("#ffffff"))
					}
				} else {
					paint.Fill(r, amber.Alpha(fillAlpha), 4)
					t := (fillAlpha - 0.12) / (1 - 0.12)
					borderAlpha := float32(0.7) + (0.9-0.7)*t
					if revealed {
						paint.StrokePath(rectPath(r).Dash(0, 4, 3), 1.25, amber.Alpha(borderAlpha))
					} else {
						paint.StrokePath(rectPath(r), 1.25, amber.Alpha(borderAlpha))
					}
					if r.W >= 54 && r.H >= 18 && t > 0.05 {
						paint.Text(r.X+6, r.Y+4, "点击揭晓", 10, ui.Hex("#ffffff").Alpha(t))
					}
				}
			}
		}
	}
	if a.pdfHighlightNoteID != "" {
		if hlNote, ok := a.tree.notes[a.pdfHighlightNoteID]; ok {
			pageNo := ref.page + 1
			hlColor := p.accent
			if hlNote.Color != nil {
				if ct := cardThemeFor(p, hlNote); ct.tinted {
					hlColor = ct.accent
				}
			}
			for _, hr := range hlNote.HighlightRects {
				if hr.Page != pageNo {
					continue
				}
				if r, valid := a.mapHighlightRect(hr, original, ref.document); valid {
					paint.Fill(r, hlColor.Alpha(.20), 4)
					paint.StrokePath(rectPath(r), 1.75, hlColor.Alpha(.88))
				}
			}
			if !a.reciteMode {
				for _, mr := range hlNote.MaskRects {
					if mr.Page != pageNo {
						continue
					}
					if r, valid := a.mapHighlightRect(mr, original, ref.document); valid {
						paint.Fill(r, amber.Alpha(.24), 3)
						paint.StrokePath(rectPath(r).Dash(0, 4, 3), 1.5, amber.Alpha(.88))
					}
				}
			}
		}
	}
	g := a.drawing.gesture
	if g.tool == excerptTool && g.key == key {
		show(a.regionFromPoints(inkBounds(g.stroke.Points)), true)
	}
}

func (a *workspace) regionFromPoints(box ui.Rect) notebook.PDFRegion {
	x, y := clamp(box.X, 0, 1), clamp(box.Y/a.pdf.aspect, 0, 1)
	right := clamp(box.X+box.W, 0, 1)
	bottom := clamp((box.Y+box.H)/a.pdf.aspect, 0, 1)
	return notebook.PDFRegion{Document: a.pdf.document, Page: a.pdf.page, X: x, Y: y, W: right - x, H: bottom - y}
}

func (a *workspace) createExcerpt(box ui.Rect) {
	region := a.regionFromPoints(box)
	if region.W < .012 || region.H < .008 {
		return
	}
	scale := max(.2, a.scale)
	x, y := (a.canvasWidth*.56-a.panX)/scale, (a.canvasHeight*.20-a.panY)/scale
	stack := float32(len(a.drawing.data.Cards)%6) * 24
	card := notebook.CanvasCard{ID: uuid.NewString(), Title: "PDF 摘录", X: x + stack, Y: y + stack, Anchor: a.selected,
		W: 254, H: clamp(58+242*region.H*a.pdf.aspect/region.W, 110, 245), Excerpt: &region}
	next := a.drawing.data
	next.Cards = append(slices.Clone(next.Cards), card)
	if !a.commitDrawing(next) {
		return
	}
	hapticSuccess()
	a.pdf.highlight, a.pdf.focusHighlight = &region, true
	if a.mode == pdfMode {
		a.showActionMessage("已摘录到画布", "分屏查看", func() {
			if a.settings.LayoutMode() == notebook.LayoutOutline {
				next := a.settings
				next.GraphLayout = notebook.LayoutCards
				a.saveSettings(next)
			}
			a.setMode(splitMode)
			a.jumpToCard = card.ID
		})
		return
	}
	if a.settings.LayoutMode() == notebook.LayoutOutline {
		a.showActionMessage("已摘录到画布（大纲流不显示手写卡片）", "切到卡片树", func() {
			next := a.settings
			next.GraphLayout = notebook.LayoutCards
			a.saveSettings(next)
			a.jumpToCard = card.ID
		})
		return
	}
	a.jumpToCard = card.ID
}

func (a *workspace) excerptPreview(c *ui.Context, p palette, region notebook.PDFRegion) {
	page, ok := a.requestPDF(pageRef{region.Document, region.Page}, pdfThumbnail, p.dark && a.settings.PDFNight, 480)
	if !ok || page.err != nil {
		message := "正在加载摘录…"
		if page.err != nil {
			message = "原文暂不可用"
		}
		ui.Text(c, message).PaddingX(10).FontSize(10).TextColor(p.quiet).SingleLine()
		return
	}
	image := page.light
	if p.dark && a.settings.PDFNight && page.night != nil {
		image = page.night
	}
	w, h := image.Size()
	src := ui.Rect{X: region.X * float32(w), Y: region.Y * float32(h), W: region.W * float32(w), H: region.H * float32(h)}
	ui.Box(c).Grow(1).FillWidth().MinHeight(0).Margin(0, 10, 8).Clip().Draw(func(paint *ui.Painter, rect ui.Rect) {
		k := min(rect.W/max(1, src.W), rect.H/max(1, src.H))
		dst := ui.Rect{X: rect.X, Y: rect.Y, W: src.W * k, H: src.H * k}
		paint.ImageRegion(image, src, dst, ui.ImageOptions{})
	})
}
