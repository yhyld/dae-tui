package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yhyld/dae-tui/internal/driver"
)

// A mutation's success message carries its label as a pending-reload note;
// the A confirm lists the notes, an unmodified status clears them, and a
// flip with no local cause reads as an external change.
func TestReloadNotesListedAndCleared(t *testing.T) {
	d := &pinDriver{routingBody: pinFixtureRouting}
	m := newPinTestModel(t, d)
	m = driveSwitch(t, m, 1) // 切换代理组 proxy → b

	mm := m.(Model)
	found := false
	for _, n := range mm.reloadNotes {
		if strings.Contains(n, "proxy") && strings.Contains(n, "b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("switch note missing from the pending list: %v", mm.reloadNotes)
	}

	// The status flip to modified is ours: no external-change line.
	m2, _ := mm.Update(statusMsg{Status: driver.Status{Version: "v", Running: true, Modified: true}})
	mm2 := m2.(Model)
	if !mm2.status.Modified {
		t.Fatal("fixture expects the status flip to land")
	}
	if n := len(mm2.reloadNotes); n != 1 {
		t.Fatalf("flip must not add an external line, notes = %v", mm2.reloadNotes)
	}

	// The A confirm names the change; the top bar counts it in yellow.
	ov := mm2.applyOverlay()
	if !strings.Contains(plainLines(ov.lines), "自上次重载以来的变更") ||
		!strings.Contains(plainLines(ov.lines), "切换代理组") {
		t.Fatalf("A confirm missing the pending list:\n%s", plainLines(ov.lines))
	}
	if bar := mm2.statusBar(); !strings.Contains(plain(bar), "需重载") || !strings.Contains(plain(bar), "1 项") {
		t.Fatalf("top bar missing the pending count:\n%s", bar)
	}

	// Once the server reports an unmodified running config the list clears
	// (reload done — here or elsewhere). Force the stale-guard window shut;
	// the note just landed and a poll may predate it.
	mm2.reloadLastNote = time.Now().Add(-reloadStaleGuard)
	m3, _ := mm2.Update(statusMsg{Status: driver.Status{Version: "v", Running: true}})
	mm3 := m3.(Model)
	if len(mm3.reloadNotes) != 0 {
		t.Fatalf("notes survived an unmodified status: %v", mm3.reloadNotes)
	}
	if ov := mm3.applyOverlay(); strings.Contains(plainLines(ov.lines), "切换代理组") {
		t.Fatalf("cleared notes must not render:\n%s", plainLines(ov.lines))
	}
}

// A flip to modified with no local mutation is an external change (daed
// web UI, or edits from before this session) — listed like any other note.
func TestReloadExternalChangeNote(t *testing.T) {
	d := &pinDriver{routingBody: pinFixtureRouting}
	m := newPinTestModel(t, d)

	m2, _ := m.Update(statusMsg{Status: driver.Status{Version: "v", Running: true, Modified: true}})
	mm := m2.(Model)
	if len(mm.reloadNotes) != 1 || !strings.Contains(mm.reloadNotes[0], "外部变更") {
		t.Fatalf("external flip should add one external note, got %v", mm.reloadNotes)
	}
	if ov := mm.applyOverlay(); !strings.Contains(plainLines(ov.lines), "外部变更") {
		t.Fatalf("A confirm missing the external note:\n%s", plainLines(ov.lines))
	}
}

// Notes are only recorded while the proxy runs: daed reports modified
// false whenever it is stopped, so notes taken then would never confirm.
func TestReloadNotesGatedOnRunning(t *testing.T) {
	d := &pinDriver{routingBody: pinFixtureRouting}
	m := newPinTestModel(t, d)

	m2, _ := m.Update(statusMsg{Status: driver.Status{Version: "v", Running: false}})
	m3, _ := m2.Update(reloadNoteMsg{Note: "x"})
	mm := m3.(Model)
	if len(mm.reloadNotes) != 0 || !mm.autoReloadAt.IsZero() {
		t.Fatalf("stopped proxy must not record notes: %v %v", mm.reloadNotes, mm.autoReloadAt)
	}
}

// The settings toggle persists auto_reload, flips the menu state, and the
// next local mutation fires exactly one reload once the debounce deadline
// passes.
func TestAutoReloadToggleAndFire(t *testing.T) {
	d := &pinDriver{routingBody: pinFixtureRouting}
	m := newPinTestModel(t, d)

	// P → walk to 自动重载 → Enter: the config write lands and the menu
	// reflects the new state.
	mm := m.(Model)
	mm.page = pageHome
	pm, _ := mm.Update(runeKey("P"))
	mm = pm.(Model)
	steps := 0
	for steps < 6 && mm.settings.cur != itemAutoReload {
		steps++
		var cmd tea.Cmd
		var m2 tea.Model
		m2, cmd = mm.Update(runeKey("j"))
		mm = m2.(Model)
		_ = cmd
	}
	if mm.settings.cur != itemAutoReload {
		t.Fatalf("cursor never reached the auto-reload row: cur=%d", mm.settings.cur)
	}
	m2, cmd := mm.Update(runeKey("enter"))
	tog, ok := cmd().(autoReloadToggledMsg)
	if !ok || !tog.On || tog.Err != nil {
		t.Fatalf("Enter produced %#v, want a successful on-toggle", cmd())
	}
	m3, _ := m2.Update(tog)
	mm3 := m3.(Model)
	if !mm3.cfg.AutoReload || !mm3.settings.autoReload {
		t.Fatalf("toggle did not stick: cfg=%v menu=%v", mm3.cfg.AutoReload, mm3.settings.autoReload)
	}
	if ov := mm3.settings.overlay(); !strings.Contains(plainLines(ov.lines), "自动重载") ||
		!strings.Contains(plainLines(ov.lines), "开启") {
		t.Fatalf("menu row missing its state:\n%s", plainLines(ov.lines))
	}
	// Close the settings modal: S must reach the groups page.
	m3b, _ := m3.Update(runeKey("esc"))

	// A local mutation arms the deadline; the past deadline fires the
	// reload exactly once via the 1s tick.
	m4 := driveSwitch(t, m3b, 1)
	mm4 := m4.(Model)
	if mm4.autoReloadAt.IsZero() {
		t.Fatal("a mutation must arm the auto-reload deadline")
	}
	mm4.autoReloadAt = time.Now().Add(-time.Second)
	m5, cmd := mm4.Update(tickMsg{n: 50})
	fired := false
	for _, msg := range execCmds(cmd) {
		if od, ok := msg.(opDoneMsg); ok && strings.Contains(od.Op, "重载") {
			fired = true
			if od.Err != nil {
				t.Fatalf("reload errored: %v", od.Err)
			}
		}
	}
	if !fired || d.runCalls != 1 || d.runDry[0] {
		t.Fatalf("auto reload fired=%v calls=%d dry=%v, want one live reload", fired, d.runCalls, d.runDry)
	}
	if !m5.(Model).autoReloadAt.IsZero() {
		t.Fatal("a fired deadline must not re-arm")
	}

	// No retries: the next tick does nothing.
	m6, cmd := m5.Update(tickMsg{n: 51})
	_ = execCmds(cmd)
	if d.runCalls != 1 {
		t.Fatalf("auto reload retried on its own: %d calls", d.runCalls)
	}
	_ = m6
}

// The safety gates: external changes never fire, the A confirm defers the
// shot, a stopped proxy never arms, and a failed reload disables itself
// until the next mutation.
func TestAutoReloadGuards(t *testing.T) {
	// External flips never arm even with the toggle on.
	d := &pinDriver{routingBody: pinFixtureRouting}
	m := newPinTestModel(t, d)
	mm := m.(Model)
	mm.cfg.AutoReload = true
	m2, _ := mm.Update(statusMsg{Status: driver.Status{Version: "v", Running: true, Modified: true}})
	mm2 := m2.(Model)
	if !mm2.autoReloadAt.IsZero() {
		t.Fatal("an external change must not arm the auto reload")
	}

	// The A confirm defers the shot instead of firing under the user.
	d2 := &pinDriver{routingBody: pinFixtureRouting}
	fresh := newPinTestModel(t, d2)
	fm := fresh.(Model)
	fm.cfg.AutoReload = true
	f2, _ := fm.Update(reloadNoteMsg{Note: "x"})
	fm2 := f2.(Model)
	fm2.confirmApply = true
	fm2.autoReloadAt = time.Now().Add(-time.Second)
	f3, cmd := fm2.Update(tickMsg{n: 50})
	_ = execCmds(cmd)
	if d2.runCalls != 0 || f3.(Model).autoReloadAt.IsZero() {
		t.Fatalf("confirm must defer the shot: calls=%d deadline=%v", d2.runCalls, f3.(Model).autoReloadAt)
	}

	// A failed reload stops the machinery: no retry ticks, the error
	// surfaces as a toast, and only a new mutation re-arms.
	d3 := &pinDriver{routingBody: pinFixtureRouting}
	fresh3 := newPinTestModel(t, d3)
	fm3 := fresh3.(Model)
	fm3.cfg.AutoReload = true
	d3.runErr = errors.New("boom")
	g1, _ := fm3.Update(reloadNoteMsg{Note: "x"})
	gm := g1.(Model)
	gm.autoReloadAt = time.Now().Add(-time.Second)
	g2, cmd := gm.Update(tickMsg{n: 50})
	var failToast opDoneMsg
	for _, msg := range execCmds(cmd) {
		if od, ok := msg.(opDoneMsg); ok && od.Err != nil {
			failToast = od
		}
	}
	if d3.runCalls != 1 || failToast.Err == nil {
		t.Fatalf("failing reload: calls=%d toast=%#v", d3.runCalls, failToast)
	}
	g3, _ := g2.Update(failToast)
	if !strings.Contains(g3.(Model).toast, "重载") && g3.(Model).toast == "" {
		t.Fatalf("reload failure must toast, got %q", g3.(Model).toast)
	}
	_, cmd = g3.Update(tickMsg{n: 51})
	_ = execCmds(cmd)
	if d3.runCalls != 1 {
		t.Fatalf("a failed auto reload retried: %d calls", d3.runCalls)
	}
}
