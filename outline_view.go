package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

type outlineItem struct {
	id    string
	depth int
}

func (a *workspace) outlineItems() []outlineItem {
	if a.hasActiveFilter() {
		matched := a.searchNotes()
		var allowed map[string]bool
		if a.scope != "" {
			allowed = make(map[string]bool)
			var mark func(id string)
			mark = func(id string) {
				if allowed[id] {
					return
				}
				allowed[id] = true
				for _, cid := range a.tree.kids[id] {
					mark(cid)
				}
			}
			mark(a.scope)
		}
		var items []outlineItem
		for _, id := range matched {
			if allowed == nil || allowed[id] {
				items = append(items, outlineItem{id: id, depth: 0})
			}
		}
		return items
	}
	a.ensureExpanded(a.currentTrail())
	rootID := a.tree.root
	if a.scope != "" {
		if _, ok := a.tree.notes[a.scope]; ok {
			rootID = a.scope
		}
	}
	var items []outlineItem
	seen := map[string]bool{rootID: true}
	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		for _, cid := range a.visibleKids(id) {
			if seen[cid] {
				continue
			}
			seen[cid] = true
			items = append(items, outlineItem{id: cid, depth: depth})
			if len(a.tree.kids[cid]) > 0 && a.expanded[cid] {
				walk(cid, depth+1)
			}
		}
	}
	walk(rootID, 0)
	return items
}

func (a *workspace) setAllExpanded(expand bool) {
	a.expanded = map[string]bool{a.tree.root: true}
	if a.scope != "" {
		a.expanded[a.scope] = true
	}
	if expand {
		for _, id := range a.tree.order {
			if len(a.tree.kids[id]) > 0 {
				a.expanded[id] = true
			}
		}
	}
}

func (a *workspace) outlineView(c *ui.Context, p palette, width, height float32) {
	items := a.outlineItems()
	padX := float32(28)
	if width < 520 {
		padX = 14
	}
	streamW := min(800, max(240, width-2*padX))

	ui.Scroll(c.Key("outline-scroll")).Fill().Background(p.canvas).TrackScroll(&a.outlineScroll).
		AlignItems(ui.Center).Padding(20, padX, 40).Gap(10).Children(func() {
		ui.Row(c).Width(streamW).Shrink(0).AlignItems(ui.Center).Gap(8).Padding(0, 2, 6).Children(func() {
			badgePill(c, p, fmt.Sprintf("%d 条笔记", len(items)))
			ui.Box(c).Grow(1).MinWidth(0)
			if a.hasExpandableBranches() {
				if a.hasCollapsedBranches() {
					pillButton(c, p, "全部展开", icLayers, false, a.editing(), func() { a.setAllExpanded(true) })
				} else {
					pillButton(c, p, "全部收起", icLayers, false, a.editing(), func() { a.setAllExpanded(false) })
				}
			}
		})

		if len(items) == 0 {
			ui.Box(c).Width(streamW).Padding(28).Radius(12).Background(p.surface).Border(1, p.rule).Center().Children(func() {
				if a.hasActiveFilter() {
					ui.Column(c).Center().Gap(8).Children(func() {
						ui.Text(c, "当前筛选条件下暂无笔记").FontSize(13).TextColor(p.quiet)
						pillButton(c, p, "清空筛选", icClose, true, false, a.clearFilters).Height(28)
					})
				} else {
					ui.Text(c, "当前分组暂无笔记").FontSize(13).TextColor(p.quiet)
				}
			})
			return
		}

		for _, item := range items {
			a.outlineCard(c.Key("outline-"+item.id), p, item, streamW)
		}
	})
}

func (a *workspace) outlineCard(c *ui.Context, p palette, item outlineItem, streamW float32) {
	n := a.tree.notes[item.id]
	title := noteTitle(n)
	rawTitle := strings.TrimSpace(n.Title)
	bodyText := noteCardText(n)
	imgs := noteCardImages(n)
	ct := cardThemeFor(p, n)
	kids := a.visibleKids(item.id)
	hasKids := len(kids) > 0
	selected := item.id == a.selected
	indent := float32(min(item.depth, 4)) * 22
	expanded := a.expanded != nil && a.expanded[item.id]
	hasBody := bodyText != "" || len(imgs) > 0 || len(n.Tags) > 0

	ui.Row(c).Width(streamW).Shrink(0).Padding(0, 0, 0, indent).Gap(8).AlignItems(ui.Stretch).Children(func() {
		if item.depth > 0 {
			railColor := ct.accent.Alpha(.45)
			if !ct.tinted {
				railColor = p.rule
			}
			if selected {
				railColor = p.accent
			}
			ui.Box(c).Width(2.5).Radius(1.5).Background(railColor).Margin(6, 0).Shrink(0)
		}

		border := ct.border
		if selected {
			border = p.accent
		}
		cardBg := ct.bodyBg
		if !hasBody {
			cardBg = ct.headerBg
		}
		cardW := max(180, streamW-indent-10)
		card := ui.Column(c).Grow(1).MinWidth(0).Radius(12).Clip().Background(cardBg).Border(1.2, border)
		if selected {
			card.Shadow(0, 0, 0, .85, p.accent).Shadow(0, 3, 10, -2, p.accent.Alpha(.18))
		} else {
			card.Shadow(0, 2, 6, -1, p.shadowSoft)
		}
		card.Children(func() {
			// Header row with optional fold button on the left + clickable title area.
			headerBg := ct.headerBg
			if !ct.tinted && hasBody {
				headerBg = p.shell
			}
			ui.Row(c).FillWidth().AlignItems(ui.Center).Background(headerBg).
				Padding(9, 12, 9, 10).Gap(6).Children(func() {
				if hasKids {
					icon := icChevronRight
					if expanded {
						icon = icChevronDown
					}
					foldSz := float32(24)
					if a.touchUI() {
						foldSz = 34
					}
					foldBtn := ui.ButtonBase(c.Key("fold-"+item.id)).Size(foldSz, foldSz).Radius(6).Center().
						Label("展开或折叠" + title).Disabled(a.editing()).
						OnClick(func() { a.toggleBranch(item.id) })
					if foldBtn.Pressed() {
						foldBtn.Background(p.pressed)
					} else if foldBtn.Hovered() {
						foldBtn.Background(p.hover)
					}
					foldBtn.Children(func() {
						ui.Icon(c, icon).Size(13, 13).TextColor(ct.headerInk.Alpha(.75))
					})
				} else if ct.tinted {
					ui.Box(c).Size(7, 7).Radius(99).Background(ct.accent).Margin(0, 2, 0, 4).Shrink(0)
				}

				openHeader := ui.ButtonBase(c.Key("open-"+item.id)).Grow(1).MinWidth(0).
					AlignItems(ui.Center).Gap(8).Label(title).
					Disabled(a.editing()).FocusRingColor(p.accent).
					OnClick(func() { a.openNote(item.id) })
				openHeader.Children(func() {
					titleColor := ct.headerInk
					if selected {
						titleColor = p.accent
					}
					displayTitle := title
					if rawTitle == "" && len(imgs) > 0 {
						displayTitle = "手写 / 附图笔记"
					}
					ui.Text(c, displayTitle).FontSize(14.5).FontWeight(650).LetterSpacing(-0.15).
						TextColor(titleColor).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
				})
				if n.Page != nil && *n.Page > 0 {
					pageLabel := fmt.Sprintf("第 %d 页", *n.Page)
					if len(a.tree.book.Documents) > 0 {
						pageBtnH := float32(22)
						if a.touchUI() {
							pageBtnH = 30
						}
						pageBtn := ui.ButtonBase(c.Key("opage-"+item.id)).Height(pageBtnH).Padding(0, 10).Radius(99).
							Center().Shrink(0).Label(pageLabel + " ↗").Disabled(a.editing()).
							FocusRingColor(p.accent).OnClick(func() { a.jumpToNotePDF(n) })
						bg, bdr, fg := p.tint, p.tintBorder, p.accent
						if !a.editing() {
							if pageBtn.Pressed() {
								bg = p.pressed
							} else if pageBtn.Hovered() {
								bg = p.hover
							}
						}
						pageBtn.Background(bg).Border(1, bdr).Children(func() {
							ui.Text(c, pageLabel+" ↗").FontSize(11).FontWeight(600).
								FontFeatures("tnum").TextColor(fg).SingleLine()
						})
					} else {
						badgePill(c, p, pageLabel)
					}
				}
				if hasKids {
					badgePill(c, p, fmt.Sprintf("%d 项", len(kids)))
				}
				if !a.editing() {
					selectForAction := func() {
						a.selected = item.id
						a.activeTrail = a.tree.trail(item.id)
					}
					actSz := float32(26)
					if a.touchUI() {
						actSz = 32
					}
					designToolbarButton(c, p, icEdit, "编辑笔记", actSz, false, a.busy, func() {
						selectForAction()
						a.startEditNote()
					})
					designToolbarButton(c, p, icPlus, "新建子笔记", actSz, false, a.busy, func() {
						selectForAction()
						a.startAddChild()
					})
					if item.id != "" {
						iconButton(c, p, icSettingDelete, "删除笔记", false, a.busy, func() {
							selectForAction()
							a.confirmDeleteNote()
						})
					}
				}
			})

			if hasBody {
				ui.Box(c).FillWidth().Height(1).Background(ct.border.Alpha(.45))
				if a.reciteMode && !a.revealedNotes[item.id] {
					occlude := ui.ButtonBase(c.Key("orec-"+item.id)).FillWidth().Padding(14).
						Center().Background(p.tint.Alpha(.7)).Label("点击揭晓内容").
						OnClick(func() {
							a.setNoteRevealed(item.id, true)
							hapticLight()
						})
					occlude.Children(func() {
						ui.Text(c, "点击揭晓内容").FontSize(12.5).FontWeight(600).TextColor(p.accent)
					})
					return
				}
				imgMaxW := max(140, cardW-44)
				ui.Column(c).FillWidth().Padding(12, 16, 14).Gap(10).Children(func() {
					renderOutlineImg := func(ref string) bool {
					iw, ih := a.imageDims(ref)
					if iw <= 0 || ih <= 0 {
							return false
						}
						dispW := min(imgMaxW, float32(iw))
						dispH := max(16, dispW*float32(ih)/float32(max(1, iw)))
						if dispH > 340 {
							dispH = 340
							dispW = max(16, dispH*float32(iw)/float32(max(1, ih)))
						}
						imgBtn := ui.ButtonBase(c.Key("oimg-"+ref)).AlignSelf(ui.Start).Radius(8).
							Padding(8).Background(ui.Hex("#ffffff")).Border(1, ct.border.Alpha(.5)).
							Label("放大"+title+"的附图").OnClick(func() { a.preview = ref })
						if imgBtn.Pressed() {
							imgBtn.Border(1, p.accent)
						} else if imgBtn.Hovered() {
							imgBtn.Border(1, p.accent.Alpha(.65))
						}
						imgBtn.Children(func() {
						ui.Box(c).Size(dispW, dispH).Draw(func(p *ui.Painter, rect ui.Rect) {
							a.paintMediaThumbnail(p, rect, ref)
						})
						})
						return true
					}

					if n.Clip != "" {
						renderOutlineImg(n.Clip)
					}
					useBlocks := n.BlocksCoverBody(bodyText)
					if !useBlocks && bodyText != "" {
						ui.Text(c, bodyText).FillWidth().FontSize(13.5).LineHeight(1.62).
							TextColor(p.ink).Selectable()
					}
					renderedBlocks := 0
					for bi, blk := range n.Blocks {
						if blk.Image != "" {
						if w, h := a.imageDims(blk.Image); w <= 0 || h <= 0 {
								continue
							}
							if renderedBlocks > 0 && useBlocks && len(n.Blocks) > 1 {
								ui.Box(c.Key(fmt.Sprintf("odiv-%d", bi))).FillWidth().Height(1).
									Shrink(0).Background(ct.border.Alpha(.38))
							}
							if renderOutlineImg(blk.Image) {
								renderedBlocks++
							}
							continue
						}
						if !useBlocks || blockPlainText(blk) == "" {
							continue
						}
						if renderedBlocks > 0 && len(n.Blocks) > 1 {
							ui.Box(c.Key(fmt.Sprintf("odiv-%d", bi))).FillWidth().Height(1).
								Shrink(0).Background(ct.border.Alpha(.38))
						}
						renderRichBlock(c.Key(fmt.Sprintf("oblk-%d", bi)), p, blk, blk.Text, 13.5, 1.62, true)
						renderedBlocks++
					}
					if len(n.Tags) > 0 {
						tagPillsRowAction(c, p, n.Tags, a.applyTagFilter)
					}
				})
			}
		})
	})
}

func tagPillsRow(c *ui.Context, p palette, tags []string) {
	tagPillsRowAction(c, p, tags, nil)
}

func tagPillsRowAction(c *ui.Context, p palette, tags []string, onTag func(string)) {
	if len(tags) == 0 {
		return
	}
	ui.Row(c).FillWidth().Wrap().Gap(6).Shrink(0).Children(func() {
		for i, raw := range tags {
			clean := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(raw), "#"))
			if clean == "" {
				continue
			}
			label := "#" + clean
			if onTag != nil {
				btn := ui.ButtonBase(c.Key(fmt.Sprintf("tag-%d", i))).Height(21).Padding(0, 8).Radius(99).
					Center().Shrink(0).Background(p.tint).Border(1, p.tintBorder.Alpha(.7)).
					Label(label).Tooltip("按标签筛选").OnClick(func() { onTag(clean) })
				btn.Children(func() {
					ui.Text(c, label).FontSize(11).FontWeight(550).TextColor(p.accent).SingleLine()
				})
			} else {
				ui.Row(c.Key(fmt.Sprintf("tag-%d", i))).Height(21).Padding(0, 8).Radius(99).
					Center().Shrink(0).Background(p.tint).Border(1, p.tintBorder.Alpha(.7)).Children(func() {
					ui.Text(c, label).FontSize(11).FontWeight(550).TextColor(p.accent).SingleLine()
				})
			}
		}
	})
}
