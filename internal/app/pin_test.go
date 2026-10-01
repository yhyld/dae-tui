package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/config"
	"dae-tui/internal/driver"
)

// pinDriver is a stubDriver whose group and routing state actually mutates
// with the pin flow's mutations, so a whole pin → re-pin → unpin sequence
// can be driven and observed end to end.
type pinDriver struct {
	stubDriver
	routingBody string
	pinnedGroup driver.Group // zero until CreateGroup
	created     bool

	lastRouting  string
	lastPolicyID string
	lastPolicy   driver.Policy
	lastAddIDs   []string
	lastDropIDs  []string

	runCalls int
	runDry   []bool
	runErr   error
}

// Run records the auto/manual reload calls the reload tests assert on.
func (d *pinDriver) Run(_ context.Context, dry bool) error {
	d.runCalls++
	d.runDry = append(d.runDry, dry)
	return d.runErr
}

const pinFixtureRouting = "pname(NetworkManager, systemd-resolved, dnsmasq) -> must_direct\n" +
	"dip(geoip:private) -> direct\ndomain(geosite:cn) -> direct\n" +
	"domain(geosite:gfw) -> proxy\ndip(geoip:us) -> b\nfallback: proxy"

func (d *pinDriver) allNodes() []driver.Node {
	return []driver.Node{
		{ID: "n1", Name: "东京-01", Protocol: "vmess", SubscriptionID: "s1"},
		{ID: "n2", Name: "HK-02", Protocol: "ss"},
		{ID: "n4", Name: "US-01", Protocol: "ss"},
	}
}

func (d *pinDriver) ListGroups(context.Context) ([]driver.Group, error) {
	gs := []driver.Group{
		{ID: "g1", Name: "proxy", Policy: "min_moving_avg",
			Nodes: []driver.Node{{ID: "n2", Name: "HK-02", Protocol: "ss"}},
			Subscriptions: []driver.GroupSubscription{{SubscriptionID: "s1", Tag: "机场A",
				MatchedCount: 1, Nodes: []driver.Node{
					{ID: "n1", Name: "东京-01", Protocol: "vmess", SubscriptionID: "s1"}}}}},
		{ID: "g3", Name: "b", Policy: "min_moving_avg",
			Nodes: []driver.Node{{ID: "n4", Name: "US-01", Protocol: "ss"}}},
	}
	if d.created {
		gs = append(gs, d.pinnedGroup)
	}
	return gs, nil
}

func (d *pinDriver) CreateGroup(_ context.Context, name, policy string) error {
	d.created = true
	d.pinnedGroup = driver.Group{ID: "pin9", Name: name, Policy: policy}
	return nil
}

func (d *pinDriver) AddGroupNodes(_ context.Context, groupID string, ids []string) error {
	d.lastAddIDs = ids
	if groupID != "pin9" {
		return nil
	}
	for _, id := range ids {
		for _, n := range d.allNodes() {
			if n.ID == id {
				d.pinnedGroup.Nodes = append(d.pinnedGroup.Nodes, n)
			}
		}
	}
	return nil
}

func (d *pinDriver) RemoveGroupNodes(_ context.Context, groupID string, ids []string) error {
	d.lastDropIDs = ids
	if groupID != "pin9" {
		return nil
	}
	var keep []driver.Node
	for _, n := range d.pinnedGroup.Nodes {
		drop := false
		for _, id := range ids {
			if id == n.ID {
				drop = true
			}
		}
		if !drop {
			keep = append(keep, n)
		}
	}
	d.pinnedGroup.Nodes = keep
	return nil
}

func (d *pinDriver) SetGroupPolicy(_ context.Context, id string, p driver.Policy) error {
	d.lastPolicyID, d.lastPolicy = id, p
	if id == "pin9" {
		d.pinnedGroup.Policy = p.Name
		if p.Name == "fixed" {
			d.pinnedGroup.PolicyParams = []driver.Param{{Val: "0"}}
		}
	}
	return nil
}

func (d *pinDriver) UpdateRoutingText(_ context.Context, id, text string) error {
	d.lastRouting = text
	d.routingBody = text
	return nil
}

func (d *pinDriver) ListSelections(context.Context) (driver.Selections, error) {
	// References derive from the body (like daed's referenceGroups would),
	// so the in-use markers track what the routing actually points at.
	refs := []string{"must_direct"}
	for _, line := range strings.Split(d.routingBody, "\n") {
		if out, ok := outboundOf(line); ok && !driver.IsBuiltinOutbound(out) {
			refs = append(refs, strings.TrimPrefix(out, "must_"))
		}
	}
	return driver.Selections{Routings: []driver.ConfigItem{{
		ID: "r1", Name: "默认路由", Selected: true, Body: d.routingBody, References: refs}}}, nil
}

// newPinTestModel boots a Model wired to a pinDriver, with its config in a
// temp dir (the pin flow persists there for real).
func newPinTestModel(t *testing.T, d *pinDriver) tea.Model {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := &config.Config{Endpoint: "http://127.0.0.1:2023/graphql"}
	m := New(d, cfg, path)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m2, _ = m2.Update(bootMsg{Users: 1, Status: driver.Status{Version: "v2.1.1", Running: true}})
	gs, _ := d.ListGroups(nil)
	m2, _ = m2.Update(groupsMsg{Groups: gs})
	m2, _ = m2.Update(subsMsg{Subs: mustSubs(t)})
	sel, _ := d.ListSelections(nil)
	m2, _ = m2.Update(selectionsMsg{Sel: sel})
	m2, _ = m2.Update(manualNodesMsg{Nodes: mustManual(t)})
	return m2
}

// drivePin arms and confirms the pin overlay for a node row in the given
// group, locating the row by node ID after opening every section.
func drivePin(t *testing.T, m tea.Model, gi int, nodeID string) tea.Model {
	t.Helper()
	mm := m.(Model)
	mm.page = pageTree
	mm.groups.gi = gi
	mm.groups.collapseSections()
	mm.groups.subOpen["s1"] = true
	mm.groups.directOpen = true
	mm.groups.rebuild()
	rowIdx := -1
	for i, r := range mm.groups.rows {
		if r.kind == rowNode && r.node.ID == nodeID {
			rowIdx = i
			break
		}
	}
	if rowIdx < 0 {
		t.Fatalf("node %s not visible in group %d", nodeID, gi)
	}
	mm.groups.rc = rowIdx
	mm.groups.focus = 1
	m3, cmd := mm.Update(runeKey("f"))
	if cmd != nil {
		t.Fatalf("f returned a command: %#v", cmd())
	}
	mm3 := m3.(Model)
	if mm3.groups.mode != pickPin {
		t.Fatalf("mode = %d, want pickPin", mm3.groups.mode)
	}
	if !mm3.anyModal() {
		t.Fatal("pin confirm must count as a modal")
	}
	if ov := mm3.groups.overlay(); ov == nil || !strings.Contains(plainLines(ov.lines), "东京") && !strings.Contains(plainLines(ov.lines), "US-01") {
		t.Fatalf("pin overlay missing the node name: %#v", ov)
	}
	m4, cmd := m3.Update(runeKey("y"))
	if cmd == nil {
		t.Fatal("y must produce the pin request")
	}
	req, ok := cmd().(pinRequestedMsg)
	if !ok {
		t.Fatalf("y produced %#v, want pinRequestedMsg", cmd())
	}
	m5, cmd := m4.Update(req)
	if cmd == nil {
		t.Fatal("pin request must produce the pin command")
	}
	return runBatch(t, m5, cmd)
}

// driveUnpin confirms home's unpin flow (x → y) and settles its messages.
func driveUnpin(t *testing.T, m tea.Model) tea.Model {
	t.Helper()
	mm := m.(Model)
	mm.page = pageHome
	m1, cmd := mm.Update(runeKey("x"))
	if cmd != nil {
		t.Fatalf("x produced a command: %#v", cmd())
	}
	if !m1.(Model).anyModal() {
		t.Fatal("unpin confirm must count as a modal")
	}
	m2, cmd := m1.Update(runeKey("y"))
	req, ok := cmd().(unpinRequestedMsg)
	if !ok {
		t.Fatalf("y produced %#v, want unpinRequestedMsg", cmd())
	}
	m3, cmd := m2.Update(req)
	if cmd == nil {
		t.Fatal("unpin request must produce the unpin command")
	}
	return runBatch(t, m3, cmd)
}

// runBatch executes a tea.BatchMsg and feeds every resulting message back.
func runBatch(t *testing.T, m tea.Model, cmd tea.Cmd) tea.Model {
	t.Helper()
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd produced %#v, want tea.BatchMsg", cmd())
	}
	for _, c := range batch {
		if msg := c(); msg != nil {
			m, _ = m.Update(msg)
		}
	}
	return m
}

func plainLines(lines []string) string {
	var out strings.Builder
	for _, l := range lines {
		out.WriteString(l)
		out.WriteByte('\n')
	}
	return out.String()
}

func TestRewriteOutboundRefs(t *testing.T) {
	raw := "# comment stays\n" +
		"pname(x) -> must_direct\n" +
		"dip(geoip:private) -> direct\n" +
		"dip(geoip:cn) -> direct\n" +
		"domain(geosite:gfw) -> proxy\n" +
		"  fallback: proxy\n"

	out, n, skipped := rewriteOutboundRefs(raw, "proxy", "pinned")
	if skipped != 0 {
		t.Fatalf("commented refs = %d, want 0 on a clean fixture", skipped)
	}
	if n != 2 {
		t.Fatalf("rewrote %d refs, want 2", n)
	}
	for _, want := range []string{"domain(geosite:gfw) -> pinned", "  fallback: pinned", "-> direct", "must_direct"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rewritten DSL missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "-> proxy") {
		t.Fatalf("proxy refs survived:\n%s", out)
	}
	// Round-trip back must restore the original text exactly.
	back, n, _ := rewriteOutboundRefs(out, "pinned", "proxy")
	if n != 2 || back != raw {
		t.Fatalf("round-trip mismatch (n=%d):\n%s", n, back)
	}

	// A group name that only appears inside conditions is never touched.
	if _, n, _ := rewriteOutboundRefs(raw, "cn", "pinned"); n != 0 {
		t.Fatalf("condition-only name matched %d refs", n)
	}
	// Builtin outbounds are refused on either side.
	if _, n, _ := rewriteOutboundRefs(raw, "direct", "pinned"); n != 0 {
		t.Fatalf("builtin from-side matched %d refs", n)
	}
	if _, n, _ := rewriteOutboundRefs(raw, "proxy", "direct"); n != 0 {
		t.Fatalf("builtin to-side matched %d refs", n)
	}

	// The must_ modifier travels with the reference.
	must := "dip(1.2.3.4) -> must_proxy\n"
	out, n, _ = rewriteOutboundRefs(must, "proxy", "pinned")
	if n != 1 || !strings.Contains(out, "must_pinned") {
		t.Fatalf("must_ variant not rewritten: n=%d %q", n, out)
	}

	if got, skippedNow := countOutboundRefs(raw, "proxy"); got != 2 || skippedNow != 0 {
		t.Fatalf("countOutboundRefs = (%d, %d), want (2, 0)", got, skippedNow)
	}
	if got, _ := countOutboundRefs(raw, "cn"); got != 0 {
		t.Fatalf("countOutboundRefs(condition decoy) = %d, want 0", got)
	}
}

// TestCommentedRefsAreReportedNotSilentlySkipped guards the honest-count
// contract: a reference glued to a trailing comment ("proxy # keep") never
// compares equal, so it is left alone. Folding it into the rewritten count
// would report a pin that moved every reference while some traffic stayed
// on the old group — the two numbers must stay distinguishable, and the
// confirm overlay shows the skipped one.
func TestCommentedRefsAreReportedNotSilentlySkipped(t *testing.T) {
	raw := "# a full-line comment -> proxy\n" +
		"domain(geosite:gfw) -> proxy\n" +
		"  fallback: proxy # keep local\n" +
		"dip(1.2.3.4) -> must_proxy # pinned too\n"

	n, skipped := countOutboundRefs(raw, "proxy")
	if n != 1 {
		t.Fatalf("rewriteable refs = %d, want 1", n)
	}
	if skipped != 2 {
		t.Fatalf("commented refs = %d, want 2", skipped)
	}

	out, n2, skipped2 := rewriteOutboundRefs(raw, "proxy", "pinned")
	if n2 != 1 || skipped2 != 2 {
		t.Fatalf("rewrite = (%d, %d), want (1, 2)", n2, skipped2)
	}
	// The rewriteable line moved; the commented ones did not.
	if !strings.Contains(out, "domain(geosite:gfw) -> pinned") {
		t.Fatalf("rewriteable ref not rewritten:\n%s", out)
	}
	for _, keep := range []string{"fallback: proxy # keep local", "must_proxy # pinned too"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("commented line should be untouched, missing %q:\n%s", keep, out)
		}
	}

	// A name that merely starts with the group is not a commented match
	// (otherwise "proxyx # c" would count as a skipped proxy reference).
	if _, _, sk := rewriteOutboundRefs("dip(1.0.0.1) -> proxyx # c\n", "proxy", "pinned"); sk != 0 {
		t.Fatalf("longer name counted as commented: skipped=%d", sk)
	}
}

func TestPinFlowWithStub(t *testing.T) {
	d := &pinDriver{routingBody: pinFixtureRouting}
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	cfg := &config.Config{Endpoint: "http://x"}
	m := New(d, cfg, cfgPath)
	m1, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m1, _ = m1.Update(bootMsg{Users: 1, Status: driver.Status{Version: "v", Running: true}})
	gs, _ := d.ListGroups(nil)
	m1, _ = m1.Update(groupsMsg{Groups: gs})
	m1, _ = m1.Update(selectionsMsg{Sel: mustSel(t)})
	m1, _ = m1.Update(manualNodesMsg{Nodes: mustManual(t)})

	// Pin 东京-01 (a subscription node, by ID) from group proxy. rows after
	// expanding s1: [sub, n1, direct-header, n2].
	m2 := drivePin(t, m1, 0, "n1")

	if cfg.Pin.GroupID != "pin9" || cfg.Pin.Restore != "proxy" {
		t.Fatalf("cfg.Pin = %+v, want group pin9 restoring proxy", cfg.Pin)
	}
	for _, want := range []string{"domain(geosite:gfw) -> pinned", "fallback: pinned", "dip(geoip:us) -> b"} {
		if !strings.Contains(d.lastRouting, want) {
			t.Fatalf("routing after pin missing %q:\n%s", want, d.lastRouting)
		}
	}
	if d.lastPolicyID != "pin9" || d.lastPolicy.Name != "fixed" || d.lastPolicy.FixedIndex != 0 {
		t.Fatalf("policy call = %s %+v", d.lastPolicyID, d.lastPolicy)
	}
	if len(d.lastAddIDs) != 1 || d.lastAddIDs[0] != "n1" {
		t.Fatalf("attach ids = %v, want [n1]", d.lastAddIDs)
	}
	mm := m2.(Model)
	if !mm.pin.active || mm.pin.node != "东京-01" || mm.pin.nodeID != "n1" {
		t.Fatalf("pin state = %+v", mm.pin)
	}
	if home := mm.home.View(driver.Status{Running: true}); !strings.Contains(home, "📌") || !strings.Contains(home, "东京-01") {
		t.Fatalf("home missing the pin status line:\n%s", home)
	}

	// Re-pin from another group: the previous pin's refs must go back to
	// proxy first, then b's single ref moves to the pinned group.
	// rows in group b: [direct-header, n4].
	m3 := drivePin(t, m2, 1, "n4")
	if cfg.Pin.Restore != "b" {
		t.Fatalf("restore after re-pin = %q, want b", cfg.Pin.Restore)
	}
	for _, want := range []string{"domain(geosite:gfw) -> proxy", "fallback: proxy", "dip(geoip:us) -> pinned"} {
		if !strings.Contains(d.lastRouting, want) {
			t.Fatalf("routing after re-pin missing %q:\n%s", want, d.lastRouting)
		}
	}
	if len(d.lastDropIDs) != 1 || d.lastDropIDs[0] != "n1" {
		t.Fatalf("swap drop ids = %v, want [n1]", d.lastDropIDs)
	}

	// Unpin from home: refs go back to b, the group and node survive.
	m7 := driveUnpin(t, m3)

	if cfg.Pin.Restore != "" || cfg.Pin.GroupID != "pin9" {
		t.Fatalf("cfg.Pin after unpin = %+v, want group kept + empty restore", cfg.Pin)
	}
	for _, want := range []string{"dip(geoip:us) -> b", "domain(geosite:gfw) -> proxy"} {
		if !strings.Contains(d.lastRouting, want) {
			t.Fatalf("routing after unpin missing %q:\n%s", want, d.lastRouting)
		}
	}
	if strings.Contains(d.lastRouting, "pinned") {
		t.Fatalf("pinned refs survived unpin:\n%s", d.lastRouting)
	}
	mm7 := m7.(Model)
	if mm7.pin.restore != "" || mm7.pin.active {
		t.Fatalf("pin state after unpin = %+v", mm7.pin)
	}
	if home := mm7.home.View(driver.Status{Running: true}); strings.Contains(home, "📌") {
		t.Fatalf("pin line survived unpin:\n%s", home)
	}
}

func TestPinGuardsManagedGroup(t *testing.T) {
	d := &pinDriver{routingBody: pinFixtureRouting}
	m := newPinTestModel(t, d)
	m = drivePin(t, m, 0, "n1")

	// The managed group refuses destructive entry points with a toast, not
	// by opening the confirm mode.
	for _, key := range []string{"D", "R", "p", "s", "n"} {
		mm := m.(Model)
		mm.page = pageTree
		for i, g := range mm.groups.groups {
			if g.ID == "pin9" {
				mm.groups.gi = i
			}
		}
		mm.groups.focus = 0
		m2, cmd := mm.Update(runeKey(key))
		if cmd == nil {
			t.Fatalf("%s on the managed group produced no command", key)
		}
		if msg, ok := cmd().(opDoneMsg); !ok || msg.Err == nil {
			t.Fatalf("%s produced %#v, want a guard toast", key, cmd())
		}
		if m2.(Model).groups.mode != pickNone {
			t.Fatalf("%s opened mode %d on the managed group", key, m2.(Model).groups.mode)
		}
	}

	// The nodes page's G picker hides the managed group entirely.
	mm := m.(Model)
	mm.page = pageNodes
	mm.nodes.mode = 3
	if lines := plainLines(mm.nodes.modalLines()); strings.Contains(lines, "pinned") {
		t.Fatalf("G picker lists the managed group:\n%s", lines)
	}
}

func TestPinStaleAndEmptyStates(t *testing.T) {
	d := &pinDriver{routingBody: pinFixtureRouting}
	m := newPinTestModel(t, d)
	m = drivePin(t, m, 0, "n1")

	// Stale: the selected routing stops referencing the managed group (the
	// user switched schemes or hand-edited the DSL). The pin must read as
	// 失效, and unpin degrades to clearing the state without touching the DSL.
	mm := m.(Model)
	stale := driver.Selections{Routings: []driver.ConfigItem{{
		ID: "r1", Name: "默认路由", Selected: true,
		Body:       "fallback: proxy",
		References: []string{"proxy", "must_direct"}}}}
	m2, _ := mm.Update(selectionsMsg{Sel: stale})
	mm2 := m2.(Model)
	if mm2.pin.active {
		t.Fatal("pin still active after the routing stopped referencing it")
	}
	if home := mm2.home.View(driver.Status{Running: true}); !strings.Contains(home, "固定已失效") {
		t.Fatalf("home missing the stale-pin line:\n%s", home)
	}

	// Empty: the managed group lost its member (subscription deleted). The
	// pin line must flag the reload blocker — the routing still references
	// the group, so References keep it active.
	empty := mm2.groups.groups
	for i := range empty {
		if empty[i].ID == "pin9" {
			empty[i].Nodes = nil
			empty[i].Subscriptions = nil
		}
	}
	m3, _ := mm2.Update(groupsMsg{Groups: empty})
	mm3 := m3.(Model)
	m4, _ := mm3.Update(selectionsMsg{Sel: driver.Selections{Routings: []driver.ConfigItem{{
		ID: "r1", Name: "默认路由", Selected: true, Body: "fallback: pinned",
		References: []string{"pinned", "must_direct"}}}}})
	mm4 := m4.(Model)
	if !mm4.pin.active || !mm4.pin.empty {
		t.Fatalf("pin state = %+v, want active+empty", mm4.pin)
	}
	if home := mm4.home.View(driver.Status{Running: true}); !strings.Contains(home, "固定组已空") {
		t.Fatalf("home missing the empty-group warning:\n%s", home)
	}
}

func TestHomeCycleGroupSkipsPinned(t *testing.T) {
	d := &pinDriver{routingBody: pinFixtureRouting}
	m := newPinTestModel(t, d)
	m = drivePin(t, m, 0, "n1")

	mm := m.(Model)
	for i := 0; i < 2*len(mm.home.groups)+3; i++ {
		mm.home.cycleGroup()
		if g := mm.home.proxyGroup(); g == mm.home.managedName() {
			t.Fatalf("g cycling landed on the managed group %q", g)
		}
	}
}
