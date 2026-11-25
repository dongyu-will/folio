package main

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// Unified Apple-inspired motion language for Folio.
var (
	animMicro   = ui.Animation{Duration: 110 * time.Millisecond, Ease: ui.EaseOut, Oklab: true}
	animPress   = ui.Animation{Spring: ui.SpringOptions{Duration: 180 * time.Millisecond, Bounce: 0}}
	animControl = ui.Animation{Spring: ui.SpringOptions{Duration: 240 * time.Millisecond, Bounce: 0.08}}
	animSurface = ui.Animation{Spring: ui.SpringOptions{Duration: 280 * time.Millisecond, Bounce: 0.05}}
	animLayout  = ui.Animation{Spring: ui.SpringOptions{Duration: 300 * time.Millisecond, Bounce: 0}}
	animCamera  = ui.Animation{Spring: ui.SpringOptions{Duration: 340 * time.Millisecond, Bounce: 0}}
	animContent = ui.Animation{Spring: ui.SpringOptions{Duration: 240 * time.Millisecond, Bounce: 0}}
	animFade    = ui.Animation{Duration: 180 * time.Millisecond, Ease: ui.EaseOut, NonMotion: true}
)

var (
	trColorMicro = ui.ElementTransition{
		Colors:    true,
		Shadows:   true,
		Animation: &animMicro,
	}
	trScrim = ui.ElementTransition{
		Animation:    &animFade,
		Enter:        &ui.Motion{Opacity: 0},
		Exit:         &ui.Motion{Opacity: 0},
		ExitDuration: 140 * time.Millisecond,
		ExitEase:     ui.EaseIn,
	}
	trModalCard = ui.ElementTransition{
		Animation:    &animSurface,
		Enter:        &ui.Motion{Y: 14, Scale: 0.96, Opacity: 0},
		Exit:         &ui.Motion{Y: 8, Scale: 0.97, Opacity: 0},
		ExitDuration: 150 * time.Millisecond,
		ExitEase:     ui.EaseIn,
	}
	trPopover = ui.ElementTransition{
		Animation:    &animSurface,
		Enter:        &ui.Motion{Y: -6, Scale: 0.96, Opacity: 0, Flip: true},
		Exit:         &ui.Motion{Y: -4, Scale: 0.97, Opacity: 0, Flip: true},
		ExitDuration: 120 * time.Millisecond,
		ExitEase:     ui.EaseIn,
	}
	trToast = ui.ElementTransition{
		Animation:    &animSurface,
		Enter:        &ui.Motion{Y: -12, Scale: 0.96, Opacity: 0},
		Exit:         &ui.Motion{Y: -10, Scale: 0.96, Opacity: 0},
		ExitDuration: 150 * time.Millisecond,
		ExitEase:     ui.EaseIn,
	}
	trFloatingBar = ui.ElementTransition{
		Animation:    &animControl,
		Position:     true,
		Colors:       true,
		Shadows:      true,
		Enter:        &ui.Motion{Y: 6, Scale: 0.96, Opacity: 0},
		Exit:         &ui.Motion{Y: 4, Scale: 0.97, Opacity: 0},
		ExitDuration: 120 * time.Millisecond,
		ExitEase:     ui.EaseIn,
	}
	trFloatingBarStatic = ui.ElementTransition{
		Animation:    &animControl,
		Colors:       true,
		Shadows:      true,
		Enter:        &ui.Motion{Y: 6, Scale: 0.96, Opacity: 0},
		Exit:         &ui.Motion{Y: 4, Scale: 0.97, Opacity: 0},
		ExitDuration: 120 * time.Millisecond,
		ExitEase:     ui.EaseIn,
	}
	trCollapseV = ui.ElementTransition{
		Animation:    &animLayout,
		Height:       true,
		Enter:        &ui.Motion{Collapse: true, Opacity: 0},
		Exit:         &ui.Motion{Collapse: true, Opacity: 0},
		ExitDuration: 160 * time.Millisecond,
		ExitEase:     ui.EaseIn,
	}
	trCollapseH = ui.ElementTransition{
		Animation:    &animLayout,
		Width:        true,
		Enter:        &ui.Motion{Collapse: true, Opacity: 0},
		Exit:         &ui.Motion{Collapse: true, Opacity: 0},
		ExitDuration: 180 * time.Millisecond,
		ExitEase:     ui.EaseIn,
	}
	// Non-collapsing panel transitions keep full layout bounds at t=0 so
	// newly opened panels can receive clicks immediately while still collapsing on Exit.
	trPanelSlideV = ui.ElementTransition{
		Animation:    &animLayout,
		Height:       true,
		Enter:        &ui.Motion{Y: -6, Opacity: 0},
		Exit:         &ui.Motion{Collapse: true, Opacity: 0},
		ExitDuration: 160 * time.Millisecond,
		ExitEase:     ui.EaseIn,
	}
	trPanelSlideH = ui.ElementTransition{
		Animation:    &animLayout,
		Width:        true,
		Enter:        &ui.Motion{X: -12, Opacity: 0},
		Exit:         &ui.Motion{Collapse: true, Opacity: 0},
		ExitDuration: 180 * time.Millisecond,
		ExitEase:     ui.EaseIn,
	}
	trCrossfade = ui.ElementTransition{
		Animation:    &animFade,
		Enter:        &ui.Motion{Opacity: 0},
		Exit:         &ui.Motion{Opacity: 0},
		ExitDuration: 130 * time.Millisecond,
		ExitEase:     ui.EaseIn,
	}
)

func trPageSlide(dir float32) ui.ElementTransition {
	return ui.ElementTransition{
		Animation:    &animSurface,
		Enter:        &ui.Motion{X: 20 * dir, Opacity: 0},
		Exit:         &ui.Motion{X: -14 * dir, Opacity: 0},
		ExitDuration: 160 * time.Millisecond,
		ExitEase:     ui.EaseIn,
	}
}

func childExitSlide(dir float32) ui.Motion {
	return ui.Motion{X: -14 * dir, Opacity: 0}
}

func ctNumeric(down bool) ui.ContentTransition {
	return ui.ContentTransition{Numeric: true, Down: down, Animation: &animContent}
}

var (
	ctIcon  = ui.ContentTransition{Animation: &animContent}
	ctText  = ui.ContentTransition{Animation: &animContent}
	ctImage = ui.ContentTransition{Animation: &animContent}
)

// applyPressPose applies a paint-only tactile press scale without triggering layout.
func applyPressPose(el ui.Element, pressed, disabled bool) ui.Element {
	s := float32(1)
	if pressed && !disabled {
		s = 0.97
	}
	return el.AnimatePose(ui.Pose{Scale: s}, animPress)
}

// applyChevronPose rotates a disclosure chevron smoothly between collapsed (0) and expanded (90).
func applyChevronPose(el ui.Element, expanded bool) ui.Element {
	rot := float32(0)
	if expanded {
		rot = 90
	}
	return el.AnimatePose(ui.Pose{Rotate: rot}, animControl)
}

// applySelectScalePose gives swatches subtle selection pop and press depression.
func applySelectScalePose(el ui.Element, selected, pressed bool) ui.Element {
	s := float32(1)
	a := animControl
	if pressed {
		s = 0.94
		a = animPress
	} else if selected {
		s = 1.12
	}
	return el.AnimatePose(ui.Pose{Scale: s}, a)
}

var errorShakeFrames = []ui.Keyframe{
	{At: 0, Value: 0},
	{At: 50 * time.Millisecond, Value: -4.5, Ease: ui.EaseOut},
	{At: 105 * time.Millisecond, Value: 4.5, Ease: ui.EaseInOut},
	{At: 160 * time.Millisecond, Value: -2.5, Ease: ui.EaseInOut},
	{At: 210 * time.Millisecond, Value: 1.5, Ease: ui.EaseInOut},
	{At: 260 * time.Millisecond, Value: 0, Ease: ui.EaseOut},
}

// applyErrorShake plays a damped horizontal shake when run > 0 changes.
// When run == 0 it is a no-op so initial frames never allocate keyframe state.
func applyErrorShake(el ui.Element, run uint64) ui.Element {
	if run == 0 {
		return el
	}
	if dx := el.Keyframes("errorShake", errorShakeFrames, ui.KeyframeOptions{Run: run}); dx != 0 {
		el.Translate(dx, 0)
	}
	return el
}

type segmentMatchKey struct{ group string }

func trSegmentPill(group string) ui.ElementTransition {
	return ui.ElementTransition{
		Match:     segmentMatchKey{group: group},
		Animation: &animControl,
		Position:  true,
		Size:      true,
	}
}

const (
	cameraScaleEps = 0.0005
	cameraPanEps   = 0.05
)

var cameraSpringOmega = 2 * math.Pi / animCamera.Spring.Duration.Seconds()

type spring1D struct {
	pos    float64
	vel    float64
	target float64
}

func (sp *spring1D) advance(dt, omega float64) {
	if dt <= 0 {
		return
	}
	// Critically damped spring: y(t) = (c1 + c2*t) * exp(-omega*t).
	c1 := sp.pos - sp.target
	c2 := sp.vel + omega*c1
	exp := math.Exp(-omega * dt)
	y := c1 + c2*dt
	sp.pos = sp.target + y*exp
	sp.vel = (c2 - omega*y) * exp
}

func (sp *spring1D) settled(eps, omega float64) bool {
	return math.Abs(sp.pos-sp.target) <= eps && math.Abs(sp.vel) <= eps*omega
}

func (sp *spring1D) snap() {
	sp.pos = sp.target
	sp.vel = 0
}

// cameraSpring drives smooth, interruptible critically damped mindmap camera transitions.
type cameraSpring struct {
	active bool
	at     time.Time
	s      spring1D
	x      spring1D
	y      spring1D
}

func (cs *cameraSpring) start(fromS, fromX, fromY, toS, toX, toY float32, now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	if cs.active {
		if dt := now.Sub(cs.at).Seconds(); dt > 0 {
			cs.s.advance(dt, cameraSpringOmega)
			cs.x.advance(dt, cameraSpringOmega)
			cs.y.advance(dt, cameraSpringOmega)
		}
		cs.s.target = float64(toS)
		cs.x.target = float64(toX)
		cs.y.target = float64(toY)
		cs.at = now
		return
	}
	cs.s = spring1D{pos: float64(fromS), target: float64(toS)}
	cs.x = spring1D{pos: float64(fromX), target: float64(toX)}
	cs.y = spring1D{pos: float64(fromY), target: float64(toY)}
	cs.at = now
	cs.active = true
}

func (cs *cameraSpring) cancel() {
	cs.active = false
	cs.s.vel = 0
	cs.x.vel = 0
	cs.y.vel = 0
}

func (cs *cameraSpring) step(now time.Time, reduceMotion bool) (s, x, y float32, moving bool) {
	if !cs.active {
		return float32(cs.s.pos), float32(cs.x.pos), float32(cs.y.pos), false
	}
	if reduceMotion {
		cs.s.snap()
		cs.x.snap()
		cs.y.snap()
		cs.active = false
		return float32(cs.s.pos), float32(cs.x.pos), float32(cs.y.pos), false
	}
	if now.IsZero() {
		now = time.Now()
	}
	if dt := now.Sub(cs.at).Seconds(); dt > 0 {
		cs.s.advance(dt, cameraSpringOmega)
		cs.x.advance(dt, cameraSpringOmega)
		cs.y.advance(dt, cameraSpringOmega)
	}
	cs.at = now
	if cs.s.settled(cameraScaleEps, cameraSpringOmega) &&
		cs.x.settled(cameraPanEps, cameraSpringOmega) &&
		cs.y.settled(cameraPanEps, cameraSpringOmega) {
		cs.s.snap()
		cs.x.snap()
		cs.y.snap()
		cs.active = false
		return float32(cs.s.pos), float32(cs.x.pos), float32(cs.y.pos), false
	}
	return float32(cs.s.pos), float32(cs.x.pos), float32(cs.y.pos), true
}

// fadeTracker tracks paint-only opacity fades by key and auto-prunes elapsed entries.
type fadeTracker struct {
	starts map[string]time.Time
}

func (f *fadeTracker) start(key string, now time.Time) {
	if f.starts == nil {
		f.starts = make(map[string]time.Time)
	}
	f.starts[key] = now
}

func (f *fadeTracker) progress(key string, now time.Time, dur time.Duration) (float32, bool) {
	if len(f.starts) == 0 {
		return 1, false
	}
	st, ok := f.starts[key]
	if !ok {
		return 1, false
	}
	if dur <= 0 {
		delete(f.starts, key)
		return 1, false
	}
	elapsed := now.Sub(st)
	if elapsed < 0 {
		return 0, true
	}
	if elapsed >= dur {
		delete(f.starts, key)
		f.prune(now, dur)
		return 1, false
	}
	if len(f.starts) > 8 {
		f.prune(now, dur)
	}
	t := float32(elapsed) / float32(dur)
	return ui.EaseOut(t), true
}

func (f *fadeTracker) prune(now time.Time, dur time.Duration) {
	if len(f.starts) == 0 {
		return
	}
	for k, st := range f.starts {
		if dur <= 0 || now.Sub(st) >= dur {
			delete(f.starts, k)
		}
	}
}

func (f *fadeTracker) clear() {
	clear(f.starts)
}
