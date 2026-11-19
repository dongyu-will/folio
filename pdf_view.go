package main

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"runtime"
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
	cache       map[pageRef]renderedPDFPage
	recent      []pageRef
	thumbs      map[pageRef]renderedPDFPage
	thumbRecent []pageRef
	foreground  *pageRef
	background  *pdfBackground
	nightWork   bool
	prefetchRef pageRef
	prefetchAt  time.Time
	counts      map[string]int
	serial      int

	scroll                   ui.ScrollState
	zoom                     float32
	lastZoomVPX, lastZoomVPY float32
	lastZoomAt               time.Time
	margin                   float32
	marginOpen             bool
	highlight              *notebook.PDFRegion
	focusHighlight         bool
	aspect                 float32
	jumpPageText           string
	pagePts                map[string][2]float32
	focusedHighlightNoteID string
	highlightSelectedAt    string
}

func (a *workspace) resetPDF(documents []notebook.Document) {
	a.pdf = pdfState{serial: a.pdf.serial + 1}
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

func (a *workspace) clearRenderCache() {
	a.pdf.cache, a.pdf.recent = nil, nil
	a.pdf.thumbs, a.pdf.thumbRecent = nil, nil
	a.pdf.serial++
	a.pdf.foreground, a.pdf.background, a.pdf.nightWork = nil, nil, false
	a.pdf.prefetchAt = time.Time{}
	a.drawing.pdf, a.drawing.canvas = inkLayers{}, inkLayers{}
	a.mediaCache = nil
	a.showMessage("已清理渲染缓存", false)
}

type pdfRequest uint8

const (
	pdfVisible pdfRequest = iota
	pdfPrefetch
	pdfThumbnail
)

type pdfBackground struct {
	ref     pageRef
	request pdfRequest
}

func (a *workspace) requestPDF(ref pageRef, request pdfRequest, dark bool) (renderedPDFPage, bool) {
	p := &a.pdf
	if item, ok := p.cache[ref]; ok {
		if request != pdfPrefetch {
			p.recent = touchPDFPage(p.recent, ref)
		}
		return a.requirePDFNight(ref, item, p.cache, dark)
	}
	if request == pdfThumbnail {
		if item, ok := p.thumbs[ref]; ok {
			p.thumbRecent = touchPDFPage(p.thumbRecent, ref)
			return a.requirePDFNight(ref, item, p.thumbs, dark)
		}
	}
	win := a.window()
	if win == nil || a.store == nil || p.foreground != nil {
		return renderedPDFPage{}, false
	}
	if p.background != nil && (request != pdfVisible || p.background.ref == ref && p.background.request == pdfPrefetch) {
		return renderedPDFPage{}, false
	}
	path := a.store.DocumentPath(ref.document)
	if path == "" {
		return renderedPDFPage{err: fmt.Errorf("找不到所选 PDF")}, true
	}
	background := request != pdfVisible
	if background {
		p.background = &pdfBackground{ref: ref, request: request}
	} else {
		p.foreground = &ref
	}
	side, serial := 2400, p.serial
	if runtime.GOOS == "android" {
		side = 2000
	}
	if request == pdfThumbnail {
		side = 480
	}
	go func() {
		item, err := renderPDF(path, ref.page, side, background)
		item.err = err
		if err == nil && dark && request == pdfVisible {
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
	return renderedPDFPage{}, false
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

func (a *workspace) prefetchPDF(c *ui.Context, ref pageRef, count int) {
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
	a.requestPDF(pageRef{ref.document, next}, pdfPrefetch, false)
}

func (a *workspace) setPDFPage(page int) {
	a.pdf.page = max(0, page)
	a.androidInk.pdf = penZone{}
	a.pdf.scroll = ui.ScrollState{}
	a.pdf.highlight = nil
	a.pdf.focusHighlight = false
	a.pdf.focusedHighlightNoteID = ""
	a.pdfHighlightNoteID = ""
	a.pdf.jumpPageText = strconv.Itoa(a.pdf.page + 1)
}

func (a *workspace) setPDFDocument(id string) {
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
				a.splitRatio = .48
			}
			minPane := min(float32(180), max(90, (bodyHeight-gripSize)*.3))
			pdfHeight := clamp((bodyHeight-gripSize)*a.splitRatio, minPane, max(minPane, bodyHeight-gripSize-minPane))
			ui.Box(c).FillWidth().Height(pdfHeight).Shrink(0).Children(func() { a.pdfPane(c, p, width, pdfHeight) })
			a.splitGripVertical(c, p, bodyHeight, pdfHeight, gripSize)
			ui.Box(c).Grow(1).FillWidth().MinHeight(0).Children(func() { showNotesPane(width, max(1, bodyHeight-pdfHeight-gripSize)) })
			return
		}
		pdfWidth := width
		if a.mode == splitMode {
			if a.splitRatio == 0 {
				a.splitRatio = .425
			}
			minPane := min(float32(195), max(120, (width-gripSize)*.3))
			pdfWidth = clamp((width-gripSize)*a.splitRatio, minPane, max(minPane, width-gripSize-minPane))
		}
		ui.Row(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
			ui.Box(c).Width(pdfWidth).FillHeight().Shrink(0).Children(func() { a.pdfPane(c, p, pdfWidth, bodyHeight) })
			if a.mode == splitMode {
				a.splitGrip(c, p, width, pdfWidth, gripSize)
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
		if a.settings.SidebarCollapsed || a.sidebarHiddenForSplit || (width >= 700 && a.lastViewWide && !a.lastViewSidebar) {
			designToolbarButton(c, p, icExpand, "展开侧边栏", btnSize, true, a.editing(), a.toggleSidebar)
			ui.Box(c).Width(1).Height(16).Background(p.rule)
		}
		title := a.tree.book.Title
		if a.mode == canvasMode && a.scope != "" {
			title = noteTitle(a.tree.notes[a.scope])
		}
		ui.Text(c, title).FontSize(14).FontWeight(650).TextColor(p.ink).SingleLine().MinWidth(0).Shrink(1)
		if !compact {
			if a.mode == canvasMode {
				badgePill(c, p, fmt.Sprintf("%d 节点", len(a.tree.order)))
			} else if page, ok := a.pdf.cache[pageRef{a.pdf.document, a.pdf.page}]; ok && page.count > 0 {
				badgePill(c, p, fmt.Sprintf("%d / %d 页", a.pdf.page+1, page.count))
			}
		}
		ui.Box(c).Grow(1).MinWidth(0)
		if a.pdf.document != "" {
			a.modeSegments(c, p, []segment[workspaceMode]{{"PDF", pdfMode}, {"分屏", splitMode}, {"画布", canvasMode}}, a.mode, a.setMode)
			ui.Box(c).Grow(1).MinWidth(0)
		}
		if a.mode != canvasMode {
			marginAction := func() { a.pdf.marginOpen = !a.pdf.marginOpen }
			if compact || (touch && width < 860) {
				designToolbarButton(c, p, icMargin, "留白", btnSize, a.pdf.marginOpen, false, marginAction)
			} else {
				pillButton(c, p, "留白", icMargin, a.pdf.marginOpen, false, marginAction).Height(btnSize)
			}
		}
		if !compact || a.mode == canvasMode {
			if a.mode == pdfMode {
				a.zoomControl(c, p, max(1, a.pdf.zoom), a.zoomPDF)
			} else if layout := a.settings.LayoutMode(); layout != notebook.LayoutOutline {
				showFoldAll := !compact && (!touch || a.mode == canvasMode || width >= 960)
				if showFoldAll && (layout == notebook.LayoutCards || layout == notebook.LayoutBoard) && len(a.tree.order) > 1 {
					if a.hasCollapsedBranches() {
						pillButton(c, p, "展开全部", icLayers, false, a.editing(), func() {
							a.setAllExpanded(true)
						}).Height(btnSize)
					} else {
						pillButton(c, p, "折叠分支", icLayers, false, a.editing(), func() {
							a.setAllExpanded(false)
						}).Height(btnSize)
					}
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
		if a.settings.SidebarCollapsed || a.sidebarHiddenForSplit || a.mode != canvasMode {
			designToolbarButton(c, p, icSettings, "设置", btnSize, true, a.editing(), a.openPreferences)
		}
	})
}

func (a *workspace) modeSegments(c *ui.Context, p palette, options []segment[workspaceMode], selected workspaceMode, change func(workspaceMode)) {
	if !a.touchUI() {
		segments(c, p, options, selected, change)
		return
	}
	ui.Row(c).Height(36).Shrink(0).Padding(3).Gap(2).Radius(9).
		AlignItems(ui.Center).Background(p.inputBg).Border(1, p.rule).Children(func() {
		for _, opt := range options {
			active := opt.value == selected
			b := ui.ButtonBase(c.Key(opt.title)).Height(28).PaddingX(12).Radius(6).Center().
				Shrink(0).Label(opt.title).Selected(active).OnClick(func() { change(opt.value) })
			fg := p.quiet
			if active {
				b.Background(p.elevated).Border(1, p.border).Shadow(0, 1, 2, 0, p.shadowSoft)
				fg = p.ink
			} else if b.Hovered() {
				b.Background(p.hover)
			}
			b.Children(func() { ui.Text(c, opt.title).FontSize(12.5).FontWeight(550).TextColor(fg).SingleLine() })
		}
	})
}

func (a *workspace) zoomControl(c *ui.Context, p palette, scale float32, action func(float32)) {
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
			b := ui.ButtonBase(c.Key(key)).Size(btnSz, btnSz).Radius(6).Center().
				Label(label).FocusRingColor(p.accent).OnClick(onClick)
			fg := p.secondary
			if b.Pressed() {
				b.Background(p.pressed)
				fg = p.ink
			} else if b.Hovered() {
				b.Background(p.hover)
				fg = p.ink
			}
			b.Children(func() {
				ui.Icon(c, icon).Size(icSz, icSz).TextColor(fg)
			})
		}

		zoomBtn("zoom-out", icMinus, "缩小", func() { action(1 / 1.18) })
		pct := ui.ButtonBase(c.Key("zoom-reset")).Height(btnSz).MinWidth(pctW).PaddingX(5).Radius(6).Center().
			Label("重置为 100%").FocusRingColor(p.accent).OnClick(func() { action(-1) })
		pctColor := p.secondary
		if pct.Pressed() {
			pct.Background(p.pressed)
			pctColor = p.ink
		} else if pct.Hovered() {
			pct.Background(p.hover)
			pctColor = p.ink
		}
		pct.Children(func() {
			ui.Text(c, fmt.Sprintf("%d%%", int(scale*100+.5))).FontSize(fontSz).FontWeight(600).
				FontFeatures("tnum").TextColor(pctColor).SingleLine()
		})
		zoomBtn("zoom-in", icPlus, "放大", func() { action(1.18) })
		ui.Box(c).Width(1).Height(14).Background(p.rule).Margin(0, 2)
		zoomBtn("zoom-fit", icFit, "适应视图", func() { action(0) })
	})
}

func (a *workspace) splitGrip(c *ui.Context, p palette, width, left, gripSize float32) {
	// The retained gesture lives on the workspace because layout rebuilds on each move.
	avail := max(1, width-gripSize)
	minPane := min(float32(195), max(120, avail*.3))
	minR, maxR := minPane/avail, max(minPane, avail-minPane)/avail
	handleW, handleH := float32(3), float32(38)
	if a.touchUI() {
		handleW, handleH = 4, 44
	}
	ui.Box(c.Key("split-grip")).Width(gripSize).FillHeight().Shrink(0).Center().
		Background(p.shell).Label("拖动调整分屏宽度").Role(ui.RoleSeparator).
		Draggable(nil, ui.DraggableOptions{Axis: ui.Horizontal, Bounds: &ui.Rect{}, Rigid: true}).
		HandleInput(func(ev ui.InputEvent) bool {
			switch ev.Kind {
			case ui.InputPointerDown:
				a.splitDragging = ev.Button == 0
				a.splitStart, a.splitPointerX = a.splitRatio, left+ev.X
				return a.splitDragging
			case ui.InputPointerMove:
				if a.splitDragging {
					a.splitRatio = clamp(a.splitStart+(left+ev.X-a.splitPointerX)/avail, minR, maxR)
					return true
				}
			case ui.InputPointerUp:
				if a.splitDragging {
					a.splitDragging = false
					if ev.Canceled {
						a.splitRatio = a.splitStart
					}
					return true
				}
			}
			return false
		}).Children(func() {
		col := p.border
		if a.splitDragging {
			col = p.accent
		}
		ui.Box(c).Width(handleW).Height(handleH).Radius(handleW / 2).Background(col)
	})
}

func (a *workspace) splitGripVertical(c *ui.Context, p palette, height, top, gripSize float32) {
	avail := max(1, height-gripSize)
	minPane := min(float32(180), max(90, avail*.3))
	minR, maxR := minPane/avail, max(minPane, avail-minPane)/avail
	handleW, handleH := float32(44), float32(4)
	ui.Box(c.Key("split-grip-v")).FillWidth().Height(gripSize).Shrink(0).Center().
		Background(p.shell).Label("拖动调整分屏高度").Role(ui.RoleSeparator).
		Draggable(nil, ui.DraggableOptions{Axis: ui.Vertical, Bounds: &ui.Rect{}, Rigid: true}).
		HandleInput(func(ev ui.InputEvent) bool {
			switch ev.Kind {
			case ui.InputPointerDown:
				a.splitDragging = ev.Button == 0
				a.splitStart, a.splitPointerX = a.splitRatio, top+ev.Y
				return a.splitDragging
			case ui.InputPointerMove:
				if a.splitDragging {
					a.splitRatio = clamp(a.splitStart+(top+ev.Y-a.splitPointerX)/avail, minR, maxR)
					return true
				}
			case ui.InputPointerUp:
				if a.splitDragging {
					a.splitDragging = false
					if ev.Canceled {
						a.splitRatio = a.splitStart
					}
					return true
				}
			}
			return false
		}).Children(func() {
		col := p.border
		if a.splitDragging {
			col = p.accent
		}
		ui.Box(c).Width(handleW).Height(handleH).Radius(handleH / 2).Background(col)
	})
}

func (a *workspace) zoomPDF(factor float32) {
	a.pdf.lastZoomAt = time.Time{}
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
	newPaperW := baseFit * newZoom
	newPadX := max(pad, (viewW-newPaperW*(1+ratio))/2)

	d.zoom = newZoom
	d.scroll.X = max(0, newPadX+u*newPaperW-vpX-panDX)
	d.scroll.Y = max(0, pad+v*newPaperW-vpY-panDY)
	d.lastZoomVPX, d.lastZoomVPY, d.lastZoomAt = vpX, vpY, evTime
}

func (a *workspace) pdfPane(c *ui.Context, p palette, width, height float32) {
	ref := pageRef{a.pdf.document, a.pdf.page}
	page, ready := a.requestPDF(ref, pdfVisible, p.dark && a.settings.PDFNight)
	count := max(page.count, a.pdf.counts[ref.document])
	if ready && page.err == nil {
		a.prefetchPDF(c, ref, count)
	}
	if !ready || page.err != nil {
		a.androidInk.pdf = penZone{}
	}
	navH := float32(38)
	if a.touchUI() {
		navH = 46
	}
	ui.Column(c).Fill().Background(p.shell).Children(func() {
		a.pdfNavigation(c, p, count, navH)
		if !ready || page.err != nil {
			ui.Box(c).Grow(1).FillWidth().Center().Children(func() {
				if page.err != nil {
					ui.Text(c, page.err.Error()).Padding(12).TextColor(p.danger).FontSize(13)
				} else {
					ui.Text(c, "正在渲染 PDF…").TextColor(p.quiet).FontSize(13)
				}
			})
			return
		}
		a.pdfSheet(c, p, width, max(1, height-navH), ref, page)
	})
}

func (a *workspace) pdfNavigation(c *ui.Context, p palette, count int, navH float32) {
	activeDoc, hasDoc := a.activePDFDocument()
	hasTOC := hasDoc && len(activeDoc.TOC) > 0
	touch := a.touchUI()
	btnH, navBtnSz, inputW, inputH, tocRowH := float32(26), float32(24), float32(34), float32(23), float32(30)
	if touch {
		btnH, navBtnSz, inputW, inputH, tocRowH = 34, 36, 46, 32, 38
	}
	ui.Row(c).Height(navH).PaddingX(8).Gap(6).AlignItems(ui.Center).Shrink(0).FillWidth().
		Background(p.surface).BorderColor(p.rule).BorderWidth(0, 0, 1, 0).Children(func() {
		if hasTOC {
			tocBtn := ui.ButtonBase(c.Key("pdf-toc-btn")).Height(btnH).PaddingX(10).Radius(7).Center().
				Gap(4).Shrink(0).Label("目录").Selected(a.pdfTOCOpen).FocusRingColor(p.accent).
				OnClick(func() { a.pdfTOCOpen = !a.pdfTOCOpen })
			fg := p.secondary
			if a.pdfTOCOpen {
				tocBtn.Background(p.tint).Border(1, p.tintBorder)
				fg = p.accent
			} else if tocBtn.Pressed() {
				tocBtn.Background(p.pressed).Border(1, p.border)
				fg = p.ink
			} else if tocBtn.Hovered() {
				tocBtn.Background(p.hover).Border(1, p.border)
				fg = p.ink
			} else {
				tocBtn.Background(p.elevated).Border(1, p.rule)
			}
			tocBtn.Children(func() {
				ui.Icon(c, icLayers).Size(12.5, 12.5).TextColor(fg)
				ui.Text(c, "目录").FontSize(12).FontWeight(550).TextColor(fg).SingleLine()
			})
			ui.PopoverBase(c.Key("pdf-toc-popover"), tocBtn, &a.pdfTOCOpen, func(panel ui.Element) {
				panel.Width(300).MaxHeight(400).Radius(10).Clip().
					Background(p.elevated).Border(1, p.border).
					InsetShadow(0, 1, 0, 0, p.specular).
					Shadow(0, 12, 32, -6, p.shadow)
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
								Label(entry.Title).FocusRingColor(p.accent).
								OnClick(func() {
									if entry.Page > 0 {
										a.setPDFPage(entry.Page - 1)
									}
									a.pdfTOCOpen = false
								})
							fg := p.secondary
							weight := 500
							if entry.Level == 0 {
								fg = p.ink
								weight = 600
							}
							if active {
								row.Background(p.tint)
								fg = p.accent
								weight = 600
							} else if row.Pressed() {
								row.Background(p.pressed)
								fg = p.ink
							} else if row.Hovered() {
								row.Background(p.hover)
								fg = p.ink
							}
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
			b.Children(func() { ui.Text(c, title+" ▾").SingleLine().FontSize(12).TextColor(p.secondary) })
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
		designToolbarButton(c, p, icArrowLeft, "上一页", navBtnSz, false, a.pdf.page == 0, func() { a.setPDFPage(a.pdf.page - 1) })
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
			if n, err := strconv.Atoi(strings.TrimSpace(a.pdf.jumpPageText)); err == nil && n > 0 && n <= count {
				a.setPDFPage(n - 1)
			} else {
				a.pdf.jumpPageText = pageNum
			}
		}
		ui.Text(c, fmt.Sprintf("/ %d", count)).SingleLine().FontSize(inputFont).TextColor(p.quiet).Shrink(0)
		designToolbarButton(c, p, icChevronRight, "下一页", navBtnSz, false, count == 0 || a.pdf.page+1 >= count, func() { a.setPDFPage(a.pdf.page + 1) })
		ui.Box(c).Grow(1).MinWidth(0)
	})
}

func (a *workspace) pdfSheet(c *ui.Context, p palette, width, height float32, ref pageRef, page renderedPDFPage) {
	d := &a.pdf
	image := page.light
	paper, marginPaper := ui.Hex("#ffffff"), ui.Hex("#f6f7fb")
	if p.dark && a.settings.PDFNight && page.night != nil {
		image = page.night
		paper, marginPaper = ui.Hex("#181818"), p.elevated
	}
	iw, ih := image.Size()
	aspect := float32(ih) / float32(max(1, iw))
	d.aspect = aspect
	ratio := float32(0)
	if d.marginOpen {
		if d.margin == 0 {
			d.margin = .32
		}
		ratio = d.margin
	}
	const pad = float32(16)
	a.checkShape(c)
	fitWidth := max(72, width-2*pad) / (1 + ratio)
	fitHeight := max(72, height-2*pad-24) / aspect
	baseFit := min(fitWidth, fitHeight)
	paperW := baseFit * max(1, d.zoom)
	paperH, addedW := paperW*aspect, paperW*ratio
	key := ref.document + ":" + strconv.Itoa(ref.page)
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
							d.scroll.X = max(0, r.X-32)
							d.scroll.Y = max(0, r.Y-48)
							break
						}
					}
				}
			}
		}
	}
	if d.focusHighlight && d.highlight != nil {
		h := d.highlight
		d.scroll.X = max(0, h.X*paperW-32)
		d.scroll.Y = max(0, h.Y*paperH-40)
		d.focusHighlight = false
	}
	ui.Box(c).Grow(1).MinHeight(0).FillWidth().Clip().Children(func() {
		horizontalPadding := max(pad, (width-paperW-addedW)/2)
		bottomPad := float32(70)
		if a.touchUI() && a.mode == pdfMode {
			bottomPad = 92
		}
		ui.ScrollBoth(c.Key("pdf-scroll")).Fill().Padding(pad, horizontalPadding, bottomPad).
			TrackScroll(&d.scroll).Label("PDF 页面").
			HandleInput(func(ev ui.InputEvent) bool {
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
				Shadow(0, 6, 16, -5, p.shadow).
				HandleInput(func(ev ui.InputEvent) bool {
					switch {
					case ev.Kind == ui.InputZoom && ev.Scale > 0:
						a.zoomPDFAt(ev.Scale, ev.X, ev.Y, width, pad, baseFit, ratio, ev.Time)
						return true
					case ev.Kind == ui.InputScroll && ev.Mods&(ui.Ctrl|ui.Super) != 0:
						a.zoomPDFAt(float32(math.Exp(float64(-ev.DY)/300)), ev.X, ev.Y, width, pad, baseFit, ratio, ev.Time)
						return true
					case ev.Kind == ui.InputPointerDown:
						d.lastZoomAt = time.Time{}
					}
					point := inkPoint{X: ev.X / paperW, Y: ev.Y / paperW, Pressure: ev.Pressure}
					accept := a.drawing.tool != excerptTool || ev.X <= paperW
					return a.inkInput(ev, key, point, paperW, accept)
				})
			if a.drawing.tool != panTool && a.drawing.tool != lassoTool {
				sheet.Cursor(ui.CursorCrosshair)
			}
			if a.drawing.tool == excerptTool && !a.splitDragging && !a.marginDragging {
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
				a.pdfPenZone(key, vis, rect.X, rect.Y, paperW, vp.Y+vp.H)
				a.drawing.drawInk(paint, vis, p, key, rect.X, rect.Y, paperW, &a.drawing.pdf)
				a.pdfHighlights(paint, p, original, ref, key)
			})
			if addedW > 0 {
				marginGripW := float32(7)
				if a.touchUI() {
					marginGripW = 20
				}
				sheet.Children(func() {
					grip := ui.Box(c.Key("pdf-margin-grip")).Absolute().Top(0).Bottom(0).Left(paperW-marginGripW/2).Width(marginGripW).
						Center().IgnoreInput(a.drawing.tool != panTool).
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
								}
							default:
								return false
							}
							return true
						})
					if a.touchUI() {
						grip.Children(func() {
							col := p.border
							if a.marginDragging {
								col = p.accent
							}
							ui.Box(c).Width(3.5).Height(36).Radius(2).Background(col)
						})
					}
				})
			}
		})
		if a.mode == pdfMode {
			a.inkDock(c, p, pdfMode, width)
		} else if width > 235 {
			hint := "手指导航 · 触控笔书写"
			if !a.stylusDrawsOnPDF() {
				hint = "手指导航 · 触控笔选择"
			}
			ui.Row(c).Absolute().Bottom(16).Left(16).Height(26).PaddingX(10).AlignItems(ui.Center).
				Radius(13).Background(p.elevated).Border(1, p.rule).Children(func() {
				ui.Text(c, hint).FontSize(11).TextColor(p.quiet).SingleLine()
			})
		}
		if a.pdfHighlightNoteID != "" {
			if hlNote, ok := a.tree.notes[a.pdfHighlightNoteID]; ok && noteHasPageHighlight(hlNote, ref.page+1) {
				clearH := float32(26)
				if a.touchUI() {
					clearH = 34
				}
				clearBtn := ui.ButtonBase(c.Key("pdf-clear-hl")).Absolute().Top(10).Right(14).
					Height(clearH).Padding(0, 10).Radius(99).AlignItems(ui.Center).Gap(5).
					Background(p.elevated).Border(1, p.tintBorder).
					Shadow(0, 2, 8, -2, p.shadowSoft).
					Label("清除高亮定位").FocusRingColor(p.accent).
					OnClick(func() {
						a.pdfHighlightNoteID = ""
						d.focusedHighlightNoteID = ""
					})
				if clearBtn.Hovered() {
					clearBtn.Background(p.tint)
				}
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
			amber := ui.Hex("#f59e0b")
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
	a.pdf.highlight, a.pdf.focusHighlight = &region, true
	a.setMode(splitMode)
	a.jumpToCard = card.ID
}

func (a *workspace) excerptPreview(c *ui.Context, p palette, region notebook.PDFRegion) {
	page, ok := a.requestPDF(pageRef{region.Document, region.Page}, pdfThumbnail, p.dark && a.settings.PDFNight)
	if !ok || page.err != nil {
		message := "正在载入摘录…"
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
