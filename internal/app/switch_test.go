package app

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yhyld/dae-tui/internal/driver"
)

// stripSGR drops SGR escapes so marker-adjacency assertions can see the
// plain text ("● b") through the styling.
var stripSGR = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plain(s string) string { return stripSGR.ReplaceAllString(s, "") }

func TestReferencedGroups(t *testing.T) {
	set := referencedGroups([]string{"proxy", "must_direct", "direct", "b"})
	if len(set) != 2 || !set["proxy"] || !set["b"] {
		t.Fatalf("referencedGroups = %v, want proxy+b only", set)
	}
}

// noPresetMode asserts a routing body the stub detector calls custom, so the
// only ● on the home page is the group marker under test.
const markerRouting = "pname(x) -> must_direct\ndip(geoip:private) -> direct\ndip(1.2.3.4) -> b"

func TestHomeMarksReferencedGroup(t *testing.T) {
	d := &pinDriver{routingBody: markerRouting}
	m := newPinTestModel(t, d)
	mm := m.(Model)
	home := mm.home.View(driver.Status{})
	// Running=false keeps the proxy badge a ○; the preset list is custom
	// (no active preset), so exactly one dot remains: group b's marker.
	if got := strings.Count(home, "●"); got != 1 {
		t.Fatalf("home shows %d dots, want exactly 1 (the referenced group):\n%s", got, home)
	}
	if !strings.Contains(home, "b") {
		t.Fatal("group b missing from the group box")
	}
}

// driveSwitch arms and confirms the groups-page switch overlay for group gi.
func driveSwitch(t *testing.T, m tea.Model, gi int) tea.Model {
	t.Helper()
	mm := m.(Model)
	mm.page = pageTree
	mm.groups.gi = gi
	mm.groups.focus = 0
	m3, cmd := mm.Update(runeKey("S"))
	if cmd != nil {
		t.Fatalf("S produced a command: %#v", cmd())
	}
	mm3 := m3.(Model)
	if mm3.groups.mode != pickSwitch {
		t.Fatalf("mode = %d, want pickSwitch", mm3.groups.mode)
	}
	if !mm3.anyModal() {
		t.Fatal("switch confirm must count as a modal")
	}
	ov := mm3.groups.overlay()
	if ov == nil || !strings.Contains(plainLines(ov.lines), "切换") {
		t.Fatalf("switch overlay missing: %#v", ov)
	}
	m4, cmd := m3.Update(runeKey("y"))
	req, ok := cmd().(switchGroupRequestedMsg)
	if !ok {
		t.Fatalf("y produced %#v, want switchGroupRequestedMsg", cmd())
	}
	m5, cmd := m4.Update(req)
	if cmd == nil {
		t.Fatal("switch request must produce the switch command")
	}
	return runBatch(t, m5, cmd)
}

func TestSwitchGroupFlow(t *testing.T) {
	d := &pinDriver{routingBody: pinFixtureRouting}
	m := newPinTestModel(t, d)
	mm0 := m.(Model)
	if mm0.groups.switchFrom != "proxy" {
		t.Fatalf("switchFrom = %q, want proxy", mm0.groups.switchFrom)
	}

	// Switch to group b from the groups page (left pane, S on the group).
	m2 := driveSwitch(t, m, 1)
	for _, want := range []string{"domain(geosite:gfw) -> b", "fallback: b"} {
		if !strings.Contains(d.lastRouting, want) {
			t.Fatalf("routing after switch missing %q:\n%s", want, d.lastRouting)
		}
	}
	if strings.Contains(strings.ReplaceAll(d.lastRouting, "geoip:us", ""), "-> proxy") {
		t.Fatalf("proxy refs survived the switch:\n%s", d.lastRouting)
	}
	mm2 := m2.(Model)
	if mm2.groups.switchFrom != "b" || mm2.home.switchFrom != "b" {
		t.Fatalf("switchFrom after switch = %q/%q, want b/b",
			mm2.groups.switchFrom, mm2.home.switchFrom)
	}
	// The in-use marker moved with it. The switched routing still reads as
	// the gfw preset (its rule shape), so the preset row keeps its own dot —
	// assert the marker by adjacency instead of by counting.
	home := plain(mm2.home.View(driver.Status{}))
	if !strings.Contains(home, "● b") || strings.Contains(home, "● proxy") {
		t.Fatalf("in-use marker did not move to b:\n%s", home)
	}
	if v := mm2.groups.View(); !strings.Contains(plain(v), "● b") {
		t.Fatalf("groups page left pane missing the in-use marker on b")
	}
}

func TestSwitchGroupDissolvesPin(t *testing.T) {
	d := &pinDriver{routingBody: pinFixtureRouting}
	m := newPinTestModel(t, d)
	m = drivePin(t, m, 0, "n1")

	// While pinned, switchFrom is the managed group, so the switch moves the
	// pinned references straight to b and clears the pin state.
	mm := m.(Model)
	if mm.groups.switchFrom != "pinned" {
		t.Fatalf("switchFrom while pinned = %q, want pinned", mm.groups.switchFrom)
	}
	m2 := driveSwitch(t, m, 1)
	for _, want := range []string{"domain(geosite:gfw) -> b", "fallback: b", "dip(geoip:us) -> b"} {
		if !strings.Contains(d.lastRouting, want) {
			t.Fatalf("routing after pinned switch missing %q:\n%s", want, d.lastRouting)
		}
	}
	if strings.Contains(d.lastRouting, "pinned") {
		t.Fatalf("pinned refs survived the switch:\n%s", d.lastRouting)
	}
	mm2 := m2.(Model)
	if mm2.pin.restore != "" {
		t.Fatalf("pin state survived the switch: %+v", mm2.pin)
	}
	if home := mm2.home.View(driver.Status{Running: true}); strings.Contains(home, "📌") {
		t.Fatalf("pin line survived the switch:\n%s", home)
	}
	// The managed group itself stays (unreferenced, inert).
	found := false
	for _, g := range mm2.groups.groups {
		if g.ID == "pin9" {
			found = true
		}
	}
	if !found {
		t.Fatal("managed group disappeared after the switch")
	}
}

func TestSwitchGroupGuards(t *testing.T) {
	d := &pinDriver{routingBody: pinFixtureRouting}
	m := newPinTestModel(t, d)
	m = drivePin(t, m, 0, "n1")

	// S on the managed group while pinned: it IS the group the routing
	// references, so the already-in-use refusal fires (switching to it is
	// the no-op the guard used to explain).
	mm := m.(Model)
	mm.page = pageTree
	for i, g := range mm.groups.groups {
		if g.ID == "pin9" {
			mm.groups.gi = i
		}
	}
	mm.groups.focus = 0
	m2, cmd := mm.Update(runeKey("S"))
	if msg, ok := cmd().(opDoneMsg); !ok || msg.Err == nil || !strings.Contains(msg.Err.Error(), "已是当前路由使用") {
		t.Fatalf("S on the managed group produced %#v, want the already-in-use toast", cmd())
	}
	if m2.(Model).groups.mode != pickNone {
		t.Fatalf("S opened mode %d on the managed group", m2.(Model).groups.mode)
	}

	// S on an ordinary group while pinned still opens the confirm (switching
	// away is exactly how a pin gets dissolved); esc cancels.
	mm2 := m2.(Model)
	mm2.groups.gi = 0 // proxy, the pin's restore group
	m3, cmd := mm2.Update(runeKey("S"))
	if cmd != nil {
		t.Fatalf("S on a normal group errored: %#v", cmd())
	}
	if m3.(Model).groups.mode != pickSwitch {
		t.Fatal("S on group proxy must open the switch confirm")
	}
	m3b, _ := m3.Update(runeKey("esc"))
	if m3b.(Model).groups.mode != pickNone {
		t.Fatal("esc must close the switch confirm")
	}

	// Unpinned: S on the current group is refused with "already in use".
	d2 := &pinDriver{routingBody: pinFixtureRouting}
	fresh := newPinTestModel(t, d2)
	fm := fresh.(Model)
	fm.page = pageTree
	fm.groups.gi = 0 // proxy = the referenced group
	fm.groups.focus = 0
	f2, cmd := fm.Update(runeKey("S"))
	if msg, ok := cmd().(opDoneMsg); !ok || msg.Err == nil {
		t.Fatalf("S on the current group produced %#v, want an already-in-use toast", cmd())
	}
	if f2.(Model).groups.mode != pickNone {
		t.Fatal("already-in-use S must not open the confirm")
	}

	// Home: S in the group box opens the confirm, esc cancels; S on the
	// managed group is guarded.
	hm := m.(Model)
	hm.page = pageHome
	hm.home.groupFocus = true
	hm.home.groupCursor = 1 // b
	h3, cmd := hm.Update(runeKey("S"))
	if cmd != nil {
		t.Fatalf("home S errored: %#v", cmd())
	}
	hm3 := h3.(Model)
	if hm3.home.confirmGroupTo != "b" || !hm3.anyModal() {
		t.Fatalf("home S did not open the confirm: %+v", hm3.home.confirmGroupTo)
	}
	if ov := hm3.home.overlay(); ov == nil || !strings.Contains(plainLines(ov.lines), "b") {
		t.Fatalf("home switch overlay missing b: %#v", ov)
	}
	_, cmd = h3.Update(runeKey("y"))
	if _, ok := cmd().(switchGroupRequestedMsg); !ok {
		t.Fatalf("home y produced %#v, want switchGroupRequestedMsg", cmd())
	}

	// Home S on the managed group while pinned: same already-in-use refusal
	// (the routing literally references it), no confirm.
	pinIdx := -1
	for i, g := range hm.groups.groups {
		if g.ID == "pin9" {
			pinIdx = i
		}
	}
	hm4 := m.(Model)
	hm4.page = pageHome
	hm4.home.groupFocus = true
	hm4.home.groupCursor = pinIdx
	h5, cmd := hm4.Update(runeKey("S"))
	if msg, ok := cmd().(opDoneMsg); !ok || msg.Err == nil || !strings.Contains(msg.Err.Error(), "已是当前路由使用") {
		t.Fatalf("home S on the managed group produced %#v, want the already-in-use toast", cmd())
	}
	if h5.(Model).home.confirmGroupTo != "" {
		t.Fatal("home S on the managed group must not open the confirm")
	}
}

// pinIndexOf finds the managed group's index in the live group list.
func pinIndexOf(t *testing.T, m Model) int {
	t.Helper()
	for i, g := range m.groups.groups {
		if g.ID == "pin9" {
			return i
		}
	}
	t.Fatal("managed group missing from the group list")
	return -1
}

// Unpinning keeps the managed group with its node; S back to it must re-use
// that node and re-arm the pin record instead of demanding a manual re-pin.
func TestSwitchToPinnedReArmsPin(t *testing.T) {
	d := &pinDriver{routingBody: pinFixtureRouting}
	m := newPinTestModel(t, d)
	m = drivePin(t, m, 0, "n1")
	m = driveUnpin(t, m)

	mm := m.(Model)
	if mm.pin.restore != "" || mm.pin.active {
		t.Fatalf("pin state after unpin = %+v, want inactive", mm.pin)
	}
	if mm.groups.switchFrom != "proxy" || mm.home.switchFrom != "proxy" {
		t.Fatalf("switchFrom after unpin = %q/%q, want proxy/proxy",
			mm.groups.switchFrom, mm.home.switchFrom)
	}

	// Home: S on the managed group arms the re-pin confirm; esc cancels.
	hm := mm
	hm.page = pageHome
	hm.home.groupFocus = true
	hm.home.groupCursor = pinIndexOf(t, hm)
	h2, cmd := hm.Update(runeKey("S"))
	if cmd != nil {
		t.Fatalf("home S on the managed group errored: %#v", cmd())
	}
	hm2 := h2.(Model)
	if hm2.home.confirmGroupTo != "pinned" || !hm2.home.confirmGroupPin || !hm2.anyModal() {
		t.Fatalf("home S did not arm the re-pin confirm: %q pin=%v",
			hm2.home.confirmGroupTo, hm2.home.confirmGroupPin)
	}
	if ov := hm2.home.overlay(); ov == nil || !strings.Contains(plainLines(ov.lines), "复用") {
		t.Fatalf("re-pin overlay missing the re-use note: %#v", ov)
	}
	h3, _ := hm2.Update(runeKey("esc"))
	if h3.(Model).home.confirmGroupTo != "" || h3.(Model).home.confirmGroupPin {
		t.Fatal("esc must close the re-pin confirm")
	}

	// Groups page: the same S runs the full switch and re-arms the pin.
	gm := mm
	gm.page = pageTree
	gm.groups.gi = pinIndexOf(t, gm)
	gm.groups.focus = 0
	g2, cmd := gm.Update(runeKey("S"))
	if cmd != nil {
		t.Fatalf("groups S on the managed group errored: %#v", cmd())
	}
	gm2 := g2.(Model)
	if gm2.groups.mode != pickSwitch {
		t.Fatalf("mode = %d, want pickSwitch", gm2.groups.mode)
	}
	if ov := gm2.groups.overlay(); ov == nil || !strings.Contains(plainLines(ov.lines), "复用") {
		t.Fatalf("re-pin overlay missing the re-use note: %#v", ov)
	}
	g3, cmd := g2.Update(runeKey("y"))
	req, ok := cmd().(switchGroupRequestedMsg)
	if !ok || req.From != "proxy" || req.To != "pinned" {
		t.Fatalf("y produced %#v, want proxy → pinned", cmd())
	}
	g4, cmd := g3.Update(req)
	if cmd == nil {
		t.Fatal("switch request must produce the switch command")
	}
	m2 := runBatch(t, g4, cmd)

	for _, want := range []string{"domain(geosite:gfw) -> pinned", "fallback: pinned", "dip(geoip:us) -> b"} {
		if !strings.Contains(d.lastRouting, want) {
			t.Fatalf("routing after the re-arm switch missing %q:\n%s", want, d.lastRouting)
		}
	}
	if strings.Contains(d.lastRouting, "proxy") {
		t.Fatalf("source-group refs survived the switch:\n%s", d.lastRouting)
	}

	mm2 := m2.(Model)
	if !mm2.pin.active || mm2.pin.restore != "proxy" || mm2.pin.groupID != "pin9" || mm2.pin.node != "东京-01" {
		t.Fatalf("pin state after the re-arm = %+v, want active proxy/东京-01", mm2.pin)
	}
	if home := mm2.home.View(driver.Status{Running: true}); !strings.Contains(home, "📌") || !strings.Contains(home, "东京-01") {
		t.Fatalf("home missing the re-armed pin line:\n%s", home)
	}
	// The re-armed pin answers to x again.
	mm2.page = pageHome
	m3, _ := mm2.Update(runeKey("x"))
	if !m3.(Model).home.confirmUnpin {
		t.Fatal("x must re-arm the unpin confirm after the switch re-armed the pin")
	}
}

// A managed group that lost its only member is the empty-fixed-group reload
// blocker; S to it must refuse and point at f instead of arming the switch.
func TestSwitchToPinnedEmptyRefused(t *testing.T) {
	d := &pinDriver{routingBody: pinFixtureRouting}
	m := newPinTestModel(t, d)
	m = drivePin(t, m, 0, "n1")
	m = driveUnpin(t, m)

	mm := m.(Model)
	emptied := mm.groups.groups
	for i := range emptied {
		if emptied[i].ID == "pin9" {
			emptied[i].Nodes = nil
			emptied[i].Subscriptions = nil
		}
	}
	m2, _ := mm.Update(groupsMsg{Groups: emptied})
	mm2 := m2.(Model)
	mm2.page = pageTree
	mm2.groups.gi = pinIndexOf(t, mm2)
	mm2.groups.focus = 0
	m3, cmd := mm2.Update(runeKey("S"))
	if msg, ok := cmd().(opDoneMsg); !ok || msg.Err == nil || !strings.Contains(msg.Err.Error(), "固定组当前没有节点") {
		t.Fatalf("S on an empty managed group produced %#v, want the empty-group refusal", cmd())
	}
	if m3.(Model).groups.mode != pickNone {
		t.Fatal("S on an empty managed group must not open the confirm")
	}
}
