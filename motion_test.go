package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo/ui"
)

func TestCameraSpringSettleAndInterrupt(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	var cs cameraSpring
	cs.start(1, 0, 0, 2, 120, -80, t0)

	// Step mid-flight at 80ms.
	t1 := t0.Add(80 * time.Millisecond)
	s1, x1, y1, moving := cs.step(t1, false)
	if !moving {
		t.Fatal("expected cameraSpring to be moving at 80ms")
	}
	if !(s1 > 1 && s1 < 2) || !(x1 > 0 && x1 < 120) || !(y1 < 0 && y1 > -80) {
		t.Fatalf("unexpected mid-flight state: s=%v x=%v y=%v", s1, x1, y1)
	}
	prevVelS, prevVelX, prevVelY := cs.s.vel, cs.x.vel, cs.y.vel
	if prevVelS <= 0 || prevVelX <= 0 || prevVelY >= 0 {
		t.Fatalf("expected non-zero directional velocities, got (%v, %v, %v)", prevVelS, prevVelX, prevVelY)
	}

	// Retarget mid-flight: position and velocity must be preserved at t1.
	cs.start(s1, x1, y1, 1.2, -40, 60, t1)
	if float32(cs.s.pos) != s1 || float32(cs.x.pos) != x1 || float32(cs.y.pos) != y1 {
		t.Fatalf("retarget jumped position: (%v,%v,%v) vs (%v,%v,%v)", cs.s.pos, cs.x.pos, cs.y.pos, s1, x1, y1)
	}
	if cs.s.vel != prevVelS || cs.x.vel != prevVelX || cs.y.vel != prevVelY {
		t.Fatalf("retarget did not preserve velocity: (%v,%v,%v) vs (%v,%v,%v)", cs.s.vel, cs.x.vel, cs.y.vel, prevVelS, prevVelX, prevVelY)
	}

	// Step forward in 16ms frames until settled.
	now := t1
	for i := 0; i < 120; i++ {
		now = now.Add(16 * time.Millisecond)
		s1, x1, y1, moving = cs.step(now, false)
		if !moving {
			break
		}
	}
	if moving {
		t.Fatalf("cameraSpring failed to settle within 1.92s: s=%v x=%v y=%v", s1, x1, y1)
	}
	if s1 != 1.2 || x1 != -40 || y1 != 60 {
		t.Fatalf("settled to wrong target: s=%v x=%v y=%v", s1, x1, y1)
	}
	// Subsequent step stays settled.
	s2, x2, y2, moving2 := cs.step(now.Add(16*time.Millisecond), false)
	if moving2 || s2 != 1.2 || x2 != -40 || y2 != 60 {
		t.Fatalf("unexpected post-settle step: moving=%v (%v,%v,%v)", moving2, s2, x2, y2)
	}
}

func TestCameraSpringReduceMotionAndCancel(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	var cs cameraSpring
	cs.start(1, 10, 20, 2.5, 300, -150, t0)

	s, x, y, moving := cs.step(t0.Add(16*time.Millisecond), true)
	if moving {
		t.Fatal("expected reduceMotion step to settle immediately")
	}
	if s != 2.5 || x != 300 || y != -150 {
		t.Fatalf("reduceMotion did not snap to target: (%v,%v,%v)", s, x, y)
	}

	// Cancel mid-flight freezes at current position and clears velocity.
	cs.start(1, 0, 0, 2, 100, 100, t0)
	sMid, xMid, yMid, mMid := cs.step(t0.Add(60*time.Millisecond), false)
	if !mMid {
		t.Fatal("expected active spring before cancel")
	}
	cs.cancel()
	sStop, xStop, yStop, mStop := cs.step(t0.Add(120*time.Millisecond), false)
	if mStop {
		t.Fatal("expected cancelled spring not to report moving")
	}
	if sStop != sMid || xStop != xMid || yStop != yMid {
		t.Fatalf("cancelled spring moved: (%v,%v,%v) -> (%v,%v,%v)", sMid, xMid, yMid, sStop, xStop, yStop)
	}
}

func TestFadeTrackerExpiryAndAutoPrune(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	const dur = 180 * time.Millisecond
	var ft fadeTracker

	// Unstarted key returns finished opacity 1 and inactive.
	if p, active := ft.progress("missing", t0, dur); active || p != 1 {
		t.Fatalf("expected (1, false) for untracked key, got (%v, %v)", p, active)
	}

	ft.start("k1", t0)
	ft.start("k2", t0.Add(100*time.Millisecond))

	p1, active1 := ft.progress("k1", t0.Add(90*time.Millisecond), dur)
	if !active1 || p1 <= 0 || p1 >= 1 {
		t.Fatalf("expected active mid-fade for k1, got (%v, %v)", p1, active1)
	}

	// k1 expires at t0 + 180ms and is pruned; k2 remains active.
	p1Done, active1Done := ft.progress("k1", t0.Add(dur), dur)
	if active1Done || p1Done != 1 {
		t.Fatalf("expected k1 elapsed to return (1, false), got (%v, %v)", p1Done, active1Done)
	}
	if _, exists := ft.starts["k1"]; exists {
		t.Fatal("expected k1 to be auto-pruned once elapsed")
	}
	if _, active2 := ft.progress("k2", t0.Add(dur), dur); !active2 {
		t.Fatal("expected k2 to remain active at 180ms")
	}

	// k2 expires at t0 + 280ms.
	if p2, active2 := ft.progress("k2", t0.Add(280*time.Millisecond), dur); active2 || p2 != 1 {
		t.Fatalf("expected k2 elapsed to return (1, false), got (%v, %v)", p2, active2)
	}
	if len(ft.starts) != 0 {
		t.Fatalf("expected fadeTracker to be empty after all keys expired, got %d", len(ft.starts))
	}

	// Bulk stale keys get pruned when another key is queried.
	for i := 0; i < 12; i++ {
		ft.start(fmt.Sprintf("stale-%d", i), t0)
	}
	ft.start("fresh", t0.Add(300*time.Millisecond))
	if _, active := ft.progress("fresh", t0.Add(320*time.Millisecond), dur); !active {
		t.Fatal("expected fresh key to be active")
	}
	if len(ft.starts) != 1 {
		t.Fatalf("expected stale keys to auto-prune leaving only fresh, got %d keys", len(ft.starts))
	}
}

func TestButtonTransitionsAndBadgeNumericSettle(t *testing.T) {
	count := 3
	clicks := 0
	view := func(c *ui.Context) {
		p := light
		ui.Column(c).Fill().Padding(24).Gap(12).Children(func() {
			primaryButton(c, p, "主要按钮", nil, false, func() { clicks++ })
			pillButton(c, p, "次要按钮", nil, true, false, func() {})
			iconButton(c, p, icPlus, "添加", false, false, func() {})
			badgePill(c, p, fmt.Sprintf("%d 条笔记", count))
		})
	}
	tt := ui.NewTester(view, 400, 300)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("expected no pending frame on idle initial render")
	}

	rect, ok := tt.Find("主要按钮")
	if !ok {
		t.Fatal("primaryButton not found")
	}
	cx, cy := rect.X+rect.W/2, rect.Y+rect.H/2
	tt.Press(cx, cy)
	if !tt.FramePending() {
		t.Fatal("expected pending frame while primaryButton press pose/color animates")
	}
	tt.Advance(400 * time.Millisecond)
	if tt.FramePending() {
		t.Fatal("expected primaryButton press animation to settle after 400ms")
	}

	tt.Release(cx, cy)
	if clicks != 1 {
		t.Fatalf("expected 1 click, got %d", clicks)
	}
	tt.Advance(400 * time.Millisecond)
	if tt.FramePending() {
		t.Fatal("expected button release animation to settle cleanly")
	}

	// Update badge numeric text and verify smooth settle to idle.
	count = 14
	tt.Frame()
	if !tt.FramePending() {
		t.Fatal("expected pending frame when badgePill numeric count changes")
	}
	tt.Advance(500 * time.Millisecond)
	if tt.FramePending() {
		t.Fatal("expected badgePill numeric ContentTransition to settle after 500ms")
	}
}

func TestErrorShakeAndReduceMotionBehavior(t *testing.T) {
	var run uint64
	showModal := false
	view := func(c *ui.Context) {
		p := light
		ui.Box(c).Fill().Children(func() {
			card := ui.Box(c).Key("shake-box").Size(120, 40).Background(p.surface)
			applyErrorShake(card, run)
			if showModal {
				ui.Box(c).Key("modal-card").Size(200, 120).Background(p.elevated).Transition(trModalCard)
			}
		})
	}
	tt := ui.NewTester(view, 400, 300)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("expected FramePending() == false when shake run == 0")
	}

	run = 1
	tt.Frame()
	if !tt.FramePending() {
		t.Fatal("expected FramePending() == true while error shake plays")
	}
	tt.Advance(320 * time.Millisecond)
	if tt.FramePending() {
		t.Fatal("expected error shake to settle after 320ms")
	}

	// Under ReduceMotion, positional keyframes snap immediately.
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	run = 2
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("expected error shake not to animate under ReduceMotion")
	}

	// Modal open/close settles cleanly even with ReduceMotion (opacity fade still settles).
	showModal = true
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	if tt.FramePending() {
		t.Fatal("expected modal open to settle under ReduceMotion")
	}
	showModal = false
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	if tt.FramePending() {
		t.Fatal("expected modal close to settle under ReduceMotion")
	}
}

func TestWorkspaceErrorShakeCounters(t *testing.T) {
	app := &workspace{store: notebook.NewStore(t.TempDir())}
	app.showMessage("正常提示", false)
	if app.toastShakeRun != 0 {
		t.Fatalf("non-error message must not increment toastShakeRun, got %d", app.toastShakeRun)
	}
	app.showMessage("操作失败", true)
	if app.toastShakeRun != 1 {
		t.Fatalf("error message must increment toastShakeRun to 1, got %d", app.toastShakeRun)
	}

	app.editMode = editNote
	app.origTitle, app.draftTitle = "原标题", "新标题"
	app.requestCancelEdit()
	if !app.discardPrompt || app.discardShakeRun != 1 {
		t.Fatalf("expected discardPrompt=true and discardShakeRun=1, got prompt=%v run=%d", app.discardPrompt, app.discardShakeRun)
	}
	app.requestCancelEdit()
	if app.editing() || app.discardPrompt || app.discardShakeRun != 1 {
		t.Fatalf("second cancel should close without incrementing shake again: editing=%v prompt=%v run=%d", app.editing(), app.discardPrompt, app.discardShakeRun)
	}
}
