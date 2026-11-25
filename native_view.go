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
	if a.preview == "" {
		a.media.original = mediaOriginal{}
	}
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
		closePreview := func() {
			a.preview = ""
			a.previewZoom = 1
		}
		c.OnShortcut(0, ui.KeyBack, closePreview)
		c.OnShortcut(0, ui.KeyEscape, closePreview)
		c.OnShortcut(0, ui.KeyLeft, func() { a.stepPreviewImage(-1) })
		c.OnShortcut(0, ui.KeyRight, func() { a.stepPreviewImage(1) })
	} else if a.reviewOpen {
		closeReview := func() { a.reviewOpen = false }
		c.OnShortcut(0, ui.KeyBack, closeReview)
		c.OnShortcut(0, ui.KeyEscape, closeReview)
		c.OnShortcut(0, ui.KeySpace, func() {
			if a.reviewIndex < len(a.reviewQueue) {
				a.reviewRevealed = !a.reviewRevealed
			}
		})
		c.OnShortcut(0, ui.KeyRight, func() {
			if a.reviewRevealed {
				a.markReviewCard(true)
			}
		})
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
		c.OnShortcut(0, ui.KeyBack, a.requestCancelEdit)
		c.OnShortcut(0, ui.KeyEscape, a.requestCancelEdit)
		c.OnShortcut(ui.Cmd, ui.KeyS, func() {
			if (a.editMode == editNote || a.editMode == editNewChild) && !a.busy {
				a.saveEditor()
			}
		})
	} else if a.pdfTOCOpen {
		closeTOC := func() { a.pdfTOCOpen = false }
		c.OnShortcut(0, ui.KeyBack, closeTOC)
		c.OnShortcut(0, ui.KeyEscape, closeTOC)
	} else if a.page != browse {
		back := func() {
			a.pageDir = -1
			a.page = browse
		}
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
	if a.bookID != "" && a.preview == "" && !a.preferencesOpen && !a.editing() {
		c.OnShortcut(ui.Cmd, ui.KeyF, func() {
			a.sidebarHiddenForSplit = false
			if !wide {
				a.pageDir = 1
				a.page = libraryScreen
			} else if a.settings.SidebarCollapsed {
				next := a.settings
				next.SidebarCollapsed = false
				a.saveSettings(next)
			}
			a.focusSearch = true
		})
	}
	if a.bookID != "" && a.preview == "" && !a.preferencesOpen && !a.editing() && a.page == browse {
		if a.mode != canvasMode || a.settings.LayoutMode() != notebook.LayoutOutline {
			c.OnShortcut(ui.Cmd, ui.KeyZ, func() { a.stepDrawing(true) })
			c.OnShortcut(ui.Cmd|ui.Shift, ui.KeyZ, func() { a.stepDrawing(false) })
		}
		if a.mode != canvasMode && !a.pdfTOCOpen {
			c.OnShortcut(0, ui.KeyPageUp, func() { a.setPDFPage(a.pdf.page - 1) })
			c.OnShortcut(0, ui.KeyPageDown, func() { a.setPDFPage(a.pdf.page + 1) })
		}
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

	ui.Box(c).Fill().Background(p.shell).Draw(func(paint *ui.Painter, _ ui.Rect) {
		if scale := paint.Scale(); scale != a.pdfDensity {
			a.pdfDensity = scale
			if a.mode != canvasMode {
				c.Invalidate()
			}
		}
	}).Children(func() {
		ui.Column(c.Key("main-shell-col")).Fill().Children(func() {
			availableH := height
			if !wide || a.bookID == "" {
				a.mobileHeader(c, p, wide)
				availableH -= 55
			}
			if a.updateBanner(c, p) {
				availableH -= a.updateBannerHeight()
			}
			if a.bookID == "" {
				a.emptyState(c, p)
				return
			}
			if !wide {
				stage := ui.Box(c.Key("mobile-stage")).Grow(1).FillWidth().MinHeight(0).Clip()
				lastPage := ui.Local(stage, "last-page", func() screen { return a.page })
				if *lastPage != a.page {
					if pageRank(a.page) > pageRank(*lastPage) {
						a.pageDir = 1
					} else {
						a.pageDir = -1
					}
					*lastPage = a.page
				}
				dir := float32(a.pageDir)
				stage.ChildExit(childExitSlide(dir)).Children(func() {
					switch a.page {
					case libraryScreen:
						ui.Box(c.Key("mob-library")).Absolute().Fill().Transition(trPageSlide(dir)).Children(func() {
							a.sidebar(c, p, width, false)
						})
					case detail:
						ui.Box(c.Key("mob-detail")).Absolute().Fill().Transition(trPageSlide(dir)).Children(func() {
							a.readerPanel(c, p, width, availableH, false, false)
						})
					default:
						ui.Box(c.Key("mob-workspace")).Absolute().Fill().Transition(trPageSlide(dir)).Children(func() {
							surface := ui.Box(c.Key("main-surface")).Fill().Clip().Background(p.surface)
							surface.Children(func() {
								a.documentWorkspace(c, p, width, max(1, availableH))
							})
						})
					}
				})
				return
			}

			ui.Row(c.Key("desktop-row")).Grow(1).MinHeight(0).FillWidth().Children(func() {
				var sideWidth float32
				if side {
					sideWidth = 240
					ui.Box(c.Key("desktop-sidebar")).Shrink(0).FillHeight().ZIndex(2).
						Transition(trPanelSlideH).Children(func() {
						a.sidebar(c, p, sideWidth, true)
					})
				}
				inset := float32(8)
				leftInset := inset
				if side {
					leftInset = 0
				}
				paneW := width - sideWidth - inset - leftInset
				pane := ui.Box(c.Key("main-pane")).Grow(1).FillHeight().MinWidth(0).
					Padding(inset, inset, inset, leftInset)
				pane.Children(func() {
					surface := ui.Box(c.Key("main-surface")).Fill().Clip().Background(p.surface).
						Radius(12).Border(1, p.rule).Shadow(0, 2, 10, -2, p.shadow)
					surface.Children(func() {
						a.documentWorkspace(c, p, paneW, max(1, availableH-2*inset))
						ui.Box(c.Key("note-modal-host")).Absolute().Fill().ZIndex(10).
							IgnoreInput(a.page != detail).Center().Children(func() {
							if a.page == detail {
								a.noteModal(c, p, paneW, max(1, availableH-2*inset))
							}
						})
						ui.Box(c.Key("review-modal-host")).Absolute().Fill().ZIndex(15).
							IgnoreInput(!a.reviewOpen).Center().Children(func() {
							if a.reviewOpen {
								a.reviewModal(c, p, paneW, max(1, availableH-2*inset))
							}
						})
						ui.Box(c.Key("settings-modal-host")).Absolute().Fill().ZIndex(20).
							IgnoreInput(!a.preferencesOpen).Center().Children(func() {
							if a.preferencesOpen {
								a.settingsCard(c, p, paneW, max(1, availableH-2*inset))
							}
						})
					})
				})
			})
		})
		showNarrowReview := a.reviewOpen && !wide && a.bookID != ""
		if !wide {
			ui.Box(c.Key("review-modal-host")).Absolute().Fill().ZIndex(15).
				IgnoreInput(!showNarrowReview).Center().Children(func() {
				if showNarrowReview {
					a.reviewModal(c, p, width, height)
				}
			})
		}
		showRootSettings := a.preferencesOpen && (!wide || a.bookID == "")
		if !wide || a.bookID == "" {
			ui.Box(c.Key("settings-modal-host")).Absolute().Fill().ZIndex(20).
				IgnoreInput(!showRootSettings).Center().Children(func() {
				if showRootSettings {
					a.settingsCard(c, p, width, height)
				}
			})
		}
		ui.Box(c.Key("image-modal-host")).Absolute().Fill().ZIndex(25).
			IgnoreInput(a.preview == "").Children(func() {
			if a.preview != "" {
				a.imageView(c, p, width, height)
			}
		})
		a.statusToast(c, p, width, !wide || a.bookID == "")
	})
	if w > 0 && h > 0 {
		a.markAndroidFramePainted()
	}
}

func pageRank(p screen) int {
	switch p {
	case browse:
		return 0
	case libraryScreen:
		return 1
	case detail:
		return 2
	default:
		return 0
	}
}

func (a *workspace) statusToast(c *ui.Context, p palette, width float32, hasMobileHeader bool) {
	visible := a.message != ""
	var left time.Duration
	if visible && !a.messagePending && !a.messageError {
		left = time.Until(a.messageUntil)
		if left <= 0 {
			visible = false
		}
	}
	topOffset := float32(14)
	if hasMobileHeader {
		topOffset = 62
	}
	maxW := max(220, min(540, width-24))
	ui.Row(c.Key("status-toast")).Absolute().Top(topOffset).Left(0).Right(0).ZIndex(30).Center().
		IgnoreInput(!visible).Children(func() {
		if !visible {
			return
		}
		fg, bg, borderCol, ic := p.accent, p.elevated, p.tintBorder, icCheck
		if a.messageError {
			fg, bg, borderCol, ic = p.danger, p.dangerWash, p.danger.Alpha(.35), icAlert
		} else if a.messagePending {
			fg, bg, borderCol, ic = p.accent, p.tint, p.tintBorder, icImport
		}
		pill := ui.Row(c.Key("status-banner")).MaxWidth(maxW).Padding(8, 14).Gap(8).Radius(12).
			AlignItems(ui.Center).Background(bg).Border(1, borderCol).
			Shadow(0, 8, 22, -4, p.shadow).Transition(trToast)
		if a.messageError {
			applyErrorShake(pill, a.toastShakeRun)
		}
		pill.Children(func() {
			if a.messagePending {
				ui.Spinner(c).Size(14, 14).TextColor(fg).Shrink(0)
			} else {
				ui.Icon(c, ic).Size(15, 15).TextColor(fg).Shrink(0).ContentTransition(ctIcon)
			}
			txt := ui.Text(c, a.message).FontSize(12.5).FontWeight(550).TextColor(fg).Shrink(1).MinWidth(0)
			if !a.messageError {
				txt.SingleLine().Ellipsis("…")
			} else {
				txt.LineHeight(1.35)
			}
			if a.toastActionLabel != "" && a.toastActionFn != nil && !a.messageError {
				fn := a.toastActionFn
				act := ui.ButtonBase(c.Key("toast-action")).Height(24).PaddingX(8).Radius(6).Center().Shrink(0).
					Background(p.accent).Label(a.toastActionLabel).FocusRingColor(p.accent).
					Transition(trColorMicro).
					OnClick(func() {
						a.dismissMessage()
						fn()
					})
				applyPressPose(act, act.Pressed(), false)
				act.Children(func() {
					ui.Text(c, a.toastActionLabel).FontSize(11.5).FontWeight(600).TextColor(p.onAccent).SingleLine()
				})
			}
			if a.messageError {
				designToolbarButton(c, p, icClose, "关闭提示", 24, false, false, a.dismissMessage)
			}
		})
	})
	if visible && !a.messagePending && !a.messageError {
		c.After(left)
	}
}

func (a *workspace) updateBannerHeight() float32 {
	if a.touchUI() {
		return 45
	}
	return 37
}

func (a *workspace) updateBanner(c *ui.Context, p palette) bool {
	if a.preferencesOpen || a.editing() || a.page == detail {
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
		primaryFn = a.openPreferencesAtUpdate
	default:
		if ver != "" {
			bannerText = fmt.Sprintf("发现新版本 v%s", ver)
		} else {
			bannerText = "发现新版本"
		}
		primaryLabel = "查看更新"
		primaryFn = a.openPreferencesAtUpdate
	}

	barH := float32(36)
	btnH := float32(26)
	padX := float32(16)
	if a.touchUI() {
		barH = 44
		btnH = 32
		padX = 12
	}
	ui.Column(c.Key("update-banner-wrap")).FillWidth().Shrink(0).Transition(trPanelSlideV).Children(func() {
		ui.Row(c.Key("update-banner")).Height(barH).FillWidth().Shrink(0).Padding(0, padX).Gap(8).
			AlignItems(ui.Center).Background(p.tint).Children(func() {
			ui.Icon(c, icon).Size(14, 14).TextColor(p.accent).Shrink(0).ContentTransition(ctIcon)
			ui.Text(c, bannerText).FontSize(12.5).FontWeight(550).TextColor(p.accent).
				Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			act := ui.ButtonBase(c.Key("update-banner-action")).Height(btnH).PaddingX(10).Radius(6).Center().Shrink(0).
				Label(primaryLabel).Disabled(a.editing()).FocusRingColor(p.accent).Transition(trColorMicro).OnClick(primaryFn)
			if a.editing() {
				act.Background(p.accent.Alpha(.45))
			} else if act.Pressed() {
				act.Background(p.accentBot)
			} else {
				act.Background(p.accent)
			}
			applyPressPose(act, act.Pressed(), a.editing())
			act.Children(func() {
				ui.Text(c, primaryLabel).FontSize(11.5).FontWeight(600).TextColor(p.onAccent).SingleLine()
			})
			later := ui.ButtonBase(c.Key("update-banner-dismiss")).Height(btnH).PaddingX(8).Radius(6).Center().Shrink(0).
				Label("稍后").FocusRingColor(p.accent).Transition(trColorMicro).OnClick(a.dismissUpdateBanner)
			if later.Pressed() {
				later.Background(p.pressed)
			} else if later.Hovered() {
				later.Background(p.hover)
			}
			applyPressPose(later, later.Pressed(), false)
			later.Children(func() {
				ui.Text(c, "稍后").FontSize(11.5).FontWeight(550).TextColor(p.secondary).SingleLine()
			})
		})
		horizontalRule(c, p)
	})
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
				ui.Text(c, "开始阅读与整理").FontSize(22).FontWeight(700).LetterSpacing(-0.4).TextColor(p.ink)
				ui.Text(c, "导入 MarginNote 笔记包或 PDF 文档，桌面端可直接拖入").
					FontSize(13).LineHeight(1.5).TextColor(p.quiet)
			})
			ui.Box(c).Height(4)
			primaryButton(c, p, "导入笔记或 PDF", icImport, a.busy, a.chooseImport).Height(40).Padding(0, 18)
		})
	})
}

func (a *workspace) mobileHeader(c *ui.Context, p palette, side bool) {
	ui.Row(c).Height(54).FillWidth().Shrink(0).AlignItems(ui.Center).Padding(0, 14).Gap(8).
		Background(p.surface).Children(func() {
		if a.bookID != "" && !side {
			switch a.page {
			case libraryScreen:
				pillButton(c, p, "返回", icArrowLeft, false, a.editing(), func() {
					a.pageDir = -1
					a.page = browse
				})
			case detail:
				target := browse
				if a.prevPage == libraryScreen {
					target = libraryScreen
				}
				pillButton(c, p, "返回", icArrowLeft, false, a.editing(), func() {
					a.pageDir = -1
					a.page = target
				})
			default:
				pillButton(c, p, "笔记库", icSidebar, false, a.editing(), func() {
					a.pageDir = 1
					a.page = libraryScreen
				}).Label("笔记本与搜索")
			}
		}
		ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).MinWidth(0).Children(func() {
			brandMark(c, p, 22)
			ui.Text(c, "Folio").FontSize(15).FontWeight(700).LetterSpacing(-0.3).TextColor(p.ink).SingleLine()
		})
		a.transferButton(c, p)
		a.mobileSettingsButton(c, p)
	})
	horizontalRule(c, p)
}

func (a *workspace) mobileSettingsButton(c *ui.Context, p palette) {
	disabled := a.editing()
	b := ui.ButtonBase(c).Label("设置").Size(34, 34).Radius(8).Center().Shrink(0).
		Disabled(disabled).FocusRingColor(p.accent).Transition(trColorMicro).OnClick(a.openPreferences)
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
	applyPressPose(b, b.Pressed(), disabled)
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
				Transition(trColorMicro).
				Disabled(a.editing()).Label("切换笔记本").Children(func() {
				ui.Icon(c, icBook).Size(14, 14).TextColor(p.accent).Shrink(0)
				ui.Text(c, a.tree.book.Title).Grow(1).MinWidth(0).SingleLine().Ellipsis("…").
					FontSize(13).FontWeight(650).LetterSpacing(-0.15).TextColor(p.ink)
				ui.Icon(c, icChevronDown).Size(13, 13).TextColor(p.quiet).Shrink(0)
			})
			applyPressPose(b.Trigger, b.Trigger.Pressed(), a.editing())
			b.Popup(func(panel ui.Element) {
				panel.Background(p.elevated).Border(1, p.border).Radius(10).Padding(5).MaxHeight(300).
					Shadow(0, 10, 30, -6, p.shadow).Transition(trPopover)
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
							ui.Text(c, book.Title).SingleLine().Ellipsis("…").Shrink(1).FontSize(13).FontWeight(550).TextColor(fg)
						})
					}
				})
			})
		})

		// Recessed Apple/Linear search field + filter popover button
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			sf := ui.SearchField(c, &a.query).Height(36).Grow(1).MinWidth(0).PaddingX(10).Radius(8).FontSize(12.5).
				Background(p.inputBg).Border(1, p.rule).TextColor(p.ink).Label("搜索笔记").
				Placeholder("搜索笔记…").Disabled(a.editing())
			if a.focusSearch {
				sf.Focus(ui.FocusOptions{PreventScroll: true})
				a.focusSearch = false
			}
			a.filterButton(c, p)
		})

		// Section kicker + scrollable nav list
		queryActive := strings.TrimSpace(a.query) != ""
		filtering := a.hasActiveFilter()
		var searchIDs []string
		kicker := "分组目录"
		if queryActive || filtering {
			searchIDs = a.searchNotes()
			switch {
			case queryActive && filtering:
				kicker = fmt.Sprintf("搜索与筛选 · %d", len(searchIDs))
			case filtering:
				kicker = fmt.Sprintf("筛选结果 · %d", len(searchIDs))
			default:
				kicker = fmt.Sprintf("搜索结果 · %d", len(searchIDs))
			}
		}
		kickerText := ui.Text(c, kicker).FontSize(11).FontWeight(600).LetterSpacing(0.45).
			TextColor(p.quiet).Padding(8, 10, 4).Shrink(0)
		if queryActive || filtering {
			kickerText.ContentTransition(ctNumeric(false))
		}

		ui.Scroll(c.Key("sidebar-scroll")).Grow(1).MinHeight(0).FillWidth().Gap(2).Children(func() {
			if queryActive || filtering {
				if len(searchIDs) == 0 {
					ui.Column(c).FillWidth().Padding(16, 10).Gap(10).Center().Children(func() {
						ui.Text(c, "没有匹配的笔记").FontSize(12.5).TextColor(p.quiet)
						clearLabel := "清空搜索"
						if filtering && !queryActive {
							clearLabel = "清空筛选"
						} else if filtering && queryActive {
							clearLabel = "清空搜索与筛选"
						}
						pillButton(c, p, clearLabel, icClose, true, false, func() {
							a.query = ""
							a.clearFilters()
						}).Height(28)
					})
					return
				}
				for _, id := range searchIDs {
					sub := a.searchResultSubtitle(id, a.query)
					a.searchNavItem(c.Key(id), p, noteTitle(a.tree.notes[id]), sub, id == a.selected, func() { a.openNote(id) })
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
			Label("设置").Disabled(a.editing()).Transition(trColorMicro).OnClick(a.openPreferences)
		if settings.Pressed() && !a.editing() {
			settings.Background(p.pressed)
		} else if settings.Hovered() && !a.editing() {
			settings.Background(p.hover)
		}
		applyPressPose(settings, settings.Pressed(), a.editing())
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
		Label(label).Disabled(a.editing()).Selected(selected).FocusRingColor(p.accent).
		Transition(trColorMicro).OnClick(onClick)
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
	applyPressPose(row, row.Pressed(), a.editing())
	row.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(14, 14).TextColor(icColor).Shrink(0)
		}
		ui.Text(c, label).SingleLine().Ellipsis("…").FontSize(13).FontWeight(weight).LetterSpacing(-0.1).
			TextColor(fg).Grow(1).MinWidth(0)
		if count != "" {
			ui.Text(c, count).FontSize(11).FontWeight(550).FontFeatures("tnum").
				TextColor(p.quiet).Shrink(0).ContentTransition(ctNumeric(false))
		}
	})
}

func (a *workspace) applyTagFilter(tag string) {
	if a.filterTag == tag {
		a.filterTag = ""
	} else {
		a.filterTag = tag
	}
	a.filterPopoverOpen = false
	a.invalidateSearch()
	hapticSelection()
}

func (a *workspace) filterButton(c *ui.Context, p palette) {
	cnt := a.activeFilterCount()
	label := "筛选"
	if cnt > 0 {
		label = fmt.Sprintf("筛选 · %d", cnt)
	}
	btn := ui.ButtonBase(c.Key("filter-btn")).Height(36).Padding(0, 10).Radius(8).Center().Shrink(0).
		Label(label).Tooltip("按颜色、标签或附件筛选笔记").Disabled(a.editing()).FocusRingColor(p.accent).
		Selected(cnt > 0).Transition(trColorMicro).OnClick(func() { a.filterPopoverOpen = !a.filterPopoverOpen })
	fg := p.secondary
	switch {
	case cnt > 0 || a.filterPopoverOpen:
		btn.Background(p.tint).Border(1, p.tintBorder)
		fg = p.accent
	case btn.Pressed():
		btn.Background(p.pressed).Border(1, p.rule)
		fg = p.ink
	case btn.Hovered():
		btn.Background(p.hover).Border(1, p.rule)
		fg = p.ink
	default:
		btn.Background(p.inputBg).Border(1, p.rule)
	}
	applyPressPose(btn, btn.Pressed(), a.editing())
	btn.Children(func() {
		ui.Text(c, label).FontSize(12).FontWeight(550).TextColor(fg).SingleLine().ContentTransition(ctNumeric(false))
	})
	ui.PopoverBase(c.Key("filter-popover"), btn, &a.filterPopoverOpen, func(panel ui.Element) {
		panel.Width(252).MaxHeight(380).Radius(12).Clip().Background(p.elevated).
			Border(1, p.border).Shadow(0, 12, 30, -6, p.shadow).Transition(trPopover)
		ui.Scroll(c.Key("filter-scroll")).FillWidth().MaxHeight(380).Padding(12).Gap(10).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, "筛选笔记").FontSize(12.5).FontWeight(650).TextColor(p.ink).Grow(1)
				if a.hasActiveFilter() {
					pillButton(c, p, "清空筛选", icClose, true, false, a.clearFilters).Height(24)
				}
			})
			usedColors := a.bookUsedColors()
			if len(usedColors) > 0 {
				ui.Column(c).FillWidth().Gap(6).Children(func() {
					ui.Text(c, "颜色").FontSize(11).FontWeight(600).TextColor(p.quiet)
					ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
						allBtn := ui.ButtonBase(c.Key("fcol-all")).Height(24).PaddingX(8).Radius(99).Center().
							Label("全部颜色").Transition(trColorMicro).OnClick(func() {
							a.filterColorSet, a.filterColor = false, 0
							a.invalidateSearch()
						})
						if !a.filterColorSet {
							allBtn.Background(p.tint).Border(1, p.tintBorder)
						} else {
							allBtn.Background(p.shell).Border(1, p.rule)
						}
						applyPressPose(allBtn, allBtn.Pressed(), false)
						allBtn.Children(func() { ui.Text(c, "全部").FontSize(11).TextColor(p.secondary) })
						for _, ci := range usedColors {
							ci := ci
							ct := mnCardLight[ci]
							if p.dark {
								ct = mnCardDark[ci]
							}
							sel := a.filterColorSet && a.filterColor == ci
							sw := ui.ButtonBase(c.Key(fmt.Sprintf("fcol-%d", ci))).Size(24, 24).Radius(99).
								Center().Label(fmt.Sprintf("筛选颜色 %d", ci)).Transition(trColorMicro).OnClick(func() {
								if sel {
									a.filterColorSet, a.filterColor = false, 0
								} else {
									a.filterColorSet, a.filterColor = true, ci
								}
								a.invalidateSearch()
							})
							applySelectScalePose(sw, sel, sw.Pressed())
							sw.Children(func() {
								dot := ui.Box(c).Size(16, 16).Radius(99).Background(ct.accent)
								if sel {
									dot.Border(2, p.ink)
								}
							})
						}
					})
				})
			}
			usedTags := a.bookUsedTags()
			if len(usedTags) > 0 {
				ui.Column(c).FillWidth().Gap(6).Children(func() {
					ui.Text(c, "标签").FontSize(11).FontWeight(600).TextColor(p.quiet)
					ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
						for _, tag := range usedTags {
							tag := tag
							sel := a.filterTag == tag
							pill := ui.ButtonBase(c.Key("ftag-"+tag)).Height(24).Padding(0, 8).Radius(99).
								Center().Label("#"+tag).Transition(trColorMicro).OnClick(func() { a.applyTagFilter(tag) })
							fg := p.secondary
							if sel {
								pill.Background(p.tint).Border(1, p.tintBorder)
								fg = p.accent
							} else {
								pill.Background(p.shell).Border(1, p.rule)
							}
							applyPressPose(pill, pill.Pressed(), false)
							pill.Children(func() { ui.Text(c, "#"+tag).FontSize(11).TextColor(fg).SingleLine() })
						}
					})
				})
			}
			ui.Column(c).FillWidth().Gap(6).Children(func() {
				ui.Text(c, "包含内容").FontSize(11).FontWeight(600).TextColor(p.quiet)
				ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
					pillButton(c, p, "有附图", icPage, a.filterHasImage, false, func() {
						a.filterHasImage = !a.filterHasImage
						a.invalidateSearch()
					}).Height(26)
					pillButton(c, p, "有页码", icBook, a.filterHasPage, false, func() {
						a.filterHasPage = !a.filterHasPage
						a.invalidateSearch()
					}).Height(26)
				})
			})
		})
	})
}

func (a *workspace) searchNavItem(c *ui.Context, p palette, label, subtitle string, selected bool, onClick func()) {
	if subtitle == "" {
		a.navItem(c, p, label, nil, "", selected, onClick)
		return
	}
	row := ui.ButtonBase(c).FillWidth().MinHeight(46).Padding(6, 10).Gap(2).
		Justify(ui.Center).Radius(8).
		Label(label).Disabled(a.editing()).Selected(selected).FocusRingColor(p.accent).
		Transition(trColorMicro).OnClick(onClick)
	fg := p.secondary
	weight := 500
	switch {
	case selected:
		row.Background(p.selectedBg).Border(1, p.rule).
			InsetShadow(0, 1, 0, 0, p.specular).
			Shadow(0, 1, 2, 0, p.shadowSoft)
		fg = p.ink
		weight = 600
	case row.Pressed():
		row.Background(p.pressed).Border(1, ui.Color{})
		fg = p.ink
	case row.Hovered():
		row.Background(p.hover).Border(1, ui.Color{})
		fg = p.ink
	default:
		row.Border(1, ui.Color{})
	}
	applyPressPose(row, row.Pressed(), a.editing())
	row.Children(func() {
		ui.Column(c).FillWidth().Gap(2).Children(func() {
			ui.Text(c, label).SingleLine().Ellipsis("…").FontSize(13).FontWeight(weight).LetterSpacing(-0.1).TextColor(fg)
			ui.Text(c, subtitle).SingleLine().Ellipsis("…").FontSize(11).TextColor(p.quiet)
		})
	})
}

func (a *workspace) selectScope(id string) {
	a.pageDir = -1
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
	// Translucent scrim backdrop dismisses the modal when clicked outside.
	ui.ButtonBase(c.Key("note-modal-scrim")).Absolute().Left(0).Top(0).Right(0).Bottom(0).
		Label("关闭笔记卡片").Background(p.scrim).Transition(trScrim).
		OnClick(func() {
			if a.editing() {
				a.requestCancelEdit()
			} else {
				a.pageDir = -1
				a.page = browse
			}
		})

	// Centered floating editorial card
	ui.Column(c.Key("note-modal-card")).ZIndex(11).Width(modalW).Height(modalH).Radius(16).Clip().
		Background(p.surface).Border(1, p.border).
		InsetShadow(0, 1, 0, 0, p.specular).
		Shadow(0, 24, 60, -14, p.modalShadow).
		Shadow(0, 4, 14, -2, p.shadow).
		Transition(trModalCard).Children(func() {
		a.readerPanel(c, p, modalW, modalH, true, true)
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
					if startIdx > 0 {
						ui.Text(c, "…").FontSize(12).FontWeight(500).TextColor(p.quiet).Padding(0, 2)
						ui.Icon(c, icChevronRight).Size(11, 11).TextColor(p.quiet).Shrink(0)
					}
					for i := startIdx; i < len(ancestors); i++ {
						ancID := ancestors[i]
						ancTrail := slices.Clone(trail[:i+1])
						crumb := ui.ButtonBase(c.Key("crumb-"+ancID)).Label("跳转到"+noteTitle(a.tree.notes[ancID])).
							Height(30).Padding(0, 6).Radius(6).AlignItems(ui.Center).Gap(4).
							Disabled(a.editing()).Transition(trColorMicro).OnClick(func() {
							a.setNoteWithTrail(ancID, ancTrail)
							a.pageDir = 1
							a.page = detail
						})
						fg := p.quiet
						if !a.editing() {
							if crumb.Pressed() {
								crumb.Background(p.pressed)
								fg = p.ink
							} else if crumb.Hovered() {
								crumb.Background(p.hover)
								fg = p.ink
							}
						}
						applyPressPose(crumb, crumb.Pressed(), a.editing())
						crumb.Children(func() {
							ui.Text(c, noteTitle(a.tree.notes[ancID])).FontSize(12).FontWeight(500).
								TextColor(fg).SingleLine().Ellipsis("…").Shrink(1)
						})
						if i < len(ancestors)-1 {
							ui.Icon(c, icChevronRight).Size(11, 11).TextColor(p.quiet).Shrink(0)
						}
					}
					ui.Box(c).Grow(1).MinWidth(0)
				})
			} else {
				ui.Box(c).Grow(1)
			}
			if !a.editing() {
				pillButton(c, p, "新建子笔记", icPlus, false, a.busy, a.startAddChild)
				pillButton(c, p, "编辑", icEdit, true, a.busy || n.ID == "", a.startEditNote)
				ui.Box(c).Width(2).Shrink(0)
				iconButton(c, p, icSettingDelete, "删除笔记", true, a.busy || n.ID == "", a.confirmDeleteNote)
			}
			if showClose {
				ui.Box(c).Width(1).Height(18).Background(p.rule).Shrink(0)
				iconButton(c, p, icClose, "关闭笔记卡片", false, a.editing(), func() {
					a.pageDir = -1
					a.page = browse
				})
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
			Padding(26, padX, 36).Gap(18).Transition(trCrossfade).Children(func() {
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
								Transition(trColorMicro).OnClick(func() { a.jumpToNotePDF(n) })
							bg, bdr, fg := p.tint, p.tintBorder, p.accent
							if !a.editing() {
								if btn.Pressed() {
									bg = p.pressed
								} else if btn.Hovered() {
									bg = p.hover
								}
							}
							applyPressPose(btn, btn.Pressed(), a.editing())
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
				tagPillsRowAction(c, p, n.Tags, a.applyTagFilter)
			}

			if a.reciteMode && (noteText(n) != "" || noteHasImage(n)) {
				if !a.revealedNotes[n.ID] {
					occlude := ui.ButtonBase(c.Key("recite-detail-reveal")).FillWidth().MinHeight(104).
						Radius(10).Center().Background(p.tint).Border(1, p.tintBorder).
						Label("点击揭晓正文").Transition(trCrossfade).OnClick(func() {
						a.setNoteRevealed(n.ID, true)
						hapticLight()
					})
					applyPressPose(occlude, occlude.Pressed(), false)
					occlude.Children(func() {
						ui.Column(c).Center().Gap(4).Children(func() {
							ui.Text(c, "背诵模式：已隐藏正文与附图").FontSize(13).FontWeight(600).TextColor(p.accent)
							ui.Text(c, "点击此处揭晓答案").FontSize(12).TextColor(p.secondary)
						})
					})
				} else {
					ui.Column(c.Key("recite-detail-revealed")).FillWidth().Gap(18).Shrink(0).
						Transition(trCrossfade).Children(func() {
						a.noteContent(c, p, n, width-padX*2-2)
						pillButton(c, p, "重新遮盖", icBook, false, false, func() {
							a.setNoteRevealed(n.ID, false)
						}).Height(26)
					})
				}
			} else {
				a.noteContent(c, p, n, width-padX*2-2)
			}

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
									Transition(trColorMicro).OnClick(func() { a.openNote(targetID) })
								bg, bdr, fg := p.shell, p.rule, p.secondary
								if !a.editing() {
									if chip.Pressed() {
										bg, bdr, fg = p.pressed, p.tintBorder, p.accent
									} else if chip.Hovered() {
										bg, bdr, fg = p.tint, p.tintBorder, p.accent
									}
								}
								applyPressPose(chip, chip.Pressed(), a.editing())
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

			var visibleChildren []string
			for _, id := range a.tree.kids[a.selected] {
				child := a.tree.notes[id]
				if child.Title != "" || hasNoteContent(child) || len(a.tree.kids[id]) > 0 {
					visibleChildren = append(visibleChildren, id)
				}
			}
			if len(visibleChildren) > 0 {
				ui.Box(c).Height(4).Shrink(0)
				horizontalRule(c, p)
				ui.Column(c).FillWidth().Gap(5).Shrink(0).Children(func() {
					ui.Text(c, fmt.Sprintf("下级笔记 · %d", len(visibleChildren))).FontSize(11).FontWeight(600).
						FontFeatures("tnum").LetterSpacing(0.4).TextColor(p.quiet).Padding(4, 2, 4)
					for _, id := range visibleChildren {
						a.childRow(c.Key(id), p, noteTitle(a.tree.notes[id]), func() { a.openChildNote(id) })
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
		Label(label).Disabled(a.editing()).FocusRingColor(p.accent).
		Transition(trColorMicro).OnClick(onClick)
	fg := p.ink
	if row.Pressed() {
		row.Background(p.pressed).Border(1, p.rule)
	} else if row.Hovered() {
		row.Background(p.hover).Border(1, p.rule)
	} else {
		row.Background(p.shell).Border(1, p.rule)
	}
	applyPressPose(row, row.Pressed(), a.editing())
	row.Children(func() {
		ui.Text(c, label).SingleLine().Ellipsis("…").FontSize(13).FontWeight(550).LetterSpacing(-0.1).
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
			if w, h := a.imageDims(b.Image); w <= 0 || h <= 0 {
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
	w, h := a.imageDims(ref)
	if w <= 0 || h <= 0 {
		return false
	}
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
		Transition(trColorMicro).
		OnClick(func() {
			a.previewDir = 0
			a.preview = ref
		})
	if card.Hovered() {
		card.Border(1, p.accent.Alpha(.5)).Shadow(0, 4, 14, -2, p.shadow)
	}
	applyPressPose(card, card.Pressed(), false)
	card.Children(func() {
		ui.Box(c).Size(imgW, imgH).Draw(func(p *ui.Painter, rect ui.Rect) {
			a.paintMediaThumbnail(p, rect, ref)
		})
	})
	return true
}

func (a *workspace) previewImagesList() []string {
	var imgs []string
	if n, ok := a.tree.notes[a.selected]; ok {
		if n.Clip != "" {
			imgs = append(imgs, n.Clip)
		}
		for _, blk := range n.Blocks {
			if blk.Image != "" && !slices.Contains(imgs, blk.Image) {
				imgs = append(imgs, blk.Image)
			}
		}
	}
	if a.preview != "" && !slices.Contains(imgs, a.preview) {
		imgs = append([]string{a.preview}, imgs...)
	}
	return imgs
}

func (a *workspace) stepPreviewImage(delta int) {
	imgs := a.previewImagesList()
	if len(imgs) <= 1 {
		return
	}
	if delta < 0 {
		a.previewDir = -1
	} else if delta > 0 {
		a.previewDir = 1
	}
	idx := slices.Index(imgs, a.preview)
	if idx < 0 {
		idx = 0
	} else {
		idx = (idx + delta + len(imgs)) % len(imgs)
	}
	a.preview = imgs[idx]
	a.previewZoom = 1
}

func (a *workspace) imageView(c *ui.Context, p palette, width, height float32) {
	bitmap, ready := a.originalMedia(a.preview)
	if a.previewZoom <= 0 {
		a.previewZoom = 1
	}
	imgs := a.previewImagesList()
	imgIdx := max(0, slices.Index(imgs, a.preview))
	btnSz := float32(30)
	if a.touchUI() {
		btnSz = 36
	}
	closePreview := func() {
		a.preview = ""
		a.previewZoom = 1
		a.previewDir = 0
	}

	ui.ButtonBase(c.Key("img-backdrop")).Absolute().Fill().ZIndex(0).Background(p.canvas).
		Label("关闭图片预览").Transition(trScrim).OnClick(closePreview)

	ui.Column(c.Key("img-toolbar")).Absolute().Top(0).Left(0).Right(0).ZIndex(2).
		Transition(trScrim).Children(func() {
		ui.Row(c).Height(52).FillWidth().PaddingX(16).Gap(8).AlignItems(ui.Center).Background(p.surface).Children(func() {
			pillButton(c, p, "返回", icArrowLeft, true, false, closePreview)
			if len(imgs) > 1 {
				designToolbarButton(c, p, icChevronLeft, "上一张", btnSz, false, false, func() { a.stepPreviewImage(-1) })
				ui.Text(c, fmt.Sprintf("%d / %d", imgIdx+1, len(imgs))).FontSize(12.5).FontWeight(550).
					FontFeatures("tnum").TextColor(p.secondary).SingleLine().ContentTransition(ctNumeric(a.previewDir < 0))
				designToolbarButton(c, p, icChevronRight, "下一张", btnSz, false, false, func() { a.stepPreviewImage(1) })
			}
			ui.Box(c).Grow(1).MinWidth(0)
			designToolbarButton(c, p, icMinus, "缩小图片", btnSz, false, a.previewZoom <= 0.5, func() {
				a.previewZoom = clamp(a.previewZoom/1.25, 0.5, 4)
			})
			pctBtn := ui.ButtonBase(c.Key("img-zoom-reset")).Height(btnSz).MinWidth(46).PaddingX(6).Radius(7).Center().
				Label("重置图片缩放").Tooltip("重置为适应尺寸").Transition(trColorMicro).OnClick(func() { a.previewZoom = 1 })
			if pctBtn.Pressed() {
				pctBtn.Background(p.pressed)
			} else if pctBtn.Hovered() {
				pctBtn.Background(p.hover)
			}
			applyPressPose(pctBtn, pctBtn.Pressed(), false)
			lastZoom := ui.Local(pctBtn, "last-zoom", func() float32 { return a.previewZoom })
			zoomDown := a.previewZoom < *lastZoom
			*lastZoom = a.previewZoom
			pctBtn.Children(func() {
				ui.Text(c, fmt.Sprintf("%d%%", int(a.previewZoom*100+.5))).FontSize(12).FontWeight(600).
					FontFeatures("tnum").TextColor(p.secondary).SingleLine().ContentTransition(ctNumeric(zoomDown))
			})
			designToolbarButton(c, p, icPlus, "放大图片", btnSz, false, a.previewZoom >= 4, func() {
				a.previewZoom = clamp(a.previewZoom*1.25, 0.5, 4)
			})
		})
		horizontalRule(c, p)
	})

	area := ui.Box(c.Key("img-preview-area")).Absolute().Top(53).Left(0).Right(0).Bottom(0).ZIndex(1).Center()
	jumpZoom := ui.Local(area, "zoom-jump", func() bool { return false })
	lastPreview := ui.Local(area, "last-preview", func() string { return "" })
	if *lastPreview != a.preview {
		*lastPreview = a.preview
		*jumpZoom = true
	}
	if bitmap == nil {
		area.Children(func() {
			ui.Row(c.Key("img-loading")).Gap(8).AlignItems(ui.Center).Transition(trCrossfade).Children(func() {
				if !ready {
					ui.Spinner(c).Size(15, 15).TextColor(p.quiet).Shrink(0)
				}
				message := "正在加载原图…"
				if ready {
					message = "无法加载原图"
				}
				ui.Text(c, message).FontSize(13).TextColor(p.quiet)
			})
		})
		return
	}
	area.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputScroll && ev.DY != 0 {
			factor := float32(1.12)
			if ev.DY > 0 {
				factor = 1 / 1.12
			}
			a.previewZoom = clamp(a.previewZoom*factor, 0.5, 4)
			*jumpZoom = true
			return true
		}
		return false
	})
	var renderZoom float32
	if *jumpZoom {
		*jumpZoom = false
		renderZoom = area.Jump("preview-zoom", a.previewZoom)
	} else {
		renderZoom = area.AnimateTo("preview-zoom", a.previewZoom, animControl)
	}
	w, h := bitmap.Size()
	baseSize := max(1, min(width-56, float32(w)))
	size := max(24, baseSize*renderZoom)
	imgW, imgH := size, size*float32(h)/float32(w)
	paperBg := ui.Hex("#ffffff")
	if p.dark {
		paperBg = ui.Hex("#e8eaef")
	}
	dir := float32(a.previewDir)
	area.Children(func() {
		ui.Scroll(c.Key("img-scroll")).Fill().AlignItems(ui.Center).Children(func() {
			ui.Box(c.Key("img-scroll-center")).FillWidth().MinHeight(max(200, height-53)).
				Center().Padding(24).Children(func() {
				ui.ButtonBase(c.Key("img-card-frame")).MinHeight(min(120, height-96)).Padding(14).Radius(12).
					Background(paperBg).Border(1, p.rule).Shadow(0, 12, 32, -8, p.shadow).
					Label("笔记原图").Transition(trModalCard).OnClick(func() {}).Children(func() {
					ui.Box(c.Key("img-slide-stage")).Size(imgW, imgH).Clip().
						ChildExit(childExitSlide(dir)).Children(func() {
						ui.Box(c.Key("img-slide-"+a.preview)).Absolute().Fill().
							Transition(trPageSlide(dir)).Children(func() {
							ui.Image(c, bitmap).Fill().Fit(ui.Contain).Label("笔记原图")
						})
					})
				})
			})
		})
	})
}

func (a *workspace) editor(c *ui.Context, p palette, availH float32) {
	heading := "编辑笔记"
	var parentTitle string
	if a.editMode == editNewChild {
		heading = "新建子笔记"
		if a.selected != "" {
			if parent, ok := a.tree.notes[a.selected]; ok {
				parentTitle = noteTitle(parent)
			}
		}
	}
	pad := float32(24)
	if a.touchUI() || availH < 420 {
		pad = 16
	}
	bodyH := max(96, availH-2*pad-280)
	ui.Column(c.Key("editor-pane")).FillWidth().Grow(1).MinHeight(0).Transition(trCrossfade).Children(func() {
		ui.Scroll(c.Key("editor")).Grow(1).MinHeight(0).FillWidth().Padding(pad, pad, 12).Gap(10).Transition(trCrossfade).Children(func() {
			ui.Column(c).FillWidth().Gap(3).Children(func() {
				ui.Text(c, heading).FontSize(19).FontWeight(700).LetterSpacing(-0.35).TextColor(p.ink)
				if parentTitle != "" {
					ui.Text(c, "归属笔记：「"+parentTitle+"」").FontSize(12).TextColor(p.quiet).SingleLine().Ellipsis("…")
				}
			})
			if a.discardPrompt {
				guard := ui.Row(c.Key("editor-discard-guard")).FillWidth().Padding(10, 12).Gap(8).Radius(10).
					AlignItems(ui.Center).Background(p.dangerWash).Border(1, p.danger.Alpha(.3)).
					Transition(trPanelSlideV)
				applyErrorShake(guard, a.discardShakeRun)
				guard.Children(func() {
					ui.Icon(c, icAlert).Size(15, 15).TextColor(p.danger).Shrink(0)
					ui.Text(c, "有未保存的修改，确定放弃吗？").FontSize(12.5).FontWeight(550).TextColor(p.danger).Grow(1).MinWidth(0)
					pillButton(c, p, "继续编辑", nil, true, false, func() { a.discardPrompt = false }).Height(28)
					pillButton(c, p, "放弃修改", nil, false, false, func() {
						a.editMode = editNone
						a.discardPrompt = false
						a.flushImports()
					}).Height(28)
				})
			}
			ui.Column(c).FillWidth().Gap(5).Children(func() {
				ui.Text(c, "标题").FontSize(12).FontWeight(600).TextColor(p.secondary)
				ui.TextInput(c, &a.draftTitle).Label("标题").Placeholder("输入笔记标题（可留空）…").
					FillWidth().Height(40).Radius(8).PaddingX(10).FontSize(14).
					Background(p.inputBg).Border(1, p.border).TextColor(p.ink)
			})
			ui.Column(c).FillWidth().Gap(5).Children(func() {
				ui.Text(c, "正文").FontSize(12).FontWeight(600).TextColor(p.secondary)
				ui.TextArea(c, &a.draftBody).Label("正文").Placeholder("输入笔记正文…").
					FillWidth().Height(bodyH).Radius(8).Padding(10).FontSize(14).LineHeight(1.55).
					Background(p.inputBg).Border(1, p.border).TextColor(p.ink)
			})
			ui.Column(c).FillWidth().Gap(6).Children(func() {
				ui.Text(c, "卡片颜色").FontSize(12).FontWeight(600).TextColor(p.secondary)
				swatches := []int{0, 1, 2, 3, 4, 7, 8, 9}
				if a.draftColorSet && !slices.Contains(swatches, a.draftColor) && a.draftColor >= 0 && a.draftColor < 16 {
					swatches = append(swatches, a.draftColor)
				}
				ui.Row(c).FillWidth().Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
					noneBtn := ui.ButtonBase(c.Key("ed-col-none")).Height(26).PaddingX(10).Radius(99).
						Center().Label("无色").Transition(trColorMicro).OnClick(func() { a.draftColorSet, a.draftColor = false, 0 })
					if !a.draftColorSet {
						noneBtn.Background(p.tint).Border(1, p.tintBorder)
					} else {
						noneBtn.Background(p.inputBg).Border(1, p.border)
					}
					applyPressPose(noneBtn, noneBtn.Pressed(), false)
					noneBtn.Children(func() { ui.Text(c, "无色").FontSize(11.5).TextColor(p.secondary) })
					for _, ci := range swatches {
						ci := ci
						ct := mnCardLight[ci]
						if p.dark {
							ct = mnCardDark[ci]
						}
						sel := a.draftColorSet && a.draftColor == ci
						btn := ui.ButtonBase(c.Key(fmt.Sprintf("ed-col-%d", ci))).Size(26, 26).Radius(99).
							Center().Label(fmt.Sprintf("卡片颜色 %d", ci)).Transition(trColorMicro).OnClick(func() {
							a.draftColorSet, a.draftColor = true, ci
						})
						applySelectScalePose(btn, sel, btn.Pressed())
						btn.Children(func() {
							dot := ui.Box(c).Size(18, 18).Radius(99).Background(ct.accent)
							if sel {
								dot.Border(2.5, p.ink)
							}
						})
					}
				})
			})
			ui.Column(c).FillWidth().Gap(5).Children(func() {
				ui.Text(c, "标签（空格或逗号分隔）").FontSize(12).FontWeight(600).TextColor(p.secondary)
				ui.TextInput(c, &a.draftTags).Label("标签").Placeholder("例如：重点 错题…").
					FillWidth().Height(36).Radius(8).PaddingX(10).FontSize(13).
					Background(p.inputBg).Border(1, p.border).TextColor(p.ink)
				if existing := a.bookUsedTags(); len(existing) > 0 {
					ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
						current := parseTagsInput(a.draftTags)
						for i, tag := range existing {
							if i >= 8 {
								break
							}
							tag := tag
							has := slices.Contains(current, tag)
							pill := ui.ButtonBase(c.Key("ed-tag-"+tag)).Height(22).Padding(0, 8).Radius(99).
								Center().Label("切换标签 " + tag).Transition(trColorMicro).OnClick(func() {
								cur := parseTagsInput(a.draftTags)
								if slices.Contains(cur, tag) {
									cur = slices.DeleteFunc(cur, func(v string) bool { return v == tag })
								} else {
									cur = append(cur, tag)
								}
								a.draftTags = strings.Join(cur, " ")
							})
							fg := p.quiet
							if has {
								pill.Background(p.tint).Border(1, p.tintBorder)
								fg = p.accent
							} else {
								pill.Background(p.shell).Border(1, p.rule)
							}
							applyPressPose(pill, pill.Pressed(), false)
							pill.Children(func() { ui.Text(c, "#"+tag).FontSize(11).TextColor(fg).SingleLine() })
						}
					})
				}
			})
			if a.editMode == editNote {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "归属分组").FontSize(12).FontWeight(600).TextColor(p.secondary)
					parentLabel := "顶层目录"
					if a.draftParent != "" && a.draftParent != a.tree.root {
						if pn, ok := a.tree.notes[a.draftParent]; ok {
							parentLabel = noteTitle(pn)
						}
					}
					moveBtn := ui.ButtonBase(c.Key("ed-move-parent")).Height(30).Padding(0, 10).Radius(8).
						Background(p.inputBg).Border(1, p.border).Center().
						Label("移动归属：" + parentLabel).Transition(trColorMicro)
					applyPressPose(moveBtn, moveBtn.Pressed(), false)
					moveBtn.Children(func() {
						ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
							ui.Text(c, parentLabel).FontSize(12.5).TextColor(p.ink).SingleLine().Ellipsis("…").MaxWidth(220)
							ui.Icon(c, icChevronDown).Size(12, 12).TextColor(p.quiet)
						})
					})
					moveBtn.PopoverMenuButton(func(m *ui.Menu) {
						if m.Item("顶层目录").ID("mv-root").Radio(a.draftParent == "" || a.draftParent == a.tree.root).Chosen() {
							a.draftParent = ""
						}
						for _, cid := range a.validParentCandidates(a.selected) {
							if m.Item(noteTitle(a.tree.notes[cid])).ID("mv-"+cid).Radio(a.draftParent == cid).Chosen() {
								a.draftParent = cid
							}
						}
					})
				})
			}
		})
		horizontalRule(c, p)
		ui.Row(c).FillWidth().Shrink(0).Padding(10, pad).Gap(8).AlignItems(ui.Center).Background(p.surface).Children(func() {
			btnH := float32(34)
			if a.touchUI() {
				btnH = 38
			}
			primaryButton(c, p, "保存", nil, a.busy || !a.canSaveEditor(), a.saveEditor).Height(btnH)
			pillButton(c, p, "取消", nil, true, a.busy, a.requestCancelEdit).Height(btnH)
		})
	})
}
