package main

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo/ui"
)

func (a *workspace) saveSettings(next notebook.Settings) bool {
	if a.store != nil {
		if err := a.store.SaveSettings(next); err != nil {
			a.showError(err)
			return false
		}
	}
	appearanceChanged := a.settings.Appearance != next.Appearance
	a.settings = next
	if appearanceChanged {
		syncAppTheme(next.Appearance)
		if win := a.window(); win != nil {
			_ = win.SetBackgroundColor(windowBackgroundForAppearance(next.Appearance))
		}
	}
	return true
}

func (a *workspace) toggleSidebar() {
	if a.sidebarHiddenForSplit {
		a.sidebarHiddenForSplit = false
		a.recenter = true
		return
	}
	next := a.settings
	next.SidebarCollapsed = !next.SidebarCollapsed
	if a.saveSettings(next) {
		a.recenter = true
	}
}

func (a *workspace) openPreferences() {
	a.preferencesOpen = true
	a.settingsScroll = ui.ScrollState{}
	a.loadWebDAVDraft()
	a.refreshStorageBytes()
}

func (a *workspace) openPreferencesAtUpdate() {
	a.preferencesOpen = true
	a.settingsScroll = ui.ScrollState{Y: 1e6}
	a.loadWebDAVDraft()
	a.refreshStorageBytes()
}

func (a *workspace) refreshStorageBytes() {
	if win := a.window(); win != nil && a.store != nil {
		go func() {
			size := a.store.StorageBytes()
			win.Update(func() { a.storageBytes = size })
		}()
	}
}

func designToolbarButton(c *ui.Context, p palette, icon *ui.SVG, label string, size float32, framed, disabled bool, action func()) {
	b := ui.ButtonBase(c).Size(size, size).Shrink(0).Center().Radius(8).Label(label).Tooltip(label).
		Disabled(disabled).FocusRingColor(p.accent).Transition(trColorMicro).OnClick(action)
	applyPressPose(b, b.Pressed(), disabled)
	fg := p.quiet
	if framed {
		b.Background(p.elevated).Border(1, p.border).Shadow(0, 1, 2, 0, p.shadowSoft)
		fg = p.secondary
	}
	if disabled {
		fg = p.quiet.Alpha(.4)
	} else if b.Pressed() {
		b.Background(p.pressed)
		fg = p.ink
	} else if b.Hovered() {
		b.Background(p.hover)
		fg = p.ink
	}
	b.Children(func() { ui.Icon(c, icon).Size(15, 15).TextColor(fg) })
}

func (a *workspace) transferButton(c *ui.Context, p palette) {
	sz := float32(28)
	if a.touchUI() {
		sz = 34
	}
	disabled := a.editing() || a.busy
	b := ui.ButtonBase(c).Label("导入 / 导出").Tooltip("导入或导出").Size(sz, sz).Center().Radius(7).Shrink(0).
		Disabled(disabled).FocusRingColor(p.accent).Transition(trColorMicro)
	applyPressPose(b, b.Pressed(), disabled)
	fg := p.quiet
	if disabled {
		fg = p.quiet.Alpha(.4)
	} else if b.Pressed() {
		b.Background(p.pressed)
		fg = p.ink
	} else if b.Hovered() {
		b.Background(p.hover)
		fg = p.ink
	}
	b.Children(func() { ui.Icon(c, icTransfer).Size(15, 15).TextColor(fg) })
	canExport := a.bookID != "" && len(a.tree.book.Notes) > 0
	b.PopoverMenuButton(func(m *ui.Menu) {
		if m.Item("导入笔记或 PDF…").Chosen() {
			a.chooseImport()
		}
		if m.Item("导出 Markdown（.zip）…").Disabled(!canExport).Chosen() {
			a.exportMarkdown()
		}
	})
}

type segment[T comparable] struct {
	title string
	value T
}

func segments[T comparable](c *ui.Context, p palette, options []segment[T], selected T, change func(T)) {
	segmentsSized(c, p, false, options, selected, change)
}

func segmentsSized[T comparable](c *ui.Context, p palette, touch bool, options []segment[T], selected T, change func(T)) {
	outerH, btnH, padX, outerR, btnR, fontSize := float32(31), float32(24), float32(10), float32(8), float32(5), float32(12)
	if touch {
		outerH, btnH, padX, outerR, btnR, fontSize = 36, 28, 12, 9, 6, 12.5
	}
	var kb strings.Builder
	for i, opt := range options {
		if i > 0 {
			kb.WriteByte('|')
		}
		kb.WriteString(opt.title)
	}
	groupKey := kb.String()
	pillTr := trSegmentPill(groupKey)
	ui.Row(c).Height(outerH).Shrink(0).Padding(3).Gap(2).Radius(outerR).
		AlignItems(ui.Center).Background(p.inputBg).Border(1, p.rule).Children(func() {
		for _, opt := range options {
			active := opt.value == selected
			b := ui.ButtonBase(c.Key(opt.title)).Height(btnH).PaddingX(padX).Radius(btnR).Center().
				Shrink(0).Label(opt.title).Selected(active).Transition(trColorMicro).OnClick(func() { change(opt.value) })
			applyPressPose(b, b.Pressed(), false)
			fg := p.quiet
			if active {
				fg = p.ink
			} else if b.Pressed() {
				b.Background(p.pressed)
				fg = p.ink
			} else if b.Hovered() {
				b.Background(p.hover)
			}
			b.Children(func() {
				if active {
					ui.Box(c.Key("seg-pill-"+groupKey)).Absolute().Fill().Radius(btnR).
						Background(p.elevated).Border(1, p.border).Shadow(0, 1, 2, 0, p.shadowSoft).
						IgnoreInput(true).Transition(pillTr)
				}
				txt := ui.Text(c, opt.title).FontSize(fontSize).TextColor(fg).SingleLine()
				if touch {
					txt.FontWeight(550)
				}
			})
		}
	})
}

func settingSwitch(c *ui.Context, p palette, label string, active bool, change func()) {
	btn := ui.ButtonBase(c).Size(44, 34).Center().Shrink(0).Label(label).Role(ui.RoleSwitch).Checked(active).
		Background(ui.Transparent).FocusRingColor(p.accent).OnClick(change)
	posTarget := float32(0)
	if active {
		posTarget = 1
	}
	pos := btn.AnimateTo("sw-pos", posTarget, animControl)
	offTrack := p.border.Mix(p.ink, 0.16)
	if p.dark {
		offTrack = p.border.Mix(p.ink, 0.12)
	}
	targetTrack := offTrack
	if active {
		targetTrack = p.accent
	}
	trackCol := btn.AnimateColor("sw-track", targetTrack, animMicro)
	stretchTarget := float32(0)
	if btn.Pressed() {
		stretchTarget = 2
	}
	stretch := btn.AnimateTo("sw-stretch", stretchTarget, animPress)
	btn.Draw(func(paint *ui.Painter, rect ui.Rect) {
		const (
			trackW = float32(39)
			trackH = float32(24)
			inset  = float32(2)
			knobD  = trackH - 2*inset
		)
		tx := rect.X + (rect.W-trackW)/2
		ty := rect.Y + (rect.H-trackH)/2
		paint.Fill(ui.Rect{X: tx, Y: ty, W: trackW, H: trackH}, trackCol, trackH/2)
		knobW := knobD + max(0, stretch)
		travel := trackW - 2*inset - knobW
		kx := tx + inset + pos*travel
		knobRect := ui.Rect{X: kx, Y: ty + inset, W: knobW, H: knobD}
		paint.Shadow(knobRect, knobD/2, 0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.22))
		paint.Fill(knobRect, ui.RGB(255, 255, 255), knobD/2)
	})
}

func settingGroup(c *ui.Context, p palette, title string, content func()) {
	ui.Column(c).FillWidth().Shrink(0).Gap(7).Children(func() {
		ui.Text(c, title).PaddingX(4).FontSize(11).FontWeight(600).TextColor(p.quiet)
		ui.Column(c).FillWidth().Radius(12).Clip().Background(p.canvas).Border(1, p.rule).Children(content)
	})
}

func settingRowWithHint(c *ui.Context, p palette, compact bool, title string, hintBlock func(), control func()) {
	if compact {
		ui.Column(c).FillWidth().Shrink(0).Padding(12, 16).Gap(8).Children(func() {
			ui.Column(c).FillWidth().Gap(2).Children(func() {
				ui.Text(c, title).FontSize(13).FontWeight(550).TextColor(p.ink).SingleLine()
				hintBlock()
			})
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(control)
		})
		return
	}
	ui.Row(c).MinHeight(57).FillWidth().Shrink(0).Padding(8, 16).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, title).FontSize(13).FontWeight(550).TextColor(p.ink).SingleLine().Ellipsis("…")
			hintBlock()
		})
		control()
	})
}

func settingRow(c *ui.Context, p palette, compact bool, title, hint string, control func()) {
	settingRowWithHint(c, p, compact, title, func() {
		el := ui.Text(c, hint).FontSize(11.5).TextColor(p.quiet)
		if !compact {
			el.LineHeight(1.4)
		}
	}, control)
}

func settingButtonHeight(touch bool) float32 {
	if touch {
		return 36
	}
	return 30
}

func settingButtonWithHeight(c *ui.Context, p palette, h float32, title string, icon *ui.SVG, danger, disabled bool, action func()) ui.Element {
	fg, bg, border := p.ink, p.elevated, p.border
	if danger {
		fg, bg, border = p.danger, p.dangerWash, p.danger.Alpha(.35)
	}
	padX := float32(10)
	if h >= 36 {
		padX = 12
	}
	button := ui.ButtonBase(c).Height(h).PaddingX(padX).Gap(6).Center().Radius(8).Shrink(0).
		Background(bg).Border(1, border).Shadow(0, 1, 2, 0, p.shadowSoft).
		Label(title).Disabled(disabled).FocusRingColor(p.accent).Transition(trColorMicro).OnClick(action)
	applyPressPose(button, button.Pressed(), disabled)
	if disabled {
		fg = p.quiet
	} else if button.Pressed() {
		if danger {
			button.Background(p.danger.Alpha(.18))
		} else {
			button.Background(p.pressed)
		}
	} else if button.Hovered() && !danger {
		button.Background(p.hover)
	}
	button.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(13, 13).TextColor(fg)
		}
		ui.Text(c, title).FontSize(12).TextColor(fg).SingleLine()
	})
	return button
}

func (a *workspace) settingButton(c *ui.Context, p palette, title string, icon *ui.SVG, danger, disabled bool, action func()) ui.Element {
	return settingButtonWithHeight(c, p, settingButtonHeight(a.touchUI()), title, icon, danger, disabled, action)
}

func (a *workspace) settingPrimaryButton(c *ui.Context, p palette, title string, icon *ui.SVG, disabled bool, action func()) ui.Element {
	h := settingButtonHeight(a.touchUI())
	padX := float32(11)
	if h >= 36 {
		padX = 13
	}
	return accentButton(c, p, h, padX, 12, 13, title, icon, disabled, action)
}

func (a *workspace) settingsCard(c *ui.Context, p palette, width, height float32) {
	cardW, cardH := modalDimensions(width, height, 24, 0, 640, 24, 0, 746)
	if cardW < 1 || cardH < 1 {
		return
	}
	closeCard := func() {
		a.renameOpen, a.preferencesOpen = false, false
	}
	ui.ButtonBase(c.Key("settings-scrim")).Absolute().Left(0).Right(0).Top(0).Bottom(0).ZIndex(20).
		Background(p.scrim).Label("关闭设置").OnClick(closeCard).Transition(trScrim)
	ui.Column(c.Key("settings-card")).Absolute().Left(0).Right(0).Top(0).Bottom(0).Margin(ui.Auto).
		Width(cardW).Height(cardH).ZIndex(21).Radius(16).Clip().
		Background(p.surface).Border(1, p.border).Shadow(0, 24, 60, -14, p.modalShadow).Transition(trModalCard).Children(func() {
		ui.Row(c).Height(52).Shrink(0).FillWidth().Padding(0, 14, 0, 20).
			Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icSettings).Size(15, 15).TextColor(p.secondary)
			ui.Text(c, "设置").Grow(1).FontSize(15).FontWeight(700).TextColor(p.ink)
			designToolbarButton(c, p, icClose, "关闭设置", 30, false, false, closeCard)
		})
		horizontalRule(c, p)
		ui.Scroll(c.Key("settings-body")).Grow(1).MinHeight(0).FillWidth().TrackScroll(&a.settingsScroll).
			Padding(18, 24).Gap(16).Children(func() {
			compact := cardW < 540
			a.displaySettings(c, p, compact)
			if a.touchUI() {
				a.stylusSettings(c, p, compact)
			}
			a.storageSettings(c, p, compact)
			a.webdavSettings(c, p, compact)
			if a.bookID != "" {
				a.bookSettings(c, p, compact)
			}
			a.aboutSettings(c, p, compact)
		})
		horizontalRule(c, p)
		ui.Row(c).Height(40).Shrink(0).FillWidth().PaddingX(24).AlignItems(ui.Center).Background(p.canvas).Children(func() {
			ui.Text(c, fmt.Sprintf("Folio v%s · 所有笔记仅保存在本地", currentAppVersion())).FontSize(11.5).TextColor(p.quiet).Grow(1)
			closeHint := "Esc 关闭"
			if a.touchUI() {
				if runtime.GOOS == "android" {
					closeHint = "返回键关闭"
				} else {
					closeHint = "点击空白处关闭"
				}
			}
			ui.Text(c, closeHint).FontSize(11.5).TextColor(p.quiet)
		})
	})
}

func (a *workspace) displaySettings(c *ui.Context, p palette, compact bool) {
	touch := a.touchUI()
	layoutMode := a.settings.LayoutMode()
	settingGroup(c, p, "外观与显示", func() {
		settingRow(c, p, compact, "外观主题", "切换浅色、深色或跟随系统", func() {
			segmentsSized(c, p, touch, []segment[string]{{"浅色", "light"}, {"深色", "dark"}, {"跟随系统", ""}}, a.settings.Appearance, func(value string) {
				next := a.settings
				next.Appearance = value
				a.saveSettings(next)
			})
		})
		horizontalRule(c, p)
		settingRow(c, p, compact, "视图样式", "选择笔记的浏览排布方式", func() {
			segmentsSized(c, p, touch, []segment[string]{
				{"卡片树", notebook.LayoutCards},
				{"专题框架", notebook.LayoutBoard},
				{"大纲流", notebook.LayoutOutline},
				{"精简树", notebook.LayoutCompact},
			}, layoutMode, func(value string) {
				next := a.settings
				next.GraphLayout = value
				if a.saveSettings(next) {
					a.canvasReady = false
					a.focusSelected = true
					a.cardMenuOpen = false
					a.outlineScroll = ui.ScrollState{}
				}
			})
		})
		if layoutMode != notebook.LayoutOutline {
			ui.Column(c.Key("canvas-display-opts")).FillWidth().Transition(trPanelSlideV).Children(func() {
				horizontalRule(c, p)
				settingRow(c, p, compact, "画布默认缩放", "打开笔记本时的初始视图比例", func() {
					segmentsSized(c, p, touch, []segment[int]{{"自适应", 0}, {"90%", 90}, {"100%", 100}, {"110%", 110}}, a.settings.GraphZoom, func(value int) {
						next := a.settings
						next.GraphZoom = value
						if a.saveSettings(next) {
							a.zoom(float32(value) / 100)
						}
					})
				})
				horizontalRule(c, p)
				settingRow(c, p, compact, "画布点阵背景", "在画布上显示参考点阵", func() {
					settingSwitch(c, p, "画布点阵背景", !a.settings.HideGrid, func() {
						next := a.settings
						next.HideGrid = !next.HideGrid
						a.saveSettings(next)
					})
				})
			})
		}
		horizontalRule(c, p)
		settingRow(c, p, compact, "PDF 夜间智能反色", "深色模式下自动反色 PDF 底纸，保留彩色批注", func() {
			settingSwitch(c, p, "PDF 夜间智能反色", a.settings.PDFNight, func() {
				next := a.settings
				next.PDFNight = !next.PDFNight
				a.saveSettings(next)
			})
		})
	})
}

func (a *workspace) stylusSettings(c *ui.Context, p palette, compact bool) {
	settingGroup(c, p, "触控笔与平板", func() {
		settingRow(c, p, compact, "触控笔提笔即写", "开启时笔尖落在画布或 PDF 直接书写；关闭时笔尖优先点击卡片与按钮", func() {
			settingSwitch(c, p, "切换触控笔提笔即写", !a.settings.PenTapToSelect, func() {
				next := a.settings
				next.PenTapToSelect = !next.PenTapToSelect
				if a.saveSettings(next) {
					a.drawing.penBrowseMode = false
				}
			})
		})
	})
}

func (a *workspace) storageSettings(c *ui.Context, p palette, compact bool) {
	count := 0
	for _, book := range a.books {
		count += len(book.Notes)
	}
	usage := fmt.Sprintf("%d 本笔记本 · %d 条笔记", len(a.books), count)
	if a.storageBytes > 0 {
		usage += " · 已占用 " + formatByteSize(a.storageBytes)
	}
	canExport := a.bookID != "" && len(a.tree.book.Notes) > 0
	settingGroup(c, p, "存储与数据", func() {
		settingRow(c, p, compact, "导入与导出", "导入 MarginNote 或 PDF；导出当前笔记本为带图 Markdown", func() {
			ui.Row(c).Gap(8).Shrink(0).Children(func() {
				a.settingButton(c, p, "导入笔记或 PDF", icSettingImport, false, a.busy, a.chooseImport)
				if canExport {
					a.settingButton(c, p, "导出 Markdown", icSettingExport, false, a.busy, a.exportMarkdown)
				}
			})
		})
		horizontalRule(c, p)
		settingRow(c, p, compact, "整库备份与恢复", "打包全部笔记本、手写批注、PDF 与设置；恢复将覆盖本地数据", func() {
			ui.Row(c).Gap(8).Shrink(0).Children(func() {
				a.settingButton(c, p, "导出整库备份", icSettingExport, false, a.busy, a.exportBackup)
				a.settingButton(c, p, "从备份恢复", icSettingImport, true, a.busy, a.restoreBackup)
			})
		})
		horizontalRule(c, p)
		settingRow(c, p, compact, "本地数据与缓存", usage+"（清理仅释放内存与临时文件）", func() {
			a.settingButton(c, p, "清理缓存", nil, false, false, a.clearRenderCache)
		})
	})
}

func (a *workspace) webdavSettings(c *ui.Context, p palette, compact bool) {
	statusText := "将笔记本、手写批注与 PDF 同步至兼容 WebDAV 的网盘（如坚果云、Nextcloud）"
	if a.webdavStatus != "" {
		statusText = a.webdavStatus
	} else if a.settings.WebDAVLastSyncUnix > 0 {
		statusText = "上次同步：" + formatSyncTime(a.settings.WebDAVLastSyncUnix)
	}
	settingGroup(c, p, "云端同步（WebDAV）", func() {
		settingRow(c, p, compact, "服务器地址", "例如 https://dav.jianguoyun.com/dav/", func() {
			ui.TextInput(c.Key("webdav-url"), &a.webdavURL).
				Width(240).Height(settingButtonHeight(a.touchUI())).PaddingX(10).Radius(8).
				FontSize(12).Background(p.inputBg).Border(1, p.border).TextColor(p.ink).
				Placeholder("https://dav.example.com/dav/").Label("WebDAV 服务器地址")
		})
		horizontalRule(c, p)
		settingRow(c, p, compact, "账户名", "用于基础身份认证的用户名或邮箱", func() {
			ui.TextInput(c.Key("webdav-user"), &a.webdavUser).
				Width(240).Height(settingButtonHeight(a.touchUI())).PaddingX(10).Radius(8).
				FontSize(12).Background(p.inputBg).Border(1, p.border).TextColor(p.ink).
				Placeholder("用户名").Label("WebDAV 用户名")
		})
		horizontalRule(c, p)
		settingRow(c, p, compact, "应用密码", "密码加密保存在系统凭据库，不会明文写入设置文件", func() {
			ui.TextInput(c.Key("webdav-password"), &a.webdavPassword).Password().
				Width(240).Height(settingButtonHeight(a.touchUI())).PaddingX(10).Radius(8).
				FontSize(12).Background(p.inputBg).Border(1, p.border).TextColor(p.ink).
				Placeholder("应用授权密码").Label("WebDAV 密码")
		})
		horizontalRule(c, p)
		settingRowWithHint(c, p, compact, "连接与同步", func() {
			statusEl := func() {
				el := ui.Text(c, statusText).FontSize(11.5).TextColor(p.quiet).ContentTransition(ctText)
				if !compact {
					el.LineHeight(1.4)
				}
				if a.webdavBusy {
					el.Grow(1).MinWidth(0)
				}
			}
			if a.webdavBusy {
				ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(13, 13).TextColor(p.accent)
					statusEl()
				})
				return
			}
			statusEl()
		}, func() {
			ui.Row(c).Gap(8).Shrink(0).Children(func() {
				a.settingButton(c, p, "测试连接", nil, false, a.webdavBusy || a.busy, a.testWebDAVConnection)
				a.settingPrimaryButton(c, p, "立即同步", icUpdateRefresh, a.webdavBusy || a.busy, a.syncWebDAV)
			})
		})
	})
}

func formatSyncTime(unixSec int64) string {
	if unixSec <= 0 {
		return "尚未同步"
	}
	return time.Unix(unixSec, 0).Format("2006-01-02 15:04")
}

func (a *workspace) bookSettings(c *ui.Context, p palette, compact bool) {
	settingGroup(c, p, "笔记本管理", func() {
		title := a.tree.book.Title
		if title == "" {
			title = "未选择笔记本"
		}
		settingRow(c, p, compact, "重命名当前笔记本", title, func() {
			if a.renameOpen {
				ui.Row(c.Key("book-rename-edit")).AlignItems(ui.Center).Gap(6).Width(245).Shrink(0).Transition(trCrossfade).Children(func() {
					if ui.TextInput(c.Key("book-name"), &a.bookName).Grow(1).MinWidth(0).Height(settingButtonHeight(a.touchUI())).Label("笔记本名称").Submitted() {
						a.renameBook()
					}
					a.settingPrimaryButton(c, p, "保存", nil, a.busy || strings.TrimSpace(a.bookName) == "", a.renameBook)
					a.settingButton(c, p, "取消", nil, false, a.busy, func() { a.renameOpen = false })
				})
				return
			}
			ui.Box(c.Key("book-rename-btn")).Shrink(0).Transition(trCrossfade).Children(func() {
				a.settingButton(c, p, "重命名", nil, false, a.busy || a.bookID == "", func() {
					a.renameOpen, a.bookName = true, a.tree.book.Title
				})
			})
		})
		horizontalRule(c, p)
		settingRow(c, p, compact, "删除当前笔记本", title, func() {
			a.settingButton(c, p, "删除笔记本", icSettingDelete, true, a.busy || a.bookID == "", a.confirmDeleteBook)
		})
	})
}

func (a *workspace) renameBook() {
	bookID, title := a.bookID, a.bookName
	a.work(func() func() {
		library, err := a.store.RenameBook(bookID, title)
		return func() {
			if err != nil {
				a.showError(err)
				return
			}
			a.setLibrary(library, bookID)
			a.renameOpen = false
			a.showMessage("已重命名笔记本", false)
		}
	})
}

func (a *workspace) aboutSettings(c *ui.Context, p palette, compact bool) {
	st := a.updateSnapshot()
	curVer := currentAppVersion()
	services := c.Services()

	hint, hintColor := updateStatusPresentation(p, st, curVer)
	releaseURL := folioReleasesURL
	if st.info != nil && strings.TrimSpace(st.info.ReleaseURL) != "" {
		releaseURL = strings.TrimSpace(st.info.ReleaseURL)
	}

	controlsRow := func(children func()) {
		row := ui.Row(c).Gap(8).AlignItems(ui.Center)
		if compact {
			row.FillWidth().Wrap()
		} else {
			row.Shrink(0)
		}
		row.Children(children)
	}

	controls := func() {
		switch st.phase {
		case updateChecking:
			a.settingButton(c, p, "检查更新", icUpdateRefresh, false, true, nil)
		case updateDownloading:
			controlsRow(func() {
				a.settingButton(c, p, "下载中…", icImport, false, true, nil)
				a.settingButton(c, p, "取消", nil, false, false, a.cancelUpdateDownload)
			})
		case updateAvailable:
			controlsRow(func() {
				if st.info != nil && st.info.canDirectInstall() {
					label := "下载更新"
					if st.info.PendingPath != "" {
						label = "立即安装"
					}
					a.settingPrimaryButton(c, p, label, icImport, false, func() {
						a.installUpdate(services)
					})
					a.settingButton(c, p, "前往发布页", icExternalLink, false, false, func() {
						a.openExternalURL(c, releaseURL)
					})
				} else {
					a.settingPrimaryButton(c, p, "前往发布页", icExternalLink, false, func() {
						a.openExternalURL(c, releaseURL)
					})
					a.settingButton(c, p, "重新检查", icUpdateRefresh, false, false, func() {
						a.checkForUpdates(services)
					})
				}
			})
		case updateRelaunchRequired:
			a.settingPrimaryButton(c, p, "立即重启", icUpdateRefresh, false, a.relaunchApp)
		case updatePermissionRequired:
			controlsRow(func() {
				a.settingPrimaryButton(c, p, "立即安装", icImport, false, func() {
					a.installUpdate(services)
				})
				a.settingButton(c, p, "前往发布页", icExternalLink, false, false, func() {
					a.openExternalURL(c, releaseURL)
				})
			})
		case updateDeferredForeground:
			controlsRow(func() {
				if st.info != nil && st.info.canDirectInstall() {
					a.settingPrimaryButton(c, p, "立即安装", icImport, false, func() {
						a.installUpdate(services)
					})
				}
				a.settingButton(c, p, "重新检查", icUpdateRefresh, false, false, func() {
					a.checkForUpdates(services)
				})
			})
		case updateInstallerLaunched:
			controlsRow(func() {
				if st.info != nil && st.info.canDirectInstall() {
					a.settingButton(c, p, "立即安装", icImport, false, false, func() {
						a.installUpdate(services)
					})
				}
				a.settingButton(c, p, "重新检查", icUpdateRefresh, false, false, func() {
					a.checkForUpdates(services)
				})
			})
		case updateError:
			if st.info != nil {
				controlsRow(func() {
					if st.info.canDirectInstall() {
						retryLabel := "重试下载"
						if st.info.PendingPath != "" {
							retryLabel = "重试安装"
						}
						a.settingPrimaryButton(c, p, retryLabel, icImport, false, func() {
							a.installUpdate(services)
						})
					} else {
						a.settingPrimaryButton(c, p, "重新检查", icUpdateRefresh, false, func() {
							a.checkForUpdates(services)
						})
					}
					a.settingButton(c, p, "前往发布页", icExternalLink, false, false, func() {
						a.openExternalURL(c, releaseURL)
					})
				})
			} else {
				a.settingButton(c, p, "检查更新", icUpdateRefresh, false, false, func() {
					a.checkForUpdates(services)
				})
			}
		default:
			a.settingButton(c, p, "检查更新", icUpdateRefresh, false, false, func() {
				a.checkForUpdates(services)
			})
		}
	}

	settingGroup(c, p, "关于 Folio", func() {
		ui.Column(c).FillWidth().Shrink(0).Padding(12, 16).Gap(10).Children(func() {
			titleBlock := func(col ui.Element) {
				col.Gap(3).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "版本与更新").FontSize(13).FontWeight(550).TextColor(p.ink).SingleLine()
						badgePill(c, p, "v"+curVer)
						if st.phase == updateChecking || st.phase == updateDownloading {
							ui.Spinner(c).Size(13, 13).TextColor(p.accent)
						}
					})
					hintEl := ui.Text(c, hint).FontSize(11.5).LineHeight(1.45).TextColor(hintColor)
					if st.phase == updateDownloading {
						hintEl.ContentTransition(ctNumeric(false))
					} else {
						hintEl.ContentTransition(ctText)
					}
				})
			}
			if compact {
				titleBlock(ui.Column(c).FillWidth())
				ui.Row(c).FillWidth().AlignItems(ui.Center).Children(controls)
			} else {
				ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
					titleBlock(ui.Column(c).Grow(1).MinWidth(0))
					controls()
				})
			}
			if st.phase == updateDownloading && st.total > 0 {
				ratio := clamp(float32(st.downloaded)/float32(st.total), 0, 1)
				bar := ui.Box(c.Key("about-dl-bar")).FillWidth().Height(4).Radius(99).Clip().
					Background(p.inputBg).Transition(trPanelSlideV)
				pct := bar.AnimateTo("update-dl-pct", ratio*100, animControl)
				bar.Children(func() {
					ui.Box(c).FillHeight().WidthPercent(clamp(pct, 0, 100)).Radius(99).Background(p.accent)
				})
			}
			if st.info != nil && (st.phase == updateAvailable || st.phase == updateRelaunchRequired || st.phase == updatePermissionRequired || st.phase == updateDeferredForeground) {
				if notes := formatReleaseNotesPreview(st.info.Notes); notes != "" {
					ui.Column(c.Key("about-release-notes")).FillWidth().Padding(8, 10).Radius(8).
						Background(p.inputBg).Border(1, p.rule).Transition(trPanelSlideV).Children(func() {
						ui.Text(c, notes).FillWidth().FontSize(11.5).LineHeight(1.45).TextColor(p.secondary)
					})
				}
			}
		})
		horizontalRule(c, p)
		settingRow(c, p, compact, "开源仓库", "github.com/dongyu-will/folio", func() {
			a.settingButton(c, p, "打开 GitHub", icExternalLink, false, false, func() {
				a.openExternalURL(c, folioRepoURL)
			})
		})
	})
}

func (a *workspace) openExternalURL(c *ui.Context, rawURL string) {
	target := strings.TrimSpace(rawURL)
	if target == "" || !ui.SafeToOpen(target) {
		a.showMessage("无效的外部链接地址", true)
		return
	}
	c.OpenURLThen(target, func(err error) {
		if err != nil {
			a.showMessage("无法打开外部链接，请检查默认浏览器设置", true)
		}
	})
}

func updateStatusPresentation(p palette, st updateUIState, curVer string) (string, ui.Color) {
	switch st.phase {
	case updateChecking:
		return "正在检查更新…", p.quiet
	case updateUpToDate:
		return "已是最新版本", p.quiet
	case updateNoRelease:
		return "暂未发现可用更新", p.quiet
	case updateAvailable:
		if st.info != nil && st.info.PendingPath != "" {
			if st.info.Version != "" {
				return fmt.Sprintf("新版本 v%s 已下载完成，点击即可安装", st.info.Version), p.accent
			}
			return "更新安装包已下载完成，点击即可安装", p.accent
		}
		if st.info != nil && st.info.Version != "" {
			if st.info.APKSize > 0 {
				return fmt.Sprintf("发现新版本 v%s（%s）", st.info.Version, formatByteSize(st.info.APKSize)), p.accent
			}
			return fmt.Sprintf("发现新版本 v%s", st.info.Version), p.accent
		}
		return "发现新版本", p.accent
	case updateDownloading:
		return formatUpdateProgress(st.downloaded, st.total), p.accent
	case updateRelaunchRequired:
		return "更新已就绪，重启后生效", p.accent
	case updatePermissionRequired:
		return "请在系统设置中允许 Folio 安装未知来源应用，授权后再次点击「立即安装」", p.secondary
	case updateDeferredForeground:
		return "下载已完成，返回应用将自动打开安装器", p.accent
	case updateInstallerLaunched:
		return "已打开系统安装器，请按提示完成更新", p.accent
	case updateError:
		if st.errText != "" {
			return st.errText, p.danger
		}
		return "检查更新失败，请稍后重试", p.danger
	default:
		return "已开启自动检查更新", p.quiet
	}
}

func formatUpdateProgress(downloaded, total int64) string {
	if total > 0 {
		pct := int(min(100, max(0, downloaded*100/total)))
		return fmt.Sprintf("正在下载更新… %d%%", pct)
	}
	if downloaded <= 0 {
		return "正在下载更新…"
	}
	switch {
	case downloaded >= 1<<20:
		return fmt.Sprintf("正在下载更新… %.1f MB", float64(downloaded)/(1<<20))
	case downloaded >= 1<<10:
		return fmt.Sprintf("正在下载更新… %.0f KB", float64(downloaded)/(1<<10))
	default:
		return fmt.Sprintf("正在下载更新… %d B", downloaded)
	}
}

func formatReleaseNotesPreview(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	lines := strings.Split(raw, "\n")
	const (
		maxLines = 4
		maxRunes = 220
	)
	var out []string
	truncated := false
	for _, ln := range lines {
		s := cleanReleaseNoteLine(ln)
		if s == "" {
			continue
		}
		if len(out) >= maxLines {
			truncated = true
			break
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return ""
	}
	joined := strings.Join(out, "\n")
	runes := []rune(joined)
	if len(runes) > maxRunes {
		return strings.TrimRight(string(runes[:maxRunes]), " \t\n") + "…"
	}
	if truncated && !strings.HasSuffix(joined, "…") {
		joined += "…"
	}
	return joined
}

func cleanReleaseNoteLine(ln string) string {
	s := strings.TrimSpace(ln)
	if s == "" || strings.HasPrefix(s, "```") {
		return ""
	}
	if strings.HasPrefix(s, "|") && strings.HasSuffix(s, "|") {
		return ""
	}
	if strings.Contains(s, "|") && strings.Trim(s, "|-: ") == "" {
		return ""
	}
	if len(s) >= 3 && (strings.Trim(s, "- ") == "" || strings.Trim(s, "* ") == "" || strings.Trim(s, "_ ") == "") {
		return ""
	}
	for strings.HasPrefix(s, "#") {
		s = strings.TrimLeft(s, "#")
		s = strings.TrimSpace(s)
	}
	for strings.HasPrefix(s, ">") {
		s = strings.TrimPrefix(s, ">")
		s = strings.TrimSpace(s)
	}
	bullet := false
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(s, prefix) {
			bullet = true
			s = strings.TrimSpace(strings.TrimPrefix(s, prefix))
			break
		}
	}
	s = stripMarkdownLinks(s)
	s = strings.NewReplacer("**", "", "__", "", "`", "").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	if bullet {
		return "• " + s
	}
	return s
}

func stripMarkdownLinks(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '[' {
			closeBracket := strings.IndexByte(s[i+1:], ']')
			if closeBracket >= 0 {
				mid := i + 1 + closeBracket
				if mid+1 < len(s) && s[mid+1] == '(' {
					closeParen := strings.IndexByte(s[mid+2:], ')')
					if closeParen >= 0 {
						b.WriteString(s[i+1 : mid])
						i = mid + 2 + closeParen + 1
						continue
					}
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
