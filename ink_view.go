package main

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo/ui"
	"github.com/google/uuid"
)

type inkTool uint8

const (
	panTool inkTool = iota
	penTool
	markerTool
	strokeEraser
	partialEraser
	lassoTool
	excerptTool
)

type inkGesture struct {
	key         string
	tool        inkTool
	stroke      inkStroke
	lastMove    time.Time
	scale       float32
	shapeTried  bool
	regularized []inkPoint
}

type inkFrame struct {
	key           string
	revision      uint64
	ox, oy, scale float32
	dpi           float32
	width, height int
	dark          bool
}

type inkLayers struct {
	markers, pens *ui.Canvas
	frame         inkFrame
	bounds        strokeBoundsCache
	hadLiveMarker bool
}

type drawingState struct {
	data             notebook.Drawing
	loadErr          error
	saveFailed       bool
	undo             []notebook.Drawing
	redo             []notebook.Drawing
	gesture          inkGesture
	lasso            []inkPoint
	selected         []int
	tool             inkTool
	color            int
	penWidth         float32
	shapeOff         bool
	dockOpen         bool
	pdfDockCollapsed bool
	toolPrefs        bool
	penBrowseMode    bool
	strokeConsumed   bool
	canvas           inkLayers
	pdf              inkLayers
}

func (d *drawingState) canvasToolActive() bool {
	switch d.tool {
	case penTool, markerTool, strokeEraser, partialEraser, lassoTool:
		return true
	}
	return false
}

var penColors = [...]string{"", "#0A66FF", "#DF343B", "#149B79"}
var markerColors = [...]string{"#F4C545", "#56B4FF", "#FF7D97", "#36BF97"}
var penColorLabels = [...]string{"默认色", "钴蓝", "红色", "绿色"}
var markerColorLabels = [...]string{"暖黄", "天蓝", "樱粉", "薄荷绿"}
var penWidths = [...]float32{2, 3.5, 6}
var penWidthLabels = [...]string{"细", "中", "粗"}
var dockIcons = [2][8]*ui.SVG{
	{designIcon("lasso"), designIcon("pen"), designIcon("highlighter"), designIcon("eraser"), designIcon("excerpt"), designIcon("card"), designIcon("undo"), designIcon("redo")},
	{designIcon("lasso-dark"), designIcon("pen-dark"), designIcon("highlighter-dark"), designIcon("eraser-dark"), designIcon("excerpt-dark"), designIcon("card-dark"), designIcon("undo-dark"), designIcon("redo-dark")},
}

func (a *workspace) loadDrawing(bookID string) {
	a.drawing = drawingState{}
	if a.store == nil || bookID == "" {
		return
	}
	data, pending := a.drawingWrites.latest(bookID)
	if !pending {
		var err error
		data, err = a.store.ReadDrawing(bookID)
		if err != nil {
			a.drawing.loadErr = err
			a.showError(err)
			return
		}
	}
	a.drawing.data = data
	a.drawing.saveFailed = false
}

func (d *drawingState) strokes(key string) []inkStroke {
	if key == "" {
		return d.data.Ink
	}
	return d.data.Pages[key]
}

func (a *workspace) saveDrawing(next notebook.Drawing) bool {
	if err := a.drawing.loadErr; err != nil {
		a.showError(err)
		return false
	}
	if a.store == nil {
		return true
	}
	before := a.drawing.data
	if win := a.window(); win != nil {
		a.drawing.saveFailed = false
		a.drawingWrites.enqueue(a.store, drawingSave{bookID: a.bookID, before: before, after: next}, func(err error) {
			win.Update(func() {
				a.drawing.saveFailed = true
				a.showError(fmt.Errorf("无法保存手写记录：%w", err))
			})
		})
		return true
	}
	if err := a.store.SaveDrawingChange(a.bookID, before, next); err != nil {
		a.drawing.saveFailed = true
		a.showError(err)
		return false
	}
	a.drawing.saveFailed = false
	return true
}

func pushDrawing(stack []notebook.Drawing, data notebook.Drawing) []notebook.Drawing {
	const limit = 100
	if len(stack) >= limit {
		stack = append(stack[:0], stack[1:]...)
	}
	return append(stack, data)
}

func (a *workspace) commitDrawing(next notebook.Drawing) bool {
	if a.busy || a.bookID == "" {
		return false
	}
	if !a.saveDrawing(next) {
		return false
	}
	d := &a.drawing
	d.undo = pushDrawing(d.undo, d.data)
	d.data, d.redo = next, nil
	d.lasso, d.selected = nil, nil
	return true
}

func (a *workspace) commitStrokes(key string, next []inkStroke) {
	data := a.drawing.data
	if key == "" {
		data.Ink = next
	} else {
		pages := make(map[string][]inkStroke, len(data.Pages)+1)
		for page, ink := range data.Pages {
			pages[page] = ink
		}
		if len(next) > 0 {
			pages[key] = next
		} else {
			delete(pages, key)
		}
		data.Pages = pages
	}
	a.commitDrawing(data)
}

func (a *workspace) stepDrawing(undo bool) {
	if a.busy {
		return
	}
	d := &a.drawing
	from, to := &d.undo, &d.redo
	if !undo {
		from, to = to, from
	}
	if len(*from) == 0 {
		return
	}
	last := len(*from) - 1
	next := (*from)[last]
	if !a.saveDrawing(next) {
		return
	}
	prevData := d.data
	*to = pushDrawing(*to, d.data)
	(*from)[last] = notebook.Drawing{}
	*from = (*from)[:last]
	d.data = next
	d.lasso, d.selected = nil, nil
	if key := diffDrawingPageKey(prevData.Pages, next.Pages); key != "" {
		if colon := strings.LastIndex(key, ":"); colon > 0 {
			docID := key[:colon]
			if pageIdx, err := strconv.Atoi(key[colon+1:]); err == nil && pageIdx >= 0 {
				if docID == a.pdf.document && pageIdx != a.pdf.page && a.mode != canvasMode {
					a.setPDFPage(pageIdx)
					a.showMessage(fmt.Sprintf("已定位到第 %d 页手写更改", pageIdx+1), false)
				}
			}
		}
	}
}

func diffDrawingPageKey(before, after map[string][]inkStroke) string {
	for k, bs := range before {
		as := after[k]
		if !slices.EqualFunc(bs, as, inkStrokeEqual) {
			return k
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			return k
		}
	}
	return ""
}

func inkStrokeEqual(a, b inkStroke) bool {
	return a.Color == b.Color && a.Width == b.Width && a.Marker == b.Marker && slices.Equal(a.Points, b.Points)
}

func (a *workspace) inkInput(ev ui.InputEvent, key string, point inkPoint, scale float32, accept bool) bool {
	d := &a.drawing
	gesture := &d.gesture
	if ev.Pointer == ui.PointerTouch && len(gesture.stroke.Points) > 0 && gesture.tool != excerptTool && gesture.tool != lassoTool {
		return true
	}
	switch ev.Kind {
	case ui.InputPointerDown:
		isPenEraser := ev.Pointer == ui.PointerPen && ev.Button == 1
		if !accept || (ev.Button != 0 && !isPenEraser) || a.editing() || a.busy {
			return false
		}
		tool := d.tool
		if isPenEraser {
			if tool != partialEraser {
				tool = strokeEraser
			}
		} else if tool == panTool && ev.Pointer == ui.PointerPen && a.stylusAutoDraw() {
			tool = penTool
			d.tool = penTool
		}
		if tool == panTool || ev.Pointer == ui.PointerTouch && tool != excerptTool && tool != lassoTool ||
			key != "" && tool == lassoTool || key == "" && tool == excerptTool {
			return false
		}
		width := d.penWidth
		if width == 0 {
			width = 3.5
		}
		marker := tool == markerTool
		color := penColors[d.color]
		if marker {
			width *= 4
			color = markerColors[d.color]
		}
		d.dockOpen = d.dockOpen || key == ""
		d.lasso, d.selected = nil, nil
		if key == "" {
			a.cardMenuOpen = false
			d.strokeConsumed = true
		}
		width /= scale
		*gesture = inkGesture{key: key, tool: tool, lastMove: ev.Time, scale: scale,
			stroke: inkStroke{Points: []inkPoint{point}, Width: width, Color: color, Marker: marker}}
		return true
	case ui.InputPointerMove, ui.InputPointerUp:
		if len(gesture.stroke.Points) == 0 || gesture.key != key {
			return false
		}
		if ev.Canceled {
			*gesture = inkGesture{}
			return true
		}
		points := gesture.stroke.Points
		last := points[len(points)-1]
		if ev.Kind == ui.InputPointerMove {
			dist := distance(point, last) * scale
			if dist >= .75 || abs(point.Pressure-last.Pressure) > .08 {
				gesture.stroke.Points = append(points, point)
			}
			if dist >= 1.8 {
				gesture.lastMove, gesture.regularized, gesture.shapeTried = ev.Time, nil, false
			}
			return true
		}
		if distance(point, last)*scale > 4 {
			gesture.regularized = nil
		}
		if distance(point, last)*scale > 1 {
			point.Pressure = last.Pressure
			gesture.stroke.Points = append(points, point)
		}
		stroke, tool, regularized := gesture.stroke, gesture.tool, gesture.regularized
		*gesture = inkGesture{}
		switch tool {
		case penTool, markerTool:
			if len(regularized) > 0 {
				stroke.Points = regularized
			}
			a.commitStrokes(key, append(slices.Clone(d.strokes(key)), stroke))
		case strokeEraser, partialEraser:
			if next, changed := eraseStrokes(d.strokes(key), stroke.Points, 9/scale, tool == strokeEraser); changed {
				a.commitStrokes(key, next)
			}
		case lassoTool:
			a.selectInk(stroke.Points)
		case excerptTool:
			a.createExcerpt(inkBounds(stroke.Points))
		}
		return true
	}
	return false
}

func (a *workspace) checkShape(c *ui.Context) {
	d := &a.drawing
	g := &d.gesture
	if d.shapeOff || g.tool != penTool || len(g.stroke.Points) < 4 || g.shapeTried {
		return
	}
	wait := 470*time.Millisecond - c.Now().Sub(g.lastMove)
	if wait > 0 {
		c.After(wait)
		return
	}
	g.shapeTried = true
	points := slices.Clone(g.stroke.Points)
	for i := range points {
		points[i].X *= g.scale
		points[i].Y *= g.scale
	}
	g.regularized = recognizeShape(points)
	for i := range g.regularized {
		g.regularized[i].X /= g.scale
		g.regularized[i].Y /= g.scale
	}
}

func (a *workspace) selectInk(polygon []inkPoint) {
	if len(polygon) < 3 {
		return
	}
	d := &a.drawing
	for i, stroke := range d.data.Ink {
		if sampleStroke(stroke.Points, 6, func(p inkPoint) bool { return pointInLasso(p, polygon) }) {
			d.selected = append(d.selected, i)
		}
	}
	if len(d.selected) > 0 {
		d.lasso = polygon
	}
}

func (a *workspace) editLasso(action string) {
	d := &a.drawing
	if len(d.selected) == 0 {
		return
	}
	if action == "color" {
		d.color = (d.color + 1) % len(penColors)
	}
	next := d.data
	next.Ink = make([]inkStroke, 0, len(d.data.Ink))
	var cardInk []inkStroke
	var points []inkPoint
	for i, stroke := range d.data.Ink {
		if !slices.Contains(d.selected, i) {
			next.Ink = append(next.Ink, stroke)
			continue
		}
		switch action {
		case "card":
			cardInk = append(cardInk, stroke)
			points = append(points, stroke.Points...)
		case "color":
			stroke.Color = penColors[d.color]
			if stroke.Marker {
				stroke.Color = markerColors[d.color]
			}
			next.Ink = append(next.Ink, stroke)
		}
	}
	if action == "card" {
		box := inkBounds(points)
		card := notebook.CanvasCard{ID: uuid.NewString(), Title: "手写笔记", X: box.X - 14, Y: box.Y - 34,
			W: max(120, box.W+28), H: max(64, box.H+48), Ink: cardInk}
		next.Cards = append(slices.Clone(next.Cards), card)
	}
	if a.commitDrawing(next) && action == "card" {
		hapticSuccess()
	}
}

func (d *drawingState) drawInk(paint *ui.Painter, rect ui.Rect, p palette, key string, ox, oy, scale float32, layers *inkLayers) {
	strokes := d.strokes(key)
	gesture := d.gesture
	drawing := gesture.key == key && len(gesture.stroke.Points) > 0
	active := gesture.stroke
	if drawing && len(gesture.regularized) > 0 {
		active.Points = gesture.regularized
	}
	liveMarker := drawing && gesture.tool == markerTool
	layers.bounds.update(strokes)
	paintStrokes(paint, rect, p, strokes, active, liveMarker, key, ox, oy, scale, layers)
	if drawing && gesture.tool == penTool {
		paintStroke(paint, active, ox, oy, scale, strokeColor(active, p))
	}
	if gesture.key == key && len(gesture.stroke.Points) > 0 {
		switch gesture.tool {
		case lassoTool:
			paint.StrokePath(inkPath(gesture.stroke.Points, ox, oy, scale).Dash(0, 5, 4), 1.5, p.accent)
		case strokeEraser, partialEraser:
			tip := gesture.stroke.Points[len(gesture.stroke.Points)-1]
			paint.StrokePath((&ui.Path{}).Circle(ox+tip.X*scale, oy+tip.Y*scale, 9), 1, p.accent)
		}
	}
}

func paintStrokes(paint *ui.Painter, rect ui.Rect, p palette, strokes []inkStroke, active inkStroke, liveMarker bool, key string, ox, oy, scale float32, layers *inkLayers) {
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	dpi := min(paint.Scale(), 2, 3000/max(rect.W, rect.H))
	x, y := (ox-rect.X)*dpi, (oy-rect.Y)*dpi
	frame := inkFrame{key: key, revision: layers.bounds.revision, ox: x, oy: y, scale: scale * dpi, dpi: dpi, dark: p.dark,
		width: int(math.Ceil(float64(rect.W * dpi))), height: int(math.Ceil(float64(rect.H * dpi)))}
	updated := frame != layers.frame
	hasMarker, hasPen := liveMarker, false
	if updated {
		for _, stroke := range strokes {
			hasMarker = hasMarker || stroke.Marker
			hasPen = hasPen || !stroke.Marker
		}
		if !hasMarker {
			layers.markers = nil
		}
		if !hasPen {
			layers.pens = nil
		}
	} else {
		hasMarker, hasPen = liveMarker || layers.markers != nil, layers.pens != nil
	}
	drawLayer := func(layer **ui.Canvas, marker, live bool) {
		repaint := updated || live
		if *layer == nil {
			*layer = ui.NewCanvas(frame.width, frame.height)
			repaint = true
		} else if w, h := (*layer).Size(); w != frame.width || h != frame.height {
			*layer = ui.NewCanvas(frame.width, frame.height)
			repaint = true
		}
		if repaint {
			(*layer).Draw(paint, func(cp *ui.Painter) {
				for i, stroke := range strokes {
					if stroke.Marker == marker {
						paintBoundedStroke(cp, stroke, layers.bounds.boxes[i], x, y, frame.scale, strokeColor(stroke, p))
					}
				}
				if marker && liveMarker {
					paintStroke(cp, active, x, y, frame.scale, strokeColor(active, p))
				}
			})
		}
		options := ui.ImageOptions{}
		if marker {
			// Composite the opaque marker layer once so overlaps keep their opacity.
			options.Tint = ui.Hex("#ffffff").Alpha(.37)
		}
		paint.ImageRegion((*layer).Bitmap(), ui.Rect{}, rect, options)
	}
	if hasMarker {
		drawLayer(&layers.markers, true, liveMarker || layers.hadLiveMarker)
	}
	if hasPen {
		drawLayer(&layers.pens, false, false)
	}
	layers.frame, layers.hadLiveMarker = frame, liveMarker
}

func (a *workspace) inkDock(c *ui.Context, p palette, mode workspaceMode, width float32) {
	d := &a.drawing
	icons := dockIcons[0]
	if p.dark {
		icons = dockIcons[1]
	}
	if mode != canvasMode && d.pdfDockCollapsed && d.tool == panTool {
		dockBtnSize := float32(38)
		if a.touchUI() {
			dockBtnSize = 44
		}
		ui.Box(c).Absolute().Left(16).Bottom(16).ZIndex(4).Radius(99).Background(p.elevated).Border(1, p.border).
			Shadow(0, 4, 12, -2, p.shadow).Children(func() {
			designToolbarButton(c, p, icons[1], "打开笔盒", dockBtnSize, false, a.editing(), func() { d.pdfDockCollapsed = false })
		})
		return
	}
	touch := a.touchUI()
	size, btnH, barH, gap, padV, padH := float32(32), float32(32), float32(44), float32(4), float32(5), float32(7)
	closeSize := float32(26)
	if touch {
		size, btnH, barH, gap, padV, padH = 38, 38, 52, 5, 6, 9
		closeSize = 34
	}
	maxButtons := float32(10)
	if mode == pdfMode {
		maxButtons = 8
	}
	if avail := width - 24 - 2*padH - 12; avail > 0 && maxButtons*(size+gap) > avail {
		gap = 2
		if width < 360 {
			gap = 1
		}
		size = clamp((avail-maxButtons*gap)/maxButtons, 20, size)
		btnH = min(btnH, max(30, size))
		closeSize = min(closeSize, size)
	}
	ui.Column(c.Key("ink-dock")).Absolute().Bottom(16).Left(0).Right(0).ZIndex(4).
		AlignItems(ui.Center).Gap(10).Children(func() {
		if d.loadErr != nil {
			errPill := ui.ButtonBase(c.Key("ink-load-retry")).Height(28).PaddingX(12).Radius(14).Center().
				Background(p.dangerWash).Border(1, p.danger.Alpha(.35)).Label("手写读取异常 · 重试").
				OnClick(func() { a.loadDrawing(a.bookID) })
			errPill.Children(func() {
				ui.Text(c, "手写读取异常 · 重试").FontSize(11.5).FontWeight(550).TextColor(p.danger).SingleLine()
			})
		} else if d.saveFailed {
			savePill := ui.ButtonBase(c.Key("ink-save-retry")).Height(28).PaddingX(12).Radius(14).Center().
				Background(p.dangerWash).Border(1, p.danger.Alpha(.35)).Label("有未保存笔迹 · 重试").
				OnClick(func() { a.saveDrawing(d.data) })
			savePill.Children(func() {
				ui.Text(c, "有未保存笔迹 · 重试").FontSize(11.5).FontWeight(550).TextColor(p.danger).SingleLine()
			})
		}
		if d.toolPrefs {
			a.penOptions(c, p)
		}
		ui.Row(c).Height(barH).Padding(padV, padH).Gap(gap).Radius(14).AlignItems(ui.Center).
			Background(p.elevated).Border(1, p.border).Shadow(0, 5, 14, -3, p.shadow).Children(func() {
			a.inkButton(c, p, size, btnH, icPointer, "浏览与选择", d.tool == panTool, false, func() {
				d.tool, d.toolPrefs, d.penBrowseMode = panTool, false, true
				hapticSelection()
			})
			toolButton := func(tool inkTool, icon *ui.SVG, label string) {
				a.inkButton(c, p, size, btnH, icon, label, d.tool == tool, false, func() {
					d.penBrowseMode = false
					hapticSelection()
					if d.tool == tool {
						d.toolPrefs = !d.toolPrefs
						if d.toolPrefs && tool != penTool && tool != markerTool && tool != strokeEraser && tool != partialEraser {
							d.tool, d.toolPrefs, d.penBrowseMode = panTool, false, true
						}
					} else {
						d.tool, d.toolPrefs = tool, false
					}
				})
			}
			if mode != pdfMode {
				toolButton(lassoTool, icons[0], "套索")
			}
			toolButton(penTool, icons[1], "钢笔")
			toolButton(markerTool, icons[2], "荧光笔")
			a.inkButton(c, p, size, btnH, icons[3], "橡皮", d.tool == strokeEraser || d.tool == partialEraser, false, func() {
				d.penBrowseMode = false
				hapticSelection()
				if d.tool != strokeEraser && d.tool != partialEraser {
					d.tool, d.toolPrefs = strokeEraser, false
				} else {
					d.toolPrefs = !d.toolPrefs
				}
			})
			if mode != canvasMode {
				toolButton(excerptTool, icons[4], "框选摘录")
			}
			ui.Box(c).Width(1).Height(18).Background(p.rule)
			a.inkButton(c, p, size, btnH, icons[6], "撤销手写", false, len(d.undo) == 0, func() { a.stepDrawing(true) })
			a.inkButton(c, p, size, btnH, icons[7], "重做手写", false, len(d.redo) == 0, func() { a.stepDrawing(false) })
			designToolbarButton(c, p, icClose, "收起笔盒", closeSize, false, false, func() {
				if mode == canvasMode {
					d.dockOpen = false
				} else {
					d.pdfDockCollapsed = true
				}
				d.toolPrefs, d.tool = false, panTool
			})
		})
	})
}

func (a *workspace) inkButton(c *ui.Context, p palette, size, height float32, icon *ui.SVG, label string, active, disabled bool, action func()) {
	b := ui.ButtonBase(c.Key(label)).Size(size, height).Center().Shrink(0).Radius(8).Label(label).Tooltip(label).
		FocusRingColor(p.accent).Selected(active).Disabled(disabled).OnClick(action)
	fg := p.secondary
	if active {
		b.Background(p.tint).Border(1, p.tintBorder)
		fg = p.accent
	} else if b.Hovered() {
		b.Background(p.hover)
	}
	icSize := float32(16)
	if height >= 36 {
		icSize = 18
	}
	b.Children(func() { ui.Icon(c, icon).Size(icSize, icSize).TextColor(fg) })
}

func (a *workspace) penOptions(c *ui.Context, p palette) {
	d := &a.drawing
	touch := a.touchUI()
	if d.tool == strokeEraser || d.tool == partialEraser {
		segmentsSized(c, p, touch, []segment[inkTool]{{"整笔橡皮", strokeEraser}, {"局部橡皮", partialEraser}}, d.tool, func(value inkTool) { d.tool = value })
		return
	}
	if d.tool != penTool && d.tool != markerTool {
		return
	}
	colors := penColors
	labels := penColorLabels
	if d.tool == markerTool {
		colors = markerColors
		labels = markerColorLabels
	}
	barH, swBtnW, swBtnH, swDot, wBtnW, wBtnH := float32(30), float32(20), float32(24), float32(14), float32(22), float32(24)
	if touch {
		barH, swBtnW, swBtnH, swDot, wBtnW, wBtnH = 40, 30, 32, 18, 32, 32
	}
	ui.Row(c).Height(barH).PaddingX(10).Radius(barH / 2).Gap(5).AlignItems(ui.Center).Background(p.elevated).
		Border(1, p.border).Shadow(0, 4, 12, -2, p.shadow).Children(func() {
		for i, label := range labels {
			color := strokeColor(inkStroke{Color: colors[i]}, p)
			btn := ui.ButtonBase(c.Key(label)).Size(swBtnW, swBtnH).Center().Label(label).Selected(d.color == i).OnClick(func() {
				d.color = i
			})
			btn.Children(func() {
				sw := ui.Box(c).Size(swDot, swDot).Radius(swDot / 2).Background(color)
				if d.color == i {
					sw.Border(2, p.ink)
				}
			})
		}
		ui.Box(c).Width(1).Height(18).Background(p.rule)
		activeW := d.penWidth
		if activeW == 0 {
			activeW = 3.5
		}
		for i, width := range penWidths {
			selected := activeW == width
			btn := ui.ButtonBase(c.Key(width)).Size(wBtnW, wBtnH).Radius(7).Center().Label(penWidthLabels[i]).
				Selected(selected).OnClick(func() { d.penWidth = width })
			if selected {
				btn.Background(p.tint).Border(1, p.tintBorder)
			} else if btn.Hovered() {
				btn.Background(p.hover)
			}
			dotColor := p.quiet
			if selected {
				dotColor = p.accent
			}
			r := width + 2.5
			if touch {
				r += 1.5
			}
			btn.Children(func() { ui.Box(c).Size(r, r).Radius(r / 2).Background(dotColor) })
		}
		if d.tool == penTool {
			shapeH := float32(24)
			if touch {
				shapeH = 30
			}
			shapeBtn := settingButtonWithHeight(c, p, shapeH, "图形吸附", nil, false, false, func() {
				d.shapeOff = !d.shapeOff
			})
			if !d.shapeOff {
				shapeBtn.Background(p.tint).Border(1, p.tintBorder)
			}
			shapeBtn.Tooltip("画完停顿自动规整直线与几何图形")
		}
	})
}
