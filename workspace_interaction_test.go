package main

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestWorkbenchCanvasGesturesAndSettings(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	book := notebook.Book{ID: "book", Title: "微积分", Roots: []string{"note"},
		Notes: []notebook.Note{{ID: "note", Title: "收敛性", Body: "级数的收敛条件"}}}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)
	tester := ui.NewTester(app.view, 1280, 800)
	tester.Frame()
	click := func(s string) {
		t.Helper()
		if err := tester.Click(s); err != nil {
			t.Fatal(err)
		}
	}
	click("折叠侧边栏")
	if settings, err := app.store.ReadSettings(); err != nil || !settings.SidebarCollapsed {
		t.Fatalf("collapsed state was not saved: %+v, %v", settings, err)
	}
	click("设置")
	click("深色")
	if app.settings.Appearance != "dark" || !tester.HasText("笔记本管理") {
		t.Fatalf("settings did not render or apply: %+v", app.settings)
	}
	tester.Key(0, ui.KeyEscape)
	if app.preferencesOpen {
		t.Fatal("Escape did not close settings")
	}
	origin := app.panX
	tester.Press(150, 210)
	tester.Move(230, 255)
	tester.Release(230, 255)
	if app.panX < origin+75 {
		t.Fatalf("canvas did not pan: %g -> %g", origin, app.panX)
	}
	scale := app.scale
	tester.Pinch(250, 300, 1.25)
	if app.scale < scale*1.20 {
		t.Fatal("canvas did not zoom")
	}
	if err := tester.Click("收敛性"); err != nil || app.page != detail {
		t.Fatalf("canvas did not open the node: %v (page %v)", err, app.page)
	}
	tester.Key(0, ui.KeyEscape)

	tester.UsePointer(ui.PointerPen, .18)
	tester.Press(210, 240)
	tester.UsePointer(ui.PointerPen, .75)
	tester.Move(230, 244)
	tester.Move(250, 239)
	tester.Move(275, 243)
	tester.Advance(500 * time.Millisecond)
	if len(app.drawing.gesture.regularized) != 2 {
		t.Fatal("holding a near-straight stroke did not preview a line")
	}
	tester.Release(275, 243)
	data, err := app.store.ReadDrawing(book.ID)
	if err != nil || len(data.Ink) != 1 || len(data.Ink[0].Points) != 2 || data.Ink[0].Points[0].Pressure == 0 {
		t.Fatalf("vector stroke was not saved: %+v, %v", data, err)
	}
	tester.UsePointer(ui.PointerMouse, 0)
	click("撤销手写")
	if len(app.drawing.data.Ink) != 0 {
		t.Fatal("undo did not remove the stroke")
	}
	click("重做手写")
	if len(app.drawing.data.Ink) != 1 {
		t.Fatal("redo did not restore the stroke")
	}
}

func TestPDFExcerptBacklinkAndPageInk(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	book := notebook.Book{ID: "book", Title: "函数", Documents: []notebook.Document{{ID: "doc", Title: "函数"}}}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)
	const index = 2
	app.setPDFPage(index)
	pageImage := image.NewRGBA(image.Rect(0, 0, 450, 600))
	draw.Draw(pageImage, pageImage.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	page := ui.NewBitmap(pageImage)
	app.pdf.cache = map[pageRef]renderedPDFPage{
		{"doc", index}:     {light: page, night: page, count: 12},
		{"doc", index + 1}: {light: page, night: page, count: 12},
	}
	tester := ui.NewTester(app.view, 1280, 800)
	tester.Frame()
	click := func(s string) {
		t.Helper()
		if err := tester.Click(s); err != nil {
			t.Fatal(err)
		}
	}

	drag := func(x, y, x2, y2 float32) {
		tester.Press(x, y)
		tester.Move(x2, y2)
		tester.Release(x2, y2)
	}
	click("留白")
	click("折叠侧边栏")
	sheet, ok := tester.Find("PDF 原页与留白")
	if !ok {
		t.Fatal("PDF page sheet missing")
	}
	tester.UsePointer(ui.PointerPen, .55)
	drag(sheet.X+50, sheet.Y+100, sheet.X+135, sheet.Y+150)
	if len(app.drawing.data.Pages["doc:2"]) != 1 {
		t.Fatalf("PDF ink should belong to current page: %+v", app.drawing.data.Pages)
	}
	tester.UsePointer(ui.PointerMouse, 0)
	click("框选摘录")
	tester.FingerDown(sheet.X+88, sheet.Y+175)
	tester.FingerMove(sheet.X+185, sheet.Y+210)
	tester.FingerMove(sheet.X+280, sheet.Y+260)
	tester.FingerUp(sheet.X+280, sheet.Y+260)
	if len(app.drawing.data.Cards) != 1 || app.mode != splitMode {
		t.Fatalf("excerpt did not create a card and show split mode: %+v, %d", app.drawing.data.Cards, app.mode)
	}
	backlink := *app.drawing.data.Cards[0].Excerpt
	click("下一页")
	if app.pdf.page != index+1 || len(app.drawing.data.Pages["doc:3"]) != 0 {
		t.Fatal("page navigation did not isolate annotations")
	}
	click("P.3 ↗")
	if app.pdf.page != index || app.pdf.highlight == nil || *app.pdf.highlight != backlink {
		t.Fatalf("backlink did not restore the exact source: page=%d region=%+v", app.pdf.page, app.pdf.highlight)
	}
	grip, ok := tester.Find("拖动调整分屏宽度")
	if !ok {
		t.Fatal("split grip missing")
	}
	ratio := app.splitRatio
	gx, gy := grip.X+grip.W/2, grip.Y+grip.H/2
	tester.FingerDown(gx, gy)
	tester.FingerMove(gx+30, gy)
	tester.FingerMove(gx+65, gy)
	tester.FingerUp(gx+65, gy)
	if app.splitRatio <= ratio+.04 {
		t.Fatalf("split ratio did not follow drag: %v -> %v", ratio, app.splitRatio)
	}
}

func TestMarkerOverlapAndPartialEraser(t *testing.T) {
	first := inkStroke{Color: "#F4C545", Width: 14, Marker: true, Points: []inkPoint{{X: 25, Y: 50}, {X: 160, Y: 50}}}
	second := inkStroke{Color: first.Color, Width: 14, Marker: true, Points: []inkPoint{{X: 90, Y: 20}, {X: 90, Y: 82}}}
	data := drawingState{data: notebook.Drawing{Ink: []inkStroke{first, second}}}
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Fill().Background(ui.Hex("#ffffff")).Draw(func(p *ui.Painter, b ui.Rect) {
			data.drawInk(p, b, light, "", b.X, b.Y, 1, &data.canvas)
		})
	}, 180, 100)
	tester.Frame()
	pixels := tester.Image()
	single, overlap := pixels.RGBAAt(55, 50), pixels.RGBAAt(90, 50)
	if single == (color.RGBA{255, 255, 255, 255}) || single != overlap {
		t.Fatalf("marker opacity changed at overlap: single=%v overlap=%v", single, overlap)
	}
	split, changed := eraseStrokes([]inkStroke{first}, []inkPoint{{X: 90, Y: 35}, {X: 90, Y: 65}}, 8, false)
	if !changed || len(split) != 2 || split[0].Points[len(split[0].Points)-1].X >= 90 || split[1].Points[0].X <= 90 {
		t.Fatalf("partial eraser did not split a long segment: %+v", split)
	}
	whole, changed := eraseStrokes([]inkStroke{first}, []inkPoint{{X: 90, Y: 35}, {X: 90, Y: 65}}, 8, true)
	if !changed || len(whole) != 0 {
		t.Fatalf("whole-stroke eraser did not remove the stroke: %+v", whole)
	}
}

func TestLassoCardPreservesVectorInk(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	book := notebook.Book{ID: "book", Title: "数学"}
	initial := notebook.Drawing{Ink: []inkStroke{{Width: 3, Points: []inkPoint{{X: -20, Y: 50}, {X: 130, Y: 50}}}}}
	if err := app.store.SaveDrawing(book.ID, initial); err != nil {
		t.Fatal(err)
	}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)

	app.selectInk([]inkPoint{{X: 30, Y: 35}, {X: 70, Y: 35}, {X: 70, Y: 65}, {X: 30, Y: 65}})
	app.editLasso("card")
	saved, err := app.store.ReadDrawing(book.ID)
	if err != nil || len(saved.Ink) != 0 || len(saved.Cards) != 1 || len(saved.Cards[0].Ink) != 1 {
		t.Fatalf("lasso did not transfer vector ink into a card: %+v, %v", saved, err)
	}
}

func TestGraphLayoutModesAndMultiBranchExpansion(t *testing.T) {
	page14 := int64(14)
	colorBlue := 2
	colorHeader := 12
	colorRed := 7
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	book := notebook.Book{
		ID:    "book-layouts",
		Title: "高等数学解题方法",
		Roots: []string{"g1", "g2"},
		Notes: []notebook.Note{
			{ID: "g1", Title: "函数与导数", Color: &colorHeader, Style: 1, Children: []string{"n1", "n2"}},
			{ID: "g2", Title: "圆锥曲线", Color: &colorHeader, Style: 1, Children: []string{"n3"}},
			{ID: "n1", Title: "隐零点代换法", Body: "设而不求，将超越项代换为代数项\n第二步利用单调性消参", Color: &colorBlue, Style: 1, Page: &page14},
			{ID: "n2", Title: "极值点偏移", Body: "构造对称差函数证明单调性", Color: &colorRed, Style: 1},
			{ID: "n3", Title: "定比点差法", Body: "利用中点与斜率积消去参数", Color: &colorBlue, Style: 1},
		},
	}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)

	// Default layout is Rich Card Tree ("cards") and opens fully expanded (like MarginNote) with zero overlap.
	if app.settings.LayoutMode() != notebook.LayoutCards {
		t.Fatalf("expected default layout cards, got %q", app.settings.LayoutMode())
	}
	nodes, _, _ := app.mapLayout()
	seenIDs := map[string]bool{}
	for i, aNode := range nodes {
		seenIDs[aNode.id] = true
		for j := i + 1; j < len(nodes); j++ {
			bNode := nodes[j]
			overlapX := aNode.x < bNode.x+bNode.w && bNode.x < aNode.x+aNode.w
			overlapY := (aNode.y - aNode.h/2) < (bNode.y + bNode.h/2) && (bNode.y - bNode.h/2) < (aNode.y + aNode.h/2)
			if overlapX && overlapY {
				t.Fatalf("nodes %q and %q overlap in cards layout: %+v vs %+v", aNode.id, bNode.id, aNode, bNode)
			}
		}
	}
	if !seenIDs["n1"] || !seenIDs["n3"] {
		t.Fatalf("default full-tree expansion should keep both n1 and n3 visible: %+v", seenIDs)
	}

	// Collapsing and re-expanding an individual branch toggles only that branch.
	app.toggleBranch("g2")
	nodesAfterFold, _, _ := app.mapLayout()
	for _, n := range nodesAfterFold {
		if n.id == "n3" {
			t.Fatal("collapsing g2 should hide n3")
		}
	}
	app.toggleBranch("g2")

	// Switch across all 4 layout modes via Settings UI and verify non-intrusive card selection & persistence.
	tester := ui.NewTester(app.view, 1280, 800)
	tester.Frame()
	click := func(s string) {
		t.Helper()
		if err := tester.Click(s); err != nil {
			t.Fatal(err)
		}
	}

	// Single-clicking an unselected card selects it in-place and shows the floating action bar without opening modal.
	if !tester.HasText("利用中点与斜率积消去参数") {
		t.Fatal("cards layout should render full card body text inline")
	}
	click("定比点差法")
	if app.page != browse || !app.cardMenuOpen || app.selected != "n3" {
		t.Fatalf("first click on canvas card should select in-place with action bar: page=%v menu=%v sel=%q", app.page, app.cardMenuOpen, app.selected)
	}
	if !tester.HasText("详情") || !tester.HasText("子卡片") {
		t.Fatal("floating card action bar did not render expected actions")
	}
	click("详情")
	if app.page != detail {
		t.Fatalf("clicking 详情 in floating bar did not open detail view: page=%v", app.page)
	}
	tester.Key(0, ui.KeyEscape)

	click("全部笔记")
	click("设置")
	click("专题框架")
	if app.settings.LayoutMode() != notebook.LayoutBoard {
		t.Fatalf("expected board layout, got %q", app.settings.LayoutMode())
	}
	if _, sections, _ := app.mapLayout(); len(sections) != 2 {
		t.Fatalf("expected 2 board framework sections, got %d", len(sections))
	}
	tester.Key(0, ui.KeyEscape)
	click("定比点差法")
	if _, sections, _ := app.mapLayout(); len(sections) != 2 {
		t.Fatalf("clicking a card in board layout must not drop sibling sections, got %d", len(sections))
	}

	click("设置")
	click("大纲流")
	tester.Key(0, ui.KeyEscape)
	if app.settings.LayoutMode() != notebook.LayoutOutline {
		t.Fatalf("expected outline layout, got %q", app.settings.LayoutMode())
	}
	if !tester.HasText("设而不求，将超越项代换为代数项") {
		t.Fatal("outline stream did not render inline note preview")
	}
	click("隐零点代换法")
	if app.page != detail {
		t.Fatalf("clicking outline card did not open detail view: page=%v", app.page)
	}
	tester.Key(0, ui.KeyEscape)

	click("设置")
	click("精简树")
	saved, err := app.store.ReadSettings()
	if err != nil || saved.GraphLayout != notebook.LayoutCompact {
		t.Fatalf("graph layout did not persist: %+v, %v", saved, err)
	}
}

func TestTwoDimensionalMultiColumnAndZoomControls(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	book := notebook.Book{
		ID:    "book-2d",
		Title: "方法论",
		Roots: []string{"root"},
		Notes: []notebook.Note{
			{ID: "root", Title: "总框架", Children: []string{"b1"}},
			{ID: "b1", Title: "重点专题", Children: []string{"c1", "c2", "c3", "c4"}},
			{ID: "c1", Title: "卡片一", Body: "第一条内容"},
			{ID: "c2", Title: "卡片二", Body: "第二条内容"},
			{ID: "c3", Title: "卡片三", Body: "第三条内容"},
			{ID: "c4", Title: "卡片四", Body: "第四条内容"},
		},
	}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)

	// Cards mode packs 4 leaf siblings into a 2D multi-column group (multiple distinct X columns, zero overlap).
	nodes, groups, _ := app.mapLayout()
	xs := map[int]bool{}
	for _, n := range nodes {
		if n.inGroup {
			xs[int(n.x)] = true
		}
	}
	if len(groups) != 1 || len(xs) < 2 {
		t.Fatalf("expected 4 leaf cards packed into >=2 columns with 1 leaf-group box, got groups=%d cols=%d", len(groups), len(xs))
	}

	// Board mode also lays out the 4 cards across 2 columns inside the nested framework container.
	app.settings.GraphLayout = notebook.LayoutBoard
	bNodes, bSecs, _ := app.mapLayout()
	bXs := map[int]bool{}
	for _, n := range bNodes {
		bXs[int(n.x)] = true
	}
	if len(bSecs) != 1 || len(bXs) < 2 {
		t.Fatalf("expected board framework to pack 4 cards across >=2 columns, got secs=%d cols=%d", len(bSecs), len(bXs))
	}

	// Verify zoom capsule buttons: zoom in, reset to 100%, zoom out, fit view.
	tester := ui.NewTester(app.view, 1280, 800)
	tester.Frame()
	if err := tester.Click("重置为 100%"); err != nil {
		t.Fatal(err)
	}
	if abs(app.scale-1) > .01 {
		t.Fatalf("reset button did not restore 100%% scale: got %v", app.scale)
	}
	if err := tester.Click("放大"); err != nil {
		t.Fatal(err)
	}
	if app.scale <= 1.1 {
		t.Fatalf("zoom in button did not increase scale: got %v", app.scale)
	}
	if err := tester.Click("适应视图"); err != nil {
		t.Fatal(err)
	}
	if app.scale <= 0 || app.scale > 1.01 {
		t.Fatalf("fit view button produced invalid scale: %v", app.scale)
	}

	// Clicking a framework container header selects it in-place and reveals the floating action bar.
	if err := tester.Click("框架 重点专题"); err != nil {
		t.Fatal(err)
	}
	if app.selected != "b1" || !app.cardMenuOpen || app.page != browse {
		t.Fatalf("clicking framework header did not select in-place: sel=%q menu=%v page=%v", app.selected, app.cardMenuOpen, app.page)
	}

	// Pinch-zooming & dragging starting ON a card both work cleanly without opening detail view.
	if cardRect, ok := tester.Find("卡片一"); ok {
		cx, cy := cardRect.X+cardRect.W/2, cardRect.Y+cardRect.H/2
		prevScale := app.scale
		tester.Pinch(cx, cy, 1.2)
		if app.scale <= prevScale*1.1 {
			t.Fatalf("pinch-zooming directly over a card did not zoom canvas: %v -> %v", prevScale, app.scale)
		}
		prevPan := app.panX
		tester.Press(cx, cy)
		tester.Move(cx+60, cy+20)
		tester.Release(cx+60, cy+20)
		if app.panX < prevPan+50 {
			t.Fatalf("dragging starting on a card did not pan canvas: %v -> %v", prevPan, app.panX)
		}
		if app.page != browse {
			t.Fatalf("dragging on a card should not open detail view: page=%v", app.page)
		}
	}

	// Clicking the framework fold pill collapses b1 in-place (not stolen by viewport & no fake "核心卡片"),
	// and clicking the collapsed card's expand pill expands it right back!
	if err := tester.Click("折叠框架重点专题"); err != nil {
		t.Fatal(err)
	}
	_, collapsedSecs, _ := app.mapLayout()
	if app.expanded["b1"] {
		t.Fatal("clicking framework fold pill did not collapse b1")
	}
	for _, s := range collapsedSecs {
		if s.title == "核心卡片" {
			t.Fatalf("collapsing top-level framework must not move it into 核心卡片: %+v", collapsedSecs)
		}
	}
	if err := tester.Click("展开或折叠重点专题"); err != nil {
		t.Fatal(err)
	}
	if !app.expanded["b1"] {
		t.Fatal("clicking collapsed card fold pill did not re-expand b1")
	}
}

func TestFullCardExpansionAndRichSpans(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	app.settings.GraphLayout = notebook.LayoutBoard
	app.settings.SidebarCollapsed = true

	var longLines []string
	for i := 1; i <= 22; i++ {
		longLines = append(longLines, "示例要点与考点详解第"+string(rune('A'+i-1))+"条内容")
	}
	longLines = append(longLines, "尾行重点标记-完全展开不可截断")
	longBody := ""
	for i, ln := range longLines {
		if i > 0 {
			longBody += "\n"
		}
		longBody += ln
	}
	richText := "普通说明" + "红字关键词" + "蓝字例题" + "\n" + longBody

	book := notebook.Book{
		ID:    "rich-book",
		Title: "方法论",
		Roots: []string{"root"},
		Notes: []notebook.Note{
			{ID: "root", Title: "方法论", Children: []string{"sec"}},
			{ID: "sec", Title: "重点专题", Children: []string{"n1"}},
			{
				ID:    "n1",
				Title: "彩色完整卡片",
				Body:  richText,
				Blocks: []notebook.ContentBlock{{
					Text: richText,
					Spans: []notebook.TextSpan{
						{Text: "普通说明"},
						{Text: "红字关键词", Color: "#ff0000", Bold: true},
						{Text: "蓝字例题", Color: "#2000ff", Underline: true},
						{Text: "\n" + longBody},
					},
				}},
			},
		},
	}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)
	app.scale = 0.85
	app.canvasReady = true
	app.recenter = false
	nodes, sections, _ := app.mapLayout()
	world := layoutBounds(nodes, sections)
	app.panX = mapInset - world.X*app.scale
	app.panY = mapInset - world.Y*app.scale

	tester := ui.NewTester(app.view, 1280, 1200)
	tester.Frame()

	if tester.HasText("再次点击查看全文") {
		t.Fatal("card must never show truncation prompt")
	}
	if !tester.HasText("红字关键词") || !tester.HasText("尾行重点标记-完全展开不可截断") {
		t.Fatalf("expected full rich card text including final line to render on canvas; texts=%v", tester.Texts())
	}

	// Verify selecting a card causes ZERO inner layout shift or text re-wrapping (border thickness stays invariant).
	app.selected = "sec"
	app.activeTrail = app.tree.trail("sec")
	tester.Frame()
	beforeImg := tester.Image()
	cardRect, ok := tester.Find("彩色完整卡片")
	if !ok {
		t.Fatal("could not locate card before selection")
	}
	// Click card to select it in-place.
	if err := tester.Click("彩色完整卡片"); err != nil {
		t.Fatalf("select card: %v", err)
	}
	if app.selected != "n1" || app.page != browse {
		t.Fatalf("expected in-place selection of n1, got sel=%q page=%v", app.selected, app.page)
	}
	afterImg := tester.Image()
	// Every pixel inside the card body interior (below header, inside border) must remain pixel-identical.
	x0 := int(cardRect.X) + 8
	x1 := int(cardRect.X+cardRect.W) - 8
	y0 := int(cardRect.Y) + 42
	y1 := int(cardRect.Y+cardRect.H) - 8
	diffPixels := 0
	for py := y0; py < y1; py++ {
		for px := x0; px < x1; px++ {
			if beforeImg.RGBAAt(px, py) != afterImg.RGBAAt(px, py) {
				diffPixels++
			}
		}
	}
	if diffPixels != 0 {
		t.Fatalf("selecting card shifted or re-wrapped inner text: %d body pixels changed", diffPixels)
	}

	// Verify Outline mode also renders full rich text spans cleanly.
	app.settings.GraphLayout = notebook.LayoutOutline
	tester.Frame()
	if !tester.HasText("红字关键词") || !tester.HasText("尾行重点标记-完全展开不可截断") {
		t.Fatal("outline mode did not render rich text block")
	}

	// Verify Detail focus reader renders rich text block cleanly.
	app.selected = "n1"
	app.page = detail
	tester.Frame()
	if !tester.HasText("蓝字例题") || !tester.HasText("尾行重点标记-完全展开不可截断") {
		t.Fatal("detail reader did not render rich text block")
	}
}

func TestMultiBlockCommentsTagsLinksAndMetadata(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	app.settings.GraphLayout = notebook.LayoutBoard
	app.settings.SidebarCollapsed = true

	blk1 := "第一段核心摘录与定义说明"
	blk2 := "第二段补充评论与注意事项"
	fullBody := blk1 + "\n\n" + blk2
	book := notebook.Book{
		ID:    "meta-book",
		Title: "通用学习集",
		Roots: []string{"root"},
		Notes: []notebook.Note{
			{ID: "root", Title: "通用学习集", Children: []string{"sec"}},
			{ID: "sec", Title: "专题一", Children: []string{"n1", "n2"}},
			{
				ID:        "n1",
				Title:     "多评论卡片",
				Body:      fullBody,
				BookTitle: "示例参考书",
				Tags:      []string{"重点", "定理"},
				Links:     []string{"n2"},
				UpdatedAt: 1763370300,
				Blocks: []notebook.ContentBlock{
					{Text: blk1},
					{Text: blk2},
				},
			},
			{ID: "n2", Title: "关联定理卡片", Body: "被双向链接引用的卡片内容"},
		},
	}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)
	app.scale = 1
	app.canvasReady = true
	app.recenter = true

	tester := ui.NewTester(app.view, 1280, 900)
	tester.Frame()

	// Both comment blocks render completely in Canvas mode (no artificial quiz toggle).
	if !tester.HasText(blk1) || !tester.HasText(blk2) {
		t.Fatalf("expected all comment blocks visible on canvas; texts=%v", tester.Texts())
	}
	if tester.HasText("刷题模式") || tester.HasText("显示答案与解析") {
		t.Fatal("generic MarginNote viewer should not inject file-specific quiz mode controls")
	}

	// Verify Outline mode displays both blocks and hashtags.
	app.settings.GraphLayout = notebook.LayoutOutline
	tester.Frame()
	if !tester.HasText(blk1) || !tester.HasText(blk2) || !tester.HasText("#重点") {
		t.Fatalf("outline mode missing multi-block content or tags; texts=%v", tester.Texts())
	}

	// Verify Detail Focus Reader displays book title, tags, cross-note link, and navigates on link click.
	app.selected = "n1"
	app.page = detail
	tester.Frame()
	if !tester.HasText("示例参考书") || !tester.HasText("#定理") || !tester.HasText("关联定理卡片") {
		t.Fatalf("detail reader missing book title, tag, or linked note chip; texts=%v", tester.Texts())
	}
	if err := tester.Click("跳转到关联笔记 关联定理卡片"); err != nil {
		t.Fatalf("click linked note chip: %v", err)
	}
	if app.selected != "n2" {
		t.Fatalf("expected clicking linked note chip to jump to n2, got %q", app.selected)
	}
}

func TestHybridBoardMiniTreeLayout(t *testing.T) {
	frameMode := 100
	treeMode := 0
	colorBlue := 2
	colorGreen := 5
	colorCoral := 7
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	app.settings.GraphLayout = notebook.LayoutBoard
	app.settings.SidebarCollapsed = true

	book := notebook.Book{
		ID:    "hybrid-book",
		Title: "数项级数",
		Roots: []string{"root"},
		Notes: []notebook.Note{
			{ID: "root", Title: "常数项级数", Children: []string{"sec"}},
			{ID: "sec", Title: "任意项级数", LayoutMode: &frameMode, Color: &colorBlue, Children: []string{"sib", "branch"}},
			{ID: "sib", Title: "交错级数莱布尼茨判别法", Body: "单调递减趋于零则收敛", Color: &colorBlue},
			{ID: "branch", Title: "绝对值相加", LayoutMode: &treeMode, Color: &colorGreen, Children: []string{"c1", "c2"}},
			{ID: "c1", Title: "收敛", Body: "原级数绝对收敛", Color: &colorGreen},
			{ID: "c2", Title: "发散", Body: "再判断原级数是否条件收敛", Color: &colorCoral},
		},
	}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)
	app.scale = 1
	app.canvasReady = true
	app.recenter = true

	nodes, sections, _ := app.mapLayout()
	if len(sections) != 1 || sections[0].id != "sec" {
		t.Fatalf("expected outer framework section 'sec' only (mini-tree should not create a framework box), got %+v", sections)
	}
	idxByID := make(map[string]int, len(nodes))
	for i, n := range nodes {
		idxByID[n.id] = i
	}
	bIdx, okB := idxByID["branch"]
	c1Idx, okC1 := idxByID["c1"]
	c2Idx, okC2 := idxByID["c2"]
	if !okB || !okC1 || !okC2 {
		t.Fatalf("missing mini-tree nodes in board layout: %+v", idxByID)
	}
	if nodes[bIdx].parent != -1 {
		t.Fatalf("mini-tree root should have parent=-1, got %d", nodes[bIdx].parent)
	}
	if nodes[c1Idx].parent != bIdx || nodes[c2Idx].parent != bIdx {
		t.Fatalf("mini-tree children must link to branch parent %d for bezier edges: c1=%d c2=%d", bIdx, nodes[c1Idx].parent, nodes[c2Idx].parent)
	}
	if nodes[c1Idx].x <= nodes[bIdx].x+nodes[bIdx].w || nodes[c2Idx].x <= nodes[bIdx].x+nodes[bIdx].w {
		t.Fatalf("mini-tree children should be positioned horizontally right of parent: branch=%+v c1=%+v c2=%+v", nodes[bIdx], nodes[c1Idx], nodes[c2Idx])
	}
	// Verify zero overlap across all rendered cards.
	for i, aNode := range nodes {
		for j := i + 1; j < len(nodes); j++ {
			bNode := nodes[j]
			overlapX := aNode.x < bNode.x+bNode.w && bNode.x < aNode.x+aNode.w
			overlapY := (aNode.y - aNode.h/2) < (bNode.y + bNode.h/2) && (bNode.y - bNode.h/2) < (aNode.y + aNode.h/2)
			if overlapX && overlapY {
				t.Fatalf("hybrid board nodes %q and %q overlap: %+v vs %+v", aNode.id, bNode.id, aNode, bNode)
			}
		}
	}

	tester := ui.NewTester(app.view, 1280, 800)
	tester.Frame()
	if !tester.HasText("绝对值相加") || !tester.HasText("原级数绝对收敛") || !tester.HasText("再判断原级数是否条件收敛") {
		t.Fatalf("hybrid mini-tree cards did not render expected text; texts=%v", tester.Texts())
	}

	// Verify bezier edge pixels actually draw in the corridor between branch right edge and child left edge.
	bRect, okBR := tester.Find("绝对值相加")
	c1Rect, okC1R := tester.Find("收敛")
	if !okBR || !okC1R {
		t.Fatalf("could not locate mini-tree card rects: branch=%v c1=%v", okBR, okC1R)
	}
	img := tester.Image()
	bgPixel := img.RGBAAt(int(bRect.X+bRect.W+4), int(bRect.Y-2))
	edgePixels := 0
	xStart := int(bRect.X + bRect.W + 6)
	xEnd := int(c1Rect.X - 6)
	yStart := int(min(bRect.Y, c1Rect.Y))
	yEnd := int(max(bRect.Y+bRect.H, c1Rect.Y+c1Rect.H))
	for py := yStart; py <= yEnd; py++ {
		for px := xStart; px <= xEnd; px++ {
			if img.RGBAAt(px, py) != bgPixel {
				edgePixels++
			}
		}
	}
	if edgePixels == 0 {
		t.Fatalf("expected bezier edges drawn between mini-tree parent (%+v) and children (%+v), found 0 edge pixels", bRect, c1Rect)
	}
}

func TestClickablePDFPageJumpAndHighlight(t *testing.T) {
	page3 := int64(3)
	colorBlue := 2
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	app.settings.GraphLayout = notebook.LayoutBoard
	app.settings.SidebarCollapsed = true

	book := notebook.Book{
		ID:    "pdf-jump-book",
		Title: "数项级数笔记",
		Documents: []notebook.Document{{
			ID:       "doc-1",
			Title:    "高等数学讲义",
			LastPage: 1,
			TOC: []notebook.TOCEntry{
				{Title: "第一章 函数与极限", Page: 1, Level: 0},
				{Title: "第三节 常数项级数", Page: 3, Level: 1},
			},
		}},
		Roots: []string{"root"},
		Notes: []notebook.Note{
			{ID: "root", Title: "常数项级数", Children: []string{"sec"}},
			{ID: "sec", Title: "核心公式", Children: []string{"n1"}},
			{
				ID:        "n1",
				Title:     "比值判别法",
				Body:      "若后项比前项极限小于1则收敛",
				Page:      &page3,
				BookTitle: "高等数学讲义",
				Color:     &colorBlue,
				Tags:      []string{"高频考点"},
				HighlightRects: []notebook.HighlightRect{
					{Page: 3, X: 78.47, Y: 180.36, W: 172.95, H: 89.92},
				},
			},
		},
	}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)
	app.scale = 1
	app.canvasReady = true
	app.recenter = true

	pageImg := image.NewRGBA(image.Rect(0, 0, 595, 842))
	draw.Draw(pageImg, pageImg.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	bmp := ui.NewBitmap(pageImg)
	app.pdf.cache = map[pageRef]renderedPDFPage{
		{"doc-1", 0}: {light: bmp, night: bmp, count: 6},
		{"doc-1", 2}: {light: bmp, night: bmp, count: 6},
	}

	tester := ui.NewTester(app.view, 1280, 800)
	tester.Frame()

	// Click the P.3 ↗ badge directly on the canvas card.
	if err := tester.Click("P.3 ↗"); err != nil {
		t.Fatalf("click canvas P.3 ↗ badge: %v", err)
	}
	if app.pdf.page != 2 {
		t.Fatalf("expected 0-based pdf page=2 (P.3), got %d", app.pdf.page)
	}
	if app.pdfHighlightNoteID != "n1" {
		t.Fatalf("expected pdfHighlightNoteID=n1, got %q", app.pdfHighlightNoteID)
	}
	if app.mode != splitMode {
		t.Fatalf("expected jumpToNotePDF to switch canvasMode -> splitMode, got %v", app.mode)
	}
	if app.page != browse {
		t.Fatalf("expected clicking P.3 ↗ badge not to open detail modal, got page=%v", app.page)
	}
	if !tester.HasText("清除高亮定位") && !tester.HasText("比值判别法") {
		t.Fatal("expected PDF highlight clear pill after jump")
	}

	// Clicking clear-highlight pill clears pdfHighlightNoteID.
	if err := tester.Click("清除高亮定位"); err != nil {
		t.Fatalf("click 清除高亮定位: %v", err)
	}
	if app.pdfHighlightNoteID != "" {
		t.Fatalf("expected pdfHighlightNoteID cleared, got %q", app.pdfHighlightNoteID)
	}

	// Open PDF TOC popover and jump to page 1.
	if err := tester.Click("目录"); err != nil {
		t.Fatalf("click 目录: %v", err)
	}
	if !app.pdfTOCOpen || !tester.HasText("文档目录") {
		t.Fatal("expected PDF TOC popover to open")
	}
	if err := tester.Click("第一章 函数与极限"); err != nil {
		t.Fatalf("click TOC entry: %v", err)
	}
	if app.pdf.page != 0 || app.pdfTOCOpen {
		t.Fatalf("TOC jump failed: page=%d tocOpen=%v", app.pdf.page, app.pdfTOCOpen)
	}

	// Open Detail focus reader and verify clickable "第 3 页 ↗" closes modal & jumps back to page 3 with highlight.
	app.openNote("n1")
	tester.Frame()
	if app.page != detail {
		t.Fatalf("expected detail modal open, got %v", app.page)
	}
	if !tester.HasText("高等数学讲义") || !tester.HasText("#高频考点") {
		t.Fatalf("expected BookTitle and #tag pill in detail reader; texts=%v", tester.Texts())
	}
	if err := tester.Click("第 3 页 ↗"); err != nil {
		t.Fatalf("click detail 第 3 页 ↗: %v", err)
	}
	if app.page != browse || app.pdf.page != 2 || app.pdfHighlightNoteID != "n1" {
		t.Fatalf("detail PDF jump failed: pageScreen=%v pdfPage=%d hl=%q", app.page, app.pdf.page, app.pdfHighlightNoteID)
	}

	// Safe behavior when a notebook has no PDF documents attached.
	noDocApp := &workspace{store: notebook.NewStore(t.TempDir())}
	noDocApp.setLibrary(notebook.Library{Books: []notebook.Book{{
		ID:    "no-pdf",
		Title: "纯笔记",
		Roots: []string{"x1"},
		Notes: []notebook.Note{{ID: "x1", Title: "无原文", Page: &page3}},
	}}}, "no-pdf")
	noDocApp.jumpToNotePDF(noDocApp.tree.notes["x1"])
	if noDocApp.pdfHighlightNoteID != "" || noDocApp.message == "" {
		t.Fatalf("expected safe toast when no PDF attached: hl=%q msg=%q", noDocApp.pdfHighlightNoteID, noDocApp.message)
	}
}

func TestDrawAndEraseDirectlyOverCardsWithPenAndMouse(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	app.settings.GraphLayout = notebook.LayoutBoard
	app.settings.SidebarCollapsed = true

	book := notebook.Book{
		ID:    "ink-over-cards",
		Title: "手写标注测试",
		Roots: []string{"root"},
		Notes: []notebook.Note{
			{ID: "root", Title: "手写标注测试", Children: []string{"sec"}},
			{ID: "sec", Title: "专题框架", Children: []string{"c1"}},
			{ID: "c1", Title: "可涂写卡片", Body: "卡片正文区域\n支持直接在卡片上方批注与荧光标记"},
		},
	}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)
	app.scale = 1
	app.canvasReady = true
	app.recenter = true

	tester := ui.NewTester(app.view, 1280, 800)
	tester.Frame()

	cardRect, ok := tester.Find("可涂写卡片")
	if !ok {
		t.Fatal("could not locate card on canvas")
	}
	beforeImg := tester.Image()

	// 1. In browse (panTool) mode, stylus (PointerPen) directly on the card draws immediately ON TOP of the card.
	cx := cardRect.X + cardRect.W*0.25
	cy := cardRect.Y + cardRect.H*0.65
	tester.UsePointer(ui.PointerPen, 0.7)
	tester.Press(cx, cy)
	tester.Move(cx+40, cy+4)
	tester.Move(cx+90, cy+8)
	tester.Release(cx+90, cy+8)
	tester.Frame()

	if len(app.drawing.data.Ink) != 1 {
		t.Fatalf("expected 1 stylus stroke drawn directly over card, got %d", len(app.drawing.data.Ink))
	}
	if app.page != browse {
		t.Fatalf("drawing on card must not open detail view, got page=%v", app.page)
	}

	// Verify ink pixels actually rendered ON TOP of the opaque card interior (DrawOver, not buried behind card).
	afterImg := tester.Image()
	changed := 0
	for py := int(cy) - 8; py <= int(cy) + 16; py++ {
		for px := int(cx) + 4; px <= int(cx) + 86; px++ {
			if beforeImg.RGBAAt(px, py) != afterImg.RGBAAt(px, py) {
				changed++
			}
		}
	}
	if changed < 20 {
		t.Fatalf("expected visible ink pixels drawn on top of card interior, got only %d changed pixels", changed)
	}

	// 2. Stylus barrel / tail eraser (PointerPen + Button 1) erases the stroke directly on top of the card.
	tester.PressButton(1, cx+20, cy+2)
	tester.Move(cx+60, cy+6)
	tester.ReleaseButton(1, cx+60, cy+6)
	tester.Frame()
	if len(app.drawing.data.Ink) != 0 {
		t.Fatalf("expected stylus eraser button to erase stroke over card, got %d remaining", len(app.drawing.data.Ink))
	}

	// 3. With mouse + penTool selected, drawing across the card also paints directly over the card without clicking it.
	tester.UsePointer(ui.PointerMouse, 0)
	app.drawing.dockOpen = true
	app.drawing.tool = penTool
	tester.Frame()
	tester.Press(cx, cy)
	tester.Move(cx+75, cy+10)
	tester.Release(cx+75, cy+10)
	tester.Frame()
	if len(app.drawing.data.Ink) != 1 || app.page != browse {
		t.Fatalf("expected mouse penTool stroke directly over card without opening card: ink=%d page=%v", len(app.drawing.data.Ink), app.page)
	}

	// 4. Cmd+Z / Cmd+Shift+Z undo & redo ink strokes cleanly.
	tester.Key(ui.Cmd, ui.KeyZ)
	if len(app.drawing.data.Ink) != 0 {
		t.Fatalf("expected Cmd+Z to undo stroke, got %d strokes", len(app.drawing.data.Ink))
	}
	tester.Key(ui.Cmd|ui.Shift, ui.KeyZ)
	if len(app.drawing.data.Ink) != 1 {
		t.Fatalf("expected Cmd+Shift+Z to redo stroke, got %d strokes", len(app.drawing.data.Ink))
	}

	// 5. Clicking "浏览与选择" in inkDock (or pressing Esc) exits penTool back to panTool cleanly.
	if err := tester.Click("浏览与选择"); err != nil {
		t.Fatalf("click 浏览与选择: %v", err)
	}
	if app.drawing.tool != panTool {
		t.Fatalf("expected 浏览与选择 to switch back to panTool, got %v", app.drawing.tool)
	}
	app.drawing.tool = markerTool
	tester.Frame()
	tester.Key(0, ui.KeyEscape)
	if app.drawing.tool != panTool {
		t.Fatalf("expected Escape to exit markerTool back to panTool, got %v", app.drawing.tool)
	}

	// 6. Editing an excerpt-only note prefills draftBody with Excerpt instead of blank.
	app.tree.notes["ex1"] = notebook.Note{ID: "ex1", Title: "纯摘录卡", Excerpt: "原始PDF摘录文本"}
	app.selected = "ex1"
	app.startEditNote()
	if app.draftBody != "原始PDF摘录文本" {
		t.Fatalf("expected startEditNote to prefill Excerpt for excerpt-only note, got %q", app.draftBody)
	}
}

func TestAndroidTabletStylusBrowseAndMultiTouchGestures(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir()), forceTouchUI: true}
	book := notebook.Book{
		ID:    "tablet-book",
		Title: "平板适配测试",
		Roots: []string{"root"},
		Notes: []notebook.Note{
			{ID: "root", Title: "总览", Children: []string{"c1", "c2"}},
			{ID: "c1", Title: "重点卡片一", Body: "用于测试触控笔浏览与手势"},
			{ID: "c2", Title: "重点卡片二", Body: "用于测试防误触与返回键"},
		},
	}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)
	app.scale = 1
	app.canvasReady = true
	app.recenter = true

	tester := ui.NewTester(app.view, 1280, 800)
	tester.Frame()

	// 1. Default state: instant stylus draw is enabled.
	if !app.stylusDrawsOnCanvas() || !app.stylusDrawsOnPDF() {
		t.Fatal("expected default stylus auto-draw to be enabled")
	}

	// Open dock and click "浏览与选择": stylus switches cleanly into pointer/browse mode.
	if err := tester.Click("打开笔盒"); err != nil {
		t.Fatalf("open ink dock: %v", err)
	}
	if err := tester.Click("浏览与选择"); err != nil {
		t.Fatalf("click 浏览与选择: %v", err)
	}
	if app.stylusDrawsOnCanvas() || app.stylusDrawsOnPDF() {
		t.Fatal("expected 浏览与选择 in inkDock to put stylus into pointer/browse mode")
	}
	// With stylus in browse mode, tapping a card selects it instead of drawing ink.
	card2, ok := tester.Find("重点卡片二")
	if !ok {
		t.Fatal("missing card 重点卡片二")
	}
	tester.UsePointer(ui.PointerPen, 0.5)
	tester.Press(card2.X+card2.W/2, card2.Y+card2.H/2)
	tester.Release(card2.X+card2.W/2, card2.Y+card2.H/2)
	tester.Frame()
	if len(app.drawing.data.Ink) != 0 || app.selected != "c2" {
		t.Fatalf("expected stylus tap in browse mode to select card c2 without drawing ink: ink=%d sel=%q", len(app.drawing.data.Ink), app.selected)
	}

	// 2. Toggle "触控笔提笔即写" OFF in Settings and verify persistence.
	tester.UsePointer(ui.PointerMouse, 0)
	if err := tester.Click("设置"); err != nil {
		t.Fatalf("open settings: %v", err)
	}
	if err := tester.Click("切换触控笔提笔即写"); err != nil {
		t.Fatalf("toggle 切换触控笔提笔即写: %v", err)
	}
	if saved, err := app.store.ReadSettings(); err != nil || !saved.PenTapToSelect {
		t.Fatalf("expected PenTapToSelect=true persisted, got %+v err=%v", saved, err)
	}
	tester.Key(0, ui.KeyBack)
	if app.preferencesOpen {
		t.Fatal("expected Android KeyBack to close settings")
	}
	if err := tester.Click("收起笔盒"); err != nil {
		t.Fatalf("close dock: %v", err)
	}
	if app.stylusDrawsOnCanvas() {
		t.Fatal("with PenTapToSelect enabled and dock closed, stylus must stay in browse mode")
	}

	// 3. Simultaneous two-finger pinch + midpoint pan on canvas.
	panXBefore, panYBefore := app.panX, app.panY
	tester.Pinch(500, 350, 1.02)
	tester.Advance(16 * time.Millisecond)
	tester.Pinch(560, 395, 1.02)
	if app.panX < panXBefore+30 || app.panY < panYBefore+20 {
		t.Fatalf("two-finger midpoint shift did not pan canvas: (%g,%g) -> (%g,%g)", panXBefore, panYBefore, app.panX, app.panY)
	}

	// 4. Finger pan/tap over a card while penTool is active must NOT select/open the card.
	app.drawing.dockOpen = true
	app.drawing.tool = penTool
	app.selected = "c1"
	app.page = browse
	tester.Frame()
	c2Rect, ok := tester.Find("重点卡片二")
	if !ok {
		t.Fatal("missing card 重点卡片二 during penTool")
	}
	tester.FingerDown(c2Rect.X+20, c2Rect.Y+20)
	tester.FingerMove(c2Rect.X+42, c2Rect.Y+22)
	tester.FingerUp(c2Rect.X+42, c2Rect.Y+22)
	tester.Frame()
	if app.selected != "c1" || app.page != browse {
		t.Fatalf("finger pan while penTool active must not select/open card: sel=%q page=%v", app.selected, app.page)
	}

	// 5. Android KeyBack exits penTool, clears lasso, and cancels editing cleanly.
	tester.Key(0, ui.KeyBack)
	if app.drawing.tool != panTool {
		t.Fatalf("expected Android KeyBack to exit penTool, got %v", app.drawing.tool)
	}
	app.drawing.lasso = []inkPoint{{X: 0, Y: 0}, {X: 20, Y: 0}, {X: 20, Y: 20}}
	app.drawing.selected = []int{0}
	tester.Frame()
	tester.Key(0, ui.KeyBack)
	if len(app.drawing.selected) != 0 || len(app.drawing.lasso) != 0 {
		t.Fatal("expected Android KeyBack to clear lasso selection")
	}
	app.startEditNote()
	tester.Frame()
	tester.Key(0, ui.KeyBack)
	if app.editing() {
		t.Fatal("expected Android KeyBack to cancel note editing")
	}
}

func TestAndroidTabletPortraitSplitAndTouchTargets(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir()), forceTouchUI: true}
	book := notebook.Book{
		ID:        "tablet-split",
		Title:     "高等数学",
		Roots:     []string{"n1"},
		Notes:     []notebook.Note{{ID: "n1", Title: "泰勒公式", Body: "拉格朗日余项与佩亚诺余项"}},
		Documents: []notebook.Document{{ID: "doc1", Title: "教材 PDF"}},
	}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)
	pageImage := image.NewRGBA(image.Rect(0, 0, 600, 800))
	draw.Draw(pageImage, pageImage.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	page := ui.NewBitmap(pageImage)
	app.pdf.cache = map[pageRef]renderedPDFPage{{"doc1", 0}: {light: page, night: page, count: 10}}

	// 1. Portrait 11" Android tablet (800x1280): entering Split Mode auto-hides sidebar transiently
	// and uses top/bottom vertical split with a touch-sized draggable horizontal divider.
	tester := ui.NewTester(app.view, 800, 1280)
	tester.Frame()
	if err := tester.Click("分屏"); err != nil {
		t.Fatalf("enter split mode on portrait tablet: %v", err)
	}
	if !app.sidebarHiddenForSplit || app.settings.SidebarCollapsed {
		t.Fatalf("expected transient sidebar hide on narrow portrait split without persisting SidebarCollapsed: hidden=%v persisted=%v",
			app.sidebarHiddenForSplit, app.settings.SidebarCollapsed)
	}
	vGrip, ok := tester.Find("拖动调整分屏高度")
	if !ok {
		t.Fatal("expected portrait tablet split mode to render vertical split grip 拖动调整分屏高度")
	}
	if vGrip.H < 14 {
		t.Fatalf("expected touch-friendly vertical split grip height >= 14dp, got %g", vGrip.H)
	}
	ratioBefore := app.splitRatio
	gx, gy := vGrip.X+vGrip.W/2, vGrip.Y+vGrip.H/2
	tester.FingerDown(gx, gy)
	tester.FingerMove(gx, gy+55)
	tester.FingerMove(gx, gy+110)
	tester.FingerUp(gx, gy+110)
	if app.splitRatio <= ratioBefore+0.05 {
		t.Fatalf("vertical split ratio did not follow drag: %v -> %v", ratioBefore, app.splitRatio)
	}

	// Verify touch-friendly PDF navigation & margin grip sizes.
	nextBtn, ok := tester.Find("下一页")
	if !ok || nextBtn.W < 34 || nextBtn.H < 34 {
		t.Fatalf("expected touch-friendly PDF 下一页 button >= 34dp, got ok=%v rect=%+v", ok, nextBtn)
	}
	if err := tester.Click("留白"); err != nil {
		t.Fatalf("open margin: %v", err)
	}
	mGrip, ok := tester.Find("拖动调整留白宽度")
	if !ok || mGrip.W < 18 {
		t.Fatalf("expected touch-friendly PDF margin grip >= 18dp, got ok=%v rect=%+v", ok, mGrip)
	}

	// Verify focal-point pinch zoom on PDF keeps the pinched lower-right corner in view by advancing scroll.
	sheet, ok := tester.Find("PDF 原页与留白")
	if !ok {
		t.Fatal("PDF sheet missing in portrait split")
	}
	tester.Pinch(sheet.X+sheet.W*0.8, sheet.Y+sheet.H*0.8, 2.2)
	if app.pdf.zoom < 2.0 || app.pdf.scroll.Y <= 0 {
		t.Fatalf("expected focal-point PDF pinch zoom to scale and scroll toward pinched point: zoom=%g scroll=%+v", app.pdf.zoom, app.pdf.scroll)
	}

	// Clicking 展开侧边栏 restores sidebar cleanly without mutating persisted SidebarCollapsed.
	if err := tester.Click("展开侧边栏"); err != nil {
		t.Fatalf("expand sidebar: %v", err)
	}
	if app.sidebarHiddenForSplit || app.settings.SidebarCollapsed {
		t.Fatalf("expected 展开侧边栏 to restore sidebar cleanly: hidden=%v persisted=%v", app.sidebarHiddenForSplit, app.settings.SidebarCollapsed)
	}

	// 2. Soft keyboard open (bottom inset 380dp): note editor modal stays inside safe area so 保存 button is reachable.
	tester.SetInsets(28, 0, 380, 0)
	app.page = detail
	app.startEditNote()
	tester.Frame()
	saveBtn, ok := tester.Find("保存")
	if !ok {
		t.Fatal("保存 button missing when soft keyboard insets active")
	}
	if saveBtn.Y+saveBtn.H > 1280-380 {
		t.Fatalf("保存 button clipped behind soft keyboard: bottom=%g > safeBottom=%d", saveBtn.Y+saveBtn.H, 1280-380)
	}

	// 3. Landscape 11" Android tablet (1280x800): uses horizontal side-by-side split with 16dp grip.
	land := ui.NewTester(app.view, 1280, 800)
	app.editMode = editNone
	app.page = browse
	app.mode = splitMode
	land.Frame()
	hGrip, ok := land.Find("拖动调整分屏宽度")
	if !ok || hGrip.W < 14 {
		t.Fatalf("expected landscape tablet horizontal split grip >= 14dp, got ok=%v rect=%+v", ok, hGrip)
	}
}

func TestSettingsAboutGitHubAndUpdateFlows(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	book := notebook.Book{
		ID:    "about-book",
		Title: "关于测试",
		Roots: []string{"n1"},
		Notes: []notebook.Note{{ID: "n1", Title: "节点一", Body: "内容"}},
	}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)

	tester := ui.NewTester(app.view, 1280, 800)
	tester.Frame()

	scrollSettingsToBottom := func() {
		// Settings card is centered inside the wide document pane (right of 240px sidebar).
		// Scroll down generously so the bottom "关于 Folio" group is always inside the viewport.
		tester.Scroll(760, 400, 0, 1200)
		tester.Frame()
	}

	waitUpdate := func(want updatePhase) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if app.updateSnapshot().phase == want {
				tester.Frame()
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for update phase %d (got %+v)", want, app.updateSnapshot())
	}

	// 1. Open Settings and verify "关于 Folio" group, current version, and buttons.
	if err := tester.Click("设置"); err != nil {
		t.Fatalf("open settings: %v", err)
	}
	scrollSettingsToBottom()
	wantVer := "当前版本 v" + currentAppVersion()
	for _, want := range []string{"关于 Folio", "版本与更新", wantVer, "检查更新", "开源仓库", "github.com/dongyu-will/folio", "打开 GitHub"} {
		if !tester.HasText(want) {
			t.Fatalf("expected %q in Settings About section; texts=%v", want, tester.Texts())
		}
	}

	// 2. Click "打开 GitHub" and verify OpenURL("https://github.com/dongyu-will/folio").
	if err := tester.Click("打开 GitHub"); err != nil {
		t.Fatalf("click 打开 GitHub: %v", err)
	}
	if !slices.Contains(tester.OpenedURLs(), folioRepoURL) {
		t.Fatalf("expected OpenURL(%q), got %v", folioRepoURL, tester.OpenedURLs())
	}

	// Verify OpenURL failure sets a friendly Chinese error status message.
	tester.FailOpenURL(errors.New("no browser"))
	if err := tester.Click("打开 GitHub"); err != nil {
		t.Fatalf("click 打开 GitHub (fail case): %v", err)
	}
	tester.Frame()
	if !app.messageError || !strings.Contains(app.message, "无法打开链接") {
		t.Fatalf("expected friendly URL open failure status, got err=%v msg=%q", app.messageError, app.message)
	}
	tester.FailOpenURL(nil)

	// 3. Up-to-date check (also verifying in-flight "正在检查更新…" state & duplicate click guard).
	releaseCheck := make(chan struct{})
	checkCalls := 0
	app.checkUpdateFn = func(ctx context.Context) (*updateInfo, error) {
		checkCalls++
		<-releaseCheck
		return nil, nil
	}
	if err := tester.Click("检查更新"); err != nil {
		t.Fatalf("click 检查更新 (up-to-date): %v", err)
	}
	if !tester.HasText("正在检查更新…") {
		t.Fatalf("expected in-flight checking state, texts=%v", tester.Texts())
	}
	// Disabled button should not re-trigger another concurrent check.
	app.checkForUpdates(ui.Services{})
	close(releaseCheck)
	waitUpdate(updateUpToDate)
	if checkCalls != 1 {
		t.Fatalf("expected exactly 1 check invocation while in flight, got %d", checkCalls)
	}
	if !tester.HasText("已是最新版本") {
		t.Fatalf("expected 已是最新版本, texts=%v", tester.Texts())
	}

	// 4. No published release yet (errNoPublishedRelease).
	app.checkUpdateFn = func(ctx context.Context) (*updateInfo, error) {
		return nil, errNoPublishedRelease
	}
	if err := tester.Click("检查更新"); err != nil {
		t.Fatalf("click 检查更新 (no release): %v", err)
	}
	waitUpdate(updateNoRelease)
	if !tester.HasText("暂无已发布版本") {
		t.Fatalf("expected 暂无已发布版本, texts=%v", tester.Texts())
	}

	// 5. Update available with release URL only (showing version, sanitized notes, 前往发布页 + 重新检查).
	relURL := "https://github.com/dongyu-will/folio/releases/tag/v0.3.0"
	app.checkUpdateFn = func(ctx context.Context) (*updateInfo, error) {
		return &updateInfo{
			Version:    "0.3.0",
			Notes:      "## 更新亮点\n- **新增**应用内[更新检查](https://github.com/dongyu-will/folio)\n- 优化手写流畅度",
			ReleaseURL: relURL,
		}, nil
	}
	if err := tester.Click("检查更新"); err != nil {
		t.Fatalf("click 检查更新 (available release-only): %v", err)
	}
	waitUpdate(updateAvailable)
	scrollSettingsToBottom()
	for _, want := range []string{"发现新版本 v0.3.0", "• 新增应用内更新检查", "前往发布页", "重新检查"} {
		if !tester.HasText(want) {
			t.Fatalf("expected %q in release-only update UI; texts=%v", want, tester.Texts())
		}
	}
	if err := tester.Click("前往发布页"); err != nil {
		t.Fatalf("click 前往发布页: %v", err)
	}
	if !slices.Contains(tester.OpenedURLs(), relURL) {
		t.Fatalf("expected OpenURL(%q), got %v", relURL, tester.OpenedURLs())
	}

	// 6. Direct macOS mygo.Updater flow: available -> downloading progress -> relaunch required -> 立即重启.
	app.checkUpdateFn = func(ctx context.Context) (*updateInfo, error) {
		return &updateInfo{
			Version:    "0.4.0",
			Notes:      "支持增量签名更新",
			ReleaseURL: folioReleasesURL,
			UsingMyGo:  true,
		}, nil
	}
	if err := tester.Click("重新检查"); err != nil {
		t.Fatalf("click 重新检查 (UsingMyGo): %v", err)
	}
	waitUpdate(updateAvailable)
	scrollSettingsToBottom()
	if !tester.HasText("发现新版本 v0.4.0") || !tester.HasText("下载并更新") {
		t.Fatalf("expected macOS direct install UI, texts=%v", tester.Texts())
	}

	progressGate := make(chan struct{})
	app.installUpdateFn = func(ctx context.Context, info *updateInfo, progress func(int64, int64)) (updateInstallResult, error) {
		progress(50, 100)
		<-progressGate
		return updateInstallResult{RelaunchRequired: true}, nil
	}
	relaunchCount := 0
	app.relaunchFn = func() { relaunchCount++ }
	if err := tester.Click("下载并更新"); err != nil {
		t.Fatalf("click 下载并更新: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := app.updateSnapshot(); st.phase == updateDownloading && st.downloaded == 50 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	tester.Frame()
	if !tester.HasText("正在下载更新… 50%") || !tester.HasText("下载中…") {
		t.Fatalf("expected 50%% download progress UI, texts=%v", tester.Texts())
	}
	close(progressGate)
	waitUpdate(updateRelaunchRequired)
	scrollSettingsToBottom()
	if !tester.HasText("更新已就绪，重启后生效") || !tester.HasText("立即重启") {
		t.Fatalf("expected relaunch UI, texts=%v", tester.Texts())
	}
	if err := tester.Click("立即重启"); err != nil {
		t.Fatalf("click 立即重启: %v", err)
	}
	if relaunchCount != 1 {
		t.Fatalf("expected relaunchFn called once, got %d", relaunchCount)
	}

	// 7. Android permission-required & installer-launched UI states.
	app.updateMu.Lock()
	app.update = updateUIState{
		phase: updateAvailable,
		info:  &updateInfo{Version: "0.4.0", ReleaseURL: folioReleasesURL, UsingMyGo: true},
	}
	app.updateMu.Unlock()
	app.installUpdateFn = func(ctx context.Context, info *updateInfo, progress func(int64, int64)) (updateInstallResult, error) {
		return updateInstallResult{PermissionRequired: true}, nil
	}
	tester.Frame()
	scrollSettingsToBottom()
	if err := tester.Click("下载并更新"); err != nil {
		t.Fatalf("click 下载并更新 (permission flow): %v", err)
	}
	waitUpdate(updatePermissionRequired)
	scrollSettingsToBottom()
	if !tester.HasText("请在系统设置中允许 Folio 安装未知来源应用，授权后再次点击「安装更新」") || !tester.HasText("安装更新") {
		t.Fatalf("expected Android permission guidance UI, texts=%v", tester.Texts())
	}
	app.installUpdateFn = func(ctx context.Context, info *updateInfo, progress func(int64, int64)) (updateInstallResult, error) {
		return updateInstallResult{}, nil
	}
	if err := tester.Click("安装更新"); err != nil {
		t.Fatalf("click 安装更新: %v", err)
	}
	waitUpdate(updateInstallerLaunched)
	scrollSettingsToBottom()
	if !tester.HasText("已打开系统安装器，请按提示完成更新") {
		t.Fatalf("expected installer launched text, texts=%v", tester.Texts())
	}

	// 8. Error state during update check.
	app.checkUpdateFn = func(ctx context.Context) (*updateInfo, error) {
		return nil, errors.New("检查更新失败：网络连接中断")
	}
	if err := tester.Click("重新检查"); err != nil {
		t.Fatalf("click 重新检查 (error state): %v", err)
	}
	waitUpdate(updateError)
	scrollSettingsToBottom()
	if !tester.HasText("检查更新失败：网络连接中断") || !tester.HasText("检查更新") {
		t.Fatalf("expected error state and 检查更新 retry button, texts=%v", tester.Texts())
	}
}

func TestSettingsAboutCompactAndHelpers(t *testing.T) {
	if got := formatUpdateProgress(0, 0); got != "正在下载更新…" {
		t.Fatalf("unexpected zero progress: %q", got)
	}
	if got := formatUpdateProgress(512, 0); got != "正在下载更新… 512 B" {
		t.Fatalf("unexpected byte progress: %q", got)
	}
	if got := formatUpdateProgress(128<<10, 0); got != "正在下载更新… 128 KB" {
		t.Fatalf("unexpected KB progress: %q", got)
	}
	if got := formatUpdateProgress(3*(1<<20)+512*(1<<10), 0); got != "正在下载更新… 3.5 MB" {
		t.Fatalf("unexpected MB progress: %q", got)
	}
	if got := formatUpdateProgress(120, 100); got != "正在下载更新… 100%" {
		t.Fatalf("unexpected clamped percent progress: %q", got)
	}

	notes := formatReleaseNotesPreview("## v0.3.0\n---\n- **第一项** `说明`\n- [第二项](https://example.com)\n- 第三项\n- 第四项\n- 第五项不应出现")
	if !strings.Contains(notes, "• 第一项 说明") || !strings.Contains(notes, "• 第二项") || strings.Contains(notes, "第五项") || !strings.HasSuffix(notes, "…") {
		t.Fatalf("unexpected formatted release notes: %q", notes)
	}

	// Verify compact Settings modal layout (< 540px card width) renders and handles clicks cleanly.
	app := &workspace{store: notebook.NewStore(t.TempDir()), forceTouchUI: true}
	app.openPreferences()
	tester := ui.NewTester(app.view, 480, 800)
	tester.Frame()
	tester.Scroll(240, 400, 0, 2000)
	tester.Frame()
	if !tester.HasText("关于 Folio") || !tester.HasText("打开 GitHub") {
		t.Fatalf("compact settings missing About section; texts=%v", tester.Texts())
	}
	if err := tester.Click("打开 GitHub"); err != nil {
		t.Fatalf("compact settings click 打开 GitHub: %v", err)
	}
	if !slices.Contains(tester.OpenedURLs(), folioRepoURL) {
		t.Fatalf("compact settings did not open repo URL: %v", tester.OpenedURLs())
	}
}

func TestAppearanceThemeAndRootBackgroundMatchesShell(t *testing.T) {
	if got := windowBackgroundForAppearance("light"); got != "#f5f5f7" {
		t.Fatalf("light background = %q, want #f5f5f7", got)
	}
	if got := windowBackgroundForAppearance("dark"); got != "#0e0f12" {
		t.Fatalf("dark background = %q, want #0e0f12", got)
	}
	if got := windowBackgroundForAppearance(""); got != "light-dark(#f5f5f7, #0e0f12)" {
		t.Fatalf("system background = %q, want light-dark(#f5f5f7, #0e0f12)", got)
	}
	if got := themeSourceForAppearance("light"); got != mygo.ThemeLight {
		t.Fatalf("light source = %q, want %q", got, mygo.ThemeLight)
	}
	if got := themeSourceForAppearance("dark"); got != mygo.ThemeDark {
		t.Fatalf("dark source = %q, want %q", got, mygo.ThemeDark)
	}
	if got := themeSourceForAppearance(""); got != mygo.ThemeSystem {
		t.Fatalf("system source = %q, want %q", got, mygo.ThemeSystem)
	}

	app := &workspace{store: notebook.NewStore(t.TempDir())}
	tester := ui.NewTester(app.view, 400, 800)
	// Safe-area insets expose the root element's background along top and bottom bars.
	tester.SetInsets(40, 0, 28, 0)

	wantRGBA := func(c ui.Color) color.RGBA {
		return color.RGBA{R: c.R, G: c.G, B: c.B, A: c.A}
	}

	// System appearance (light in default tester) must still match light.shell, not #ffffff.
	app.settings.Appearance = ""
	tester.Frame()
	if got := tester.Image().RGBAAt(16, 12); got != wantRGBA(light.shell) {
		t.Fatalf("system top safe-area pixel = %v, want light.shell %v", got, wantRGBA(light.shell))
	}

	// Explicit light mode.
	app.settings.Appearance = "light"
	tester.Frame()
	img := tester.Image()
	if got := img.RGBAAt(16, 12); got != wantRGBA(light.shell) {
		t.Fatalf("light top safe-area pixel = %v, want %v", got, wantRGBA(light.shell))
	}
	if got := img.RGBAAt(16, 788); got != wantRGBA(light.shell) {
		t.Fatalf("light bottom safe-area pixel = %v, want %v", got, wantRGBA(light.shell))
	}

	// Explicit dark mode.
	app.settings.Appearance = "dark"
	tester.Frame()
	img = tester.Image()
	if got := img.RGBAAt(16, 12); got != wantRGBA(dark.shell) {
		t.Fatalf("dark top safe-area pixel = %v, want %v", got, wantRGBA(dark.shell))
	}
	if got := img.RGBAAt(16, 788); got != wantRGBA(dark.shell) {
		t.Fatalf("dark bottom safe-area pixel = %v, want %v", got, wantRGBA(dark.shell))
	}
}

func TestStartupFlushImportsDoesNotFlickerBusyWhenEmpty(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	uiCh := make(chan func(), 16)
	app.postUI = func(fn func()) { uiCh <- fn }

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	app.takeDownloadFn = func(ctx context.Context) (*pendingAndroidDownload, error) {
		close(started)
		<-release
		defer close(done)
		return nil, nil
	}

	app.notifyDownloadsReady()
	select {
	case fn := <-uiCh:
		fn()
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial flushImports UI callback")
	}
	<-started
	if app.busy {
		t.Fatal("empty startup download poll must not set busy=true while polling")
	}
	close(release)
	<-done

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		app.importMu.Lock()
		polling := app.downloadsPolling
		app.importMu.Unlock()
		if !polling {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if app.busy {
		t.Fatal("empty startup download poll must leave busy=false after completion")
	}
	select {
	case <-uiCh:
		t.Fatal("empty download poll should not trigger an extra UI redraw")
	default:
	}

	// When "take" returns an actual file path, import runs under busy=true and cleans up the temp file.
	tmpDir, err := os.MkdirTemp(t.TempDir(), "margin-open-")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile := filepath.Join(tmpDir, "bad.txt")
	if err := os.WriteFile(tmpFile, []byte("not-a-notebook"), 0o600); err != nil {
		t.Fatal(err)
	}
	takeDone := make(chan struct{})
	takeCount := 0
	app.takeDownloadFn = func(ctx context.Context) (*pendingAndroidDownload, error) {
		takeCount++
		if takeCount == 1 {
			return &pendingAndroidDownload{Path: tmpFile}, nil
		}
		if takeCount == 2 {
			defer close(takeDone)
		}
		return nil, nil
	}

	app.notifyDownloadsReady()
	sawBusy := false
	finishDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(finishDeadline) {
		select {
		case fn := <-uiCh:
			fn()
			if app.busy {
				sawBusy = true
			}
		case <-time.After(5 * time.Millisecond):
			app.importMu.Lock()
			polling := app.downloadsPolling
			pending := app.downloadsPending
			app.importMu.Unlock()
			if sawBusy && !app.busy && !polling && !pending {
				goto imported
			}
		}
	}
imported:
	select {
	case <-takeDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for follow-up download queue drain")
	}
	if !sawBusy {
		t.Fatal("expected real file import from Android download queue to set busy=true")
	}
	if app.busy {
		t.Fatal("expected busy to clear after file import completes")
	}
	if _, err := os.Stat(tmpFile); !os.IsNotExist(err) {
		t.Fatalf("expected Android staged temp file to be cleaned up, stat err=%v", err)
	}
}

func TestAndroidFrameReadySignaledOncePerLaunchAndResume(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	readyCount := 0
	app.androidFrameReadyFn = func() { readyCount++ }
	app.androidFramePending.Store(true)

	tester := ui.NewTester(app.view, 400, 800)
	tester.Frame()
	tester.Frame()
	tester.Frame()
	if readyCount != 1 {
		t.Fatalf("cold-start frameReady count = %d, want 1", readyCount)
	}

	app.onAndroidSurfaceResumed(nil)
	tester.Frame()
	tester.Frame()
	if readyCount != 2 {
		t.Fatalf("post-resume frameReady count = %d, want 2", readyCount)
	}
}

func TestAutoCheckUpdatesThrottleSilentAndBanner(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	book := notebook.Book{
		ID:    "auto-update-book",
		Title: "自动更新测试",
		Roots: []string{"n1"},
		Notes: []notebook.Note{{ID: "n1", Title: "节点一", Body: "内容"}},
	}
	app.setLibrary(notebook.Library{Books: []notebook.Book{book}}, book.ID)

	// Verify sidebar displays dynamic currentAppVersion() and has no update dot/banner initially.
	tester := ui.NewTester(app.view, 1280, 800)
	tester.Frame()
	wantSidebarVer := "v" + currentAppVersion()
	if !tester.HasText(wantSidebarVer) {
		t.Fatalf("expected sidebar version %q, texts=%v", wantSidebarVer, tester.Texts())
	}
	if _, ok := tester.Find("有可用更新"); ok {
		t.Fatal("did not expect update badge dot before any update is found")
	}

	// 1. Offline skips network check without touching LastUpdateCheckUnix.
	checkCalls := 0
	app.isOnlineFn = func() bool { return false }
	app.checkUpdateFn = func(ctx context.Context) (*updateInfo, error) {
		checkCalls++
		return nil, nil
	}
	app.maybeAutoCheckUpdates()
	if checkCalls != 0 || app.settings.LastUpdateCheckUnix != 0 {
		t.Fatalf("offline check should skip cleanly: calls=%d lastCheck=%d", checkCalls, app.settings.LastUpdateCheckUnix)
	}

	// 2. Network error stays completely silent (no updateError phase, no banner, no timestamp bump).
	app.isOnlineFn = func() bool { return true }
	app.checkUpdateFn = func(ctx context.Context) (*updateInfo, error) {
		checkCalls++
		return nil, errors.New("dial tcp: network unreachable")
	}
	app.maybeAutoCheckUpdates()
	if checkCalls != 1 {
		t.Fatalf("expected 1 check attempt, got %d", checkCalls)
	}
	if st := app.updateSnapshot(); st.phase != updateIdle || st.errText != "" {
		t.Fatalf("startup network error must stay silent, got %+v", st)
	}
	if app.settings.LastUpdateCheckUnix != 0 {
		t.Fatalf("failed startup check must not advance LastUpdateCheckUnix, got %d", app.settings.LastUpdateCheckUnix)
	}
	tester.Frame()
	if tester.HasText("network unreachable") || tester.HasText("检查更新失败") {
		t.Fatalf("startup network error leaked into UI: texts=%v", tester.Texts())
	}

	// 3. Up-to-date check persists LastUpdateCheckUnix and activates 12h throttle.
	app.checkUpdateFn = func(ctx context.Context) (*updateInfo, error) {
		checkCalls++
		return nil, nil
	}
	app.maybeAutoCheckUpdates()
	if checkCalls != 2 {
		t.Fatalf("expected 2 total check calls, got %d", checkCalls)
	}
	if app.settings.LastUpdateCheckUnix <= 0 {
		t.Fatal("expected LastUpdateCheckUnix persisted in workspace settings")
	}
	if saved, err := app.store.ReadSettings(); err != nil || saved.LastUpdateCheckUnix != app.settings.LastUpdateCheckUnix {
		t.Fatalf("expected LastUpdateCheckUnix persisted to disk: saved=%+v err=%v", saved, err)
	}

	// Within 12h throttle -> skip!
	app.maybeAutoCheckUpdates()
	app.settings.LastUpdateCheckUnix = time.Now().Add(-11 * time.Hour).Unix()
	app.maybeAutoCheckUpdates()
	if checkCalls != 2 {
		t.Fatalf("expected checks within 12h throttle to be skipped, got %d calls", checkCalls)
	}

	// 4. Stale (>12h) check with errNoPublishedRelease persists timestamp & stays silent.
	app.settings.LastUpdateCheckUnix = time.Now().Add(-13 * time.Hour).Unix()
	app.checkUpdateFn = func(ctx context.Context) (*updateInfo, error) {
		checkCalls++
		return nil, errNoPublishedRelease
	}
	app.maybeAutoCheckUpdates()
	if checkCalls != 3 {
		t.Fatalf("expected stale check to run (calls=%d)", checkCalls)
	}
	if time.Since(time.Unix(app.settings.LastUpdateCheckUnix, 0)) > time.Minute {
		t.Fatalf("expected errNoPublishedRelease to refresh LastUpdateCheckUnix, got %d", app.settings.LastUpdateCheckUnix)
	}
	if st := app.updateSnapshot(); st.phase != updateIdle {
		t.Fatalf("expected silent idle state for errNoPublishedRelease on startup, got %+v", st)
	}

	// 5. Stale check finds a newer version -> surfaces banner + badge dot (resetting dismissal).
	app.settings.LastUpdateCheckUnix = time.Now().Add(-13 * time.Hour).Unix()
	app.updateBannerDismissed = true
	app.checkUpdateFn = func(ctx context.Context) (*updateInfo, error) {
		checkCalls++
		return &updateInfo{
			Version:    "0.9.0",
			Notes:      "新增自动更新提示",
			ReleaseURL: folioReleasesURL,
			UsingMyGo:  true,
		}, nil
	}
	app.maybeAutoCheckUpdates()
	if checkCalls != 4 {
		t.Fatalf("expected stale check to run (calls=%d)", checkCalls)
	}
	if st := app.updateSnapshot(); st.phase != updateAvailable || st.info == nil || st.info.Version != "0.9.0" {
		t.Fatalf("expected updateAvailable v0.9.0, got %+v", st)
	}
	if app.updateBannerDismissed {
		t.Fatal("expected newly discovered update to clear updateBannerDismissed")
	}

	// Desktop UI shows banner + sidebar badge dot.
	tester.Frame()
	for _, want := range []string{"发现新版本 v0.9.0", "查看更新", "稍后"} {
		if !tester.HasText(want) {
			t.Fatalf("expected %q in desktop update banner; texts=%v", want, tester.Texts())
		}
	}
	if _, ok := tester.Find("有可用更新"); !ok {
		t.Fatal("expected Settings badge dot in desktop sidebar when updateAvailable")
	}

	// Clicking "查看更新" opens Settings and hides the top banner while Settings is open.
	if err := tester.Click("查看更新"); err != nil {
		t.Fatalf("click 查看更新: %v", err)
	}
	if !app.preferencesOpen {
		t.Fatal("expected 查看更新 to open Settings")
	}
	if tester.HasText("稍后") {
		t.Fatal("expected update banner hidden while Settings is open")
	}
	tester.Key(0, ui.KeyEscape)
	if app.preferencesOpen || !tester.HasText("发现新版本 v0.9.0") {
		t.Fatalf("expected banner visible again after closing Settings: prefs=%v", app.preferencesOpen)
	}

	// Clicking "稍后" dismisses banner cleanly while keeping the Settings badge dot.
	if err := tester.Click("稍后"); err != nil {
		t.Fatalf("click 稍后: %v", err)
	}
	if !app.updateBannerDismissed {
		t.Fatal("expected 稍后 to set updateBannerDismissed=true")
	}
	if tester.HasText("发现新版本 v0.9.0") {
		t.Fatal("expected banner hidden after clicking 稍后")
	}
	if _, ok := tester.Find("有可用更新"); !ok {
		t.Fatal("expected Settings badge dot to remain visible after dismissing banner")
	}

	// Narrow mobile layout also renders mobile header badge dot and compact banner cleanly.
	app.updateMu.Lock()
	app.updateBannerDismissed = false
	app.updateMu.Unlock()
	app.forceTouchUI = true
	mobile := ui.NewTester(app.view, 400, 800)
	mobile.Frame()
	if !mobile.HasText("发现新版本 v0.9.0") || !mobile.HasText("查看更新") || !mobile.HasText("稍后") {
		t.Fatalf("expected compact update banner on mobile; texts=%v", mobile.Texts())
	}
	if _, ok := mobile.Find("有可用更新"); !ok {
		t.Fatal("expected Settings badge dot in mobile header")
	}
}

func TestAutoCheckUpdatesPendingAndroidAPKOneTapInstall(t *testing.T) {
	// Even when offline and checked seconds ago, a verified pending Android APK surfaces immediately.
	app := &workspace{store: notebook.NewStore(t.TempDir()), forceTouchUI: true}
	app.settings.LastUpdateCheckUnix = time.Now().Unix()
	app.isOnlineFn = func() bool { return false }
	remoteChecks := 0
	app.checkUpdateFn = func(ctx context.Context) (*updateInfo, error) {
		remoteChecks++
		return nil, nil
	}
	app.checkPendingAndroidUpdateFn = func() (*updateInfo, bool) {
		return &updateInfo{
			Version:     "0.9.1",
			PendingPath: "/cache/updates/folio-0.9.1-arm64.apk",
			ReleaseURL:  folioReleasesURL,
		}, true
	}

	app.maybeAutoCheckUpdates()
	if remoteChecks != 0 {
		t.Fatalf("pending Android APK should bypass network check, got %d remote calls", remoteChecks)
	}
	if st := app.updateSnapshot(); st.phase != updateAvailable || st.info == nil || st.info.PendingPath == "" {
		t.Fatalf("expected ready pending APK state, got %+v", st)
	}

	installCalled := make(chan string, 1)
	app.installUpdateFn = func(ctx context.Context, info *updateInfo, progress func(int64, int64)) (updateInstallResult, error) {
		installCalled <- info.PendingPath
		return updateInstallResult{}, nil
	}
	tester := ui.NewTester(app.view, 400, 800)
	tester.Frame()
	if !tester.HasText("新版本 v0.9.1 安装包已就绪") || !tester.HasText("立即安装") {
		t.Fatalf("expected 1-tap pending APK banner; texts=%v", tester.Texts())
	}
	if err := tester.Click("立即安装"); err != nil {
		t.Fatalf("click 立即安装: %v", err)
	}
	select {
	case gotPath := <-installCalled:
		if gotPath != "/cache/updates/folio-0.9.1-arm64.apk" {
			t.Fatalf("installUpdateFn received path %q, want pending APK path", gotPath)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for 1-tap pending APK install invocation")
	}

	// When Unknown Sources permission is still missing, pending APK surfaces permission guidance.
	permApp := &workspace{store: notebook.NewStore(t.TempDir()), forceTouchUI: true}
	permApp.checkPendingAndroidUpdateFn = func() (*updateInfo, bool) {
		return &updateInfo{
			Version:     "0.9.1",
			PendingPath: "/cache/updates/folio-0.9.1-arm64.apk",
			ReleaseURL:  folioReleasesURL,
		}, false
	}
	permApp.maybeAutoCheckUpdates()
	if st := permApp.updateSnapshot(); st.phase != updatePermissionRequired {
		t.Fatalf("expected updatePermissionRequired when canInstallNow=false, got %+v", st)
	}
	if !permApp.hasUpdateBadge() {
		t.Fatal("expected badge dot when pending APK awaits install permission")
	}
}

func TestUpdateDownloadFailureRetryCancelDeferredAndTouchTargets(t *testing.T) {
	relURL := "https://github.com/dongyu-will/folio/releases/tag/v0.9.2"
	app := &workspace{store: notebook.NewStore(t.TempDir()), forceTouchUI: true}
	app.updateMu.Lock()
	app.update = updateUIState{
		phase: updateAvailable,
		info: &updateInfo{
			Version:    "0.9.2",
			Notes:      "修复移动端下载稳定性",
			ReleaseURL: relURL,
			UsingMyGo:  true,
		},
	}
	app.updateMu.Unlock()
	app.openPreferences()

	tester := ui.NewTester(app.view, 480, 800)
	scrollToBottom := func() {
		tester.Frame()
		tester.Scroll(240, 400, 0, 2000)
		tester.Frame()
	}
	waitPhase := func(want updatePhase) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if app.updateSnapshot().phase == want {
				scrollToBottom()
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for phase %d, got %+v", want, app.updateSnapshot())
	}

	scrollToBottom()
	// 1. Verify touchUI bumps settingPrimaryButton & settingButton height to 36.
	primaryRect, ok := tester.Find("下载并更新")
	if !ok || primaryRect.H != 36 {
		t.Fatalf("expected touch settingPrimaryButton height=36, got ok=%v rect=%+v", ok, primaryRect)
	}
	secRect, ok := tester.Find("前往发布页")
	if !ok || secRect.H != 36 {
		t.Fatalf("expected touch settingButton height=36, got ok=%v rect=%+v", ok, secRect)
	}

	// 2. Download failure retains st.info so user keeps "重试下载" + "前往发布页" right beside the error.
	app.installUpdateFn = func(ctx context.Context, info *updateInfo, progress func(int64, int64)) (updateInstallResult, error) {
		return updateInstallResult{}, errDownloadStalled
	}
	if err := tester.Click("下载并更新"); err != nil {
		t.Fatalf("click 下载并更新: %v", err)
	}
	waitPhase(updateError)
	if st := app.updateSnapshot(); st.info == nil || st.info.Version != "0.9.2" {
		t.Fatalf("download failure must preserve st.info, got %+v", st)
	}
	for _, want := range []string{errDownloadStalled.Error(), "重试下载", "前往发布页"} {
		if !tester.HasText(want) {
			t.Fatalf("expected %q after download failure; texts=%v", want, tester.Texts())
		}
	}
	if err := tester.Click("前往发布页"); err != nil {
		t.Fatalf("click 前往发布页 after failure: %v", err)
	}
	if !slices.Contains(tester.OpenedURLs(), relURL) {
		t.Fatalf("expected OpenURL(%q), got %v", relURL, tester.OpenedURLs())
	}

	// 3. Clicking "取消" while downloading cancels the in-flight context and returns cleanly to updateAvailable.
	started := make(chan struct{})
	ctxCanceled := make(chan struct{})
	returned := make(chan struct{})
	app.installUpdateFn = func(ctx context.Context, info *updateInfo, progress func(int64, int64)) (updateInstallResult, error) {
		progress(25, 100)
		close(started)
		<-ctx.Done()
		close(ctxCanceled)
		defer close(returned)
		return updateInstallResult{}, ctx.Err()
	}
	if err := tester.Click("重试下载"); err != nil {
		t.Fatalf("click 重试下载: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for retry download to start")
	}
	scrollToBottom()
	if !tester.HasText("下载中…") || !tester.HasText("取消") {
		t.Fatalf("expected 下载中… and 取消 while downloading; texts=%v", tester.Texts())
	}
	if err := tester.Click("取消"); err != nil {
		t.Fatalf("click 取消: %v", err)
	}
	select {
	case <-ctxCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelUpdateDownload did not cancel in-flight download context")
	}
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("download goroutine did not exit after cancel")
	}
	scrollToBottom()
	if st := app.updateSnapshot(); st.phase != updateAvailable || st.info == nil || st.info.Version != "0.9.2" {
		t.Fatalf("expected clean return to updateAvailable with info preserved after cancel, got %+v", st)
	}
	if !tester.HasText("下载并更新") {
		t.Fatalf("expected 下载并更新 button restored after cancel; texts=%v", tester.Texts())
	}

	// 4. DeferredUntilForeground renders the exact Chinese hint cleanly.
	app.installUpdateFn = func(ctx context.Context, info *updateInfo, progress func(int64, int64)) (updateInstallResult, error) {
		return updateInstallResult{DeferredUntilForeground: true}, nil
	}
	if err := tester.Click("下载并更新"); err != nil {
		t.Fatalf("click 下载并更新 (deferred): %v", err)
	}
	waitPhase(updateDeferredForeground)
	if !tester.HasText("下载已完成，返回应用将自动打开安装器") || !tester.HasText("安装更新") {
		t.Fatalf("expected deferred foreground guidance and 安装更新 button; texts=%v", tester.Texts())
	}
}
