package main

import (
	"fmt"
	"strings"

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
	b := ui.ButtonBase(c).Size(size, size).Shrink(0).Center().Radius(8).Label(label).
		Disabled(disabled).FocusRingColor(p.accent).OnClick(action)
	fg := p.quiet
	if framed {
		b.Background(p.elevated).Border(1, p.border).Shadow(0, 1, 2, 0, p.shadowSoft)
		fg = p.secondary
	}
	if b.Hovered() && !disabled {
		b.Background(p.hover)
	}
	b.Children(func() { ui.Icon(c, icon).Size(15, 15).TextColor(fg) })
}

func (a *workspace) transferButton(c *ui.Context, p palette) {
	sz := float32(28)
	if a.touchUI() {
		sz = 34
	}
	b := ui.ButtonBase(c).Label("导入 / 导出").Size(sz, sz).Center().Radius(7).Shrink(0).
		Disabled(a.editing() || a.busy).FocusRingColor(p.accent)
	if b.Hovered() {
		b.Background(p.hover)
	}
	b.Children(func() { ui.Icon(c, icTransfer).Size(15, 15).TextColor(p.quiet) })
	b.PopoverMenuButton(func(m *ui.Menu) {
		if m.Item("导入笔记或 PDF…").Chosen() {
			a.chooseImport()
		}
		if m.Item("导出 Markdown 和图片（.zip）…").Disabled(a.bookID == "").Chosen() {
			a.exportMarkdown()
		}
	})
}

type segment[T comparable] struct {
	title string
	value T
}

func segments[T comparable](c *ui.Context, p palette, options []segment[T], selected T, change func(T)) {
	ui.Row(c).Height(31).Shrink(0).Padding(3).Gap(2).Radius(8).
		AlignItems(ui.Center).Background(p.inputBg).Border(1, p.rule).Children(func() {
		for _, opt := range options {
			active := opt.value == selected
			b := ui.ButtonBase(c.Key(opt.title)).Height(24).PaddingX(10).Radius(5).Center().
				Shrink(0).Label(opt.title).Selected(active).OnClick(func() { change(opt.value) })
			fg := p.quiet
			if active {
				b.Background(p.elevated).Border(1, p.border).Shadow(0, 1, 2, 0, p.shadowSoft)
				fg = p.ink
			} else if b.Hovered() {
				b.Background(p.hover)
			}
			b.Children(func() { ui.Text(c, opt.title).FontSize(12).TextColor(fg).SingleLine() })
		}
	})
}

func settingSwitch(c *ui.Context, p palette, label string, active bool, change func()) {
	icon := toggleOff
	if active {
		icon = toggleOn
		if p.dark {
			icon = toggleOnDark
		}
	}
	ui.ButtonBase(c).Size(44, 34).Center().Shrink(0).Label(label).Role(ui.RoleSwitch).Checked(active).Background(ui.Transparent).
		FocusRingColor(p.accent).OnClick(change).Children(func() {
		ui.Image(c, icon).Size(39, 24)
	})
}

func settingGroup(c *ui.Context, p palette, title string, content func()) {
	ui.Column(c).FillWidth().Shrink(0).Gap(7).Children(func() {
		ui.Text(c, title).PaddingX(4).FontSize(11).FontWeight(600).TextColor(p.quiet)
		ui.Column(c).FillWidth().Radius(12).Clip().Background(p.canvas).Border(1, p.rule).Children(content)
	})
}

func settingRow(c *ui.Context, p palette, compact bool, title, hint string, control func()) {
	if compact {
		ui.Column(c).FillWidth().Shrink(0).Padding(12, 16).Gap(8).Children(func() {
			ui.Column(c).FillWidth().Gap(2).Children(func() {
				ui.Text(c, title).FontSize(13).FontWeight(550).TextColor(p.ink).SingleLine()
				ui.Text(c, hint).FontSize(11.5).TextColor(p.quiet)
			})
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(control)
		})
		return
	}
	ui.Row(c).MinHeight(57).FillWidth().Shrink(0).Padding(8, 16).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, title).FontSize(13).FontWeight(550).TextColor(p.ink).SingleLine()
			ui.Text(c, hint).FontSize(11.5).TextColor(p.quiet).SingleLine()
		})
		control()
	})
}

func settingButton(c *ui.Context, p palette, title string, icon *ui.SVG, danger, disabled bool, action func()) {
	fg, bg, border := p.ink, p.elevated, p.border
	if danger {
		fg, bg, border = p.danger, p.dangerWash, p.danger.Alpha(.35)
	}
	button := ui.ButtonBase(c).Height(30).PaddingX(10).Gap(6).Center().Radius(8).Shrink(0).
		Background(bg).Border(1, border).Shadow(0, 1, 2, 0, p.shadowSoft).
		Label(title).Disabled(disabled).FocusRingColor(p.accent).OnClick(action)
	if disabled {
		fg = p.quiet
	} else if button.Hovered() && !danger {
		button.Background(p.hover)
	}
	button.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(13, 13).TextColor(fg)
		}
		ui.Text(c, title).FontSize(12).TextColor(fg).SingleLine()
	})
}

var (
	icUpdateRefresh = svgIcon(`<path d="M21 12a9 9 0 1 1-2.64-6.36"/><path d="M21 3v6h-6"/>`)
	icExternalLink  = svgIcon(`<path d="M14 5h5v5"/><path d="M10 14 19 5"/><path d="M19 13v5a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h5"/>`)
)

func settingPrimaryButton(c *ui.Context, p palette, title string, icon *ui.SVG, disabled bool, action func()) {
	button := ui.ButtonBase(c).Height(30).PaddingX(11).Gap(6).Center().Radius(8).Shrink(0).
		Label(title).Disabled(disabled).FocusRingColor(p.accent).OnClick(action)
	if disabled {
		button.Background(p.accent.Alpha(.45))
	} else {
		button.Gradient(p.accentTop, p.accentBot, 180).
			Border(1, p.accentBot).
			InsetShadow(0, 1, 0, 0, ui.Hex("#ffffff").Alpha(.24)).
			Shadow(0, 1, 3, 0, p.accent.Alpha(.28))
		if button.Pressed() {
			button.Background(p.accentBot)
		}
	}
	button.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(13, 13).TextColor(p.onAccent)
		}
		ui.Text(c, title).FontSize(12).FontWeight(550).TextColor(p.onAccent).SingleLine()
	})
}

func (a *workspace) settingsCard(c *ui.Context, p palette, width, height float32) {
	cardW, cardH := min(640, width-24), min(746, height-24)
	if cardW < 1 || cardH < 1 {
		return
	}
	closeCard := func() {
		a.renameOpen, a.preferencesOpen = false, false
	}
	ui.Box(c.Key("settings-layer")).Absolute().Left(0).Right(0).Top(0).Bottom(0).ZIndex(20).Center().Children(func() {
		ui.ButtonBase(c).Absolute().Left(0).Right(0).Top(0).Bottom(0).
			Background(p.scrim).Label("关闭设置").OnClick(closeCard)
		ui.Column(c.Key("settings-card")).Width(cardW).Height(cardH).ZIndex(21).Radius(16).Clip().
			Background(p.surface).Border(1, p.border).Shadow(0, 24, 60, -14, p.modalShadow).Children(func() {
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
				a.stylusSettings(c, p, compact)
				a.storageSettings(c, p, compact)
				a.bookSettings(c, p, compact)
				a.aboutSettings(c, p, compact)
			})
			horizontalRule(c, p)
			ui.Row(c).Height(40).Shrink(0).FillWidth().PaddingX(24).AlignItems(ui.Center).Background(p.canvas).Children(func() {
				ui.Text(c, fmt.Sprintf("Folio v%s · 本地离线优先存储", currentAppVersion())).FontSize(11.5).TextColor(p.quiet).Grow(1)
				closeHint := "Esc 关闭"
				if a.touchUI() {
					closeHint = "返回键关闭"
				}
				ui.Text(c, closeHint).FontSize(11.5).TextColor(p.quiet)
			})
		})
	})
}

func (a *workspace) displaySettings(c *ui.Context, p palette, compact bool) {
	settingGroup(c, p, "外观与显示", func() {
		settingRow(c, p, compact, "外观主题", "切换浅色、深色或跟随系统", func() {
			segments(c, p, []segment[string]{{"浅色", "light"}, {"深色", "dark"}, {"跟随系统", ""}}, a.settings.Appearance, func(value string) {
				next := a.settings
				next.Appearance = value
				a.saveSettings(next)
			})
		})
		horizontalRule(c, p)
		settingRow(c, p, compact, "脑图视图形态", "切换富卡片树、专题框架、大纲流或精简树", func() {
			segments(c, p, []segment[string]{
				{"卡片树", notebook.LayoutCards},
				{"专题框架", notebook.LayoutBoard},
				{"大纲流", notebook.LayoutOutline},
				{"精简树", notebook.LayoutCompact},
			}, a.settings.LayoutMode(), func(value string) {
				next := a.settings
				next.GraphLayout = value
				if a.saveSettings(next) {
					a.scale = float32(next.GraphZoom) / 100
					a.canvasReady = false
					a.recenter = true
					a.cardMenuOpen = false
					a.outlineScroll = ui.ScrollState{}
				}
			})
		})
		horizontalRule(c, p)
		settingRow(c, p, compact, "脑图默认缩放", "打开笔记本时的初始视图比例", func() {
			segments(c, p, []segment[int]{{"自适应", 0}, {"90%", 90}, {"100%", 100}, {"110%", 110}}, a.settings.GraphZoom, func(value int) {
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
		usage += fmt.Sprintf(" · %.1f MB", float64(a.storageBytes)/1e6)
	}
	settingGroup(c, p, "存储与数据", func() {
		settingRow(c, p, compact, "导入与导出", "MarginNote、PDF 或带图 Markdown", func() {
			ui.Row(c).Gap(8).Shrink(0).Children(func() {
				settingButton(c, p, "导入笔记", icSettingImport, false, a.busy, a.chooseImport)
				settingButton(c, p, "导出 MD", icSettingExport, false, a.busy || a.bookID == "", a.exportMarkdown)
			})
		})
		horizontalRule(c, p)
		settingRow(c, p, compact, "本地存储与缓存", usage, func() {
			settingButton(c, p, "清理缓存", nil, false, false, a.clearRenderCache)
		})
	})
}

func (a *workspace) bookSettings(c *ui.Context, p palette, compact bool) {
	settingGroup(c, p, "笔记本管理", func() {
		title := a.tree.book.Title
		if title == "" {
			title = "未选择笔记本"
		}
		settingRow(c, p, compact, "重命名当前笔记本", title, func() {
			if a.renameOpen {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Width(245).Shrink(0).Children(func() {
					if ui.TextInput(c.Key("book-name"), &a.bookName).Grow(1).MinWidth(0).Height(30).Label("笔记本名称").Submitted() {
						a.renameBook()
					}
					settingButton(c, p, "保存", nil, false, a.busy || strings.TrimSpace(a.bookName) == "", a.renameBook)
					settingButton(c, p, "取消", nil, false, a.busy, func() { a.renameOpen = false })
				})
				return
			}
			settingButton(c, p, "重命名", nil, false, a.busy || a.bookID == "", func() {
				a.renameOpen, a.bookName = true, a.tree.book.Title
			})
		})
		horizontalRule(c, p)
		settingRow(c, p, compact, "删除当前笔记本", title, func() {
			settingButton(c, p, "删除笔记本", icSettingDelete, true, a.busy || a.bookID == "", a.confirmDeleteBook)
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

	controls := func() {
		switch st.phase {
		case updateChecking:
			settingButton(c, p, "检查更新", icUpdateRefresh, false, true, nil)
		case updateDownloading:
			settingButton(c, p, "下载中…", icImport, false, true, nil)
		case updateAvailable:
			ui.Row(c).Gap(8).Shrink(0).AlignItems(ui.Center).Children(func() {
				if st.info != nil && st.info.canDirectInstall() {
					label := "下载并安装"
					if st.info.UsingMyGo {
						label = "下载并更新"
					}
					settingPrimaryButton(c, p, label, icImport, false, func() {
						a.installUpdate(services)
					})
					settingButton(c, p, "前往发布页", icExternalLink, false, false, func() {
						a.openExternalURL(c, releaseURL)
					})
				} else {
					settingPrimaryButton(c, p, "前往发布页", icExternalLink, false, func() {
						a.openExternalURL(c, releaseURL)
					})
					settingButton(c, p, "重新检查", icUpdateRefresh, false, false, func() {
						a.checkForUpdates(services)
					})
				}
			})
		case updateRelaunchRequired:
			settingPrimaryButton(c, p, "立即重启", icUpdateRefresh, false, a.relaunchApp)
		case updatePermissionRequired:
			ui.Row(c).Gap(8).Shrink(0).AlignItems(ui.Center).Children(func() {
				settingPrimaryButton(c, p, "安装更新", icImport, false, func() {
					a.installUpdate(services)
				})
				settingButton(c, p, "前往发布页", icExternalLink, false, false, func() {
					a.openExternalURL(c, releaseURL)
				})
			})
		case updateInstallerLaunched:
			ui.Row(c).Gap(8).Shrink(0).AlignItems(ui.Center).Children(func() {
				if st.info != nil && st.info.canDirectInstall() {
					settingButton(c, p, "安装更新", icImport, false, false, func() {
						a.installUpdate(services)
					})
				}
				settingButton(c, p, "重新检查", icUpdateRefresh, false, false, func() {
					a.checkForUpdates(services)
				})
			})
		default:
			settingButton(c, p, "检查更新", icUpdateRefresh, false, false, func() {
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
					})
					ui.Text(c, hint).FontSize(11.5).LineHeight(1.45).TextColor(hintColor)
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
				ui.Box(c).FillWidth().Height(4).Radius(99).Clip().Background(p.inputBg).Children(func() {
					ui.Box(c).FillHeight().WidthPercent(ratio * 100).Radius(99).Background(p.accent)
				})
			}
			if st.info != nil && (st.phase == updateAvailable || st.phase == updateRelaunchRequired || st.phase == updatePermissionRequired) {
				if notes := formatReleaseNotesPreview(st.info.Notes); notes != "" {
					ui.Column(c).FillWidth().Padding(8, 10).Radius(8).
						Background(p.inputBg).Border(1, p.rule).Children(func() {
						ui.Text(c, notes).FillWidth().FontSize(11.5).LineHeight(1.45).TextColor(p.secondary)
					})
				}
			}
		})
		horizontalRule(c, p)
		settingRow(c, p, compact, "开源仓库", "github.com/dongyu-will/folio", func() {
			settingButton(c, p, "打开 GitHub", icExternalLink, false, false, func() {
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
			a.showMessage("无法打开链接："+err.Error(), true)
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
		return "暂无已发布版本", p.quiet
	case updateAvailable:
		if st.info != nil && st.info.Version != "" {
			return fmt.Sprintf("发现新版本 v%s", st.info.Version), p.accent
		}
		return "发现新版本", p.accent
	case updateDownloading:
		return formatUpdateProgress(st.downloaded, st.total), p.accent
	case updateRelaunchRequired:
		return "更新已就绪，重启后生效", p.accent
	case updatePermissionRequired:
		return "请在系统设置中允许 Folio 安装未知来源应用，授权后再次点击「安装更新」", p.secondary
	case updateInstallerLaunched:
		return "已打开系统安装器，请按提示完成更新", p.accent
	case updateError:
		if st.errText != "" {
			return st.errText, p.danger
		}
		return "检查更新失败，请稍后重试", p.danger
	default:
		return fmt.Sprintf("当前版本 v%s", curVer), p.quiet
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
