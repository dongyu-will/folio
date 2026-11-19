package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo/ui"
)

func (a *workspace) view(c *ui.Context) {
	a.disablePenZones()
	if a.bookChoice != a.bookID && !a.editing() {
		a.setLibrary(notebook.Library{Books: a.books}, a.bookChoice)
	}
	var th ui.Theme
	switch a.settings.Appearance {
	case "light":
		th = *ui.LightTheme()
	case "dark":
		th = *ui.DarkTheme()
	default:
		th = *c.Theme()
	}
	p := light
	if th.Dark {
		p = dark
	}
	th.Background = p.shell
	c.SetTheme(&th)
	w, h := c.Size()
	top, right, bottom, left := c.SafeArea()
	width := w - left - right
	height := h - top - bottom
	wide := width >= 700
	side := wide && !a.settings.SidebarCollapsed && !a.sidebarHiddenForSplit
	a.lastViewW = width
	a.lastViewWide, a.lastViewSidebar = wide, side
	if a.preview != "" {
		closePreview := func() { a.preview = "" }
		c.OnShortcut(0, ui.KeyBack, closePreview)
		c.OnShortcut(0, ui.KeyEscape, closePreview)
	} else if a.preferencesOpen {
		closeSettings := func() {
			if a.renameOpen {
				a.renameOpen = false
			} else {
				a.preferencesOpen = false
			}
		}
		c.OnShortcut(0, ui.KeyBack, closeSettings)
		c.OnShortcut(0, ui.KeyEscape, closeSettings)
	} else if a.editing() {
		cancelEdit := func() {
			a.editMode = editNone
			a.flushImports()
		}
		c.OnShortcut(0, ui.KeyBack, cancelEdit)
		c.OnShortcut(0, ui.KeyEscape, cancelEdit)
		c.OnShortcut(ui.Cmd, ui.KeyS, func() {
			if (a.editMode == editNote || a.editMode == editNewChild) && !a.busy && strings.TrimSpace(a.draftTitle) != "" {
				a.saveEditor()
			}
		})
	} else if a.pdfTOCOpen {
		closeTOC := func() { a.pdfTOCOpen = false }
		c.OnShortcut(0, ui.KeyBack, closeTOC)
		c.OnShortcut(0, ui.KeyEscape, closeTOC)
	} else if a.page != browse {
		back := func() { a.page = browse }
		c.OnShortcut(0, ui.KeyBack, back)
		c.OnShortcut(0, ui.KeyEscape, back)
	} else if len(a.drawing.selected) > 0 || len(a.drawing.lasso) > 0 {
		clearLasso := func() { a.drawing.lasso, a.drawing.selected = nil, nil }
		c.OnShortcut(0, ui.KeyBack, clearLasso)
		c.OnShortcut(0, ui.KeyEscape, clearLasso)
	} else if a.drawing.tool != panTool {
		exitDraw := func() { a.drawing.tool, a.drawing.toolPrefs, a.drawing.penBrowseMode = panTool, false, true }
		c.OnShortcut(0, ui.KeyBack, exitDraw)
		c.OnShortcut(0, ui.KeyEscape, exitDraw)
	} else if a.cardMenuOpen {
		dismiss := func() { a.cardMenuOpen = false }
		c.OnShortcut(0, ui.KeyBack, dismiss)
		c.OnShortcut(0, ui.KeyEscape, dismiss)
	}
	if a.bookID != "" && a.preview == "" && !a.preferencesOpen && !a.editing() && a.page == browse {
		c.OnShortcut(ui.Cmd, ui.KeyZ, func() { a.stepDrawing(true) })
		c.OnShortcut(ui.Cmd|ui.Shift, ui.KeyZ, func() { a.stepDrawing(false) })
		if a.mode == pdfMode {
			c.OnShortcut(ui.Cmd, ui.KeyEqual, func() { a.zoomPDF(1.18) })
			c.OnShortcut(ui.Cmd, ui.KeyMinus, func() { a.zoomPDF(1 / 1.18) })
			c.OnShortcut(ui.Cmd, ui.Key0, func() { a.zoomPDF(0) })
		} else if a.settings.LayoutMode() != notebook.LayoutOutline {
			c.OnShortcut(ui.Cmd, ui.KeyEqual, func() {
				a.zoomAt(a.scale*1.18, a.canvasWidth/2, a.canvasHeight/2)
			})
			c.OnShortcut(ui.Cmd, ui.KeyMinus, func() {
				a.zoomAt(a.scale/1.18, a.canvasWidth/2, a.canvasHeight/2)
			})
			c.OnShortcut(ui.Cmd, ui.Key0, func() { a.zoom(0) })
			c.OnShortcut(ui.Cmd, ui.Key1, func() {
				a.zoomAt(1, a.canvasWidth/2, a.canvasHeight/2)
			})
		}
	}

	ui.Box(c).Fill().Background(p.shell).Children(func() {
		ui.Column(c).Fill().Children(func() {
			if a.preview != "" {
				a.imageView(c, p, width, height)
				return
			}
			availableH := height
			if !wide || a.bookID == "" {
				a.mobileHeader(c, p, wide)
				availableH -= 55
			}
			if a.statusBanner(c, p) {
				availableH -= 30
			}
			if a.updateBanner(c, p) {
				availableH -= a.updateBannerHeight()
			}
			if a.bookID == "" {
				a.emptyState(c, p)
				return
			}
			if !wide && a.page == libraryScreen {
				a.sidebar(c, p, width, false)
				return
			}
			if !wide && a.page == detail {
				a.readerPanel(c, p, width, availableH, false, false)
				return
			}

			ui.Row(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
				var sideWidth float32
				if side {
					sideWidth = 240
					a.sidebar(c, p, sideWidth, true)
				}
				inset := float32(0)
				if wide {
					inset = 8
				}
				leftInset := inset
				if side {
					leftInset = 0
				}
				paneW := width - sideWidth - inset - leftInset
				pane := ui.Box(c).Grow(1).FillHeight().MinWidth(0).Padding(inset, inset, inset, leftInset)
				pane.Children(func() {
					surface := ui.Box(c).Fill().Clip().Background(p.surface)
					if wide {
						surface.Radius(12).Border(1, p.rule).Shadow(0, 2, 10, -2, p.shadow)
					}
					surface.Children(func() {
						a.documentWorkspace(c, p, paneW, max(1, availableH-2*inset))
						if a.page == detail {
							a.noteModal(c, p, paneW, max(1, availableH-2*inset))
						}
						if wide && a.preferencesOpen {
							a.settingsCard(c, p, paneW, max(1, availableH-2*inset))
						}
					})
				})
			})
		})
		if a.preferencesOpen && a.preview == "" && (!wide || a.bookID == "") {
			a.settingsCard(c, p, width, height)
		}
	})
	if w > 0 && h > 0 {
		a.markAndroidFramePainted()
	}
}

func (a *workspace) statusBanner(c *ui.Context, p palette) bool {
	if a.message == "" {
		return false
	}
	left := time.Until(a.messageUntil)
	if left <= 0 {
		return false
	}
	fg, bg, ic := p.accent, p.tint, icCheck
	if a.messageError {
		fg, bg, ic = p.danger, p.dangerWash, icAlert
	}
	ui.Row(c).FillWidth().Shrink(0).Padding(7, 16).Gap(8).AlignItems(ui.Center).
		Background(bg).Children(func() {
		ui.Icon(c, ic).Size(14, 14).TextColor(fg)
		ui.Text(c, a.message).FontSize(12.5).FontWeight(550).TextColor(fg).Grow(1).MinWidth(0).SingleLine()
	})
	horizontalRule(c, p)
	c.After(left)
	return true
}

func (a *workspace) updateBannerHeight() float32 {
	if a.touchUI() {
		return 45
	}
	return 37
}

func (a *workspace) updateBanner(c *ui.Context, p palette) bool {
	if a.preferencesOpen {
		return false
	}
	st, dismissed := a.updateViewState()
	if dismissed || !st.showsBanner() {
		return false
	}

	ver := ""
	if st.info != nil {
		ver = strings.TrimSpace(st.info.Version)
	}
	var (
		icon         = icUpdateRefresh
		bannerText   string
		primaryLabel string
		primaryFn    func()
	)
	switch {
	case st.phase == updateRelaunchRequired:
		if ver != "" {
			bannerText = fmt.Sprintf("新版本 v%s 已就绪，重启后生效", ver)
		} else {
			bannerText = "更新已就绪，重启后生效"
		}
		primaryLabel = "立即重启"
		primaryFn = a.relaunchApp
	case st.phase == updateAvailable && st.info != nil && st.info.PendingPath != "":
		icon = icImport
		if ver != "" {
			bannerText = fmt.Sprintf("新版本 v%s 安装包已就绪", ver)
		} else {
			bannerText = "更新安装包已就绪"
		}
		services := c.Services()
		primaryLabel = "立即安装"
		primaryFn = func() { a.installUpdate(services) }
	case st.phase == updatePermissionRequired && st.info != nil && st.info.PendingPath != "":
		icon = icImport
		if ver != "" {
			bannerText = fmt.Sprintf("新版本 v%s 已下载，请开启安装权限", ver)
		} else {
			bannerText = "更新已下载，请开启安装权限"
		}
		primaryLabel = "查看更新"
		primaryFn = a.openPreferences
	default:
		if ver != "" {
			bannerText = fmt.Sprintf("发现新版本 v%s", ver)
		} else {
			bannerText = "发现新版本"
		}
		primaryLabel = "查看更新"
		primaryFn = a.openPreferences
	}

	barH := float32(36)
	btnH := float32(26)
	padX := float32(16)
	if a.touchUI() {
		barH = 44
		btnH = 32
		padX = 12
	}
	ui.Row(c.Key("update-banner")).Height(barH).FillWidth().Shrink(0).Padding(0, padX).Gap(8).
		AlignItems(ui.Center).Background(p.tint).Children(func() {
		ui.Icon(c, icon).Size(14, 14).TextColor(p.accent).Shrink(0)
		ui.Text(c, bannerText).FontSize(12.5).FontWeight(550).TextColor(p.accent).
			Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
		act := ui.ButtonBase(c.Key("update-banner-action")).Height(btnH).PaddingX(10).Radius(6).Center().Shrink(0).
			Label(primaryLabel).Disabled(a.editing()).FocusRingColor(p.accent).OnClick(primaryFn)
		if a.editing() {
			act.Background(p.accent.Alpha(.45))
		} else if act.Pressed() {
			act.Background(p.accentBot)
		} else {
			act.Background(p.accent)
		}
		act.Children(func() {
			ui.Text(c, primaryLabel).FontSize(11.5).FontWeight(600).TextColor(p.onAccent).SingleLine()
		})
		later := ui.ButtonBase(c.Key("update-banner-dismiss")).Height(btnH).PaddingX(8).Radius(6).Center().Shrink(0).
			Label("稍后").FocusRingColor(p.accent).OnClick(a.dismissUpdateBanner)
		if later.Pressed() {
			later.Background(p.pressed)
		} else if later.Hovered() {
			later.Background(p.hover)
		}
		later.Children(func() {
			ui.Text(c, "稍后").FontSize(11.5).FontWeight(550).TextColor(p.secondary).SingleLine()
		})
	})
	horizontalRule(c, p)
	return true
}

func (a *workspace) emptyState(c *ui.Context, p palette) {
	ui.Column(c).Grow(1).FillWidth().Center().Padding(24).Children(func() {
		ui.Column(c).Width(420).MaxWidth(600).FillWidth().Padding(34, 32).Radius(16).Center().Gap(14).
			Background(p.surface).Border(1, p.rule).
			InsetShadow(0, 1, 0, 0, p.specular).
			Shadow(0, 8, 28, -6, p.shadow).Children(func() {
			brandMark(c, p, 48)
			ui.Column(c).Center().Gap(6).Children(func() {
				ui.Text(c, "打开笔记或 PDF").FontSize(22).FontWeight(700).LetterSpacing(-0.4).TextColor(p.ink)
				ui.Text(c, ".marginpkg、.marginnotes、.pdf").
					FontSize(13).LineHeight(1.5).TextColor(p.quiet)
			})
			ui.Box(c).Height(4)
			primaryButton(c, p, "选择笔记文件", icImport, a.busy, a.chooseImport).Height(40).Padding(0, 18)
		})
	})
}

func (a *workspace) mobileHeader(c *ui.Context, p palette, side bool) {
	ui.Row(c).Height(54).FillWidth().Shrink(0).AlignItems(ui.Center).Padding(0, 14).Gap(8).
		Background(p.surface).Children(func() {
		if a.bookID != "" && !side {
			if a.page == libraryScreen {
				pillButton(c, p, "脑图", icArrowLeft, false, a.editing(), func() { a.page = browse })
			} else {
				pillButton(c, p, "目录", icSidebar, false, a.editing(), func() { a.page = libraryScreen }).Label("笔记本与搜索")
			}
		}
		ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).MinWidth(0).Children(func() {
			brandMark(c, p, 22)
			ui.Text(c, "Folio").FontSize(15).FontWeight(700).LetterSpacing(-0.3).TextColor(p.ink).SingleLine()
		})
		if !side && a.bookID != "" && a.page == detail {
			pillButton(c, p, "脑图", icArrowLeft, false, a.editing(), func() { a.page = browse })
		}
		a.transferButton(c, p)
		a.mobileSettingsButton(c, p)
	})
	horizontalRule(c, p)
}

func (a *workspace) mobileSettingsButton(c *ui.Context, p palette) {
	disabled := a.editing()
	b := ui.ButtonBase(c).Label("设置").Size(34, 34).Radius(8).Center().Shrink(0).
		Disabled(disabled).FocusRingColor(p.accent).OnClick(a.openPreferences)
	fg := p.quiet
	if !disabled {
		if b.Pressed() {
			b.Background(p.pressed)
			fg = p.ink
		} else if b.Hovered() {
			b.Background(p.hover)
			fg = p.ink
		}
	} else {
		fg = p.quiet.Alpha(.4)
	}
	b.Children(func() {
		ui.Icon(c, icSettings).Size(15, 15).TextColor(fg)
		if a.hasUpdateBadge() {
			ui.Box(c.Key("mobile-update-dot")).Absolute().Top(5).Right(5).Size(7, 7).Radius(99).
				Background(p.accent).Border(1, p.surface).Label("有可用更新")
		}
	})
}

func (a *workspace) sidebar(c *ui.Context, p palette, width float32, showBrandBar bool) {
	ui.Column(c).Width(width).FillHeight().Shrink(0).Padding(12, 12, 10).Gap(10).
		Background(p.shell).Children(func() {
		if showBrandBar {
			ui.Row(c).Height(36).FillWidth().Shrink(0).AlignItems(ui.Center).PaddingX(4).Gap(4).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).MinWidth(0).Children(func() {
					brandMark(c, p, 22)
					ui.Text(c, "Folio").FontSize(14.5).FontWeight(700).LetterSpacing(-0.3).TextColor(p.ink).SingleLine()
				})
				a.transferButton(c, p)
				collapseSz := float32(28)
				if a.touchUI() {
					collapseSz = 34
				}
				designToolbarButton(c, p, icCollapse, "折叠侧边栏", collapseSz, false, a.editing(), a.toggleSidebar)
			})
		}

		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			b := ui.SelectBase(c, &a.bookChoice)
			b.Trigger.Height(38).Grow(1).MinWidth(0).PaddingX(10).Gap(7).AlignItems(ui.Center).Radius(8).
				Background(p.elevated).Border(1, p.border).
				InsetShadow(0, 1, 0, 0, p.specular).
				Shadow(0, 1, 2, 0, p.shadowSoft).
				Disabled(a.editing()).Label("切换笔记本").Children(func() {
				ui.Icon(c, icBook).Size(14, 14).TextColor(p.accent).Shrink(0)
				ui.Text(c, a.tree.book.Title).Grow(1).MinWidth(0).SingleLine().
					FontSize(13).FontWeight(650).LetterSpacing(-0.15).TextColor(p.ink)
				ui.Icon(c, icChevronDown).Size(13, 13).TextColor(p.quiet).Shrink(0)
			})
			b.Popup(func(panel ui.Element) {
				panel.Background(p.elevated).Border(1, p.border).Radius(10).Padding(5).MaxHeight(300).
					Shadow(0, 10, 30, -6, p.shadow)
				ui.Scroll(c).Shrink(1).Gap(2).Children(func() {
					for _, book := range a.books {
						active := book.ID == a.bookID
						row := b.Item(book.ID).MinHeight(38).FillWidth().Padding(0, 10).Gap(8).
							AlignItems(ui.Center).Radius(7)
						if active {
							row.Background(p.tint)
						} else if row.Highlighted() {
							row.Background(p.hover)
						}
						fg := p.ink
						if active {
							fg = p.accent
						}
						row.Children(func() {
							ui.Icon(c, icBook).Size(13, 13).TextColor(fg).Shrink(0)
							ui.Text(c, book.Title).SingleLine().Shrink(1).FontSize(13).FontWeight(550).TextColor(fg)
						})
					}
				})
			})
		})

		// Recessed Apple/Linear search field
		ui.SearchField(c, &a.query).Height(36).FillWidth().PaddingX(10).Radius(8).FontSize(12.5).
			Background(p.inputBg).Border(1, p.rule).TextColor(p.ink).Label("搜索笔记").
			Placeholder("搜索笔记…").Disabled(a.editing())

		// Section kicker + scrollable nav list
		kicker := "分组目录"
		if strings.TrimSpace(a.query) != "" {
			kicker = "搜索结果"
		}
		sectionKicker(c, p, kicker)

		ui.Scroll(c.Key("sidebar-scroll")).Grow(1).MinHeight(0).FillWidth().Gap(2).Children(func() {
			if strings.TrimSpace(a.query) != "" {
				ids := a.searchNotes()
				if len(ids) == 0 {
					ui.Text(c, "没有匹配的笔记").Padding(14, 10).FontSize(12.5).TextColor(p.quiet)
				}
				for _, id := range ids {
					a.navItem(c.Key(id), p, noteTitle(a.tree.notes[id]), nil, "", id == a.selected, func() { a.openNote(id) })
				}
				return
			}
			a.navItem(c.Key("all-notes"), p, "全部笔记", icLayers, fmt.Sprint(len(a.tree.order)), a.scope == "", func() { a.selectScope("") })
			for _, id := range a.tree.kids[a.tree.root] {
				n := a.tree.notes[id]
				if n.Title == "" && len(a.tree.kids[id]) == 0 && !hasNoteContent(n) {
					continue
				}
				countLabel := ""
				if kids := len(a.tree.kids[id]); kids > 0 {
					countLabel = fmt.Sprint(kids)
				}
				a.navItem(c.Key(id), p, noteTitle(n), icFolder, countLabel, id == a.scope, func() { a.selectScope(id) })
			}
		})
		horizontalRule(c, p)
		settings := ui.ButtonBase(c).Height(36).FillWidth().Shrink(0).PaddingX(10).Gap(8).
			AlignItems(ui.Center).Radius(8).Background(p.elevated).Border(1, p.rule).
			Label("设置").Disabled(a.editing()).OnClick(a.openPreferences)
		if settings.Hovered() {
			settings.Background(p.hover)
		}
		settings.Children(func() {
			ui.Icon(c, icSettings).Size(15, 15).TextColor(p.quiet)
			ui.Text(c, "设置").FontSize(13).FontWeight(550).TextColor(p.secondary).Grow(1)
			if a.hasUpdateBadge() {
				ui.Box(c.Key("sidebar-update-dot")).Size(7, 7).Radius(99).Shrink(0).
					Background(p.accent).Label("有可用更新")
			}
			ui.Text(c, "v"+currentAppVersion()).FontSize(11).TextColor(p.quiet)
		})

	})
}

func (a *workspace) navItem(c *ui.Context, p palette, label string, icon *ui.SVG, count string, selected bool, onClick func()) {
	row := ui.ButtonBase(c).FillWidth().MinHeight(36).Padding(0, 10).Gap(8).
		Justify(ui.Start).AlignItems(ui.Center).Radius(8).
		Label(label).Disabled(a.editing()).Selected(selected).FocusRingColor(p.accent).OnClick(onClick)
	fg := p.secondary
	icColor := p.quiet
	weight := 500
	switch {
	case selected:
		row.Background(p.selectedBg).Border(1, p.rule).
			InsetShadow(0, 1, 0, 0, p.specular).
			Shadow(0, 1, 2, 0, p.shadowSoft)
		fg = p.ink
		icColor = p.accent
		weight = 600
	case row.Pressed():
		row.Background(p.pressed).Border(1, ui.Color{})
		fg = p.ink
		icColor = p.secondary
	case row.Hovered():
		row.Background(p.hover).Border(1, ui.Color{})
		fg = p.ink
		icColor = p.secondary
	default:
		row.Border(1, ui.Color{})
	}
	row.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(14, 14).TextColor(icColor).Shrink(0)
		}
		ui.Text(c, label).SingleLine().FontSize(13).FontWeight(weight).LetterSpacing(-0.1).
			TextColor(fg).Grow(1).MinWidth(0)
		if count != "" {
			ui.Text(c, count).FontSize(11).FontWeight(550).FontFeatures("tnum").TextColor(p.quiet).Shrink(0)
		}
	})
}

func (a *workspace) selectScope(id string) {
	a.scope, a.query, a.page = id, "", browse
	a.cardMenuOpen = false
	a.outlineScroll = ui.ScrollState{}
	a.recenter = true
	if id != "" && a.expanded != nil {
		a.expanded[id] = true
	}
	a.setNote(a.firstNote())
}

// noteModal renders the Apple x Linear centered focus card over the live mindmap.
func (a *workspace) noteModal(c *ui.Context, p palette, paneW, paneH float32) {
	maxW := max(260, paneW-24)
	modalW := min(maxW, clamp(paneW-96, min(maxW, 540), 820))
	maxH := max(160, paneH-16)
	modalH := min(maxH, clamp(paneH-64, min(maxH, 420), 740))
	ui.Box(c.Key("note-modal-layer")).Absolute().Left(0).Top(0).Right(0).Bottom(0).ZIndex(10).Center().Children(func() {
		// Translucent scrim backdrop dismisses the modal when clicked outside.
		ui.ButtonBase(c.Key("note-modal-scrim")).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			Label("关闭笔记卡片").Background(p.scrim).
			OnClick(func() {
				if !a.editing() {
					a.page = browse
				}
			})

		// Centered floating editorial card
		ui.Column(c.Key("note-modal-card")).ZIndex(11).Width(modalW).Height(modalH).Radius(16).Clip().
			Background(p.surface).Border(1, p.border).
			InsetShadow(0, 1, 0, 0, p.specular).
			Shadow(0, 24, 60, -14, p.modalShadow).
			Shadow(0, 4, 14, -2, p.shadow).Children(func() {
			a.readerPanel(c, p, modalW, modalH, true, true)
		})
	})
}

func (a *workspace) readerPanel(c *ui.Context, p palette, width, height float32, modal, showClose bool) {
	n := a.tree.notes[a.selected]
	ct := cardThemeFor(p, n)
	panel := ui.Column(c).Fill().Background(p.surface)
	if !modal {
		panel.Width(width).FillHeight().Shrink(0)
	}
	panel.Children(func() {
		if ct.tinted {
			ui.Box(c).FillWidth().Height(4).Shrink(0).Background(ct.accent)
		}
		// Sleek 52px modal/reader toolbar with clickable breadcrumb chain + New / Edit + Close.
		barH := float32(48)
		padH := float32(14)
		if modal {
			barH = 52
			padH = 18
		}
		ui.Row(c).Height(barH).FillWidth().Shrink(0).AlignItems(ui.Center).PaddingX(padH).Gap(6).Children(func() {
			trail := a.currentTrail()
			ancestors := trail
			if len(ancestors) > 1 {
				ancestors = trail[:len(trail)-1]
			} else {
				ancestors = nil
			}
			// Show up to the last 2 ancestors for a clean Linear-style breadcrumb path.
			startIdx := max(0, len(ancestors)-2)
			if len(ancestors) > 0 {
				ui.Row(c).AlignItems(ui.Center).Gap(2).Grow(1).MinWidth(0).Children(func() {
					for i := startIdx; i < len(ancestors); i++ {
						ancID := ancestors[i]
						ancTrail := slices.Clone(trail[:i+1])
						crumb := ui.ButtonBase(c.Key("crumb-"+ancID)).Label("跳转到"+noteTitle(a.tree.notes[ancID])).
							Height(30).Padding(0, 6).Radius(6).AlignItems(ui.Center).Gap(4).
							Disabled(a.editing()).OnClick(func() {
							a.setNoteWithTrail(ancID, ancTrail)
							if !hasNoteContent(a.tree.notes[ancID]) && len(a.tree.kids[ancID]) > 0 {
								a.page = browse
							}
						})
						fg := p.quiet
						if crumb.Hovered() && !a.editing() {
							crumb.Background(p.hover)
							fg = p.ink
						}
						crumb.Children(func() {
							ui.Text(c, noteTitle(a.tree.notes[ancID])).FontSize(12).FontWeight(500).
								TextColor(fg).SingleLine().Shrink(1)
						})
						if i < len(ancestors)-1 || len(trail) > 1 {
							ui.Icon(c, icChevronRight).Size(11, 11).TextColor(p.quiet).Shrink(0)
						}
					}
					ui.Box(c).Grow(1).MinWidth(0)
				})
			} else {
				ui.Box(c).Grow(1)
			}
			if !a.editing() {
				pillButton(c, p, "新增", icPlus, false, a.busy, a.startAddChild).Label("新建子笔记")
				pillButton(c, p, "编辑", icEdit, true, a.busy || n.ID == "", a.startEditNote)
				iconButton(c, p, icSettingDelete, "删除笔记", true, a.busy || n.ID == "", a.confirmDeleteNote)
			}
			if showClose {
				ui.Box(c).Width(1).Height(18).Background(p.rule).Shrink(0)
				iconButton(c, p, icClose, "关闭笔记卡片", false, a.editing(), func() { a.page = browse })
			}
		})
		horizontalRule(c, p)
		if a.editing() {
			a.editor(c, p, max(120, height-barH-1))
			return
		}

		// Generous Apple-editorial reading canvas
		padX := float32(24)
		titleSize := float32(23)
		if modal {
			padX = 32
			titleSize = 26
		}
		ui.Scroll(c.Key("reader-scroll")).Grow(1).MinHeight(0).FillWidth().TrackScroll(&a.readerScroll).
			Padding(26, padX, 36).Gap(18).Children(func() {
			hasPage := n.Page != nil && *n.Page > 0
			bookTitle := strings.TrimSpace(n.BookTitle)
			if hasPage || bookTitle != "" {
				ui.Row(c).FillWidth().Wrap().AlignItems(ui.Center).Gap(8).Shrink(0).Children(func() {
					if hasPage {
						pageLabel := fmt.Sprintf("第 %d 页", *n.Page)
						if len(a.tree.book.Documents) > 0 {
							jumpH := float32(24)
							if a.touchUI() {
								jumpH = 30
							}
							btn := ui.ButtonBase(c.Key("reader-page-jump")).Height(jumpH).Padding(0, 10).Radius(99).
								AlignItems(ui.Center).Gap(5).Shrink(0).
								Label(pageLabel + " ↗").Disabled(a.editing()).FocusRingColor(p.accent).
								OnClick(func() { a.jumpToNotePDF(n) })
							bg, bdr, fg := p.tint, p.tintBorder, p.accent
							if !a.editing() {
								if btn.Pressed() {
									bg = p.pressed
								} else if btn.Hovered() {
									bg = p.hover
								}
							}
							btn.Background(bg).Border(1, bdr).Children(func() {
								ui.Icon(c, icPage).Size(11, 11).TextColor(fg)
								ui.Text(c, pageLabel+" ↗").FontSize(11).FontWeight(600).
									FontFeatures("tnum").TextColor(fg).SingleLine()
							})
						} else {
							ui.Row(c).Height(22).Padding(0, 8).Radius(99).AlignItems(ui.Center).
								Gap(5).Background(p.badgeBg).Border(1, p.rule).Shrink(0).Children(func() {
								ui.Icon(c, icPage).Size(11, 11).TextColor(p.quiet)
								ui.Text(c, pageLabel).FontSize(11).FontWeight(550).
									FontFeatures("tnum").TextColor(p.quiet).SingleLine()
							})
						}
					}
					if bookTitle != "" {
						ui.Row(c).Height(22).Padding(0, 8).Radius(99).AlignItems(ui.Center).
							Gap(5).Background(p.badgeBg).Border(1, p.rule).Shrink(1).MinWidth(0).Children(func() {
							ui.Icon(c, icBook).Size(11, 11).TextColor(p.quiet).Shrink(0)
							ui.Text(c, bookTitle).FontSize(11).FontWeight(500).
								TextColor(p.secondary).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
						})
					}
				})
			}

			ui.Text(c, noteTitle(n)).FontSize(titleSize).FontWeight(700).LetterSpacing(-0.5).
				TextColor(p.ink).LineHeight(1.24).Shrink(0)

			if len(n.Tags) > 0 {
				tagPillsRow(c, p, n.Tags)
			}

			a.noteContent(c, p, n, width-padX*2-2)

			if len(n.Links) > 0 {
				var linked []notebook.Note
				seen := map[string]bool{n.ID: true}
				for _, lid := range n.Links {
					if seen[lid] {
						continue
					}
					if target, ok := a.tree.notes[lid]; ok {
						seen[lid] = true
						linked = append(linked, target)
					}
				}
				if len(linked) > 0 {
					ui.Box(c).Height(2).Shrink(0)
					horizontalRule(c, p)
					ui.Column(c).FillWidth().Gap(8).Shrink(0).Children(func() {
						ui.Text(c, fmt.Sprintf("关联笔记 · %d", len(linked))).FontSize(11).FontWeight(600).
							FontFeatures("tnum").LetterSpacing(0.4).TextColor(p.quiet).Padding(2, 2, 0)
						ui.Row(c).FillWidth().Wrap().Gap(6).Shrink(0).Children(func() {
							for _, target := range linked {
								targetID := target.ID
								targetTitle := noteTitle(target)
								chipH := float32(28)
								if a.touchUI() {
									chipH = 34
								}
								chip := ui.ButtonBase(c.Key("link-"+targetID)).Height(chipH).Padding(0, 11).Radius(8).
									AlignItems(ui.Center).Gap(4).Shrink(0).
									Label("跳转到关联笔记 " + targetTitle).Disabled(a.editing()).FocusRingColor(p.accent).
									OnClick(func() { a.openNote(targetID) })
								bg, bdr, fg := p.shell, p.rule, p.secondary
								if !a.editing() {
									if chip.Pressed() {
										bg, bdr, fg = p.pressed, p.tintBorder, p.accent
									} else if chip.Hovered() {
										bg, bdr, fg = p.tint, p.tintBorder, p.accent
									}
								}
								chip.Background(bg).Border(1, bdr).Children(func() {
									ui.Text(c, targetTitle).FontSize(12).FontWeight(550).
										TextColor(fg).SingleLine().Ellipsis("…").MaxWidth(260)
									ui.Text(c, "↗").FontSize(11).FontWeight(600).TextColor(p.accent).SingleLine()
								})
							}
						})
					})
				}
			}

			children := a.tree.kids[a.selected]
			if len(children) > 0 {
				ui.Box(c).Height(4).Shrink(0)
				horizontalRule(c, p)
				ui.Column(c).FillWidth().Gap(5).Shrink(0).Children(func() {
					ui.Text(c, fmt.Sprintf("下级节点 · %d", len(children))).FontSize(11).FontWeight(600).
						FontFeatures("tnum").LetterSpacing(0.4).TextColor(p.quiet).Padding(4, 2, 4)
					for _, id := range children {
						child := a.tree.notes[id]
						if child.Title != "" || hasNoteContent(child) || len(a.tree.kids[id]) > 0 {
							a.childRow(c.Key(id), p, noteTitle(child), func() { a.openChildNote(id) })
						}
					}
				})
			}

			created := formatNoteTime(n.CreatedAt)
			updated := formatNoteTime(n.UpdatedAt)
			var timeMeta string
			switch {
			case updated != "" && created != "" && updated != created:
				timeMeta = "创建于 " + created + " · 更新于 " + updated
			case updated != "":
				timeMeta = "更新于 " + updated
			case created != "":
				timeMeta = "创建于 " + created
			}
			if timeMeta != "" {
				ui.Row(c).FillWidth().Padding(4, 2, 0).AlignItems(ui.Center).Shrink(0).Children(func() {
					ui.Text(c, timeMeta).FontSize(11).FontFeatures("tnum").TextColor(p.quiet).SingleLine()
				})
			}
		})
	})
}

func (a *workspace) childRow(c *ui.Context, p palette, label string, onClick func()) {
	row := ui.ButtonBase(c).FillWidth().MinHeight(38).Padding(0, 12).Gap(8).
		Justify(ui.SpaceBetween).AlignItems(ui.Center).Radius(8).
		Label(label).Disabled(a.editing()).FocusRingColor(p.accent).OnClick(onClick)
	fg := p.ink
	if row.Pressed() {
		row.Background(p.pressed).Border(1, p.rule)
	} else if row.Hovered() {
		row.Background(p.hover).Border(1, p.rule)
	} else {
		row.Background(p.shell).Border(1, p.rule)
	}
	row.Children(func() {
		ui.Text(c, label).SingleLine().FontSize(13).FontWeight(550).LetterSpacing(-0.1).
			TextColor(fg).Grow(1).MinWidth(0)
		ui.Icon(c, icChevronRight).Size(13, 13).TextColor(p.quiet).Shrink(0)
	})
}

func (a *workspace) noteContent(c *ui.Context, p palette, n notebook.Note, width float32) {
	text := noteText(n)
	if a.addImage(c, p, n.Clip, noteTitle(n)+"的原始摘录", width) {
		text = n.Body
	} else if n.Body != "" && n.Excerpt != n.Body {
		copyText(c, p, n.Excerpt)
	}
	useBlocks := n.BlocksCoverBody(text)
	if !useBlocks {
		copyText(c, p, text)
	}
	renderedBlocks := 0
	for bi, b := range n.Blocks {
		if b.Image != "" {
			if a.mediaBitmap(b.Image) == nil {
				continue
			}
			if renderedBlocks > 0 && useBlocks && len(n.Blocks) > 1 {
				ui.Box(c.Key(fmt.Sprintf("ndiv-%d", bi))).FillWidth().Height(1).Shrink(0).Background(p.rule)
			}
			if a.addImage(c, p, b.Image, noteTitle(n)+"的笔记图片", width) {
				renderedBlocks++
			}
			continue
		}
		if !useBlocks || blockPlainText(b) == "" {
			continue
		}
		if renderedBlocks > 0 && len(n.Blocks) > 1 {
			ui.Box(c.Key(fmt.Sprintf("ndiv-%d", bi))).FillWidth().Height(1).Shrink(0).Background(p.rule)
		}
		renderRichBlock(c.Key(fmt.Sprintf("nblk-%d", bi)), p, b, b.Text, 14.5, 1.68, true)
		renderedBlocks++
	}
}

func copyText(c *ui.Context, p palette, text string) {
	if text != "" {
		ui.Text(c, text).FillWidth().Shrink(0).FontSize(14.5).LineHeight(1.68).
			TextColor(p.ink).Selectable()
	}
}

func (a *workspace) addImage(c *ui.Context, p palette, ref, label string, width float32) bool {
	bitmap := a.mediaBitmap(ref)
	if bitmap == nil {
		return false
	}
	w, h := bitmap.Size()
	imgW := min(width-20, float32(w))
	imgH := max(8, imgW*float32(h)/float32(w))
	const maxInlineH = float32(460)
	if imgH > maxInlineH {
		imgH = maxInlineH
		imgW = max(8, imgH*float32(w)/float32(h))
	}
	card := ui.ButtonBase(c.Key(ref)).Label("放大"+label).AlignSelf(ui.Start).Shrink(0).Radius(10).
		Background(ui.Hex("#ffffff")).Border(1, p.rule).Padding(10).
		Shadow(0, 2, 8, -2, p.shadowSoft).
		OnClick(func() { a.preview = ref })
	if card.Hovered() {
		card.Border(1, p.accent.Alpha(.5)).Shadow(0, 4, 14, -2, p.shadow)
	}
	card.Children(func() {
		ui.Image(c, bitmap).Size(imgW, imgH).Fit(ui.Contain)
	})
	return true
}

func (a *workspace) imageView(c *ui.Context, p palette, width, height float32) {
	bitmap := a.mediaBitmap(a.preview)
	if bitmap == nil {
		a.preview = ""
		c.Invalidate()
		return
	}
	w, h := bitmap.Size()
	size := min(width-56, float32(w))
	ui.Row(c).Height(52).FillWidth().PaddingX(16).AlignItems(ui.Center).Background(p.surface).Children(func() {
		pillButton(c, p, "返回笔记", icArrowLeft, true, false, func() { a.preview = "" })
	})
	horizontalRule(c, p)
	ui.Scroll(c).Grow(1).FillWidth().Background(p.canvas).AlignItems(ui.Center).Padding(24).Children(func() {
		ui.Box(c).MinHeight(min(120, height-96)).Padding(14).Radius(12).Background(ui.Hex("#ffffff")).
			Border(1, p.rule).Shadow(0, 12, 32, -8, p.shadow).Children(func() {
			ui.Image(c, bitmap).Size(size, size*float32(h)/float32(w)).Fit(ui.Contain).Label("笔记原图")
		})
	})
}

func (a *workspace) editor(c *ui.Context, p palette, availH float32) {
	heading := "编辑笔记"
	if a.editMode == editNewChild {
		heading = "新建子笔记"
	}
	pad := float32(24)
	if a.touchUI() || availH < 420 {
		pad = 16
	}
	bodyH := clamp(availH-220, 110, 260)
	ui.Scroll(c.Key("editor")).Grow(1).MinHeight(0).FillWidth().Padding(pad).Gap(10).Children(func() {
		ui.Text(c, heading).FontSize(19).FontWeight(700).LetterSpacing(-0.35).TextColor(p.ink)
		ui.Column(c).FillWidth().Gap(5).Children(func() {
			ui.Text(c, "标题").FontSize(12).FontWeight(600).TextColor(p.secondary)
			ui.TextInput(c, &a.draftTitle).Label("标题").Placeholder("输入笔记标题…").
				FillWidth().Height(40).Radius(8).PaddingX(10).FontSize(14).
				Background(p.inputBg).Border(1, p.border).TextColor(p.ink)
		})
		ui.Column(c).FillWidth().Gap(5).Children(func() {
			ui.Text(c, "正文").FontSize(12).FontWeight(600).TextColor(p.secondary)
			ui.TextArea(c, &a.draftBody).Label("正文").Placeholder("输入笔记正文…").
				FillWidth().Height(bodyH).Radius(8).Padding(10).FontSize(14).LineHeight(1.55).
				Background(p.inputBg).Border(1, p.border).TextColor(p.ink)
		})
		ui.Row(c).Gap(8).Padding(4, 0, 0).Children(func() {
			btnH := float32(34)
			if a.touchUI() {
				btnH = 38
			}
			primaryButton(c, p, "保存", nil, a.busy || strings.TrimSpace(a.draftTitle) == "", a.saveEditor).Height(btnH)
			pillButton(c, p, "取消", nil, true, a.busy, func() {
				a.editMode = editNone
				a.flushImports()
			}).Height(btnH)
		})
	})
}
