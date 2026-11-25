package main

import (
	"github.com/dongyu-will/folio/internal/notebook"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

type palette struct {
	dark        bool
	shell       ui.Color
	canvas      ui.Color
	dot         ui.Color
	surface     ui.Color
	elevated    ui.Color
	inputBg     ui.Color
	hover       ui.Color
	pressed     ui.Color
	selectedBg  ui.Color
	rule        ui.Color
	border      ui.Color
	edge        ui.Color
	ink         ui.Color
	secondary   ui.Color
	quiet       ui.Color
	accent      ui.Color
	accentTop   ui.Color
	accentBot   ui.Color
	onAccent    ui.Color
	tint        ui.Color
	tintBorder  ui.Color
	badgeBg     ui.Color
	danger      ui.Color
	dangerWash  ui.Color
	shadow      ui.Color
	shadowSoft  ui.Color
	modalShadow ui.Color
	scrim       ui.Color
	specular    ui.Color
}

var light = palette{
	dark:        false,
	shell:       ui.Hex("#f5f5f7"),
	canvas:      ui.Hex("#fbfbfc"),
	dot:         ui.Hex("#e3e6ee"),
	surface:     ui.Hex("#ffffff"),
	elevated:    ui.Hex("#ffffff"),
	inputBg:     ui.Hex("#eceef2"),
	hover:       ui.Hex("#e9ecf1"),
	pressed:     ui.Hex("#dfe3ea"),
	selectedBg:  ui.Hex("#ffffff"),
	rule:        ui.Hex("#e6e8ee"),
	border:      ui.Hex("#d8dce5"),
	edge:        ui.Hex("#cdd3df"),
	ink:         ui.Hex("#111216"),
	secondary:   ui.Hex("#474c59"),
	quiet:       ui.Hex("#787f90"),
	accent:      ui.Hex("#0a66ff"),
	accentTop:   ui.Hex("#2b7dff"),
	accentBot:   ui.Hex("#0052e0"),
	onAccent:    ui.Hex("#ffffff"),
	tint:        ui.Hex("#edf4ff"),
	tintBorder:  ui.Hex("#b3d1ff"),
	badgeBg:     ui.Hex("#f0f1f6"),
	danger:      ui.Hex("#d9383a"),
	dangerWash:  ui.Hex("#fdecec"),
	shadow:      ui.Hex("#0f172a").Alpha(.06),
	shadowSoft:  ui.Hex("#0f172a").Alpha(.035),
	modalShadow: ui.Hex("#0f172a").Alpha(.18),
	scrim:       ui.Hex("#0b0d14").Alpha(.20),
	specular:    ui.Hex("#ffffff").Alpha(.9),
}

var dark = palette{
	dark:        true,
	shell:       ui.Hex("#0e0f12"),
	canvas:      ui.Hex("#111216"),
	dot:         ui.Hex("#23252f"),
	surface:     ui.Hex("#16171c"),
	elevated:    ui.Hex("#1c1d24"),
	inputBg:     ui.Hex("#17181f"),
	hover:       ui.Hex("#22242d"),
	pressed:     ui.Hex("#2a2d38"),
	selectedBg:  ui.Hex("#22242e"),
	rule:        ui.Hex("#23252f"),
	border:      ui.Hex("#2e313e"),
	edge:        ui.Hex("#353949"),
	ink:         ui.Hex("#f2f3f5"),
	secondary:   ui.Hex("#b4b8c5"),
	quiet:       ui.Hex("#7b8090"),
	accent:      ui.Hex("#3b8bff"),
	accentTop:   ui.Hex("#559bff"),
	accentBot:   ui.Hex("#2473eb"),
	onAccent:    ui.Hex("#ffffff"),
	tint:        ui.Hex("#13233b"),
	tintBorder:  ui.Hex("#244b85"),
	badgeBg:     ui.Hex("#262834"),
	danger:      ui.Hex("#f06466"),
	dangerWash:  ui.Hex("#2e181b"),
	shadow:      ui.Hex("#000000").Alpha(.38),
	shadowSoft:  ui.Hex("#000000").Alpha(.24),
	modalShadow: ui.Hex("#000000").Alpha(.62),
	scrim:       ui.Hex("#000000").Alpha(.56),
	specular:    ui.Hex("#ffffff").Alpha(.06),
}

func svgIcon(body string) *ui.SVG {
	return ui.MustParseSVG([]byte(
		`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">` +
			body + `</svg>`,
	))
}

var (
	icImport       = svgIcon(`<path d="M12 4v11m-4-3.5 4 4 4-4"/><path d="M4.5 16.5v2a1.5 1.5 0 0 0 1.5 1.5h12a1.5 1.5 0 0 0 1.5-1.5v-2"/>`)
	icBook         = svgIcon(`<path d="M4 19.5v-15A2.5 2.5 0 0 1 6.5 2H19a1 1 0 0 1 1 1v18a1 1 0 0 1-1 1H6.5a2.5 2.5 0 0 1 0-5H20"/>`)
	icChevronDown  = svgIcon(`<path d="m6.5 9.5 5.5 5.5 5.5-5.5"/>`)
	icChevronLeft  = svgIcon(`<path d="m14.5 6.5-5.5 5.5 5.5 5.5"/>`)
	icChevronRight = svgIcon(`<path d="m9.5 6.5 5.5 5.5-5.5 5.5"/>`)
	icTrash        = svgIcon(`<path d="M3.5 6h17"/><path d="M8.5 6V4.2A1.2 1.2 0 0 1 9.7 3h4.6a1.2 1.2 0 0 1 1.2 1.2V6"/><path d="M6.5 6l.9 13.3A1.7 1.7 0 0 0 9.1 21h5.8a1.7 1.7 0 0 0 1.7-1.7L17.5 6"/>`)
	icPlus         = svgIcon(`<path d="M12 5v14M5 12h14"/>`)
	icMinus        = svgIcon(`<path d="M5 12h14"/>`)
	icEdit         = svgIcon(`<path d="M12 20h8.5"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/>`)
	icLayers       = svgIcon(`<path d="m12 3 9 4.5-9 4.5-9-4.5Z"/><path d="m3 12 9 4.5 9-4.5"/><path d="m3 16.5 9 4.5 9-4.5"/>`)
	icFolder       = svgIcon(`<path d="M3 6.5A2 2 0 0 1 5 4.5h4.2a1.8 1.8 0 0 1 1.4.7l1 1.3H19a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z"/>`)
	icCheck        = svgIcon(`<circle cx="12" cy="12" r="9"/><path d="m8.5 12.2 2.3 2.3 4.8-5"/>`)
	icAlert        = svgIcon(`<circle cx="12" cy="12" r="9"/><path d="M12 8v4.5"/><path d="M12 16h.01"/>`)
	icSidebar      = svgIcon(`<rect x="3" y="4" width="18" height="16" rx="2.5"/><path d="M9 4v16"/>`)
	icArrowLeft    = svgIcon(`<path d="M19 12H5"/><path d="m11 6-6 6 6 6"/>`)
	icClose        = svgIcon(`<path d="M18 6 6 18M6 6l12 12"/>`)
	icPage         = svgIcon(`<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8Z"/><path d="M14 3v5h5"/>`)
	icFit          = svgIcon(`<path d="M4 9V6a2 2 0 0 1 2-2h3"/><path d="M15 4h3a2 2 0 0 1 2 2v3"/><path d="M20 15v3a2 2 0 0 1-2 2h-3"/><path d="M9 20H6a2 2 0 0 1-2-2v-3"/>`)
	icPointer      = svgIcon(`<path d="M5 3.5 17.5 11l-5.3 1.4L9.8 18Z"/><path d="m12.8 12.8 4.4 4.4"/>`)
	icPDFSearch    = svgIcon(`<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>`)
)

func clamp(n, low, high float32) float32 { return max(low, min(n, high)) }

func horizontalRule(c *ui.Context, p palette) {
	ui.Box(c).FillWidth().Height(1).Shrink(0).Background(p.rule)
}

func brandMark(c *ui.Context, p palette, size float32) {
	iconSize := max(14, size*.62)
	ui.Box(c).Size(size, size).Radius(size*.28).Center().Shrink(0).
		Gradient(p.accentTop, p.accentBot, 180).
		InsetShadow(0, 1, 0, 0, ui.Hex("#ffffff").Alpha(.25)).
		Shadow(0, 1, 3, 0, p.accent.Alpha(.3)).
		Children(func() {
			ui.Icon(c, icFigmaBrand).Size(iconSize, iconSize).TextColor(p.onAccent)
		})
}

func sectionKicker(c *ui.Context, p palette, text string) {
	ui.Text(c, text).FontSize(11).FontWeight(600).LetterSpacing(0.45).
		TextColor(p.quiet).Padding(8, 10, 4).Shrink(0)
}

func hasDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			return true
		}
	}
	return false
}

func badgePill(c *ui.Context, p palette, text string) {
	ui.Row(c).Height(20).Padding(0, 7).Radius(99).Center().Shrink(0).
		Background(p.badgeBg).Border(1, p.rule).Children(func() {
		t := ui.Text(c, text).FontSize(11).FontWeight(550).FontFeatures("tnum").
			TextColor(p.quiet).SingleLine()
		if hasDigit(text) {
			t.ContentTransition(ctNumeric(false))
		}
	})
}

// primaryButton renders a Linear-inspired gradient CTA with specular top bevel.
func primaryButton(c *ui.Context, p palette, label string, icon *ui.SVG, disabled bool, pressed func()) ui.Element {
	b := ui.ButtonBase(c).Label(label).MinHeight(36).Padding(0, 14).Radius(8).Center().Gap(6).
		Disabled(disabled).FocusRingColor(p.accent).OnClick(pressed).
		Transition(trColorMicro)
	if disabled {
		b.Background(p.accent.Alpha(.45))
	} else {
		b.Gradient(p.accentTop, p.accentBot, 180).
			Border(1, p.accentBot).
			InsetShadow(0, 1, 0, 0, ui.Hex("#ffffff").Alpha(.24)).
			Shadow(0, 1, 3, 0, p.accent.Alpha(.28))
		if b.Pressed() {
			b.Background(p.accentBot)
		}
	}
	applyPressPose(b, b.Pressed(), disabled)
	b.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(14, 14).TextColor(p.onAccent)
		}
		ui.Text(c, label).TextColor(p.onAccent).FontSize(13).FontWeight(600).LetterSpacing(-0.1).SingleLine()
	})
	return b
}

// pillButton renders an Apple/Linear secondary or ghost pill button with optional icon.
func pillButton(c *ui.Context, p palette, label string, icon *ui.SVG, elevated, disabled bool, pressed func()) ui.Element {
	minH := float32(34)
	if isMobilePlatform() {
		minH = 38
	}
	b := ui.ButtonBase(c).Label(label).Tooltip(label).MinHeight(minH).Padding(0, 10).Radius(8).Center().Gap(5).
		Disabled(disabled).FocusRingColor(p.accent).OnClick(pressed).
		Transition(trColorMicro)
	fg := p.secondary
	if elevated {
		fg = p.ink
		b.Background(p.elevated).Border(1, p.border).
			InsetShadow(0, 1, 0, 0, p.specular).
			Shadow(0, 1, 2, 0, p.shadowSoft)
	}
	if !disabled {
		if b.Pressed() {
			b.Background(p.pressed)
			fg = p.ink
		} else if b.Hovered() {
			b.Background(p.hover)
			fg = p.ink
		}
	} else {
		fg = p.quiet.Alpha(.45)
	}
	applyPressPose(b, b.Pressed(), disabled)
	b.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(14, 14).TextColor(fg)
		}
		ui.Text(c, label).TextColor(fg).FontSize(12.5).FontWeight(550).LetterSpacing(-0.1).SingleLine()
	})
	return b
}

// iconButton renders a tactile icon button (or danger icon button).
func iconButton(c *ui.Context, p palette, icon *ui.SVG, label string, danger, disabled bool, pressed func()) ui.Element {
	sz := float32(34)
	if isMobilePlatform() {
		sz = 38
	}
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Size(sz, sz).Radius(8).Center().Shrink(0).
		Disabled(disabled).FocusRingColor(p.accent).OnClick(pressed).
		Transition(trColorMicro)
	fg := p.quiet
	if !disabled {
		if b.Pressed() {
			if danger {
				b.Background(p.dangerWash)
				fg = p.danger
			} else {
				b.Background(p.pressed)
				fg = p.ink
			}
		} else if b.Hovered() {
			if danger {
				b.Background(p.dangerWash)
				fg = p.danger
			} else {
				b.Background(p.hover)
				fg = p.ink
			}
		}
	} else {
		fg = p.quiet.Alpha(.4)
	}
	applyPressPose(b, b.Pressed(), disabled)
	b.Children(func() {
		ui.Icon(c, icon).Size(15, 15).TextColor(fg)
	})
	return b
}

type noteCardTheme struct {
	headerBg  ui.Color
	bodyBg    ui.Color
	border    ui.Color
	headerInk ui.Color
	accent    ui.Color
	tinted    bool
}

var mnCardLight = [16]noteCardTheme{
	// 0: Light Yellow
	{headerBg: ui.Hex("#fef3c7"), bodyBg: ui.Hex("#fffdf5"), border: ui.Hex("#ecc968"), headerInk: ui.Hex("#4a3408"), accent: ui.Hex("#d97706"), tinted: true},
	// 1: Light Green
	{headerBg: ui.Hex("#dcfce7"), bodyBg: ui.Hex("#f7fef9"), border: ui.Hex("#86dfaa"), headerInk: ui.Hex("#144225"), accent: ui.Hex("#16a34a"), tinted: true},
	// 2: MarginNote Classic Sky Blue (mbooks-annotation2 / 12)
	{headerBg: ui.Hex("#dcebff"), bodyBg: ui.Hex("#f8fbff"), border: ui.Hex("#9cc3f5"), headerInk: ui.Hex("#122c52"), accent: ui.Hex("#0a66ff"), tinted: true},
	// 3: Soft Rose Pink
	{headerBg: ui.Hex("#ffe4e6"), bodyBg: ui.Hex("#fff8f8"), border: ui.Hex("#f7a8b4"), headerInk: ui.Hex("#5c1424"), accent: ui.Hex("#e11d48"), tinted: true},
	// 4: Warm Amber Yellow
	{headerBg: ui.Hex("#fde68a"), bodyBg: ui.Hex("#fffced"), border: ui.Hex("#eab308"), headerInk: ui.Hex("#473005"), accent: ui.Hex("#ca8a04"), tinted: true},
	// 5: Vibrant Sage Green (mbooks-annotation15)
	{headerBg: ui.Hex("#c6f6d5"), bodyBg: ui.Hex("#f5fdf7"), border: ui.Hex("#68d391"), headerInk: ui.Hex("#123b23"), accent: ui.Hex("#15803d"), tinted: true},
	// 6: Deep Sky Blue (mbooks-annotation16)
	{headerBg: ui.Hex("#c7e0ff"), bodyBg: ui.Hex("#f5f9ff"), border: ui.Hex("#7ab3fb"), headerInk: ui.Hex("#0d2852"), accent: ui.Hex("#0369a1"), tinted: true},
	// 7: Coral Red Priority (mbooks-annotation17)
	{headerBg: ui.Hex("#ffd5dc"), bodyBg: ui.Hex("#fff6f7"), border: ui.Hex("#f4889a"), headerInk: ui.Hex("#5e1121"), accent: ui.Hex("#dc2626"), tinted: true},
	// 8: Apricot Orange
	{headerBg: ui.Hex("#fed7aa"), bodyBg: ui.Hex("#fffaf4"), border: ui.Hex("#f5a65b"), headerInk: ui.Hex("#522607"), accent: ui.Hex("#ea580c"), tinted: true},
	// 9: Fresh Teal / Cyan (mbooks-annotation19)
	{headerBg: ui.Hex("#ccf4ee"), bodyBg: ui.Hex("#f4fdfb"), border: ui.Hex("#6bd4c6"), headerInk: ui.Hex("#0e3c38"), accent: ui.Hex("#0d9488"), tinted: true},
	// 10: Warm Peach
	{headerBg: ui.Hex("#ffedd5"), bodyBg: ui.Hex("#fffcf7"), border: ui.Hex("#f6b97d"), headerInk: ui.Hex("#4e2809"), accent: ui.Hex("#d97706"), tinted: true},
	// 11: Steel Slate Blue
	{headerBg: ui.Hex("#dbe4f0"), bodyBg: ui.Hex("#f8fafc"), border: ui.Hex("#a3b5cd"), headerInk: ui.Hex("#1e293b"), accent: ui.Hex("#475569"), tinted: true},
	// 12: MarginNote Framework / Chapter Heading (mbooks-annotation1c)
	{headerBg: ui.Hex("#e8eff9"), bodyBg: ui.Hex("#ffffff"), border: ui.Hex("#b3c5df"), headerInk: ui.Hex("#132338"), accent: ui.Hex("#2563eb"), tinted: true},
	// 13: Warm Sand
	{headerBg: ui.Hex("#efe9dc"), bodyBg: ui.Hex("#fcfaf6"), border: ui.Hex("#cfc3ad"), headerInk: ui.Hex("#3d3324"), accent: ui.Hex("#92744a"), tinted: true},
	// 14: Coral Blush
	{headerBg: ui.Hex("#fce1e4"), bodyBg: ui.Hex("#fff7f8"), border: ui.Hex("#f0a6b0"), headerInk: ui.Hex("#541826"), accent: ui.Hex("#be123c"), tinted: true},
	// 15: Cool Stone Gray
	{headerBg: ui.Hex("#e8ebf0"), bodyBg: ui.Hex("#fafafa"), border: ui.Hex("#c4cbd6"), headerInk: ui.Hex("#1f2430"), accent: ui.Hex("#64748b"), tinted: true},
}

var mnCardDark = [16]noteCardTheme{
	// 0: Light Yellow
	{headerBg: ui.Hex("#302815"), bodyBg: ui.Hex("#1d1b16"), border: ui.Hex("#6e5722"), headerInk: ui.Hex("#fde68a"), accent: ui.Hex("#f59e0b"), tinted: true},
	// 1: Light Green
	{headerBg: ui.Hex("#152c1e"), bodyBg: ui.Hex("#161d19"), border: ui.Hex("#286441"), headerInk: ui.Hex("#bbf7d0"), accent: ui.Hex("#4ade80"), tinted: true},
	// 2: MarginNote Classic Sky Blue
	{headerBg: ui.Hex("#162842"), bodyBg: ui.Hex("#171c25"), border: ui.Hex("#2b538a"), headerInk: ui.Hex("#cde3ff"), accent: ui.Hex("#3b8bff"), tinted: true},
	// 3: Soft Rose Pink
	{headerBg: ui.Hex("#341820"), bodyBg: ui.Hex("#1e1619"), border: ui.Hex("#782d3f"), headerInk: ui.Hex("#fecdd3"), accent: ui.Hex("#fb7185"), tinted: true},
	// 4: Warm Amber Yellow
	{headerBg: ui.Hex("#342a12"), bodyBg: ui.Hex("#1e1b14"), border: ui.Hex("#7c621d"), headerInk: ui.Hex("#fef08a"), accent: ui.Hex("#facc15"), tinted: true},
	// 5: Vibrant Sage Green
	{headerBg: ui.Hex("#143020"), bodyBg: ui.Hex("#151e19"), border: ui.Hex("#277046"), headerInk: ui.Hex("#a7f3d0"), accent: ui.Hex("#34d399"), tinted: true},
	// 6: Deep Sky Blue
	{headerBg: ui.Hex("#152b4a"), bodyBg: ui.Hex("#161c26"), border: ui.Hex("#2e5d9e"), headerInk: ui.Hex("#bfdbfe"), accent: ui.Hex("#60a5fa"), tinted: true},
	// 7: Coral Red Priority
	{headerBg: ui.Hex("#3a1820"), bodyBg: ui.Hex("#201618"), border: ui.Hex("#882f42"), headerInk: ui.Hex("#fecaca"), accent: ui.Hex("#f87171"), tinted: true},
	// 8: Apricot Orange
	{headerBg: ui.Hex("#342114"), bodyBg: ui.Hex("#1f1915"), border: ui.Hex("#7c4923"), headerInk: ui.Hex("#fed7aa"), accent: ui.Hex("#fb923c"), tinted: true},
	// 9: Fresh Teal / Cyan
	{headerBg: ui.Hex("#132e2c"), bodyBg: ui.Hex("#151e1e"), border: ui.Hex("#256b65"), headerInk: ui.Hex("#99f6e4"), accent: ui.Hex("#2dd4bf"), tinted: true},
	// 10: Warm Peach
	{headerBg: ui.Hex("#322216"), bodyBg: ui.Hex("#1e1a16"), border: ui.Hex("#764c29"), headerInk: ui.Hex("#ffedd5"), accent: ui.Hex("#fdba74"), tinted: true},
	// 11: Steel Slate Blue
	{headerBg: ui.Hex("#212734"), bodyBg: ui.Hex("#181b22"), border: ui.Hex("#414d63"), headerInk: ui.Hex("#dbe4f0"), accent: ui.Hex("#94a3b8"), tinted: true},
	// 12: MarginNote Framework / Chapter Heading
	{headerBg: ui.Hex("#1b2638"), bodyBg: ui.Hex("#181b23"), border: ui.Hex("#354c6e"), headerInk: ui.Hex("#e2ecfa"), accent: ui.Hex("#559bff"), tinted: true},
	// 13: Warm Sand
	{headerBg: ui.Hex("#2b2720"), bodyBg: ui.Hex("#1c1a17"), border: ui.Hex("#5c5242"), headerInk: ui.Hex("#e8dfce"), accent: ui.Hex("#c4b293"), tinted: true},
	// 14: Coral Blush
	{headerBg: ui.Hex("#331a21"), bodyBg: ui.Hex("#1e1719"), border: ui.Hex("#743546"), headerInk: ui.Hex("#fbcfe8"), accent: ui.Hex("#f472b6"), tinted: true},
	// 15: Cool Stone Gray
	{headerBg: ui.Hex("#242730"), bodyBg: ui.Hex("#191b21"), border: ui.Hex("#434857"), headerInk: ui.Hex("#e2e5ec"), accent: ui.Hex("#94a3b8"), tinted: true},
}

func cardThemeFor(p palette, n notebook.Note) noteCardTheme {
	if n.Color != nil {
		idx := *n.Color
		if idx >= 0 && idx < 16 {
			if p.dark {
				return mnCardDark[idx]
			}
			return mnCardLight[idx]
		}
	}
	return noteCardTheme{
		headerBg:  p.shell,
		bodyBg:    p.elevated,
		border:    p.border,
		headerInk: p.ink,
		accent:    p.accent,
		tinted:    false,
	}
}

func richTextColor(p palette, hex string) ui.Color {
	h := strings.ToLower(strings.TrimSpace(hex))
	if h == "" {
		return p.ink
	}
	// Calibrated MarginNote HTML highlight ink palette for razor-sharp contrast in Light & Dark modes.
	switch h {
	case "#2000ff", "#0040ff":
		if p.dark {
			return ui.Hex("#6ea8ff")
		}
		return ui.Hex("#1448e6")
	case "#009fff", "#118aff", "#275bd1":
		if p.dark {
			return ui.Hex("#60b4ff")
		}
		return ui.Hex("#0a6ce6")
	case "#ff0000":
		if p.dark {
			return ui.Hex("#ff6b72")
		}
		return ui.Hex("#dc2626")
	case "#d16056":
		if p.dark {
			return ui.Hex("#f2847b")
		}
		return ui.Hex("#c94a40")
	case "#ff6000":
		if p.dark {
			return ui.Hex("#fb923c")
		}
		return ui.Hex("#ea580c")
	case "#20ff00":
		if p.dark {
			return ui.Hex("#4ade80")
		}
		return ui.Hex("#15803d")
	}
	if len(h) == 7 && h[0] == '#' {
		rv, err1 := strconv.ParseUint(h[1:3], 16, 8)
		gv, err2 := strconv.ParseUint(h[3:5], 16, 8)
		bv, err3 := strconv.ParseUint(h[5:7], 16, 8)
		if err1 == nil && err2 == nil && err3 == nil {
			r, g, b := float64(rv)/255, float64(gv)/255, float64(bv)/255
			lum := 0.2126*r + 0.7152*g + 0.0722*b
			if p.dark && lum < 0.44 {
				r = r + (1-r)*0.48
				g = g + (1-g)*0.48
				b = b + (1-b)*0.48
				return ui.RGB(uint8(min(255, int(r*255+0.5))), uint8(min(255, int(g*255+0.5))), uint8(min(255, int(b*255+0.5))))
			}
			if !p.dark && lum > 0.72 {
				r *= 0.62
				g *= 0.62
				b *= 0.62
				return ui.RGB(uint8(int(r*255+0.5)), uint8(int(g*255+0.5)), uint8(int(b*255+0.5)))
			}
		}
	}
	return ui.Hex(h)
}

func richBgColor(p palette, hex string) ui.Color {
	h := strings.ToLower(strings.TrimSpace(hex))
	if h == "" {
		return ui.Color{}
	}
	if p.dark {
		return ui.Hex(h).Alpha(.22)
	}
	return ui.Hex(h)
}

func dominantSpanFontSize(spans []notebook.TextSpan) float32 {
	const defaultCSSFontSize = float32(18)
	weights := make(map[float32]int)
	hasNonZero := false
	for _, s := range spans {
		n := len([]rune(strings.TrimSpace(s.Text)))
		if n == 0 {
			continue
		}
		sz := s.FontSize
		if sz > 0 {
			hasNonZero = true
		} else {
			sz = defaultCSSFontSize
		}
		weights[sz] += n
	}
	if !hasNonZero || len(weights) == 0 {
		return defaultCSSFontSize
	}
	bestSize := defaultCSSFontSize
	bestWeight := -1
	for sz, w := range weights {
		if w > bestWeight || (w == bestWeight && abs(sz-defaultCSSFontSize) < abs(bestSize-defaultCSSFontSize)) {
			bestSize, bestWeight = sz, w
		}
	}
	if bestSize <= 0 {
		return defaultCSSFontSize
	}
	return bestSize
}

func spanFontScale(s notebook.TextSpan, dominant float32) float32 {
	if s.FontSize <= 0 || dominant <= 0 {
		return 1
	}
	return clamp(s.FontSize/dominant, 0.78, 1.32)
}

func buildUISpans(p palette, spans []notebook.TextSpan, baseFontSize float32) []ui.Span {
	dominant := dominantSpanFontSize(spans)
	out := make([]ui.Span, 0, len(spans))
	for _, s := range spans {
		if s.Text == "" {
			continue
		}
		sz := baseFontSize * spanFontScale(s, dominant)
		sp := ui.Span{
			Text:      s.Text,
			Size:      sz,
			Italic:    s.Italic,
			Underline: s.Underline,
			Color:     richTextColor(p, s.Color),
		}
		if s.Bold {
			sp.Weight = 650
		}
		if s.Bg != "" {
			sp.Background = richBgColor(p, s.Bg)
			sp.Radius = max(2, sz*0.2)
		}
		out = append(out, sp)
	}
	return out
}

func renderRichBlock(c *ui.Context, p palette, blk notebook.ContentBlock, fallbackText string, fontSize, lineHeight float32, selectable bool) {
	if len(blk.Spans) > 0 {
		if uiSpans := buildUISpans(p, blk.Spans, fontSize); len(uiSpans) > 0 {
			el := ui.RichText(c, uiSpans...).
				FillWidth().Shrink(0).FontSize(fontSize).LineHeight(lineHeight).TextColor(p.ink)
			if selectable {
				el.Selectable()
			}
			return
		}
	}
	txt := fallbackText
	if txt == "" {
		txt = blk.Text
	}
	if strings.TrimSpace(txt) == "" {
		return
	}
	el := ui.Text(c, txt).FillWidth().Shrink(0).FontSize(fontSize).LineHeight(lineHeight).TextColor(p.ink)
	if selectable {
		el.Selectable()
	}
}
