package main

import (
	"context"
	"encoding/json"
	"runtime"
	"sync"
	"time"

	"github.com/dongyu-will/folio/internal/androidopen"
	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func themeSourceForAppearance(appearance string) mygo.ThemeSource {
	switch appearance {
	case "light":
		return mygo.ThemeLight
	case "dark":
		return mygo.ThemeDark
	default:
		return mygo.ThemeSystem
	}
}

func windowBackgroundForAppearance(appearance string) string {
	switch appearance {
	case "light":
		return "#f5f5f7"
	case "dark":
		return "#0e0f12"
	default:
		return "light-dark(#f5f5f7, #0e0f12)"
	}
}

var androidAppearanceSync struct {
	mu      sync.Mutex
	latest  string
	running bool
}

func syncAndroidAppearance(appearance string) {
	androidAppearanceSync.mu.Lock()
	androidAppearanceSync.latest = appearance
	if androidAppearanceSync.running {
		androidAppearanceSync.mu.Unlock()
		return
	}
	androidAppearanceSync.running = true
	androidAppearanceSync.mu.Unlock()

	go func() {
		for {
			androidAppearanceSync.mu.Lock()
			v := androidAppearanceSync.latest
			androidAppearanceSync.mu.Unlock()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = mygo.Native(androidopen.PluginID).Call(ctx, "setAppearance", map[string]string{"appearance": v}, nil)
			cancel()

			androidAppearanceSync.mu.Lock()
			if androidAppearanceSync.latest == v {
				androidAppearanceSync.running = false
				androidAppearanceSync.mu.Unlock()
				return
			}
			androidAppearanceSync.mu.Unlock()
		}
	}()
}

// syncAppTheme aligns the platform theme source and Android's launch cover
// cache with Folio's saved appearance. It is a no-op before App.Run becomes
// ready (including headless unit tests) so backend() is never initialized early.
func syncAppTheme(appearance string) {
	if !mygo.App.IsReady() {
		return
	}
	mygo.Theme.SetSource(themeSourceForAppearance(appearance))
	if runtime.GOOS == "android" {
		syncAndroidAppearance(appearance)
	}
}

func (a *workspace) onAndroidSurfaceResumed([]byte) {
	a.androidFramePending.Store(true)
	if win := a.window(); win != nil {
		win.Invalidate()
	}
}

func (a *workspace) markAndroidFramePainted() {
	if !a.androidFramePending.CompareAndSwap(true, false) {
		return
	}
	if fn := a.androidFrameReadyFn; fn != nil {
		fn()
		return
	}
	if runtime.GOOS != "android" {
		return
	}
	// NativePlugin.Call posts to MyGo's mainLoop, which cannot run until the
	// in-progress runFrame() finishes layout, paint, present(), and eglSwapBuffers().
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = mygo.Native(androidopen.PluginID).Call(ctx, "frameReady", nil, nil)
	}()
}

type penZone struct {
	name   string
	key    string
	clip   ui.Rect
	origin ui.Point
	scale  float32
}

type androidInk struct {
	canvas, pdf penZone
	sent        [2]penZone
	updating    bool
}

func (a *workspace) stylusAutoDraw() bool {
	return !a.settings.PenTapToSelect && !a.drawing.penBrowseMode
}

func (a *workspace) stylusDrawsOnCanvas() bool {
	if a.drawing.canvasToolActive() {
		return true
	}
	return a.drawing.tool == panTool && a.stylusAutoDraw()
}

func (a *workspace) stylusDrawsOnPDF() bool {
	switch a.drawing.tool {
	case penTool, markerTool, strokeEraser, partialEraser, excerptTool:
		return true
	case panTool:
		return a.stylusAutoDraw()
	default:
		return false
	}
}

func (a *workspace) dockBottomReserve() float32 {
	dockVisible := a.drawing.dockOpen || a.mode == pdfMode || a.mode == splitMode
	if a.touchUI() {
		if !dockVisible {
			return 84
		}
		if a.drawing.toolPrefs {
			return 156
		}
		return 108
	}
	if !dockVisible {
		return 72
	}
	if a.drawing.toolPrefs {
		return 128
	}
	return 96
}

func (a *workspace) disablePenZones() {
	if runtime.GOOS != "android" {
		return
	}
	blocked := a.bookID == "" || a.editing() || a.page != browse || a.preferencesOpen || a.preview != "" || a.pdfTOCOpen
	if blocked || a.mode == pdfMode || a.settings.LayoutMode() == notebook.LayoutOutline || !a.stylusDrawsOnCanvas() {
		a.androidInk.canvas = penZone{}
	}
	if blocked || a.mode == canvasMode || !a.stylusDrawsOnPDF() {
		a.androidInk.pdf = penZone{}
	}
	a.publishPenZones()
}

func (a *workspace) canvasPenZone(rect ui.Rect, ox, oy, scale float32) {
	if runtime.GOOS != "android" || a.page != browse || a.preferencesOpen || a.preview != "" {
		return
	}
	if !a.stylusDrawsOnCanvas() {
		a.androidInk.canvas = penZone{}
		a.publishPenZones()
		return
	}
	// Leave room for the minimap and floating ink dock / pen options to take stylus taps.
	rect.H = max(0, rect.H-a.dockBottomReserve())
	a.androidInk.canvas = penZone{name: "canvas", clip: rect, origin: ui.Point{X: ox, Y: oy}, scale: scale}
	a.publishPenZones()
}

func (a *workspace) pdfPenZone(key string, clip ui.Rect, ox, oy, scale, viewBottom float32) {
	if runtime.GOOS != "android" || a.page != browse || a.preferencesOpen || a.preview != "" {
		return
	}
	if !a.stylusDrawsOnPDF() {
		a.androidInk.pdf = penZone{}
		a.publishPenZones()
		return
	}
	if a.mode == pdfMode && viewBottom > 0 {
		// In fullscreen PDF mode the ink dock floats at the bottom; keep it stylus-clickable.
		if maxY := viewBottom - a.dockBottomReserve(); clip.Y+clip.H > maxY {
			clip.H = max(0, maxY-clip.Y)
		}
	}
	a.androidInk.pdf = penZone{name: "pdf", key: key, clip: clip, origin: ui.Point{X: ox, Y: oy}, scale: scale}
	a.publishPenZones()
}

func (a *workspace) publishPenZones() {
	b := &a.androidInk
	win := a.window()
	if win == nil || b.updating {
		return
	}
	next := [2]penZone{b.canvas, b.pdf}
	// Android needs hit regions only. Coordinates for ink stay on the Go side.
	if b.sent[0].clip == next[0].clip && b.sent[1].clip == next[1].clip {
		return
	}
	zones := make([]map[string]any, 0, 2)
	for _, zone := range next {
		if r := zone.clip; r.W > 0 && r.H > 0 {
			zones = append(zones, map[string]any{"name": zone.name, "x": r.X, "y": r.Y, "w": r.W, "h": r.H})
		}
	}
	b.sent, b.updating = next, true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := mygo.Native(androidopen.PluginID).Call(ctx, "penZones", map[string]any{"zones": zones}, nil)
		win.Update(func() {
			a.androidInk.updating = false
			if err == nil {
				a.publishPenZones()
			} else {
				a.showError(err)
			}
		})
	}()
}

func (a *workspace) onAndroidStylus(raw []byte) {
	var event struct {
		Zone, Phase string
		Eraser      bool
		Samples     [][3]float32
	}
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	if win := a.window(); win != nil {
		win.Update(func() {
			zone := a.androidInk.canvas
			if event.Zone == "pdf" {
				zone = a.androidInk.pdf
			}
			if zone.scale <= 0 || zone.name != event.Zone || event.Phase == "cancel" {
				a.drawing.gesture = inkGesture{}
				return
			}
			kind := ui.InputPointerMove
			switch event.Phase {
			case "down":
				kind = ui.InputPointerDown
			case "up":
				kind = ui.InputPointerUp
			}
			btn := 0
			if event.Eraser {
				btn = 1
			}
			ev := ui.InputEvent{Kind: kind, Button: btn, Pointer: ui.PointerPen, Time: time.Now()}
			for _, sample := range event.Samples {
				point := inkPoint{X: (sample[0] - zone.origin.X) / zone.scale,
					Y: (sample[1] - zone.origin.Y) / zone.scale, Pressure: clamp(sample[2], 0, 1)}
				a.inkInput(ev, zone.key, point, zone.scale, true)
			}
		})
	}
}
