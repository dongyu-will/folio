package main

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo/ui"
)

type mapNode struct {
	id          string
	x, y        float32
	w, h        float32
	parent      int
	depth       int
	active      bool
	hasKids     bool
	expanded    bool
	inGroup     bool // true when card lives inside a 2D leaf-grid group box (single trunk edge instead of spaghetti)
	isHeading   bool
	isFrameHead bool // true when rendered as the embedded header/intro inside a framework section
	imageHeight []float32
}

type boardSection struct {
	id          string
	note        notebook.Note
	title       string
	count       int
	x, y        float32
	w, h        float32
	depth       int
	isLeafGroup bool // subtle 2D leaf-grid wrapper in cards mode
	parentNode  int  // parent mapNode index for leaf-group trunk edge (-1 for board frames)
	active      bool
}

type mapNodeKey struct {
	parent int
	noteID string
}

const (
	nodeWidth        = 172
	rootWidth        = 156
	columnGap        = 52
	rowStep          = 58
	nodeH            = 44
	mapInset         = 32
	cardHeadingMinW  = 148
	cardHeadingMaxW  = 244
	cardRootMinW     = 176
	cardContentWidth = 304
	cardColGap       = 48
	cardRowGap       = 12
	cardBranchGap    = 20
	cardGridGapX     = 12
	cardGridGapY     = 12
	cardGroupPad     = 12
	cardMaxImgH      = 210
	boardCardWidth   = 292
	boardHeaderH     = 40
	boardSubHeaderH  = 34
	boardPad         = 12
	boardCardGap     = 12
	boardFrameGap     = 18
	boardTopMaxW      = 2860
	boardSubMaxW      = 2520
	miniTreeColGap    = 36
	miniTreeRowGap    = 10
	miniTreeBranchGap = 14
	cardBlockDividerH = 9
)

func (a *workspace) visibleKids(id string) []string {
	children := a.tree.kids[id]
	shown := make([]string, 0, len(children))
	for _, cid := range children {
		n := a.tree.notes[cid]
		if n.Title != "" || hasNoteContent(n) || len(a.tree.kids[cid]) > 0 || cid == a.selected {
			shown = append(shown, cid)
		}
	}
	return shown
}

func (a *workspace) ensureExpanded(trail []string) {
	if a.expanded == nil {
		// MarginNote opens mindmaps fully expanded so the entire "一页纸" is immediately visible.
		a.expanded = make(map[string]bool, len(a.tree.order)+2)
		a.expanded[a.tree.root] = true
		for _, id := range a.tree.order {
			if len(a.tree.kids[id]) > 0 {
				a.expanded[id] = true
			}
		}
		for i := 0; i < len(trail)-1; i++ {
			a.expanded[trail[i]] = true
		}
		if a.scope != "" {
			a.expanded[a.scope] = true
		}
	}
	a.expanded[a.tree.root] = true
}

func (a *workspace) hasCollapsedBranches() bool {
	if a.expanded == nil {
		return false
	}
	for _, id := range a.tree.order {
		if id != a.tree.root && len(a.visibleKids(id)) > 0 && !a.expanded[id] {
			return true
		}
	}
	return false
}

func (a *workspace) toggleBranch(id string) {
	trail := a.currentTrail()
	a.ensureExpanded(trail)
	a.expanded[id] = !a.expanded[id]
}

// estimateTextWidth estimates rendered pixel width for CJK/ASCII mixed strings.
func estimateTextWidth(s string, fontSize float32) float32 {
	var w float32
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t':
			w += fontSize * 0.36
		case r <= 0x007f:
			w += fontSize * 0.60
		default:
			w += fontSize * 1.07
		}
	}
	return w
}

// wrapLineCount computes conservative wrapped line count preserving explicit newlines.
func wrapLineCount(s string, maxW, fontSize float32) int {
	if strings.TrimSpace(s) == "" || maxW <= 8 {
		return 0
	}
	paragraphs := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	total := 0
	for _, para := range paragraphs {
		if strings.TrimSpace(para) == "" {
			total++
			continue
		}
		w := estimateTextWidth(para, fontSize)
		lines := max(1, int(math.Ceil(float64(w/maxW))))
		total += lines
	}
	return total
}

type cardMeasure struct {
	w, h        float32
	isHeading   bool
	imageHeight []float32
}

func measureRichTextBlock(blk notebook.ContentBlock, fallbackTxt string, maxW, baseFontSize float32) float32 {
	if len(blk.Spans) == 0 {
		cleaned := cleanCardBody(fallbackTxt)
		if cleaned == "" {
			return 0
		}
		lines := wrapLineCount(cleaned, maxW, baseFontSize)
		return float32(lines)*18.6 + 4
	}
	if maxW <= 8 {
		return 0
	}
	dominant := dominantSpanFontSize(blk.Spans)
	var totalH float32
	var paraW float32
	paraMaxScale := float32(1)
	paraHasText := false
	flushPara := func() {
		if !paraHasText {
			totalH += 18.6
		} else {
			lines := max(1, int(math.Ceil(float64(paraW/maxW))))
			totalH += float32(lines) * (18.6 * paraMaxScale)
		}
		paraW = 0
		paraMaxScale = 1
		paraHasText = false
	}
	anyContent := false
	for _, s := range blk.Spans {
		if s.Text == "" {
			continue
		}
		sc := spanFontScale(s, dominant)
		fs := baseFontSize * sc
		txt := strings.ReplaceAll(s.Text, "\r\n", "\n")
		parts := strings.Split(txt, "\n")
		for pi, part := range parts {
			if pi > 0 {
				flushPara()
			}
			if strings.TrimSpace(part) != "" {
				paraHasText = true
				anyContent = true
				if sc > paraMaxScale {
					paraMaxScale = sc
				}
			}
			paraW += estimateTextWidth(part, fs)
		}
	}
	if !anyContent {
		return 0
	}
	flushPara()
	return totalH + 4
}

func (a *workspace) measureCard(n notebook.Note, fixedWidth float32, depth int, hasKids, noHeader bool) cardMeasure {
	title := strings.TrimSpace(n.Title)
	bodyText := noteCardText(n)
	imgs := noteCardImages(n)
	hasPage := n.Page != nil && *n.Page > 0
	hasDocs := len(a.tree.book.Documents) > 0
	pageReserve := float32(46)
	if hasDocs {
		pageReserve = 56
	}
	isHeading := !noHeader && bodyText == "" && len(imgs) == 0

	if isHeading {
		displayTitle := noteTitle(n)
		w := fixedWidth
		if w <= 0 {
			minW := float32(cardHeadingMinW)
			maxW := float32(cardHeadingMaxW)
			if depth == 0 {
				minW = cardRootMinW
				maxW = 264
			}
			extra := float32(34)
			if hasKids && depth > 0 {
				extra += 42
			}
			if hasPage {
				extra += pageReserve
			}
			w = clamp(estimateTextWidth(displayTitle, 13.5)+extra, minW, maxW)
		}
		titleAvail := max(60, w-26)
		if hasPage {
			titleAvail = max(60, titleAvail-pageReserve)
		}
		if hasKids && depth > 0 {
			titleAvail = max(60, titleAvail-40)
		}
		lines := min(2, max(1, wrapLineCount(displayTitle, titleAvail, 13.5)))
		h := float32(24 + lines*20)
		if depth == 0 {
			h = max(h, 52)
		}
		return cardMeasure{w: w, h: h, isHeading: true}
	}

	w := fixedWidth
	if w <= 0 {
		w = cardContentWidth
	}
	innerW := max(110, w-22)

	var h float32
	if !noHeader {
		if title != "" || hasPage {
			titleAvail := innerW
			if hasPage {
				titleAvail = max(70, innerW-pageReserve)
			}
			if hasKids && depth > 0 {
				titleAvail = max(70, titleAvail-40)
			}
			tLines := min(2, max(1, wrapLineCount(noteTitle(n), titleAvail, 13)))
			h += float32(15 + tLines*18)
		} else {
			// Pure sketch/image card without title uses a sleek colored top strip.
			h += 5
		}
	}

	// Body container top+bottom padding (9px + 9px = 18px) + slight bottom cushion for full-text cards.
	h += 22
	items := 0
	imgHeights := make([]float32, 0, len(imgs))
	frameInnerW := max(80, innerW-10)
	addImg := func(ref string) {
		iw, ih := a.imageDims(ref)
		dispH := float32(64)
		if iw > 0 && ih > 0 {
			dispH = clamp(frameInnerW*float32(ih)/float32(iw), 32, cardMaxImgH)
		}
		imgHeights = append(imgHeights, dispH)
		if items > 0 {
			h += 8
		}
		h += dispH + 11 // frame padding + hairline border
		items++
	}

	addText := func(blk notebook.ContentBlock, fallbackTxt string) {
		blockH := measureRichTextBlock(blk, fallbackTxt, innerW, 12.2)
		if blockH == 0 {
			return
		}
		if items > 0 {
			h += 8
		}
		h += blockH
		items++
	}

	if n.Clip != "" {
		addImg(n.Clip)
	}
	useBlocks := n.BlocksCoverBody(bodyText)
	if !useBlocks && bodyText != "" {
		addText(notebook.ContentBlock{Text: bodyText}, bodyText)
	}
	renderedBlocks := 0
	addDivider := func() {
		if useBlocks && len(n.Blocks) > 1 && renderedBlocks > 0 {
			h += cardBlockDividerH
		}
	}
	for _, blk := range n.Blocks {
		if blk.Image != "" {
			addDivider()
			addImg(blk.Image)
			renderedBlocks++
		} else if useBlocks && blockPlainText(blk) != "" {
			addDivider()
			addText(blk, blk.Text)
			renderedBlocks++
		}
	}
	minH := float32(48)
	if noHeader {
		minH = 36
	}
	return cardMeasure{
		w:           w,
		h:           max(minH, h),
		isHeading:   false,
		imageHeight: imgHeights,
	}
}

// chooseGridCols picks a natural 2D column count so sibling leaf lists form
// balanced landscape "一页纸" blocks instead of endless 1-column skyscrapers.
func chooseGridCols(count, minForMulti, maxCols int) int {
	if count < minForMulti || maxCols <= 1 {
		return 1
	}
	var cols int
	switch {
	case count == 2:
		cols = 2
	case count == 3:
		cols = 3
	case count == 4:
		cols = 2
	case count <= 9:
		cols = 3
	case count <= 20:
		cols = 4
	default:
		cols = 5
	}
	return min(min(cols, count), maxCols)
}

type packedCell struct {
	idx  int
	x, y float32
}

// packBalancedColumns uses exact sequential Painter's Partition DP to split `n`
// items into `cols` non-empty columns minimizing the tallest column while
// preserving top-to-bottom, left-to-right sequential reading order.
func packBalancedColumns(widths, heights []float32, cols int, gapX, gapY float32) ([]packedCell, float32, float32) {
	n := len(heights)
	if n == 0 {
		return nil, 0, 0
	}
	cols = clampInt(cols, 1, n)

	// prefix[i] = sum of heights[0..i-1]
	prefix := make([]float32, n+1)
	for i := 0; i < n; i++ {
		prefix[i+1] = prefix[i] + heights[i]
	}
	spanHeight := func(l, r int) float32 {
		if r <= l {
			return 0
		}
		return (prefix[r] - prefix[l]) + float32(r-l-1)*gapY
	}

	// dp[c][i] = minimal max column height partitioning items [0..i-1] into c columns.
	const inf = float32(1e18)
	dp := make([][]float32, cols+1)
	split := make([][]int, cols+1)
	for c := 0; c <= cols; c++ {
		dp[c] = make([]float32, n+1)
		split[c] = make([]int, n+1)
		for i := range dp[c] {
			dp[c][i] = inf
		}
	}
	for i := 1; i <= n; i++ {
		dp[1][i] = spanHeight(0, i)
	}
	for c := 2; c <= cols; c++ {
		for i := c; i <= n; i++ {
			best := inf
			bestK := c - 1
			for k := c - 1; k < i; k++ {
				cost := max(dp[c-1][k], spanHeight(k, i))
				if cost < best {
					best = cost
					bestK = k
				}
			}
			dp[c][i] = best
			split[c][i] = bestK
		}
	}

	// Reconstruct column ranges [starts[c], ends[c]).
	ranges := make([][2]int, cols)
	end := n
	for c := cols; c >= 1; c-- {
		start := 0
		if c > 1 {
			start = split[c][end]
		}
		ranges[c-1] = [2]int{start, end}
		end = start
	}

	cells := make([]packedCell, 0, n)
	var x, maxH float32
	for c, rg := range ranges {
		if rg[1] <= rg[0] {
			continue
		}
		if c > 0 && x > 0 {
			x += gapX
		}
		var colW, y float32
		for idx := rg[0]; idx < rg[1]; idx++ {
			if idx > rg[0] {
				y += gapY
			}
			cells = append(cells, packedCell{idx: idx, x: x, y: y})
			y += heights[idx]
			colW = max(colW, widths[idx])
		}
		maxH = max(maxH, y)
		x += colW
	}
	return cells, x, maxH
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (a *workspace) mapNodes(_ float32) ([]mapNode, int) {
	nodes, _, picked := a.mapLayout()
	return nodes, picked
}

func (a *workspace) mapLayout() ([]mapNode, []boardSection, int) {
	switch a.settings.LayoutMode() {
	case notebook.LayoutCompact:
		nodes, picked := a.compactMapNodes()
		return nodes, nil, picked
	case notebook.LayoutBoard:
		return a.boardMapNodes()
	default:
		return a.cardMapNodes()
	}
}

func (a *workspace) compactMapNodes() ([]mapNode, int) {
	trail := a.currentTrail()
	nodes := []mapNode{{id: a.tree.root, w: rootWidth, h: nodeH, y: 0, parent: -1, active: true}}
	parent, picked := 0, 0
	for depth := 0; depth < len(trail); depth++ {
		children := a.tree.kids[nodes[parent].id]
		shown := make([]string, 0, len(children))
		for _, id := range children {
			if slices.Contains(trail[:depth+1], id) {
				continue
			}
			n := a.tree.notes[id]
			if n.Title != "" || hasNoteContent(n) || len(a.tree.kids[id]) > 0 || id == a.selected {
				shown = append(shown, id)
			}
		}
		nextID := ""
		if depth+1 < len(trail) {
			nextID = trail[depth+1]
		}
		anchor := float32(len(shown)-1) / 2
		if len(shown) > 12 && nextID != "" {
			anchor = clamp(float32(slices.Index(shown, nextID)), 0, float32(len(shown)-1))
		}
		start := nodes[parent].y - anchor*rowStep
		x := nodes[parent].x + nodes[parent].w + columnGap
		next := -1
		for row, id := range shown {
			active := id == nextID
			nodes = append(nodes, mapNode{
				id: id, x: x, y: start + float32(row)*rowStep, w: nodeWidth, h: nodeH,
				parent: parent, depth: depth + 1, active: active, hasKids: len(a.tree.kids[id]) > 0,
			})
			if active {
				next = len(nodes) - 1
				picked = next
			}
		}
		if next < 0 {
			break
		}
		parent = next
	}
	return nodes, picked
}

type cardChildBlock struct {
	branch    *cardTreeNode
	leafGroup []*cardTreeNode
	cells     []packedCell
	w, h      float32
}

type cardTreeNode struct {
	id       string
	path     []string
	m        cardMeasure
	subH     float32
	hasKids  bool
	expanded bool
	blocks   []cardChildBlock
}

func (a *workspace) cardMapNodes() ([]mapNode, []boardSection, int) {
	trail := a.currentTrail()
	a.ensureExpanded(trail)
	// Keep every ancestor on the active trail expanded so the active node is always reachable.
	for i := 0; i < len(trail)-1; i++ {
		a.expanded[trail[i]] = true
	}

	rootID := a.tree.root

	var build func(id string, path []string, depth int) *cardTreeNode
	build = func(id string, path []string, depth int) *cardTreeNode {
		n := a.tree.notes[id]
		kids := a.visibleKids(id)
		filtered := make([]string, 0, len(kids))
		for _, cid := range kids {
			if !slices.Contains(path, cid) {
				filtered = append(filtered, cid)
			}
		}
		hasKids := len(filtered) > 0
		m := a.measureCard(n, 0, depth, hasKids, false)
		exp := a.expanded[id] && hasKids && depth < 10
		tn := &cardTreeNode{id: id, path: path, m: m, hasKids: hasKids, expanded: exp}
		if !exp {
			tn.subH = m.h
			return tn
		}

		built := make([]*cardTreeNode, 0, len(filtered))
		for _, cid := range filtered {
			childPath := append(slices.Clone(path), cid)
			built = append(built, build(cid, childPath, depth+1))
		}

		// Group contiguous runs of >=3 unexpanded leaf siblings into 2D multi-column grids!
		flushLeaves := func(run []*cardTreeNode) {
			if len(run) == 0 {
				return
			}
			if len(run) < 3 {
				for _, leaf := range run {
					tn.blocks = append(tn.blocks, cardChildBlock{branch: leaf, w: leaf.m.w, h: leaf.subH})
				}
				return
			}
			cols := chooseGridCols(len(run), 3, 5)
			ws := make([]float32, len(run))
			hs := make([]float32, len(run))
			for i, leaf := range run {
				ws[i], hs[i] = leaf.m.w, leaf.m.h
			}
			cells, gw, gh := packBalancedColumns(ws, hs, cols, cardGridGapX, cardGridGapY)
			tn.blocks = append(tn.blocks, cardChildBlock{
				leafGroup: run,
				cells:     cells,
				w:         gw + 2*cardGroupPad,
				h:         gh + 2*cardGroupPad,
			})
		}

		var leafRun []*cardTreeNode
		for _, ch := range built {
			if !ch.expanded {
				leafRun = append(leafRun, ch)
			} else {
				flushLeaves(leafRun)
				leafRun = nil
				tn.blocks = append(tn.blocks, cardChildBlock{branch: ch, w: ch.m.w, h: ch.subH})
			}
		}
		flushLeaves(leafRun)

		var total float32
		for i, blk := range tn.blocks {
			if i > 0 {
				gap := float32(cardRowGap)
				if blk.leafGroup != nil || tn.blocks[i-1].leafGroup != nil ||
					(blk.branch != nil && blk.branch.expanded) ||
					(tn.blocks[i-1].branch != nil && tn.blocks[i-1].branch.expanded) {
					gap = cardBranchGap
				}
				total += gap
			}
			total += blk.h
		}
		tn.subH = max(m.h, total)
		return tn
	}

	root := build(rootID, []string{rootID}, 0)
	var nodes []mapNode
	var groups []boardSection
	picked := 0

	pathOnTrail := func(path []string) bool {
		if len(path) == 0 || len(path) > len(trail) {
			return false
		}
		return slices.Equal(path, trail[:len(path)])
	}

	var place func(tn *cardTreeNode, depth int, x, topY float32, parentIdx int)
	place = func(tn *cardTreeNode, depth int, x, topY float32, parentIdx int) {
		cy := topY + tn.subH/2
		if tn.subH > 520 {
			cy = topY + min(tn.subH/2, max(tn.m.h/2+12, 150))
		}
		idx := len(nodes)
		active := pathOnTrail(tn.path)
		nodes = append(nodes, mapNode{
			id: tn.id, x: x, y: cy, w: tn.m.w, h: tn.m.h,
			parent: parentIdx, depth: depth,
			active: active, hasKids: tn.hasKids, expanded: tn.expanded,
			isHeading: tn.m.isHeading, imageHeight: tn.m.imageHeight,
		})
		if slices.Equal(tn.path, trail) {
			picked = idx
		}
		if len(tn.blocks) == 0 {
			return
		}

		var kidsTotal float32
		for i, blk := range tn.blocks {
			if i > 0 {
				gap := float32(cardRowGap)
				if blk.leafGroup != nil || tn.blocks[i-1].leafGroup != nil ||
					(blk.branch != nil && blk.branch.expanded) ||
					(tn.blocks[i-1].branch != nil && tn.blocks[i-1].branch.expanded) {
					gap = cardBranchGap
				}
				kidsTotal += gap
			}
			kidsTotal += blk.h
		}
		curY := topY + (tn.subH-kidsTotal)/2
		nextX := x + tn.m.w + cardColGap

		for i, blk := range tn.blocks {
			if i > 0 {
				gap := float32(cardRowGap)
				if blk.leafGroup != nil || tn.blocks[i-1].leafGroup != nil ||
					(blk.branch != nil && blk.branch.expanded) ||
					(tn.blocks[i-1].branch != nil && tn.blocks[i-1].branch.expanded) {
					gap = cardBranchGap
				}
				curY += gap
			}
			if blk.branch != nil {
				place(blk.branch, depth+1, nextX, curY, idx)
			} else {
				groupActive := false
				for _, cell := range blk.cells {
					leaf := blk.leafGroup[cell.idx]
					leafActive := pathOnTrail(leaf.path)
					if leafActive {
						groupActive = true
					}
					lx := nextX + cardGroupPad + cell.x
					ly := curY + cardGroupPad + cell.y + leaf.m.h/2
					leafIdx := len(nodes)
					nodes = append(nodes, mapNode{
						id: leaf.id, x: lx, y: ly, w: leaf.m.w, h: leaf.m.h,
						parent: idx, depth: depth + 1,
						active: leafActive, hasKids: leaf.hasKids, expanded: false, inGroup: true,
						isHeading: leaf.m.isHeading, imageHeight: leaf.m.imageHeight,
					})
					if slices.Equal(leaf.path, trail) {
						picked = leafIdx
					}
				}
				groups = append(groups, boardSection{
					id:          tn.id + "-grp",
					note:        a.tree.notes[tn.id],
					x:           nextX,
					y:           curY,
					w:           blk.w,
					h:           blk.h,
					depth:       depth,
					isLeafGroup: true,
					parentNode:  idx,
					active:      groupActive,
				})
			}
			curY += blk.h
		}
	}

	place(root, 0, 0, -root.subH/2, -1)
	if len(nodes) == 0 {
		nodes = []mapNode{{id: rootID, w: cardRootMinW, h: 54, parent: -1, active: true, isHeading: true}}
	}
	return nodes, groups, picked
}

func isInlineTreeMode(n notebook.Note) bool {
	return n.LayoutMode != nil && (*n.LayoutMode == 0 || *n.LayoutMode == 1)
}

// boardMapNodes implements MarginNote's iconic 2D Recursive Nested Framework ("专题框架") layout:
// sibling branches wrap side-by-side in 2D rows, leaf cards pack into balanced multi-column grids,
// and sub-branches with explicit tree layoutMode (0/1) embed as horizontal mini-trees with bezier edges.
func (a *workspace) boardMapNodes() ([]mapNode, []boardSection, int) {
	trail := a.currentTrail()
	a.ensureExpanded(trail)
	trailSet := make(map[string]bool, len(trail))
	for _, id := range trail {
		trailSet[id] = true
	}

	rootID := a.tree.root
	if a.scope != "" {
		if _, ok := a.tree.notes[a.scope]; ok {
			rootID = a.scope
		}
	}

	type miniTreeNode struct {
		id       string
		depth    int
		m        cardMeasure
		hasKids  bool
		expanded bool
		kids     []*miniTreeNode
		w, h     float32
		yOffset  float32
		kidYs    []float32
		count    int
	}

	type frameBox struct {
		id      string
		note    notebook.Note
		title   string
		depth   int
		isLeaf  bool
		hasKids bool
		m       cardMeasure
		w, h    float32
		count   int

		// Embedded horizontal mini-tree when sub-branch explicitly uses tree layoutMode (0 or 1).
		tree *miniTreeNode

		// Children when !isLeaf && tree == nil: placed relative to (0, 0) of this frameBox.
		introCard *cardMeasure
		introY    float32
		kids      []*frameBox
		kidPos    [][2]float32
	}

	seen := map[string]bool{rootID: true}

	var buildMiniTree func(id string, depth int) *miniTreeNode
	buildMiniTree = func(id string, depth int) *miniTreeNode {
		n := a.tree.notes[id]
		kids := a.visibleKids(id)
		var unseenKids []string
		for _, cid := range kids {
			if !seen[cid] {
				unseenKids = append(unseenKids, cid)
			}
		}
		hasKids := len(unseenKids) > 0
		exp := hasKids && a.expanded[id] && depth < 10

		fixedW := float32(boardCardWidth)
		if noteCardText(n) == "" && !noteHasImage(n) {
			fixedW = 0 // compact heading width (148..244px)
		}
		m := a.measureCard(n, fixedW, depth+1, hasKids, false)
		mt := &miniTreeNode{
			id: id, depth: depth, m: m,
			hasKids: hasKids, expanded: exp, count: 1,
		}
		if !exp {
			mt.w, mt.h = m.w, m.h
			return mt
		}

		for _, cid := range unseenKids {
			seen[cid] = true
		}
		mt.kids = make([]*miniTreeNode, 0, len(unseenKids))
		mt.kidYs = make([]float32, 0, len(unseenKids))
		var curY, kidsMaxW float32
		childCount := 0
		for i, cid := range unseenKids {
			ch := buildMiniTree(cid, depth+1)
			if i > 0 {
				gap := float32(miniTreeRowGap)
				if ch.expanded || mt.kids[i-1].expanded {
					gap = miniTreeBranchGap
				}
				curY += gap
			}
			mt.kids = append(mt.kids, ch)
			mt.kidYs = append(mt.kidYs, curY)
			curY += ch.h
			kidsMaxW = max(kidsMaxW, ch.w)
			childCount += ch.count
		}
		kidsH := curY
		mt.h = max(m.h, kidsH)
		mt.w = m.w + miniTreeColGap + kidsMaxW
		mt.count = childCount
		if mt.h > m.h {
			mt.yOffset = (mt.h - m.h) / 2
			if mt.h > 420 {
				mt.yOffset = min(mt.yOffset, max(12, 130))
			}
		}
		if mt.h > kidsH {
			shift := (mt.h - kidsH) / 2
			for i := range mt.kidYs {
				mt.kidYs[i] += shift
			}
		}
		return mt
	}

	var buildFrame func(id string, depth int) *frameBox
	buildFrame = func(id string, depth int) *frameBox {
		n := a.tree.notes[id]
		kids := a.visibleKids(id)
		var unseenKids []string
		for _, cid := range kids {
			if !seen[cid] {
				unseenKids = append(unseenKids, cid)
			}
		}
		hasKids := len(unseenKids) > 0
		exp := hasKids && a.expanded[id] && depth < 6

		if !exp {
			m := a.measureCard(n, boardCardWidth, depth+1, hasKids, false)
			return &frameBox{
				id: id, note: n, title: noteTitle(n), depth: depth,
				isLeaf: true, hasKids: hasKids,
				m: m, w: m.w, h: m.h, count: 1,
			}
		}

		// Hybrid MarginNote layout: sub-branches explicitly configured as tree (layoutMode 0 or 1)
		// render as clean inline horizontal mini-trees inside their parent framework.
		if depth > 0 && isInlineTreeMode(n) {
			mt := buildMiniTree(id, depth)
			return &frameBox{
				id: id, note: n, title: noteTitle(n), depth: depth,
				isLeaf: false, hasKids: true,
				w: mt.w, h: mt.h, count: mt.count, tree: mt,
			}
		}

		for _, cid := range unseenKids {
			seen[cid] = true
		}
		children := make([]*frameBox, 0, len(unseenKids))
		totalCount := 0
		for _, cid := range unseenKids {
			ch := buildFrame(cid, depth+1)
			children = append(children, ch)
			totalCount += ch.count
		}

		headerH := float32(boardHeaderH)
		if depth > 0 {
			headerH = boardSubHeaderH
		}

		fb := &frameBox{
			id: id, note: n, title: noteTitle(n), depth: depth,
			isLeaf: false, hasKids: true,
			count: totalCount,
		}

		curY := headerH + boardPad
		var contentMaxW float32

		// Pack children first so any framework intro card aligns flush with the full
		// content width of the child columns underneath it.
		maxRowW := float32(boardSubMaxW)
		if depth == 0 {
			maxRowW = boardTopMaxW - 2*boardPad
		}

		flushLeaves := func(run []*frameBox) {
			if len(run) == 0 {
				return
			}
			maxCols := 5
			if depth >= 2 {
				maxCols = 3
			} else if depth == 1 {
				maxCols = 4
			}
			cols := chooseGridCols(len(run), 2, maxCols)
			ws := make([]float32, len(run))
			hs := make([]float32, len(run))
			for i, lf := range run {
				ws[i], hs[i] = lf.w, lf.h
			}
			cells, gw, gh := packBalancedColumns(ws, hs, cols, boardCardGap, boardCardGap)
			for _, cell := range cells {
				fb.kids = append(fb.kids, run[cell.idx])
				fb.kidPos = append(fb.kidPos, [2]float32{boardPad + cell.x, curY + cell.y})
			}
			contentMaxW = max(contentMaxW, gw)
			curY += gh + boardFrameGap
		}

		flushSubFrames := func(run []*frameBox) {
			if len(run) == 0 {
				return
			}
			var rowX, rowH float32
			for _, sf := range run {
				if rowX > 0 && rowX+boardFrameGap+sf.w > maxRowW {
					curY += rowH + boardFrameGap
					rowX, rowH = 0, 0
				}
				if rowX > 0 {
					rowX += boardFrameGap
				}
				fb.kids = append(fb.kids, sf)
				fb.kidPos = append(fb.kidPos, [2]float32{boardPad + rowX, curY})
				rowX += sf.w
				rowH = max(rowH, sf.h)
				contentMaxW = max(contentMaxW, rowX)
			}
			curY += rowH + boardFrameGap
		}

		var leafRun []*frameBox
		var subRun []*frameBox
		for _, ch := range children {
			if ch.isLeaf {
				flushSubFrames(subRun)
				subRun = nil
				leafRun = append(leafRun, ch)
			} else {
				flushLeaves(leafRun)
				leafRun = nil
				subRun = append(subRun, ch)
			}
		}
		flushLeaves(leafRun)
		flushSubFrames(subRun)

		if len(fb.kids) > 0 {
			curY -= boardFrameGap
		}

		// If the branch container itself has body text or images (e.g., 审敛流水线, 正项级数, 墓碑词),
		// render its own rich lead block flush across the full framework width right above its children.
		if noteCardText(n) != "" || noteHasImage(n) {
			introW := max(float32(boardCardWidth), contentMaxW)
			intro := a.measureCard(n, introW, depth+1, false, true)
			fb.introCard = &intro
			fb.introY = headerH + boardPad
			shift := intro.h + boardCardGap
			for i := range fb.kidPos {
				fb.kidPos[i][1] += shift
			}
			curY += shift
			contentMaxW = max(contentMaxW, intro.w)
		}

		headerMinW := estimateTextWidth(fb.title, 13.5) + 96
		fb.w = max(max(contentMaxW, headerMinW)+2*boardPad, boardCardWidth+2*boardPad)
		fb.h = max(headerH+2*boardPad+36, curY+boardPad)
		return fb
	}

	topKids := a.visibleKids(rootID)
	var topFrames []*frameBox
	var looseLeaves []*frameBox

	for _, id := range topKids {
		if seen[id] {
			continue
		}
		seen[id] = true
		fb := buildFrame(id, 0)
		// Keep branch nodes in topFrames even when collapsed so folding a top section
		// collapses cleanly in-place instead of moving into "核心卡片".
		if fb.isLeaf && !fb.hasKids {
			looseLeaves = append(looseLeaves, fb)
		} else {
			topFrames = append(topFrames, fb)
		}
	}

	if len(looseLeaves) > 0 || len(topFrames) == 0 {
		rootNote := a.tree.notes[rootID]
		title := noteTitle(rootNote)
		if len(topFrames) > 0 {
			title = "核心卡片"
		}
		if len(looseLeaves) == 0 && rootID != "" {
			m := a.measureCard(rootNote, boardCardWidth, 1, false, false)
			looseLeaves = []*frameBox{{
				id: rootID, note: rootNote, title: title, depth: 1,
				isLeaf: true, m: m, w: m.w, h: m.h, count: 1,
			}}
		}
		cols := chooseGridCols(len(looseLeaves), 2, 4)
		ws := make([]float32, len(looseLeaves))
		hs := make([]float32, len(looseLeaves))
		for i, lf := range looseLeaves {
			ws[i], hs[i] = lf.w, lf.h
		}
		cells, gw, gh := packBalancedColumns(ws, hs, cols, boardCardGap, boardCardGap)
		wrap := &frameBox{
			id: rootID, note: rootNote, title: title, depth: 0,
			isLeaf: false, hasKids: true, count: len(looseLeaves),
			w: gw + 2*boardPad, h: boardHeaderH + gh + 2*boardPad,
		}
		for _, cell := range cells {
			wrap.kids = append(wrap.kids, looseLeaves[cell.idx])
			wrap.kidPos = append(wrap.kidPos, [2]float32{boardPad + cell.x, boardHeaderH + boardPad + cell.y})
		}
		topFrames = append([]*frameBox{wrap}, topFrames...)
	}

	var nodes []mapNode
	var sections []boardSection
	picked := 0

	var emitMiniTree func(mt *miniTreeNode, ox, oy float32, parentIdx int)
	emitMiniTree = func(mt *miniTreeNode, ox, oy float32, parentIdx int) {
		idx := len(nodes)
		cy := oy + mt.yOffset + mt.m.h/2
		nodes = append(nodes, mapNode{
			id: mt.id, x: ox, y: cy, w: mt.m.w, h: mt.m.h,
			parent: parentIdx, depth: mt.depth + 1, active: trailSet[mt.id],
			hasKids: mt.hasKids, expanded: mt.expanded,
			isHeading: mt.m.isHeading, imageHeight: mt.m.imageHeight,
		})
		if mt.id == a.selected {
			picked = idx
		}
		childX := ox + mt.m.w + miniTreeColGap
		for i, ch := range mt.kids {
			emitMiniTree(ch, childX, oy+mt.kidYs[i], idx)
		}
	}

	var emit func(fb *frameBox, ox, oy float32)
	emit = func(fb *frameBox, ox, oy float32) {
		if fb.tree != nil {
			emitMiniTree(fb.tree, ox, oy, -1)
			return
		}
		if fb.isLeaf {
			idx := len(nodes)
			nodes = append(nodes, mapNode{
				id: fb.id, x: ox, y: oy + fb.h/2, w: fb.w, h: fb.h,
				parent: -1, depth: fb.depth + 1, active: trailSet[fb.id],
				hasKids: fb.hasKids, expanded: false,
				isHeading: fb.m.isHeading, imageHeight: fb.m.imageHeight,
			})
			if fb.id == a.selected {
				picked = idx
			}
			return
		}

		// Emit outer framework container first so nested sub-frames paint cleanly on top.
		sections = append(sections, boardSection{
			id: fb.id, note: fb.note, title: fb.title, count: fb.count,
			x: ox, y: oy, w: fb.w, h: fb.h, depth: fb.depth,
			parentNode: -1, active: trailSet[fb.id],
		})

		// If the framework node itself carries body/image content, emit its seamless full-width lead block inside.
		if fb.introCard != nil {
			m := *fb.introCard
			idx := len(nodes)
			nodes = append(nodes, mapNode{
				id: fb.id, x: ox + boardPad, y: oy + fb.introY + m.h/2, w: m.w, h: m.h,
				parent: -1, depth: fb.depth + 1, active: trailSet[fb.id],
				hasKids: false, expanded: false, isFrameHead: true,
				isHeading: m.isHeading, imageHeight: m.imageHeight,
			})
			if fb.id == a.selected {
				picked = idx
			}
		}

		for i, ch := range fb.kids {
			pos := fb.kidPos[i]
			emit(ch, ox+pos[0], oy+pos[1])
		}
	}

	// Arrange top-level frameworks cleanly in 2D:
	// - 1..3 big top frameworks use clean row-wrap (preserving natural chapter order).
	// - 4+ top frameworks (e.g. multi-topic study maps) use 2D Skyline packing to eliminate huge empty holes!
	if len(topFrames) >= 4 {
		gap := float32(boardFrameGap + 6)
		maxW := float32(boardTopMaxW)
		if len(topFrames) >= 6 {
			maxW = 3560
		}
		for _, tf := range topFrames {
			maxW = max(maxW, tf.w)
		}
		type placedRect struct{ x, y, w, h float32 }
		var placed []placedRect
		for _, tf := range topFrames {
			candX := []float32{0}
			for _, pr := range placed {
				if rx := pr.x + pr.w + gap; rx+tf.w <= maxW+1 {
					candX = append(candX, rx)
				}
			}
			bestX, bestY := float32(0), float32(1e18)
			for _, cx := range candX {
				cy := float32(0)
				for _, pr := range placed {
					if cx < pr.x+pr.w+gap-1 && pr.x < cx+tf.w+gap-1 {
						cy = max(cy, pr.y+pr.h+gap)
					}
				}
				if cy < bestY-1 || (abs(cy-bestY) <= 1 && cx < bestX) {
					bestX, bestY = cx, cy
				}
			}
			emit(tf, bestX, bestY)
			placed = append(placed, placedRect{x: bestX, y: bestY, w: tf.w, h: tf.h})
		}
	} else {
		var rowX, rowY, rowH float32
		for _, tf := range topFrames {
			if rowX > 0 && rowX+boardFrameGap+tf.w > boardTopMaxW {
				rowY += rowH + boardFrameGap + 6
				rowX, rowH = 0, 0
			}
			if rowX > 0 {
				rowX += boardFrameGap + 4
			}
			emit(tf, rowX, rowY)
			rowX += tf.w
			rowH = max(rowH, tf.h)
		}
	}

	if len(nodes) == 0 {
		nodes = []mapNode{{id: rootID, x: 12, y: 74, w: boardCardWidth, h: 52, parent: -1, active: true, isHeading: true}}
	}
	return nodes, sections, picked
}

func mapNodeTrail(nodes []mapNode, idx int) []string {
	var path []string
	for cur := idx; cur >= 0 && cur < len(nodes); cur = nodes[cur].parent {
		path = append(path, nodes[cur].id)
	}
	slices.Reverse(path)
	return path
}

func (a *workspace) selectGraphNode(id string, trail []string) {
	if !a.tree.validTrail(trail, id) {
		trail = a.tree.trail(id)
	}
	wasSelected := id == a.selected && slices.Equal(trail, a.currentTrail())
	if a.settings.LayoutMode() != notebook.LayoutBoard {
		a.scope = ""
		if len(trail) > 1 && len(a.tree.kids[trail[1]]) > 0 {
			a.scope = trail[1]
		}
	}
	if a.expanded != nil && len(a.tree.kids[id]) > 0 && !a.expanded[id] {
		a.expanded[id] = true
	}
	// Select in place without jerking the camera; only a second click opens the detail focus modal.
	a.setNoteAt(id, trail, false)
	if wasSelected {
		a.cardMenuOpen = false
		a.page = detail
		return
	}
	a.page = browse
	a.cardMenuOpen = true
}

type canvasPointer struct {
	active, dragged bool
	startX, startY  float32
	left, top       float32
}

func (a *workspace) graph(c *ui.Context, p palette, width, height float32) {
	width, height = max(1, width), max(1, height)
	a.canvasWidth, a.canvasHeight = width, height
	layoutMode := a.settings.LayoutMode()
	nodes, sections, picked := a.mapLayout()
	world := layoutBounds(nodes, sections)
	if a.scale == 0 {
		fitW := (width - 2*mapInset) / max(1, world.W)
		fitH := (height - 2*mapInset) / max(1, world.H)
		if a.canvasReady {
			// Explicit "适应视图" button fits the entire 2D panorama cleanly on screen.
			a.scale = clamp(min(fitW, fitH), .10, 1)
		} else if layoutMode == notebook.LayoutBoard {
			a.scale = clamp(fitW, .58, 1)
		} else {
			// Initial open stays crisp & readable rather than shrinking huge maps to tiny dots.
			a.scale = clamp(fitW, .64, 1)
		}
	}
	if a.focusSelected && a.canvasReady {
		focused := false
		if picked >= 0 && picked < len(nodes) && nodes[picked].id == a.selected {
			nd := nodes[picked]
			a.panX = width/2 - (nd.x+nd.w/2)*a.scale
			a.panY = height/2 - nd.y*a.scale
			focused = true
		} else {
			for _, sec := range sections {
				if sec.id == a.selected {
					a.panX = width/2 - (sec.x+sec.w/2)*a.scale
					a.panY = mapInset - sec.y*a.scale
					focused = true
					break
				}
			}
		}
		a.focusSelected = false
		if focused {
			a.recenter = false
		}
	}
	if a.recenter || !a.canvasReady {
		if world.W*a.scale < width-2*mapInset {
			a.panX = (width-world.W*a.scale)/2 - world.X*a.scale
		} else {
			a.panX = mapInset - world.X*a.scale
		}
		if world.H*a.scale < height-2*mapInset {
			a.panY = (height-world.H*a.scale)/2 - world.Y*a.scale
		} else if layoutMode != notebook.LayoutBoard && a.scope != "" {
			if idx := slices.IndexFunc(nodes, func(n mapNode) bool { return n.id == a.scope }); idx >= 0 {
				anchor := nodes[idx]
				a.panX = mapInset - anchor.x*a.scale
				a.panY = max(mapInset-world.Y*a.scale, mapInset-(anchor.y-anchor.h/2)*a.scale)
				if anchor.y*a.scale+a.panY > height-mapInset || anchor.y*a.scale+a.panY < mapInset {
					a.panY = height/3 - anchor.y*a.scale
				}
			} else {
				a.panY = mapInset - world.Y*a.scale
			}
		} else {
			// Start cleanly at the top-left of tall "一页纸" mindmaps just like MarginNote.
			a.panY = mapInset - world.Y*a.scale
		}
		a.recenter, a.canvasReady = false, true
	}
	if a.jumpToCard != "" {
		for _, card := range a.drawing.data.Cards {
			if card.ID == a.jumpToCard {
				a.panX = width/2 - (card.X+card.W/2)*a.scale
				a.panY = height/2 - (card.Y+card.H/2)*a.scale
				break
			}
		}
		a.jumpToCard = ""
	}

	a.checkShape(c)
	scale, x, y := a.scale, a.panX, a.panY
	var selectedBox ui.Rect
	var selectedNode mapNode
	hasSelectedBox := false

	ui.Box(c).Fill().Clip().Background(p.canvas).Children(func() {
		drawingActive := a.drawing.canvasToolActive()
		viewport := ui.Box(c.Key("mindmap-viewport")).Fill().Label("无限画布").
			HandleInput(func(ev ui.InputEvent) bool { return a.canvasInput(ev, nodes) })
		if drawingActive {
			viewport.Cursor(ui.CursorCrosshair)
		}
		if a.drawing.tool == lassoTool && !a.splitDragging {
			viewport.Draggable(nil, ui.DraggableOptions{Bounds: &ui.Rect{}, Rigid: true})
		}
		viewport.Draw(func(paint *ui.Painter, rect ui.Rect) {
			if !a.settings.HideGrid {
				drawGrid(paint, rect, p, x, y, scale)
			}
			if len(sections) > 0 {
				drawBoardSections(paint, rect, p, sections, a.selected, x, y, scale)
			}
			drawMapEdges(paint, rect, p, nodes, sections, x, y, scale)
			drawExcerptLinks(paint, rect, p, nodes, a.drawing.data.Cards, x, y, scale)
			a.canvasPenZone(rect, rect.X+x, rect.Y+y, scale)
		}).DrawOver(func(paint *ui.Painter, rect ui.Rect) {
			// Paint canvas ink and lasso selection ABOVE all cards so users can annotate directly on cards.
			a.drawing.drawInk(paint, rect, p, "", rect.X+x, rect.Y+y, scale, &a.drawing.canvas)
			if selected := a.drawing.lasso; len(selected) > 0 {
				shape := inkPath(selected, rect.X+x, rect.Y+y, scale).Close()
				paint.FillPath(shape, p.accent.Alpha(.06))
				paint.StrokePath(shape.Dash(0, 5, 4), 1.5, p.accent)
			}
		}).Children(func() {
			// Framework header bars (clickable bold vertically-centered title + fold toggle in one aligned Row).
			if layoutMode == notebook.LayoutBoard && scale >= .35 {
				for _, sec := range sections {
					if sec.isLeafGroup {
						continue
					}
					headerH := float32(boardHeaderH)
					fontSize := float32(13.5)
					if sec.depth > 0 {
						headerH = boardSubHeaderH
						fontSize = 12.5
					}
					box := ui.Rect{X: x + sec.x*scale, Y: y + sec.y*scale, W: sec.w * scale, H: headerH * scale}
					if !onScreen(box, width, height) {
						continue
					}
					secID, secTitle, secCount := sec.id, sec.title, sec.count
					ct := cardThemeFor(p, sec.note)
					secSelected := secID != "" && secID == a.selected
					if secSelected && !hasSelectedBox {
						selectedBox = box
						selectedNode = mapNode{
							id: secID, x: sec.x, y: sec.y + headerH/2, w: sec.w, h: headerH,
							depth: sec.depth + 1, hasKids: true, expanded: true,
						}
						hasSelectedBox = true
					}
					ui.Row(c.Key("sec-hdr-"+secID)).Absolute().
						Left(box.X).Top(box.Y).Size(box.W, box.H).ZIndex(2).
						IgnoreInput(drawingActive).
						PaddingX(12*scale).Gap(8*scale).AlignItems(ui.Center).Children(func() {
						hdrBtn := ui.ButtonBase(c.Key("sec-title-"+secID)).Grow(1).MinWidth(0).FillHeight().
							AlignItems(ui.Center).Label("框架 " + secTitle).Disabled(a.editing()).FocusRingColor(p.accent).
							HandleInput(func(ev ui.InputEvent) bool {
								return a.cardOverlayInput(ev, box.X+12*scale, box.Y, true)
							}).
							OnClick(func() {
								if a.drawing.strokeConsumed {
									a.drawing.strokeConsumed = false
									return
								}
								if secID != "" {
									a.selectGraphNode(secID, a.tree.trail(secID))
								}
							})
						hdrBtn.Children(func() {
							fg := ct.headerInk
							if secSelected {
								fg = p.accent
							}
							ui.Text(c, secTitle).FontSize(fontSize*scale).FontWeight(700).LetterSpacing(-0.15*scale).
								TextColor(fg).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
						})
						if scale >= .45 && secID != "" && secID != a.tree.root {
							secToggleH := 20 * scale
							secToggleMinW := 34 * scale
							if a.touchUI() {
								secToggleH = max(secToggleH, 24)
								secToggleMinW = max(secToggleMinW, 40)
							}
							toggle := ui.ButtonBase(c.Key("sec-toggle-"+secID)).
								Height(secToggleH).MinWidth(secToggleMinW).PaddingX(max(6*scale, 7)).Gap(2.5*scale).Radius(99).Center().
								Background(p.elevated.Alpha(.88)).Border(max(.75, 1*scale), ct.border).Shrink(0).
								Label("折叠框架" + secTitle).Disabled(a.editing()).
								HandleInput(func(ev ui.InputEvent) bool {
									return a.cardOverlayInput(ev, box.X+box.W-46*scale, box.Y, false)
								}).
								OnClick(func() { a.toggleBranch(secID) })
							if toggle.Hovered() {
								toggle.Background(p.hover)
							}
							toggle.Children(func() {
								ui.Icon(c, icChevronDown).Size(9.5*scale, 9.5*scale).TextColor(ct.headerInk.Alpha(.75)).Shrink(0)
								ui.Text(c, fmt.Sprint(secCount)).FontSize(10 * scale).
									FontWeight(650).FontFeatures("tnum").TextColor(ct.headerInk.Alpha(.75)).SingleLine()
							})
						}
					})
				}
			}

			trail := a.currentTrail()
			for i, node := range nodes {
				box := ui.Rect{X: x + node.x*scale, Y: y + (node.y-node.h/2)*scale, W: node.w * scale, H: node.h * scale}
				if !onScreen(box, width, height) {
					continue
				}
				var path []string
				if node.parent >= 0 || layoutMode != notebook.LayoutBoard {
					path = mapNodeTrail(nodes, i)
				}
				if !a.tree.validTrail(path, node.id) {
					path = a.tree.trail(node.id)
				}
				selected := node.id == a.selected && slices.Equal(path, trail)
				if selected && (!node.isFrameHead || !hasSelectedBox) {
					selectedBox = box
					selectedNode = node
					hasSelectedBox = true
				}
				if layoutMode == notebook.LayoutCompact {
					a.graphNode(c.Key(mapNodeKey{node.parent, node.id}), p, node, box, scale, selected,
						func() { a.selectGraphNode(node.id, path) })
				} else {
					a.richGraphNode(c.Key(mapNodeKey{node.parent, node.id}), p, node, box, scale, selected,
						func() { a.selectGraphNode(node.id, path) })
				}
			}
			for _, card := range a.drawing.data.Cards {
				box := ui.Rect{X: x + card.X*scale, Y: y + card.Y*scale, W: card.W * scale, H: card.H * scale}
				if onScreen(box, width, height) {
					a.canvasCard(c.Key(card.ID), p, card, box, scale)
				}
			}
		})

		if !drawingActive && a.cardMenuOpen && hasSelectedBox && a.page == browse && !a.editing() && len(a.drawing.selected) == 0 {
			a.cardActionBar(c, p, selectedNode, selectedBox, width, height)
		}
		if len(a.drawing.selected) != 0 {
			a.lassoMenu(c, p, width, height)
		}
		if a.drawing.dockOpen || a.mode == splitMode {
			a.inkDock(c, p, a.mode, width)
		} else {
			ui.Box(c).Absolute().Bottom(16).Left(16).ZIndex(5).Children(func() {
				icons := dockIcons[0]
				if p.dark {
					icons = dockIcons[1]
				}
				fabSize := float32(34)
				if a.touchUI() {
					fabSize = 44
				}
				designToolbarButton(c, p, icons[1], "打开笔盒", fabSize, true, a.editing(), func() {
					a.drawing.dockOpen, a.drawing.tool, a.drawing.penBrowseMode = true, penTool, false
				})
			})
		}
		a.minimap(c, p, nodes, sections, world, width, height)
	})
}

func (a *workspace) cardActionBar(c *ui.Context, p palette, node mapNode, box ui.Rect, width, height float32) {
	n := a.tree.notes[node.id]
	imgs := noteCardImages(n)
	touch := a.touchUI()
	barH, btnH, btnPadX, fontSize, icSize := float32(34), float32(26), float32(7), float32(11.5), float32(12.5)
	estW := float32(256)
	if touch {
		barH, btnH, btnPadX, fontSize, icSize = 42, 34, 10, 12.5, 14
		estW = 304
	}
	if len(imgs) > 0 {
		if touch {
			estW += 62
		} else {
			estW += 52
		}
	}
	if node.hasKids && node.depth > 0 {
		if touch {
			estW += 64
		} else {
			estW += 54
		}
	}
	barX := clamp(box.X+(box.W-estW)/2, 12, max(12, width-estW-12))
	barY := box.Y - (barH + 8)
	if barY < 10 {
		barY = clamp(box.Y+box.H+8, 12, max(12, height-barH-12))
	}

	ui.Row(c.Key("card-action-bar")).Absolute().Left(barX).Top(barY).ZIndex(7).
		Height(barH).PaddingX(6).Gap(2).AlignItems(ui.Center).Radius(10).
		Background(p.elevated).Border(1, p.border).
		InsetShadow(0, 1, 0, 0, p.specular).
		Shadow(0, 8, 22, -4, p.modalShadow).Children(func() {
		actionBtn := func(key, label string, icon *ui.SVG, danger bool, onClick func()) {
			btn := ui.ButtonBase(c.Key(key)).Height(btnH).PaddingX(btnPadX).Gap(4).Radius(6).Center().
				Label(label).Disabled(a.busy).OnClick(onClick)
			fg := p.ink
			icFg := p.secondary
			if danger {
				fg, icFg = p.danger, p.danger
			}
			if btn.Hovered() {
				if danger {
					btn.Background(p.dangerWash)
				} else {
					btn.Background(p.hover)
				}
			}
			btn.Children(func() {
				if icon != nil {
					ui.Icon(c, icon).Size(icSize, icSize).TextColor(icFg).Shrink(0)
				}
				if label != "" {
					ui.Text(c, label).FontSize(fontSize).FontWeight(550).TextColor(fg).SingleLine()
				}
			})
		}

		actionBtn("act-detail", "详情", icBook, false, func() {
			a.cardMenuOpen = false
			a.page = detail
		})
		actionBtn("act-edit", "编辑", icEdit, false, a.startEditNote)
		actionBtn("act-child", "子卡片", icPlus, false, a.startAddChild)
		if len(imgs) > 0 {
			actionBtn("act-img", "原图", icPage, false, func() {
				if a.mediaBitmap(imgs[0]) != nil {
					a.preview = imgs[0]
				}
			})
		}
		if node.hasKids && node.depth > 0 {
			foldLabel := "折叠"
			foldIcon := icChevronDown
			if !node.expanded {
				foldLabel = "展开"
				foldIcon = icChevronRight
			}
			actionBtn("act-fold", foldLabel, foldIcon, false, func() {
				a.toggleBranch(node.id)
			})
		}
		if n.ID != "" {
			ui.Box(c).Width(1).Height(14).Background(p.rule).Margin(0, 2)
			actionBtn("act-del", "", icTrash, true, a.confirmDeleteNote)
		}
	})
}

func drawBoardSections(paint *ui.Painter, rect ui.Rect, p palette, sections []boardSection, selectedID string, ox, oy, scale float32) {
	for _, sec := range sections {
		box := ui.Rect{
			X: rect.X + ox + sec.x*scale,
			Y: rect.Y + oy + sec.y*scale,
			W: sec.w * scale,
			H: sec.h * scale,
		}
		if !onScreen(ui.Rect{X: box.X - rect.X, Y: box.Y - rect.Y, W: box.W, H: box.H}, rect.W, rect.H) {
			continue
		}
		ct := cardThemeFor(p, sec.note)

		if sec.isLeafGroup {
			// Subtle 2D leaf-grid group frame in cards mode (eliminates spaghetti lines!).
			radius := 12 * scale
			fill := p.shell.Alpha(.55)
			border := p.border.Alpha(.65)
			if ct.tinted {
				fill = ct.bodyBg.Alpha(.48)
				border = ct.border.Alpha(.55)
			}
			if sec.active {
				border = p.accent.Alpha(.45)
			}
			paint.Fill(box, fill, radius)
			paint.StrokePath(roundedRectPath(box, radius), max(.75, 1*scale), border)
			continue
		}

		// True MarginNote Nested Framework Container!
		radius := max(3, (14-float32(min(sec.depth, 3))*2)*scale)
		headerH := float32(boardHeaderH) * scale
		if sec.depth > 0 {
			headerH = float32(boardSubHeaderH) * scale
		}

		bodyFill := p.shell.Alpha(.9)
		if ct.tinted {
			alpha := float32(.62)
			if sec.depth > 0 {
				alpha = .82
			}
			bodyFill = ct.bodyBg.Alpha(alpha)
		} else if sec.depth%2 == 1 {
			bodyFill = p.surface.Alpha(.88)
		}
		paint.Fill(box, bodyFill, radius)

		// Colored MarginNote framework header band.
		headerBox := ui.Rect{X: box.X, Y: box.Y, W: box.W, H: headerH}
		paint.Fill(headerBox, ct.headerBg, radius)
		if headerH > radius {
			paint.Fill(ui.Rect{X: box.X, Y: box.Y + headerH - radius, W: box.W, H: radius}, ct.headerBg, 0)
		}
		frameBorder := ct.border
		strokeW := max(.75, 1.25*scale)
		if selectedID != "" && sec.id == selectedID {
			frameBorder = p.accent
			strokeW = max(1.5, 2*scale)
		}
		paint.Line(box.X, box.Y+headerH, box.X+box.W, box.Y+headerH, max(.6, 1*scale), ct.border.Alpha(.7))
		paint.StrokePath(roundedRectPath(box, radius), strokeW, frameBorder)

		if scale >= .14 && scale < .35 {
			fontSize := 13.5 * scale
			if sec.depth > 0 {
				fontSize = 12.5 * scale
			}
			fs := max(3.5, fontSize)
			textY := box.Y + max(1, (headerH-fs*1.22)/2)
			title := sec.title
			maxChars := max(6, int((sec.w-42)/14))
			runes := []rune(title)
			if len(runes) > maxChars {
				title = string(runes[:maxChars]) + "…"
			}
			paint.Text(box.X+12*scale, textY, title, fs, ct.headerInk)
		}
	}
}

func roundedRectPath(r ui.Rect, radius float32) *ui.Path {
	rad := min(radius, min(r.W, r.H)/2)
	return (&ui.Path{}).
		MoveTo(r.X+rad, r.Y).
		LineTo(r.X+r.W-rad, r.Y).
		QuadTo(r.X+r.W, r.Y, r.X+r.W, r.Y+rad).
		LineTo(r.X+r.W, r.Y+r.H-rad).
		QuadTo(r.X+r.W, r.Y+r.H, r.X+r.W-rad, r.Y+r.H).
		LineTo(r.X+rad, r.Y+r.H).
		QuadTo(r.X, r.Y+r.H, r.X, r.Y+r.H-rad).
		LineTo(r.X, r.Y+rad).
		QuadTo(r.X, r.Y, r.X+rad, r.Y).
		Close()
}

// pinchZoomCanvas handles both zoom scaling and simultaneous two-finger midpoint panning
// (Android multi-touch pinches shift midpoint between frames without sending separate scroll events).
func (a *workspace) pinchZoomCanvas(ev ui.InputEvent, sx, sy float32) {
	if !a.lastZoomAt.IsZero() && !ev.Time.IsZero() {
		dt := ev.Time.Sub(a.lastZoomAt)
		if dt >= 0 && dt < 150*time.Millisecond {
			dx, dy := sx-a.lastZoomX, sy-a.lastZoomY
			if abs(dx) < 160 && abs(dy) < 160 {
				a.panX += dx
				a.panY += dy
			}
		}
	}
	a.zoomAt(a.scale*ev.Scale, sx, sy)
	a.lastZoomX, a.lastZoomY, a.lastZoomAt = sx, sy, ev.Time
	a.canvasPointer = canvasPointer{}
}

// cardOverlayInput handles scroll/pinch zoom, stylus ink, and optional mouse drag-pan
// on interactive buttons inside the mindmap viewport so ButtonBase clicks work reliably
// without breaking trackpad scrolling, pinching, or pen drawing over cards.
func (a *workspace) cardOverlayInput(ev ui.InputEvent, ox, oy float32, allowPan bool) bool {
	if a.editing() {
		return false
	}
	scale := a.scale
	if scale <= 0 {
		return false
	}
	sx, sy := ox+ev.X, oy+ev.Y
	point := inkPoint{X: (sx - a.panX) / scale, Y: (sy - a.panY) / scale, Pressure: ev.Pressure}
	if a.inkInput(ev, "", point, scale, true) {
		return true
	}
	switch ev.Kind {
	case ui.InputZoom:
		a.pinchZoomCanvas(ev, sx, sy)
		return true
	case ui.InputScroll:
		a.lastZoomAt = time.Time{}
		if ev.Mods&(ui.Ctrl|ui.Super) != 0 {
			a.zoomAt(scale*float32(math.Exp(float64(-ev.DY)/260)), sx, sy)
		} else {
			a.panX -= ev.DX
			a.panY -= ev.DY
		}
		return true
	}
	if !allowPan {
		return false
	}
	g := &a.canvasPointer
	switch ev.Kind {
	case ui.InputPointerDown:
		a.lastZoomAt = time.Time{}
		a.drawing.strokeConsumed = false
		if ev.Button == 0 {
			*g = canvasPointer{active: true, startX: sx, startY: sy, left: a.panX, top: a.panY}
		}
		return false
	case ui.InputPointerMove:
		if !g.active {
			return false
		}
		dx, dy := sx-g.startX, sy-g.startY
		if !g.dragged && abs(dx)+abs(dy) > 5 {
			g.dragged = true
		}
		if g.dragged {
			a.panX, a.panY = g.left+dx, g.top+dy
			return true
		}
		return false
	case ui.InputPointerUp:
		if !g.active {
			return false
		}
		wasDragged := g.dragged
		*g = canvasPointer{}
		return wasDragged
	}
	return false
}

func (a *workspace) canvasInput(ev ui.InputEvent, nodes []mapNode) bool {
	if a.editing() {
		return false
	}
	scale := a.scale
	if scale <= 0 {
		return false
	}
	point := inkPoint{X: (ev.X - a.panX) / scale, Y: (ev.Y - a.panY) / scale, Pressure: ev.Pressure}
	if a.inkInput(ev, "", point, scale, true) {
		return true
	}
	g := &a.canvasPointer
	switch ev.Kind {
	case ui.InputZoom:
		a.pinchZoomCanvas(ev, ev.X, ev.Y)
	case ui.InputScroll:
		a.lastZoomAt = time.Time{}
		if ev.Mods&(ui.Ctrl|ui.Super) != 0 {
			a.zoomAt(scale*float32(math.Exp(float64(-ev.DY)/260)), ev.X, ev.Y)
		} else {
			a.panX -= ev.DX
			a.panY -= ev.DY
		}
	case ui.InputPointerDown:
		a.lastZoomAt = time.Time{}
		if ev.Button != 0 && ev.Button != 2 {
			return false
		}
		*g = canvasPointer{active: true, startX: ev.X, startY: ev.Y, left: a.panX, top: a.panY}
	case ui.InputPointerMove:
		if !g.active {
			return false
		}
		dx, dy := ev.X-g.startX, ev.Y-g.startY
		g.dragged = g.dragged || abs(dx)+abs(dy) > 4
		if g.dragged {
			a.panX, a.panY = g.left+dx, g.top+dy
		}
	case ui.InputPointerUp:
		if !g.active {
			return false
		}
		click := !ev.Canceled && !g.dragged && ev.Button == 0
		*g = canvasPointer{}
		if click {
			if !a.drawing.canvasToolActive() {
				for i := len(nodes) - 1; i >= 0; i-- {
					n := nodes[i]
					if (ui.Rect{X: n.x, Y: n.y - n.h/2, W: n.w, H: n.h}).Contains(point.X, point.Y) {
						a.selectGraphNode(n.id, mapNodeTrail(nodes, i))
						return true
					}
				}
			}
			a.cardMenuOpen = false
			a.drawing.lasso, a.drawing.selected = nil, nil
		}
	default:
		return false
	}
	return true
}

func (a *workspace) zoom(scale float32) {
	a.scale, a.recenter = scale, true
}

func (a *workspace) zoomAt(scale, x, y float32) {
	scale = clamp(scale, .10, 3)
	if a.scale == 0 {
		a.zoom(scale)
		return
	}
	factor := scale / a.scale
	a.panX = x - (x-a.panX)*factor
	a.panY = y - (y-a.panY)*factor
	a.scale = scale
}

// drawGrid renders a zoom-responsive dot grid (octave scaling like Figma/MarginNote).
func drawGrid(paint *ui.Painter, rect ui.Rect, p palette, panX, panY, scale float32) {
	step := 28 * max(.15, scale)
	for step < 18 {
		step *= 2
	}
	for step > 44 {
		step /= 2
	}
	dotSize := clamp(1.5*scale, 1.1, 2.2)
	x0 := float32(math.Mod(float64(panX), float64(step)))
	if x0 < 0 {
		x0 += step
	}
	y0 := float32(math.Mod(float64(panY), float64(step)))
	if y0 < 0 {
		y0 += step
	}
	for y := y0; y < rect.H; y += step {
		for x := x0; x < rect.W; x += step {
			paint.Fill(ui.Rect{X: rect.X + x, Y: rect.Y + y, W: dotSize, H: dotSize}, p.dot, dotSize/2)
		}
	}
}

func drawMapEdges(paint *ui.Painter, rect ui.Rect, p palette, nodes []mapNode, sections []boardSection, ox, oy, scale float32) {
	baseW := max(.85, 1.4*scale)
	activeW := max(1.3, 2.2*scale)

	for _, active := range []bool{false, true} {
		// Individual branch edges (skipping cards grouped inside a 2D leaf-grid box).
		for _, node := range nodes {
			if node.parent < 0 || node.inGroup || node.active != active {
				continue
			}
			parent := nodes[node.parent]
			x1, x2 := ox+(parent.x+parent.w)*scale, ox+node.x*scale
			y1, y2 := oy+parent.y*scale, oy+node.y*scale
			if !onScreen(ui.Rect{X: min(x1, x2), Y: min(y1, y2), W: max(2, abs(x2-x1)), H: max(2, abs(y1-y2))}, rect.W, rect.H) {
				continue
			}
			mid := (x1 + x2) / 2
			path := (&ui.Path{}).MoveTo(rect.X+x1, rect.Y+y1).
				CubeTo(rect.X+mid, rect.Y+y1, rect.X+mid, rect.Y+y2, rect.X+x2, rect.Y+y2)
			color, weight := p.edge, baseW
			if active {
				color, weight = p.accent, activeW
			}
			paint.StrokePath(path, weight, color)
		}
		// Single clean trunk edge from parent node into each 2D leaf-grid group box.
		for _, sec := range sections {
			if !sec.isLeafGroup || sec.parentNode < 0 || sec.parentNode >= len(nodes) || sec.active != active {
				continue
			}
			parent := nodes[sec.parentNode]
			x1, x2 := ox+(parent.x+parent.w)*scale, ox+sec.x*scale
			y1, y2 := oy+parent.y*scale, oy+(sec.y+sec.h/2)*scale
			if !onScreen(ui.Rect{X: min(x1, x2), Y: min(y1, y2), W: max(2, abs(x2-x1)), H: max(2, abs(y1-y2))}, rect.W, rect.H) {
				continue
			}
			mid := (x1 + x2) / 2
			path := (&ui.Path{}).MoveTo(rect.X+x1, rect.Y+y1).
				CubeTo(rect.X+mid, rect.Y+y1, rect.X+mid, rect.Y+y2, rect.X+x2, rect.Y+y2)
			color, weight := p.edge, baseW
			if active {
				color, weight = p.accent, activeW
			}
			paint.StrokePath(path, weight, color)
		}
	}
}

func drawExcerptLinks(paint *ui.Painter, rect ui.Rect, p palette, nodes []mapNode, cards []notebook.CanvasCard, ox, oy, scale float32) {
	for _, card := range cards {
		if card.Excerpt == nil || card.Anchor == "" {
			continue
		}
		i := slices.IndexFunc(nodes, func(n mapNode) bool { return n.id == card.Anchor && n.active })
		if i < 0 {
			i = slices.IndexFunc(nodes, func(n mapNode) bool { return n.id == card.Anchor })
		}
		if i < 0 {
			continue
		}
		n := nodes[i]
		sx, ex := rect.X+ox+(n.x+n.w)*scale, rect.X+ox+card.X*scale
		sy, ey := rect.Y+oy+n.y*scale, rect.Y+oy+(card.Y+card.H/2)*scale
		mid := (sx + ex) / 2
		edge := (&ui.Path{}).MoveTo(sx, sy).CubeTo(mid, sy, mid, ey, ex, ey)
		paint.StrokePath(edge, max(1, 1.5*scale), p.accent)
	}
}

func onScreen(b ui.Rect, width, height float32) bool {
	return b.X+b.W >= 0 && b.Y+b.H >= 0 && b.X <= width && b.Y <= height
}

func layoutBounds(nodes []mapNode, sections []boardSection) ui.Rect {
	if len(nodes) == 0 && len(sections) == 0 {
		return ui.Rect{}
	}
	var left, top, right, bottom float32
	init := false
	include := func(x, y, w, h float32) {
		if !init {
			left, top, right, bottom, init = x, y, x+w, y+h, true
			return
		}
		left, top = min(left, x), min(top, y)
		right, bottom = max(right, x+w), max(bottom, y+h)
	}
	for _, n := range nodes {
		include(n.x, n.y-n.h/2, n.w, n.h)
	}
	for _, s := range sections {
		include(s.x, s.y, s.w, s.h)
	}
	return ui.Rect{X: left, Y: top, W: right - left, H: bottom - top}
}

func (a *workspace) graphNode(c *ui.Context, p palette, node mapNode, box ui.Rect, scale float32, selected bool, click func()) {
	n := a.tree.notes[node.id]
	title := noteTitle(n)
	ct := cardThemeFor(p, n)
	item := ui.ButtonBase(c).Absolute().Left(box.X).Top(box.Y).Size(box.W, box.H).Clip().
		IgnoreInput(a.drawing.canvasToolActive()).
		Padding(0, 10*scale, 0, 0).Radius(9*scale).AlignItems(ui.Center).Gap(6*scale).
		Label(title).Disabled(a.editing()).FocusRingColor(p.accent).
		HandleInput(func(ev ui.InputEvent) bool {
			return a.cardOverlayInput(ev, box.X, box.Y, true)
		}).OnClick(func() {
		if a.drawing.strokeConsumed {
			a.drawing.strokeConsumed = false
			return
		}
		click()
	})
	bg, border, fg := p.elevated, p.border, p.ink
	if ct.tinted {
		bg, border = ct.bodyBg, ct.border
	}
	if node.active || selected {
		bg, border, fg = p.tint, p.tintBorder, p.accent
	}
	if selected {
		border = p.accent
	} else if item.Hovered() && !node.active {
		bg = p.hover
	}
	item.Background(bg).Border(max(.75, 1.1*scale), border).Shadow(0, 1*scale, 3*scale, 0, p.shadowSoft)
	item.Children(func() {
		stripColor := ct.accent
		if !ct.tinted {
			stripColor = p.rule
			if node.active || selected {
				stripColor = p.accent
			}
		}
		ui.Box(c).Width(max(1.5, 4*scale)).FillHeight().Background(stripColor).Shrink(0)
		ui.Text(c, title).FontSize(max(3.5, 12.5*scale)).FontWeight(550).
			TextColor(fg).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
		if count := len(a.tree.kids[node.id]); node.parent >= 0 && count > 0 && scale >= .5 {
			ui.Text(c, fmt.Sprint(count)).FontSize(10 * scale).TextColor(p.quiet).Shrink(0)
		}
	})
}

func (a *workspace) richGraphNode(c *ui.Context, p palette, node mapNode, box ui.Rect, scale float32, selected bool, click func()) {
	n := a.tree.notes[node.id]
	title := noteTitle(n)
	rawTitle := strings.TrimSpace(n.Title)
	bodyText := noteCardText(n)
	ct := cardThemeFor(p, n)
	count := len(a.visibleKids(node.id))
	hasPage := n.Page != nil && *n.Page > 0
	showFold := node.hasKids && node.depth > 0 && scale >= .48

	border := ct.border
	borderW := max(.75, 1.2*scale)
	if node.depth == 0 {
		if !selected {
			border = p.accent.Alpha(.55)
		}
		borderW = max(1, 1.5*scale)
	}
	if node.isFrameHead {
		// Seamless lead block inside its own framework container (the outer framework shows the selection ring).
		border = ct.border.Alpha(.28)
		borderW = max(.6, .9*scale)
	} else if selected {
		// Keep borderW invariant so selecting a card never shrinks inner text width or causes line reflow.
		border = p.accent
	}

	bg := ct.bodyBg
	if node.isHeading {
		bg = ct.headerBg
	} else if node.isFrameHead {
		if p.dark {
			bg = p.surface.Alpha(.65)
		} else {
			bg = p.surface.Alpha(.88)
		}
	}
	if selected && node.isHeading && !ct.tinted {
		bg = p.tint
	}

	radius := 11 * scale
	if node.isFrameHead {
		radius = 8 * scale
	}
	card := ui.Box(c).Absolute().Left(box.X).Top(box.Y).Size(box.W, box.H).Radius(radius).Clip().
		IgnoreInput(a.drawing.canvasToolActive()).
		Background(bg).Border(borderW, border)
	if selected && !node.isFrameHead {
		// Draw extra ring thickness outward via zero-blur spread shadow so inner layout stays pixel-identical.
		card.Shadow(0, 0, 0, max(.8, 1*scale), p.accent).
			Shadow(0, 6*scale, 18*scale, -4*scale, p.accent.Alpha(.28))
	} else if !node.isFrameHead {
		card.Shadow(0, 2*scale, 7*scale, -1*scale, p.shadowSoft)
	}

	card.Children(func() {
		btn := ui.ButtonBase(c.Key("card-body")).Fill().Column().AlignItems(ui.Stretch).
			Label(title).Disabled(a.editing()).FocusRingColor(p.accent).
			HandleInput(func(ev ui.InputEvent) bool {
				return a.cardOverlayInput(ev, box.X, box.Y, true)
			}).OnClick(func() {
			if a.drawing.strokeConsumed {
				a.drawing.strokeConsumed = false
				return
			}
			click()
		})

		renderPageBadge := func() {
			if !hasPage || scale < .5 {
				return
			}
			hasDocs := len(a.tree.book.Documents) > 0
			label := fmt.Sprintf("P.%d", *n.Page)
			if hasDocs {
				label += " ↗"
			}
			badgeBg := p.elevated.Alpha(.85)
			badgeBorder := ct.border.Alpha(.65)
			badgeFg := p.quiet
			if hasDocs {
				badgeBg = p.tint
				badgeBorder = p.tintBorder
				badgeFg = p.accent
			}
			badgeH := 17 * scale
			badgePadX := 5.5 * scale
			if a.touchUI() {
				badgeH = max(badgeH, 22)
				badgePadX = max(badgePadX, 7)
			}
			pb := ui.ButtonBase(c.Key("page-badge")).Height(badgeH).PaddingX(badgePadX).Radius(99).Center().Shrink(0).
				Label(label).Disabled(a.editing()).FocusRingColor(p.accent).
				Background(badgeBg).Border(max(.75, 1*scale), badgeBorder).
				HandleInput(func(ev ui.InputEvent) bool {
					return a.cardOverlayInput(ev, box.X+box.W-50*scale, box.Y+7*scale, false)
				}).
				OnClick(func() { a.jumpToNotePDF(n) })
			if pb.Hovered() && !a.editing() {
				pb.Background(p.hover)
			}
			pb.Children(func() {
				ui.Text(c, label).FontSize(9.5 * scale).
					FontWeight(600).FontFeatures("tnum").TextColor(badgeFg).SingleLine()
			})
		}

		btn.Children(func() {
			if node.isHeading {
				ui.Row(c).Fill().AlignItems(ui.Center).Children(func() {
					accentBar := ct.accent
					if node.depth == 0 {
						accentBar = p.accent
					}
					ui.Box(c).Width(max(1.8, 4.5*scale)).FillHeight().Background(accentBar).Shrink(0)
					padR := 12 * scale
					if showFold {
						padR = 46 * scale
					}
					ui.Row(c).Grow(1).MinWidth(0).FillHeight().AlignItems(ui.Center).
						Padding(8*scale, padR, 8*scale, 10*scale).Gap(5 * scale).Children(func() {
						titleColor := ct.headerInk
						if selected {
							titleColor = p.accent
						}
						fontSize := 13.5 * scale
						if node.depth == 0 {
							fontSize = 14.5 * scale
						}
						ui.Text(c, title).FontSize(max(3.5, fontSize)).FontWeight(700).LetterSpacing(-0.15 * scale).
							LineHeight(1.3).TextColor(titleColor).MaxLines(2).Ellipsis("…").Grow(1).MinWidth(0)
						renderPageBadge()
					})
				})
				return
			}

			// Full MarginNote Content Card: Proportionally Scaled Colored Header + Body + Inline Images.
			if !node.isFrameHead && (rawTitle != "" || hasPage) {
				padR := 11 * scale
				if showFold {
					padR = 46 * scale
				}
				ui.Row(c).FillWidth().Shrink(0).AlignItems(ui.Center).
					Padding(7*scale, padR, 7*scale, 11*scale).Gap(6 * scale).
					Background(ct.headerBg).Children(func() {
					headerInk := ct.headerInk
					if selected {
						headerInk = p.accent
					}
					ui.Text(c, title).FontSize(max(3.5, 13*scale)).FontWeight(650).LetterSpacing(-0.1 * scale).
						LineHeight(1.32).TextColor(headerInk).MaxLines(2).Ellipsis("…").Grow(1).MinWidth(0)
					renderPageBadge()
				})
				ui.Box(c).FillWidth().Height(max(.6, 1*scale)).Shrink(0).Background(ct.border.Alpha(.5))
			} else if !node.isFrameHead {
				ui.Box(c).FillWidth().Height(max(2, 5*scale)).Shrink(0).Background(ct.accent.Alpha(.8))
			}

			padX := 11 * scale
			padY := 9 * scale
			innerW := max(16, box.W-2*padX)
			ui.Column(c).FillWidth().Grow(1).MinHeight(0).Padding(padY, padX).Gap(7 * scale).Children(func() {
				imgIdx := 0
				renderImg := func(ref string) {
					if ref == "" {
						return
					}
					dispH := float32(64) * scale
					if imgIdx < len(node.imageHeight) {
						dispH = node.imageHeight[imgIdx] * scale
					}
					imgIdx++
					bmp := a.mediaBitmap(ref)
					framePad := 4.5 * scale
					frameW := max(12, innerW)
					imgW := max(8, frameW-2*framePad)
					ui.Box(c.Key("img-"+ref)).Width(frameW).Shrink(0).
						Padding(framePad).Radius(6 * scale).Center().
						Background(ui.Hex("#ffffff")).Border(max(.5, 1*scale), ct.border.Alpha(.45)).Children(func() {
						if bmp != nil {
							ui.Image(c, bmp).Size(imgW, max(6, dispH)).Fit(ui.Contain)
						} else {
							ui.Box(c).Size(imgW, max(6, dispH))
						}
					})
				}

				if n.Clip != "" {
					renderImg(n.Clip)
				}

				renderTextBlock := func(key string, blk notebook.ContentBlock, fallbackTxt string) {
					cleaned := cleanCardBody(fallbackTxt)
					if cleaned == "" && len(blk.Spans) > 0 {
						cleaned = cleanCardBody(blockPlainText(blk))
					}
					if cleaned == "" {
						return
					}
					if scale < .24 {
						// Ultra-zoomed-out panorama: clean proportional semantic tint lines (no ugly clipped text).
						lines := wrapLineCount(cleaned, max(110, node.w-22), 12.2)
						lineCount := clampInt(lines, 1, 6)
						ui.Column(c.Key("mini-"+key)).FillWidth().Gap(3.5 * scale).Shrink(0).Children(func() {
							for li := 0; li < lineCount; li++ {
								wFrac := float32(0.92)
								if li == lineCount-1 && lineCount > 1 {
									wFrac = 0.62
								}
								ui.Box(c).Width(innerW * wFrac).Height(max(1.5, 5*scale)).Radius(2 * scale).
									Background(p.ink.Alpha(.18))
							}
						})
						return
					}
					renderRichBlock(c.Key(key), p, blk, cleaned, max(3.2, 12.2*scale), 1.48, false)
				}

				useBlocks := n.BlocksCoverBody(bodyText)
				if !useBlocks && bodyText != "" {
					renderTextBlock("body", notebook.ContentBlock{Text: bodyText}, bodyText)
				}

				renderedBlocks := 0
				renderDivider := func(bi int) {
					if useBlocks && len(n.Blocks) > 1 && renderedBlocks > 0 {
						ui.Box(c.Key(fmt.Sprintf("div-%d", bi))).FillWidth().
							Height(max(.6, 1*scale)).Shrink(0).
							Background(ct.border.Alpha(.38))
					}
				}

				for bi, blk := range n.Blocks {
					if blk.Image != "" {
						renderDivider(bi)
						renderImg(blk.Image)
						renderedBlocks++
					} else if useBlocks && blockPlainText(blk) != "" {
						renderDivider(bi)
						renderTextBlock(fmt.Sprintf("blk-%d", bi), blk, blk.Text)
						renderedBlocks++
					}
				}
			})
		})

		// MarginNote branch expand/collapse toggle pill scaled smoothly with canvas zoom.
		if showFold {
			icon := icChevronRight
			toggleBg := p.tint
			toggleBorder := p.accent.Alpha(.55)
			toggleFg := p.accent
			labelTxt := fmt.Sprintf("+%d", count)
			if node.expanded {
				icon = icChevronDown
				toggleBg = p.elevated.Alpha(.92)
				toggleBorder = ct.border
				toggleFg = p.secondary
				labelTxt = fmt.Sprint(count)
			}
			pillH := 20 * scale
			pillPadX := 6 * scale
			if a.touchUI() {
				pillH = max(pillH, 24)
				pillPadX = max(pillPadX, 7.5)
			}
			topOffset := 7 * scale
			if node.isHeading {
				topOffset = max(2, (box.H-pillH)/2)
			}
			toggle := ui.ButtonBase(c.Key("toggle-"+node.id)).Absolute().
				Right(7*scale).Top(topOffset).ZIndex(3).
				Height(pillH).PaddingX(pillPadX).Gap(2.5*scale).Radius(99).Center().
				Background(toggleBg).Border(max(.75, 1*scale), toggleBorder).
				Label("展开或折叠" + title).Disabled(a.editing()).
				HandleInput(func(ev ui.InputEvent) bool {
					return a.cardOverlayInput(ev, box.X+box.W-40*scale, box.Y+topOffset, false)
				}).
				OnClick(func() { a.toggleBranch(node.id) })
			if toggle.Hovered() {
				toggle.Background(p.hover)
			}
			toggle.Children(func() {
				ui.Icon(c, icon).Size(10*scale, 10*scale).TextColor(toggleFg).Shrink(0)
				ui.Text(c, labelTxt).FontSize(10 * scale).
					FontWeight(650).FontFeatures("tnum").TextColor(toggleFg).SingleLine()
			})
		}
	})
}

func (a *workspace) minimap(c *ui.Context, p palette, nodes []mapNode, sections []boardSection, extent ui.Rect, width, height float32) {
	miniW, miniH, inset := float32(126), float32(82), float32(9)
	if width < 700 {
		miniW, miniH, inset = 104, 66, 7
	}
	if width < 380 && (a.drawing.dockOpen || a.mode == splitMode) {
		return
	}
	cam := ui.Rect{X: -a.panX / a.scale, Y: -a.panY / a.scale, W: width / a.scale, H: height / a.scale}
	extent = unionRect(extent, cam)
	for _, card := range a.drawing.data.Cards {
		extent = unionRect(extent, ui.Rect{X: card.X, Y: card.Y, W: card.W, H: card.H})
	}
	ink := &a.drawing.canvas.bounds
	ink.update(a.drawing.data.Ink)
	if len(ink.boxes) > 0 {
		extent = unionRect(extent, ink.extent)
	}
	k := min((miniW-2*inset)/max(1, extent.W), (miniH-2*inset)/max(1, extent.H))
	x0, y0 := (miniW-extent.W*k)/2, (miniH-extent.H*k)/2
	mapRect := func(r ui.Rect) ui.Rect {
		return ui.Rect{X: x0 + (r.X-extent.X)*k, Y: y0 + (r.Y-extent.Y)*k, W: max(1, r.W*k), H: max(1, r.H*k)}
	}
	center := func(x, y float32) {
		a.panX = width/2 - (extent.X+(x-x0)/k)*a.scale
		a.panY = height/2 - (extent.Y+(y-y0)/k)*a.scale
	}
	button := ui.ButtonBase(c.Key("minimap")).Absolute().Right(16).Bottom(16).ZIndex(5).Size(miniW, miniH).
		Radius(10).Background(p.elevated).Border(1, p.border).Shadow(0, 3, 12, -2, p.shadow).
		Label("导航缩略图，定位到画布中心").OnClick(func() { a.recenter = true }).
		HandleInput(func(ev ui.InputEvent) bool {
			if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
				center(ev.X, ev.Y)
				return true
			}
			return false
		})
	button.Draw(func(paint *ui.Painter, rect ui.Rect) {
		draw := func(r ui.Rect, color ui.Color, radius float32) {
			r = mapRect(r)
			r.X, r.Y = r.X+rect.X, r.Y+rect.Y
			paint.Fill(r, color, radius)
		}
		for _, sec := range sections {
			col := p.rule.Alpha(.45)
			if !sec.isLeafGroup {
				ct := cardThemeFor(p, sec.note)
				if ct.tinted {
					col = ct.headerBg.Alpha(.8)
				}
			}
			draw(ui.Rect{X: sec.x, Y: sec.y, W: sec.w, H: sec.h}, col, 2)
		}
		for _, n := range nodes {
			ct := cardThemeFor(p, a.tree.notes[n.id])
			col := p.quiet.Alpha(.55)
			if ct.tinted {
				col = ct.accent.Alpha(.55)
			}
			if n.active {
				col = p.accent.Alpha(.82)
			}
			draw(ui.Rect{X: n.x, Y: n.y - n.h/2, W: n.w, H: n.h}, col, 1.5)
		}
		for _, card := range a.drawing.data.Cards {
			draw(ui.Rect{X: card.X, Y: card.Y, W: card.W, H: card.H}, p.accent.Alpha(.55), 1.5)
		}
		for _, bounds := range ink.boxes {
			draw(bounds, p.quiet.Alpha(.4), 1.5)
		}
		r := mapRect(cam)
		r.X, r.Y = r.X+rect.X, r.Y+rect.Y
		paint.Fill(r, p.accent.Alpha(.12), 2)
		paint.StrokePath(rectPath(r), 1, p.accent)
	})
}

func rectPath(b ui.Rect) *ui.Path {
	return (&ui.Path{}).MoveTo(b.X, b.Y).LineTo(b.X+b.W, b.Y).
		LineTo(b.X+b.W, b.Y+b.H).LineTo(b.X, b.Y+b.H).Close()
}

func (a *workspace) lassoMenu(c *ui.Context, p palette, width, height float32) {
	b := inkBounds(a.drawing.lasso)
	touch := a.touchUI()
	barH, btnH, btnPadX, fontSize, menuW := float32(30), float32(24), float32(4), float32(11.5), float32(190)
	if touch {
		barH, btnH, btnPadX, fontSize, menuW = 40, 32, 9, 12.5, 236
	}
	x := clamp(a.panX+(b.X+b.W/2)*a.scale-menuW/2, 4, max(4, width-menuW))
	y := clamp(a.panY+b.Y*a.scale-(barH+6), 4, max(4, height-barH-4))
	ui.Row(c).Absolute().Left(x).Top(y).ZIndex(6).Height(barH).PaddingX(6).Gap(3).
		AlignItems(ui.Center).Background(p.elevated).Radius(10).Border(1, p.border).
		Shadow(0, 4, 14, -2, p.shadow).Children(func() {
		for _, opt := range []segment[string]{{"转成脑图卡片", "card"}, {"换色", "color"}, {"删除", "delete"}} {
			btn := ui.ButtonBase(c.Key(opt.value)).Height(btnH).PaddingX(btnPadX).Center().Radius(6).
				Label(opt.title).OnClick(func() { a.editLasso(opt.value) })
			if btn.Hovered() {
				btn.Background(p.hover)
			}
			btn.Children(func() { ui.Text(c, opt.title).FontSize(fontSize).TextColor(p.secondary) })
		}
	})
}

func (a *workspace) canvasCard(c *ui.Context, p palette, card notebook.CanvasCard, box ui.Rect, scale float32) {
	panel := ui.Column(c).Absolute().Left(box.X).Top(box.Y).Size(box.W, box.H).ZIndex(2).Clip().
		IgnoreInput(a.drawing.canvasToolActive()).
		Background(p.elevated).Border(max(1, 1.5*scale), p.accent).Radius(12*scale).
		Shadow(0, 6*scale, 16*scale, -4*scale, p.accent.Alpha(.18)).
		ContextMenu(func(m *ui.Menu) {
			if m.Item("删除画布卡片").Danger().Chosen() {
				next := a.drawing.data
				next.Cards = slices.DeleteFunc(slices.Clone(next.Cards), func(c notebook.CanvasCard) bool { return c.ID == card.ID })
				a.commitDrawing(next)
			}
		})
	if card.Excerpt == nil {
		panel.Draw(func(paint *ui.Painter, rect ui.Rect) {
			a.drawCardInk(paint, rect, p, card, scale)
		})
	}
	panel.Children(func() {
		ui.Row(c).FillWidth().Padding(7*scale, 12*scale).Gap(4*scale).AlignItems(ui.Center).Children(func() {
			ui.Text(c, card.Title).Grow(1).MinWidth(0).FontSize(max(3.5, 13*scale)).FontWeight(650).TextColor(p.ink).SingleLine()
			if r := card.Excerpt; r != nil && scale >= .45 {
				label := fmt.Sprintf("P.%d ↗", r.Page+1)
				pillH := 20 * scale
				if a.touchUI() {
					pillH = max(pillH, 26)
				}
				b := ui.ButtonBase(c).Height(pillH).PaddingX(max(7*scale, 8)).Center().Radius(pillH/2).
					Background(p.tint).Border(max(.75, 1*scale), p.tintBorder).Label(label).
					HandleInput(func(ev ui.InputEvent) bool {
						return a.cardOverlayInput(ev, box.X+box.W-52*scale, box.Y+7*scale, false)
					}).
					OnClick(func() { a.jumpToPDF(*r) })
				b.Children(func() { ui.Text(c, label).FontSize(max(10.5, 11*scale)).TextColor(p.accent).SingleLine() })
			}
		})
		if card.Excerpt != nil {
			a.excerptPreview(c, p, *card.Excerpt)
		}
	})
}

func (a *workspace) drawCardInk(paint *ui.Painter, rect ui.Rect, p palette, card notebook.CanvasCard, scale float32) {
	for _, stroke := range card.Ink {
		color := strokeColor(stroke, p)
		if stroke.Marker {
			color = color.Alpha(.37)
		}
		paintStroke(paint, stroke, rect.X-card.X*scale, rect.Y-card.Y*scale, scale, color)
	}
}
