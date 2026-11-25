package main

import (
	"fmt"
	"math/rand/v2"
	"slices"

	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo/ui"
)

func (a *workspace) toggleReciteMode() {
	a.reciteMode = !a.reciteMode
	if !a.reciteMode {
		a.revealedMasks = nil
		a.revealedNotes = nil
	}
	hapticSelection()
}

func (a *workspace) setMaskRevealed(key string, revealed bool) {
	if a.revealedMasks == nil {
		a.revealedMasks = make(map[string]bool)
	}
	if revealed {
		a.revealedMasks[key] = true
	} else {
		delete(a.revealedMasks, key)
	}
}

func (a *workspace) setNoteRevealed(id string, revealed bool) {
	if a.revealedNotes == nil {
		a.revealedNotes = make(map[string]bool)
	}
	if revealed {
		a.revealedNotes[id] = true
	} else {
		delete(a.revealedNotes, id)
	}
}

func (a *workspace) noteHasReviewBody(n notebook.Note) bool {
	return stringsTrimAny(n.Body, n.Excerpt) || n.Clip != "" || len(noteCardImages(n)) > 0 || blockHasAnyText(n)
}

func stringsTrimAny(values ...string) bool {
	for _, v := range values {
		if len(v) > 0 {
			for i := 0; i < len(v); i++ {
				if v[i] > ' ' {
					return true
				}
			}
		}
	}
	return false
}

func blockHasAnyText(n notebook.Note) bool {
	for _, b := range n.Blocks {
		if blockPlainText(b) != "" {
			return true
		}
	}
	return false
}

func (a *workspace) collectReviewCandidates() []string {
	var base []string
	switch {
	case a.hasActiveFilter() || a.query != "":
		base = a.searchNotes()
	case a.scope != "":
		seen := map[string]bool{}
		var walk func(id string)
		walk = func(id string) {
			if seen[id] {
				return
			}
			seen[id] = true
			if id != a.tree.root {
				base = append(base, id)
			}
			for _, cid := range a.tree.kids[id] {
				walk(cid)
			}
		}
		walk(a.scope)
	default:
		base = a.tree.order
	}
	var out []string
	for _, id := range base {
		n, ok := a.tree.notes[id]
		if !ok {
			continue
		}
		if a.noteHasReviewBody(n) {
			out = append(out, id)
		}
	}
	return out
}

func (a *workspace) startReview() {
	if a.bookID == "" || a.editing() {
		return
	}
	queue := a.collectReviewCandidates()
	if len(queue) == 0 {
		a.showMessage("当前没有可供背诵的内容卡片", false)
		return
	}
	a.cardMenuOpen = false
	a.reviewQueue = queue
	a.reviewIndex = 0
	a.reviewRevealed = false
	a.reviewKnown = make(map[string]bool)
	a.reviewOpen = true
	hapticSelection()
}

func (a *workspace) shuffleReview() {
	if len(a.reviewQueue)-a.reviewIndex <= 1 {
		return
	}
	rem := a.reviewQueue[a.reviewIndex:]
	rand.Shuffle(len(rem), func(i, j int) { rem[i], rem[j] = rem[j], rem[i] })
	a.reviewRevealed = false
	hapticLight()
}

func (a *workspace) markReviewCard(known bool) {
	if a.reviewIndex < 0 || a.reviewIndex >= len(a.reviewQueue) {
		return
	}
	curID := a.reviewQueue[a.reviewIndex]
	if known {
		if a.reviewKnown == nil {
			a.reviewKnown = make(map[string]bool)
		}
		a.reviewKnown[curID] = true
		a.reviewIndex++
		a.reviewRevealed = false
		hapticSuccess()
		return
	}
	if len(a.reviewQueue)-a.reviewIndex > 1 {
		a.reviewQueue = append(append(slices.Clone(a.reviewQueue[:a.reviewIndex]), a.reviewQueue[a.reviewIndex+1:]...), curID)
	}
	a.reviewRevealed = false
	hapticLight()
}

func (a *workspace) reviewModal(c *ui.Context, p palette, paneW, paneH float32) {
	maxW := max(260, paneW-24)
	modalW := min(maxW, clamp(paneW-96, min(maxW, 520), 760))
	maxH := max(220, paneH-16)
	modalH := min(maxH, clamp(paneH-56, min(maxH, 460), 680))
	closeReview := func() { a.reviewOpen = false }

	ui.Box(c.Key("review-modal-layer")).Absolute().Left(0).Top(0).Right(0).Bottom(0).ZIndex(15).Center().Children(func() {
		ui.ButtonBase(c.Key("review-modal-scrim")).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			Label("关闭抽卡背诵").Background(p.scrim).OnClick(closeReview)
		card := ui.Column(c.Key("review-modal-card")).ZIndex(16).Width(modalW).Height(modalH).Radius(14).Clip().
			Background(p.elevated).Border(1, p.border).
			InsetShadow(0, 1, 0, 0, p.specular).
			Shadow(0, 20, 48, -10, p.modalShadow)
		card.Children(func() {
			ui.Row(c).Height(50).FillWidth().Shrink(0).PaddingX(16).Gap(8).AlignItems(ui.Center).
				Background(p.surface).Children(func() {
				ui.Icon(c, icBook).Size(15, 15).TextColor(p.accent).Shrink(0)
				ui.Text(c, "抽卡背诵").FontSize(14).FontWeight(650).TextColor(p.ink).SingleLine()
				total := len(a.reviewQueue)
				curNum := min(a.reviewIndex+1, total)
				badgePill(c, p, fmt.Sprintf("第 %d / %d 张 · 已掌握 %d", curNum, total, len(a.reviewKnown)))
				ui.Box(c).Grow(1).MinWidth(0)
				if a.reviewIndex < total-1 {
					pillButton(c, p, "随机打乱", icLayers, false, false, a.shuffleReview).Height(28)
				}
				iconButton(c, p, icClose, "关闭抽卡背诵", false, false, closeReview)
			})
			horizontalRule(c, p)

			if a.reviewIndex >= len(a.reviewQueue) {
				ui.Column(c).Grow(1).FillWidth().Center().Padding(28).Gap(14).Children(func() {
					ui.Icon(c, icCheck).Size(32, 32).TextColor(p.accent)
					ui.Text(c, "本轮背诵已完成").FontSize(19).FontWeight(700).TextColor(p.ink)
					ui.Text(c, fmt.Sprintf("共巩固 %d 张笔记卡片", len(a.reviewQueue))).FontSize(13).TextColor(p.quiet)
					ui.Row(c).Gap(10).Padding(8, 0, 0).Children(func() {
						pillButton(c, p, "重新开始", icUpdateRefresh, false, false, func() {
							a.reviewIndex = 0
							a.reviewRevealed = false
							a.reviewKnown = make(map[string]bool)
						}).Height(34)
						primaryButton(c, p, "完成", nil, false, closeReview).Height(34)
					})
				})
				return
			}

			curID := a.reviewQueue[a.reviewIndex]
			n := a.tree.notes[curID]
			ui.Scroll(c.Key("review-scroll")).Grow(1).MinHeight(0).FillWidth().Padding(20, 24).Gap(12).Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Wrap().Children(func() {
					if pid := a.tree.parent[curID]; pid != "" && pid != a.tree.root {
						badgePill(c, p, noteTitle(a.tree.notes[pid]))
					}
					if n.Page != nil && *n.Page > 0 && len(a.tree.book.Documents) > 0 {
						pillButton(c, p, fmt.Sprintf("第 %d 页 ↗", *n.Page), icPage, false, false, func() {
							a.reviewOpen = false
							a.jumpToNotePDF(n)
						}).Height(24)
					}
				})
				ui.Text(c, noteTitle(n)).FontSize(20).FontWeight(700).TextColor(p.ink).LineHeight(1.3)
				if len(n.Tags) > 0 {
					tagPillsRowAction(c, p, n.Tags, nil)
				}
				horizontalRule(c, p)
				if !a.reviewRevealed {
					reveal := ui.ButtonBase(c.Key("review-reveal-box")).FillWidth().MinHeight(140).Radius(12).
						Center().Background(p.tint).Border(1, p.tintBorder).Label("显示答案（空格）").
						FocusRingColor(p.accent).OnClick(func() {
						a.reviewRevealed = true
						hapticLight()
					})
					reveal.Children(func() {
						ui.Column(c).Center().Gap(6).Children(func() {
							ui.Text(c, "点击显示答案").FontSize(14).FontWeight(650).TextColor(p.accent)
							ui.Text(c, "先回忆要点，再核对正文与附图（按空格键揭晓）").FontSize(12).TextColor(p.secondary)
						})
					})
				} else {
					a.noteContent(c, p, n, modalW-52)
				}
			})

			horizontalRule(c, p)
			ui.Row(c).Height(56).FillWidth().Shrink(0).PaddingX(20).Gap(10).AlignItems(ui.Center).
				Background(p.surface).Children(func() {
				if !a.reviewRevealed {
					ui.Box(c).Grow(1)
					primaryButton(c, p, "显示答案", nil, false, func() {
						a.reviewRevealed = true
						hapticLight()
					}).Height(34)
					ui.Box(c).Grow(1)
				} else {
					ui.Box(c).Grow(1)
					pillButton(c, p, "再记一遍", icUpdateRefresh, true, false, func() {
						a.markReviewCard(false)
					}).Height(34)
					primaryButton(c, p, "已掌握", icCheck, false, func() {
						a.markReviewCard(true)
					}).Height(34)
					ui.Box(c).Grow(1)
				}
			})
		})
	})
}
