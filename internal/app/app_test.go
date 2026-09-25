package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/config"
	"dae-tui/internal/driver"
)

// stubDriver feeds the UI canned data; all mutations succeed.
type stubDriver struct {
	driver.Driver // embed for forward compatibility
}

func (stubDriver) Name() string { return "stub" }
func (stubDriver) Capabilities() driver.Caps {
	return driver.Caps{SwitchNode: true, TestLatency: true, TrafficStats: true, Subscriptions: true, ConfigMgmt: true}
}
func (stubDriver) Connect(context.Context) (driver.Status, error) {
	return driver.Status{Version: "v2.1.1", Running: true, UpdatedAt: time.Now()}, nil
}
func (stubDriver) NumberUsers(context.Context) (int, error) { return 1, nil }
func (stubDriver) Login(_ context.Context, u, p string) (driver.Status, error) {
	return driver.Status{}, errors.New("bad credentials")
}
func (stubDriver) CreateUser(_ context.Context, u, p string) (driver.Status, error) {
	return driver.Status{}, nil
}
func (stubDriver) ListGroups(context.Context) ([]driver.Group, error) {
	return []driver.Group{
		{
			ID: "g1", Name: "proxy", Policy: "min_moving_avg",
			// Group.nodes = directly-attached nodes only (daed v2).
			Nodes: []driver.Node{
				{ID: "n2", Name: "HK-02", Protocol: "ss"},
				{ID: "n3", Name: "SG-03", Protocol: "trojan", Tag: "机场A", SubscriptionID: "s1"},
			},
			Subscriptions: []driver.GroupSubscription{{
				SubscriptionID: "s1", Tag: "机场A", MatchedCount: 2,
				Nodes: []driver.Node{
					{ID: "n1", Name: "东京-01", Protocol: "vmess", Tag: "机场A", SubscriptionID: "s1"},
					{ID: "n3", Name: "SG-03", Protocol: "trojan", Tag: "机场A", SubscriptionID: "s1"},
				},
			}},
		},
		{ID: "g2", Name: "direct", Policy: "min_moving_avg"},
	}, nil
}
func (stubDriver) SetGroupPolicy(_ context.Context, id string, p driver.Policy) error { return nil }
func (stubDriver) SubscriptionNodes(_ context.Context, subID string) ([]driver.Node, error) {
	if subID == "s1" {
		return []driver.Node{
			{ID: "x1", Name: "机场A-01", Protocol: "vmess", Tag: "机场A", SubscriptionID: "s1"},
			{ID: "x2", Name: "机场A-02", Protocol: "ss", Tag: "机场A", SubscriptionID: "s1"},
		}, nil
	}
	return nil, nil
}
func (stubDriver) CreateGroup(_ context.Context, name string, policy string) error { return nil }
func (stubDriver) RemoveGroup(_ context.Context, groupID string) error             { return nil }
func (stubDriver) RenameGroup(_ context.Context, groupID, name string) error       { return nil }
func (stubDriver) ImportNode(_ context.Context, link, tag string) error            { return nil }
func (stubDriver) ImportNodes(_ context.Context, links []string, tag string) ([]driver.NodeImportResult, error) {
	out := make([]driver.NodeImportResult, 0, len(links))
	for i, l := range links {
		r := driver.NodeImportResult{Link: l}
		if strings.Contains(l, "bad") {
			r.Error = "unsupported protocol"
			out = append(out, r)
			continue
		}
		n := driver.Node{ID: fmt.Sprintf("imp%d", i), Name: "导入-" + fmt.Sprint(i+1),
			Protocol: "vmess", Link: l, Tag: tag}
		r.Node = &n
		out = append(out, r)
	}
	return out, nil
}
func (stubDriver) RemoveNodes(_ context.Context, nodeIDs []string) error { return nil }
func (stubDriver) TagNode(_ context.Context, id, tag string) error       { return nil }
func (stubDriver) UpdateNode(_ context.Context, id, newLink string) error {
	return nil
}
func (stubDriver) AddGroupSubscriptions(_ context.Context, groupID string, subIDs []string, filter string) error {
	return nil
}
func (stubDriver) RemoveGroupSubscriptions(_ context.Context, groupID string, subIDs []string) error {
	return nil
}

// lastAddNodeIDs records what AddGroupNodes was last asked to attach, so a
// batch attach can be told from a single one.
var lastAddNodeIDs []string

func (stubDriver) AddGroupNodes(_ context.Context, groupID string, nodeIDs []string) error {
	lastAddNodeIDs = append([]string(nil), nodeIDs...)
	return nil
}
func (stubDriver) RemoveGroupNodes(_ context.Context, groupID string, nodeIDs []string) error {
	return nil
}
func (stubDriver) ListManualNodes(context.Context) ([]driver.Node, error) {
	return []driver.Node{
		{ID: "m1", Name: "自建-HK", Protocol: "vmess"},
		{ID: "m2", Name: "自建-SG", Protocol: "trojan"},
	}, nil
}

// lastTestIDs records what the stub was last asked to probe, so tests can
// tell a whole-group test (every member) from a direct-nodes-only one.
var lastTestIDs []string

func (stubDriver) TestLatency(_ context.Context, ids []string) error {
	lastTestIDs = append([]string(nil), ids...)
	return nil
}

// lastLatencyIDs records the node IDs the stub was last asked latencies
// for, so a scoped poll can be told from a full-instance one.
var lastLatencyIDs []string

func (stubDriver) Latencies(_ context.Context, ids []string) ([]driver.Latency, error) {
	lastLatencyIDs = append([]string(nil), ids...)
	return []driver.Latency{
		{NodeID: "n1", Ms: 88, Alive: true, TestedAt: time.Now()},
		{NodeID: "n2", Ms: 420, Alive: true, TestedAt: time.Now()},
		{NodeID: "n3", Alive: false, TestedAt: time.Now(), Message: "timeout"},
	}, nil
}
func (stubDriver) Traffic(_ context.Context, w, mp int) (driver.TrafficSnapshot, error) {
	series := make([]float64, 40)
	for i := range series {
		series[i] = float64(i * 1000)
	}
	return driver.TrafficSnapshot{
		UpRate: 12_345, DownRate: 2_000_000, UpTotal: 1 << 30, DownTotal: 42 << 30,
		Conns: 15, UDPSessions: 3, UpSeries: series, DownSeries: series, UpdatedAt: time.Now(),
	}, nil
}
func (stubDriver) ListSubscriptions(context.Context) ([]driver.Subscription, error) {
	return []driver.Subscription{
		{ID: "s1", Tag: "机场A", Link: "https://a.example/sub", Status: "updated", NodeCount: 24, CronExp: "0 */6 * * *", CronEnable: true, UpdatedAt: time.Now()},
		{ID: "s2", Tag: "机场B", Link: "https://b.example/sub", Status: "failed", Info: "HTTP 503", NodeCount: 0, UpdatedAt: time.Now().Add(-time.Hour)},
	}, nil
}
func (stubDriver) AddSubscription(_ context.Context, link, tag string) error { return nil }
func (stubDriver) UpdateSubscriptionCron(_ context.Context, id, cronExp string, enable bool) error {
	return nil
}
func (stubDriver) CreateProfile(_ context.Context, section, name string, src *driver.ConfigItem) error {
	return nil
}
func (stubDriver) UpdateConfigFields(_ context.Context, id string, fields []driver.ConfigField) error {
	return nil
}
func (stubDriver) RenameProfile(_ context.Context, section, id, name string) error {
	return nil
}
func (stubDriver) RemoveProfile(_ context.Context, section, id string) error {
	return nil
}
func (stubDriver) UpdateDnsText(_ context.Context, id, text string) error {
	return nil
}
func (stubDriver) UpdateRoutingText(_ context.Context, id, text string) error {
	return nil
}
func (stubDriver) ValidateDns(_ context.Context, raw string) error     { return nil }
func (stubDriver) ValidateRouting(_ context.Context, raw string) error { return nil }
func (stubDriver) RoutingPresets() []driver.RoutingPreset {
	return []driver.RoutingPreset{
		{ID: "gfw", Group: true}, {ID: "nonCn", Group: true},
		{ID: "cnOnly", Group: true}, {ID: "global", Group: true},
	}
}
func (stubDriver) BuildRoutingPreset(id, proxyGroup string) (string, error) {
	if proxyGroup == "" {
		return "", errors.New("no proxy group")
	}
	rules := map[string]string{
		"gfw":    "domain(geosite:gfw) -> " + proxyGroup + "\nfallback: direct",
		"nonCn":  "dip(geoip:cn) -> direct\ndomain(geosite:cn) -> direct\nfallback: " + proxyGroup,
		"cnOnly": "dip(geoip:cn) -> " + proxyGroup + "\ndomain(geosite:cn) -> " + proxyGroup + "\nfallback: direct",
		"global": "fallback: " + proxyGroup,
	}[id]
	if rules == "" {
		return "", fmt.Errorf("unknown preset %q", id)
	}
	return "pname(NetworkManager, systemd-resolved, dnsmasq) -> must_direct\n" +
		"dip(geoip:private) -> direct\n" + rules, nil
}
func (stubDriver) DetectRoutingPreset(raw string) string {
	switch {
	case strings.Contains(raw, "geosite:gfw"):
		return "gfw"
	case strings.Contains(raw, "fallback: proxy"):
		return "nonCn"
	}
	return ""
}
func (stubDriver) UpdateConfigField(_ context.Context, id string, field driver.ConfigField, value string) error {
	return nil
}
func (stubDriver) UpdateSubscription(_ context.Context, id string) error     { return nil }
func (stubDriver) RemoveSubscriptions(_ context.Context, ids []string) error { return nil }
func (stubDriver) TagSubscription(_ context.Context, id, tag string) error   { return nil }
func (stubDriver) UpdateSubscriptionLink(_ context.Context, id, link string) error {
	return nil
}
func (stubDriver) Interfaces(context.Context) ([]driver.NetworkInterface, error) {
	return []driver.NetworkInterface{
		{Name: "eth0", Up: true, Default: true, Gateway: "192.168.1.1", IPs: []string{"192.168.1.5"}},
		{Name: "wlan0", Up: false, IPs: []string{}},
	}, nil
}
func (stubDriver) UpdatePassword(_ context.Context, currentPassword, newPassword string) error {
	return nil
}
func (stubDriver) Logout(context.Context) error { return nil }
func (stubDriver) ListSelections(context.Context) (driver.Selections, error) {
	return driver.Selections{
		Configs: []driver.ConfigItem{{ID: "c1", Name: "默认", Selected: true, Detail: "log=info",
			Fields: []driver.ConfigField{
				{Name: "logLevel", Label: "日志级别", Value: "info", Type: "string",
					Default: "info", Desc: "Log level: error, warn, info, debug, trace."},
				{Name: "checkInterval", Label: "检查间隔", Value: "30s", Type: "duration", Default: "30s"},
				{Name: "lanInterface", Value: "eth0", Type: "array"},
			}}},
		Dns: []driver.ConfigItem{
			{ID: "d1", Name: "默认DNS", Selected: true, Body: "upstream {}",
				Summary: []string{"alidns: udp://223.5.5.5:53"}},
			{ID: "d2", Name: "备用DNS", Selected: false, Body: "upstream {}"},
		},
		Routings: []driver.ConfigItem{{ID: "r1", Name: "默认路由", Selected: true,
			Body: "pname(NetworkManager, systemd-resolved, dnsmasq) -> must_direct\n" +
				"dip(geoip:private) -> direct\ndip(geoip:cn) -> direct\n" +
				"domain(geosite:cn) -> direct\nfallback: proxy",
			Summary: []string{"a(domain: example.com) -> proxy", "fallback: direct"},
			// daed's referenceGroups mixes group names with dae built-ins.
			References: []string{"proxy", "must_direct"}}},
	}, nil
}
func (stubDriver) SelectConfig(_ context.Context, id string) error  { return nil }
func (stubDriver) SelectDns(_ context.Context, id string) error     { return nil }
func (stubDriver) SelectRouting(_ context.Context, id string) error { return nil }
func (stubDriver) Run(ctx context.Context, dry bool) error          { return nil }

// rejectingDriver fails DSL syntax validation, like a daed that cannot parse
// what $EDITOR left behind.
type rejectingDriver struct{ stubDriver }

func (rejectingDriver) ValidateDns(_ context.Context, raw string) error {
	return errors.New("line 1:24 upstream {{{\n                              ^: mismatched input '{' expecting '}'")
}
func (rejectingDriver) ValidateRouting(_ context.Context, raw string) error { return nil }

// presetRejectingDriver fails validation of a rendered preset, as a daed
// would for a proxy group name that cannot be written as DSL.
type presetRejectingDriver struct{ stubDriver }

func (presetRejectingDriver) ValidateRouting(_ context.Context, raw string) error {
	return errors.New("line 4:10 fallback: bad group\n                  ^: no viable alternative")
}

func newTestModel(t *testing.T) tea.Model {
	t.Helper()
	return newTestModelWith(t, stubDriver{})
}

func newTestModelWith(t *testing.T, drv driver.Driver) tea.Model {
	t.Helper()
	cfg := &config.Config{Endpoint: "http://127.0.0.1:2023/graphql"}
	m := New(drv, cfg, "/tmp/dae-tui-test.toml")
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m2, _ = m2.Update(bootMsg{Users: 1, Status: driver.Status{Version: "v2.1.1", Running: true}})
	m2, _ = m2.Update(groupsMsg{Groups: mustGroups(t)})
	m2, _ = m2.Update(subsMsg{Subs: mustSubs(t)})
	m2, _ = m2.Update(selectionsMsg{Sel: mustSel(t)})
	m2, _ = m2.Update(trafficMsg{Snap: mustTraffic(t)})
	m2, _ = m2.Update(manualNodesMsg{Nodes: mustManual(t)})
	m2, _ = m2.Update(latenciesMsg{Lats: mustLats(t)})
	return m2
}

func mustLats(t *testing.T) []driver.Latency {
	t.Helper()
	l, _ := stubDriver{}.Latencies(nil, nil)
	return l
}

func mustManual(t *testing.T) []driver.Node {
	t.Helper()
	ns, _ := stubDriver{}.ListManualNodes(nil)
	return ns
}

func mustGroups(t *testing.T) []driver.Group {
	t.Helper()
	g, _ := stubDriver{}.ListGroups(nil)
	return g
}
func mustSubs(t *testing.T) []driver.Subscription {
	t.Helper()
	s, _ := stubDriver{}.ListSubscriptions(nil)
	return s
}
func mustSel(t *testing.T) driver.Selections {
	t.Helper()
	s, _ := stubDriver{}.ListSelections(nil)
	return s
}
func mustTraffic(t *testing.T) driver.TrafficSnapshot {
	t.Helper()
	s, _ := stubDriver{}.Traffic(nil, 10, 60)
	return s
}

func key(s string) tea.KeyMsg {
	if s == "enter" {
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	if s == "tab" {
		return tea.KeyMsg{Type: tea.KeyTab}
	}
	if s == "shift+tab" {
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	}
	if s == "space" {
		// What a terminal actually sends: KeyMsg.String() is " " for it,
		// never the literal "space".
		return tea.KeyMsg{Type: tea.KeySpace}
	}
	if s == "up" {
		return tea.KeyMsg{Type: tea.KeyUp}
	}
	if s == "down" {
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	if s == "esc" {
		return tea.KeyMsg{Type: tea.KeyEscape}
	}
	if s == "backspace" {
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestRenderAllPages(t *testing.T) {
	m := newTestModel(t)
	want := map[string]string{
		"1": "各组当前节点",
		"2": "路由组",
		"3": "订阅",
		"4": "手动节点",
		"5": "配置方案",
		"?": "帮助",
	}
	for _, pageKey := range []string{"1", "2", "3", "4", "5", "?"} {
		mm, _ := m.Update(key(pageKey))
		m = mm
		v := m.View()
		if !strings.Contains(v, "dae-tui") {
			t.Fatalf("page %q view missing title:\n%s", pageKey, v)
		}
		if !strings.Contains(v, want[pageKey]) {
			t.Fatalf("page %q missing %q:\n%s", pageKey, want[pageKey], v)
		}
	}
}

func TestHomePageSwitchAndCurrentNodes(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	for _, want := range []string{"代理运行中", "各组当前节点", "≈ 东京-01", "direct"} {
		if !strings.Contains(v, want) {
			t.Fatalf("home missing %q:\n%s", want, v)
		}
	}
	// o asks for confirmation, y fires the toggle, esc cancels.
	m, _ = m.Update(key("o"))
	if v := m.View(); !strings.Contains(v, "确认停止代理") {
		t.Fatalf("switch confirmation missing:\n%s", v)
	}
	m, cmd := m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y should fire runToggleCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	m, _ = m.Update(key("o"))
	m, _ = m.Update(key("n")) // decline
	if v := m.View(); strings.Contains(v, "确认") {
		t.Fatalf("confirmation should be gone:\n%s", v)
	}
}

func TestGroupsTreeNavigationAndSwitch(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("2"))
	v := m.View()
	// Collapsed by default: only group names on the left, no nodes.
	for _, want := range []string{"proxy", "direct"} {
		if !strings.Contains(v, want) {
			t.Fatalf("left pane missing group %q:\n%s", want, v)
		}
	}
	for _, hidden := range []string{"东京-01", "订阅 机场A"} {
		if strings.Contains(v, hidden) {
			t.Fatalf("detail should be collapsed by default, found %q:\n%s", hidden, v)
		}
	}

	// Expand the group: right pane shows section HEADERS only (collapsed).
	m, _ = m.Update(key("l"))
	v = m.View()
	for _, want := range []string{"订阅 机场A", "直接添加的节点"} {
		if !strings.Contains(v, want) {
			t.Fatalf("right pane missing header %q:\n%s", want, v)
		}
	}
	for _, hidden := range []string{"东京-01", "HK-02"} {
		if strings.Contains(v, hidden) {
			t.Fatalf("section should be collapsed, found %q:\n%s", hidden, v)
		}
	}

	// The cursor must be visible on the sub header itself.
	if !strings.Contains(v, "❯") {
		t.Fatalf("cursor missing on section header:\n%s", v)
	}

	// Enter on the sub header expands it; nodes become visible.
	m, _ = m.Update(key("enter"))
	v = m.View()
	for _, want := range []string{"东京-01", "SG-03"} {
		if !strings.Contains(v, want) {
			t.Fatalf("subscription nodes missing after expand:\n%s", v)
		}
	}
	// Enter again collapses it.
	m, _ = m.Update(key("enter"))
	if v := m.View(); strings.Contains(v, "东京-01") {
		t.Fatalf("subscription section should collapse:\n%s", v)
	}
	m, _ = m.Update(key("enter")) // reopen for the rest of the test

	// Enter on a node row must not pin: the fixed-node flow was removed
	// (a daed v2 fixed group allows exactly one member, so pinning rebuilt
	// the whole group). No confirmation, no mutation.
	m, _ = m.Update(key("j"))
	m, cmd := m.Update(key("enter"))
	if cmd != nil {
		t.Fatal("enter on a node row should not fire a cmd")
	}
	if v := m.View(); strings.Contains(v, "固定到节点") {
		t.Fatalf("pin confirmation should be gone:\n%s", v)
	}

	// T still tests the single node under the cursor.
	m, cmd = m.Update(key("T"))
	if cmd == nil {
		t.Fatal("T on node row should fire a test")
	}
	m, cmd = m.Update(key("a"))
	if cmd == nil {
		t.Fatal("a should fire switchNodeCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	// t with focus on right and cursor on the sub row tests matched nodes.
	m, _ = m.Update(key("g"))
	m, cmd = m.Update(key("t"))
	if cmd == nil {
		t.Fatal("t on sub row should fire a test")
	}

	// h collapses back to the group list.
	m, _ = m.Update(key("h"))
	if v := m.View(); strings.Contains(v, "东京-01") {
		t.Fatalf("detail should be collapsed again:\n%s", v)
	}
}

func TestGroupAttachDetachFlows(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("2"))
	// s on the left group opens the picker in the right pane (s2 unattached).
	m, cmd := m.Update(key("s"))
	if v := m.View(); !strings.Contains(v, "机场B") {
		t.Fatalf("picker should list unattached subscriptions:\n%s", v)
	}
	if cmd != nil {
		t.Fatal("s should not fire a network cmd directly")
	}
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("picker enter should fire groupMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}

	// n opens the node picker (manual + subscription nodes) after loading.
	m, cmd = m.Update(key("n"))
	if cmd == nil {
		t.Fatal("n should fire loadAttachCandidatesCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg) // attachCandidatesMsg
	}
	v := m.View()
	for _, want := range []string{"自建-HK", "机场A-01", "·机场A"} {
		if !strings.Contains(v, want) {
			t.Fatalf("node picker missing %q:\n%s", want, v)
		}
	}
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("picker enter should fire groupMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}

	// x on a direct node: expand the group, move to the direct header
	// (row 1), open it, then move onto its first node.
	m, _ = m.Update(key("l"))
	m, _ = m.Update(key("g"))
	m, _ = m.Update(key("j"))     // direct header
	m, _ = m.Update(key("enter")) // open direct section
	m, _ = m.Update(key("j"))     // first direct node (HK-02)
	m, _ = m.Update(key("x"))
	if v := m.View(); !strings.Contains(v, "从组中移除") {
		t.Fatalf("node removal confirmation missing:\n%s", v)
	}
	m, cmd = m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y should fire groupMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}

	// Back to the sub row: x asks for subscription detach.
	m, _ = m.Update(key("g")) // rc=0 → sub row
	m, _ = m.Update(key("x"))
	if v := m.View(); !strings.Contains(v, "确认将订阅") {
		t.Fatalf("detach confirmation missing:\n%s", v)
	}
	m, cmd = m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y should fire groupMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
}

func TestSubsDetailPaneAndModal(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("3"))
	// Expanding fires the node fetch for the selected subscription.
	m, cmd := m.Update(key("l"))
	if cmd == nil {
		t.Fatal("expanding subscription should fetch its nodes")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg) // subNodesMsg
	}
	v := m.View()
	for _, want := range []string{"标签 机场A", "机场A-01", "机场A-02", "状态 updated"} {
		if !strings.Contains(v, want) {
			t.Fatalf("sub detail missing %q:\n%s", want, v)
		}
	}
	// t tests the subscription's nodes.
	m, cmd = m.Update(key("t"))
	if cmd == nil {
		t.Fatal("t in detail pane should fire a test")
	}
	// Back to left; add form; delete confirm.
	m, _ = m.Update(key("h"))
	m, _ = m.Update(key("n")) // add form
	m, _ = m.Update(key("s")) // typing into link input
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("add submit should return a cmd")
	}
	m, _ = m.Update(key("x")) // delete confirm
	m, _ = m.Update(key("n")) // decline
	if v := m.View(); v == "" {
		t.Fatal("subs view empty")
	}
}

func TestSubsEditTagAndLink(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("3"))

	// e opens the edit form prefilled with the subscription's tag and link.
	m, cmd := m.Update(key("e"))
	if cmd == nil {
		t.Fatal("e should focus the edit form (blink)")
	}
	v := m.View()
	if !strings.Contains(v, "编辑订阅") || !strings.Contains(v, "机场A") ||
		!strings.Contains(v, "https://a.example/sub") {
		t.Fatalf("edit form missing or not prefilled:\n%s", v)
	}
	m, _ = m.Update(key("esc"))

	// Submitting fires the edit mutation (kind 4) and reloads the list.
	m, _ = m.Update(key("e"))
	m, _ = m.Update(key("x")) // type into the tag field
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("edit submit should fire subMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	if v := m.View(); strings.Contains(v, "编辑订阅") {
		t.Fatalf("edit form should be closed:\n%s", v)
	}
}

func TestGlobalApplyWorksFromAnyPage(t *testing.T) {
	m := newTestModel(t)
	// On the groups page, A opens the global confirmation; y fires runCmd.
	m, _ = m.Update(key("2"))
	m, _ = m.Update(key("A"))
	if v := m.View(); !strings.Contains(v, "确认应用当前选中") {
		t.Fatalf("global apply confirmation missing:\n%s", v)
	}
	m, cmd := m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y should fire runCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg) // opDoneMsg
	}
	// Decline path on the subs page.
	m, _ = m.Update(key("3"))
	m, _ = m.Update(key("A"))
	m, _ = m.Update(key("n"))
	if v := m.View(); strings.Contains(v, "确认应用") {
		t.Fatalf("confirmation should be gone:\n%s", v)
	}
}

// t from the group list must probe every member of the group. daed v2 keeps
// subscription-contributed nodes out of Group.Nodes, so testing only the
// direct nodes would silently skip most of the group.
func TestGroupsWholeGroupTestCoversAllMembers(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("2"))
	lastTestIDs = nil

	m, cmd := m.Update(key("t"))
	if cmd == nil {
		t.Fatal("t on the group list should fire a test")
	}
	cmd() // run the cmd against the stub driver
	// g1: 订阅贡献 n1,n3 + 直接挂载 n2,n3 → 3 distinct members.
	if len(lastTestIDs) != 3 {
		t.Fatalf("whole-group test covered %d nodes (%v), want 3", len(lastTestIDs), lastTestIDs)
	}
}

func TestGroupLifecycleManagement(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("2"))

	// c creates a group via an input modal.
	m, cmd := m.Update(key("c"))
	if v := m.View(); !strings.Contains(v, "创建群组") {
		t.Fatalf("create modal missing:\n%s", v)
	}
	_ = cmd
	m, _ = m.Update(key("n"))
	m, _ = m.Update(key("e"))
	m, _ = m.Update(key("w"))
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("create enter should fire groupMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}

	// R renames.
	m, cmd = m.Update(key("R"))
	if v := m.View(); !strings.Contains(v, "重命名群组") {
		t.Fatalf("rename modal missing:\n%s", v)
	}
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("rename enter should fire groupMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}

	// D asks for confirmation, y fires removal.
	m, _ = m.Update(key("D"))
	if v := m.View(); !strings.Contains(v, "确认删除群组") {
		t.Fatalf("delete confirmation missing:\n%s", v)
	}
	m, cmd = m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y should fire groupMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}

	// p opens the policy picker; enter applies.
	m, _ = m.Update(key("p"))
	if v := m.View(); !strings.Contains(v, "选择群组策略") {
		t.Fatalf("policy picker missing:\n%s", v)
	}
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("policy enter should fire groupMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
}

func TestGroupReferenceWarnings(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("2"))

	// The collapsed detail names the routing profiles referencing the group.
	if v := m.View(); !strings.Contains(v, "引用") || !strings.Contains(v, "默认路由") {
		t.Fatalf("reference note missing:\n%s", v)
	}

	// R shows the warning inside the rename modal.
	m, _ = m.Update(key("R"))
	if v := m.View(); !strings.Contains(v, "静默失效") {
		t.Fatalf("rename reference warning missing:\n%s", v)
	}
	m, _ = m.Update(key("esc"))

	// D shows it in the delete confirmation.
	m, _ = m.Update(key("D"))
	if v := m.View(); !strings.Contains(v, "静默失效") {
		t.Fatalf("delete reference warning missing:\n%s", v)
	}
	m, _ = m.Update(key("n"))

	// A group no routing references gets no warning.
	m, _ = m.Update(key("j")) // direct
	if v := m.View(); strings.Contains(v, "静默失效") {
		t.Fatalf("unreferenced group should not warn:\n%s", v)
	}

	// The configs page lists the referenced groups of a routing profile;
	// dae built-ins are labeled, never flagged as missing.
	m, _ = m.Update(key("5"))
	m, _ = m.Update(key("G")) // last row: the routing profile
	v := m.View()
	if !strings.Contains(v, "引用组") || !strings.Contains(v, "proxy") {
		t.Fatalf("routing references missing on configs page:\n%s", v)
	}
	if !strings.Contains(v, "must_direct (内置)") {
		t.Fatalf("built-in outbound not labeled:\n%s", v)
	}
	if strings.Contains(v, "已不存在") {
		t.Fatalf("built-in outbound flagged as missing:\n%s", v)
	}
}

func TestManualNodesPage(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("4"))
	v := m.View()
	if !strings.Contains(v, "自建-HK") || !strings.Contains(v, "自建-SG") {
		t.Fatalf("manual nodes missing:\n%s", v)
	}

	// Detail pane.
	m, _ = m.Update(key("tab"))
	if v := m.View(); !strings.Contains(v, "名称") {
		t.Fatalf("node detail missing:\n%s", v)
	}
	// G opens the group picker; enter attaches to the first group.
	m, cmd := m.Update(key("G"))
	if v := m.View(); !strings.Contains(v, "选择要加入的群组") {
		t.Fatalf("group picker missing:\n%s", v)
	}
	if cmd != nil {
		t.Fatal("G should not fire a cmd directly")
	}
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("picker enter should fire groupMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}

	// a opens the import form; a pasted batch submits from the tag field.
	m, _ = m.Update(key("h"))
	m, _ = m.Update(key("a"))
	if v := m.View(); !strings.Contains(v, "导入手动节点") {
		t.Fatalf("import form missing:\n%s", v)
	}
	m, _ = m.Update(key("s"))
	m, _ = m.Update(key("s"))
	m, _ = m.Update(key(":"))
	m, _ = m.Update(key("/"))
	m, _ = m.Update(key("/"))
	m, _ = m.Update(key("1"))
	m, _ = m.Update(key("enter")) // newline inside the link box
	m, _ = m.Update(key("s"))
	m, _ = m.Update(key("s"))
	m, _ = m.Update(key(":"))
	m, _ = m.Update(key("/"))
	m, _ = m.Update(key("/"))
	m, _ = m.Update(key("2"))
	m, _ = m.Update(key("enter"))
	m, _ = m.Update(key("s"))
	m, _ = m.Update(key("s"))
	m, _ = m.Update(key(":"))
	m, _ = m.Update(key("/"))
	m, _ = m.Update(key("/"))
	m, _ = m.Update(key("b"))
	m, _ = m.Update(key("a"))
	m, _ = m.Update(key("d"))
	m, cmd = m.Update(key("tab")) // move to the tag field
	if cmd == nil {
		t.Fatal("tab should refocus (blink)")
	}
	m, cmd = m.Update(key("enter")) // submit from the tag field
	if cmd == nil {
		t.Fatal("import submit should fire nodeMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg) // importDoneMsg
	}
	// The batch report names the failed link in the detail pane.
	if v := m.View(); !strings.Contains(v, "上次导入") || !strings.Contains(v, "2 成功 / 1 失败") {
		t.Fatalf("batch import report missing:\n%s", v)
	}
	m, _ = m.Update(key("x"))
	if v := m.View(); !strings.Contains(v, "确认删除节点") {
		t.Fatalf("delete confirm missing:\n%s", v)
	}
	m, cmd = m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y should fire nodeMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
}

func TestManualNodeEditKeepsID(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("4"))

	// e opens the edit form prefilled with the node's tag and link.
	m, cmd := m.Update(key("e"))
	if cmd == nil {
		t.Fatal("e should focus the edit form (blink)")
	}
	v := m.View()
	if !strings.Contains(v, "编辑手动节点") || !strings.Contains(v, "自建-HK") {
		t.Fatalf("edit form missing:\n%s", v)
	}
	m, _ = m.Update(key("esc"))

	// Also reachable from the detail pane.
	m, _ = m.Update(key("tab"))
	m, cmd = m.Update(key("e"))
	if cmd == nil {
		t.Fatal("e in detail pane should open the edit form")
	}
	if v := m.View(); !strings.Contains(v, "标签") || !strings.Contains(v, "链接") {
		t.Fatalf("edit fields missing:\n%s", v)
	}
	// Change the tag, then submit: nodeMutateCmd fires (kind 2 edit).
	m, _ = m.Update(key("x"))
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("edit submit should fire nodeMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	// The edit refreshes both the node list and the groups.
	if v := m.View(); strings.Contains(v, "编辑手动节点") {
		t.Fatalf("edit form should be closed:\n%s", v)
	}
}

func TestHomeAccountMenuAndLogout(t *testing.T) {
	cfg := &config.Config{Endpoint: "http://127.0.0.1:2023/graphql", Username: "admin"}
	var m tea.Model = New(stubDriver{}, cfg, "/tmp/dae-tui-test.toml")
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m2, _ = m2.Update(bootMsg{Users: 1, Status: driver.Status{Version: "v2.1.1", Running: true}})
	m2, _ = m2.Update(groupsMsg{Groups: mustGroups(t)})
	m2, _ = m2.Update(selectionsMsg{Sel: mustSel(t)})
	m = m2

	// P opens the account menu; global hotkeys must not leak through it.
	m, _ = m.Update(key("P"))
	v := m.View()
	for _, want := range []string{"账户", "admin", "修改密码", "退出登录"} {
		if !strings.Contains(v, want) {
			t.Fatalf("account menu missing %q:\n%s", want, v)
		}
	}
	m, _ = m.Update(key("2")) // swallowed while the menu is open
	if v := m.View(); !strings.Contains(v, "账户") {
		t.Fatalf("page hotkey leaked into the account menu:\n%s", v)
	}

	// Enter opens the password form; submitting fires passwordCmd.
	m, _ = m.Update(key("enter"))
	if v := m.View(); !strings.Contains(v, "当前密码") || !strings.Contains(v, "确认新密码") {
		t.Fatalf("password form missing:\n%s", v)
	}
	for _, k := range []string{"o", "l", "d"} {
		m, _ = m.Update(key(k))
	}
	m, _ = m.Update(key("tab"))
	for _, k := range []string{"n", "e", "w", "p", "w", "1"} {
		m, _ = m.Update(key(k))
	}
	m, _ = m.Update(key("tab"))
	for _, k := range []string{"n", "e", "w", "p", "w", "1"} {
		m, _ = m.Update(key(k))
	}
	m, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("password submit should fire passwordCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	if v := m.View(); !strings.Contains(v, "✓ 修改密码") {
		t.Fatalf("password success toast missing:\n%s", v)
	}

	// Logout: menu → second entry → confirm → y returns to the login form.
	m, _ = m.Update(key("P"))
	m, _ = m.Update(key("j"))
	m, _ = m.Update(key("enter"))
	if v := m.View(); !strings.Contains(v, "确认退出登录") {
		t.Fatalf("logout confirmation missing:\n%s", v)
	}
	m, cmd = m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y should fire the logout command")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg) // logoutMsg
	}
	if v := m.View(); !strings.Contains(v, "登录 daed") {
		t.Fatalf("expected the login form after logout:\n%s", v)
	}
}

func TestSubsCronEdit(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("3"))
	m, _ = m.Update(key("c")) // open cron editor
	v := m.View()
	if !strings.Contains(v, "定时刷新") {
		t.Fatalf("cron modal missing:\n%s", v)
	}
	if !strings.Contains(v, "0 */6 * * *") {
		t.Fatalf("cron modal should show the current expression:\n%s", v)
	}
	// type an expression, tab to the toggle, flip it, submit.
	m, _ = m.Update(key("0"))
	m, _ = m.Update(key(" "))
	m, _ = m.Update(key("tab"))
	// The stub subscription starts with cronEnable=true, so the toggle line
	// reads 启用 before the keypress.
	if v := m.View(); !strings.Contains(v, "启用") || strings.Contains(v, "停用") {
		t.Fatalf("toggle should start enabled:\n%s", v)
	}
	m, cmd := m.Update(key("space"))
	if cmd != nil {
		t.Fatal("space on the toggle should not fire a cmd")
	}
	if v := m.View(); !strings.Contains(v, "停用") {
		t.Fatalf("space did not toggle the enable flag:\n%s", v)
	}
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("cron submit should fire subMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
}

func TestEditorArgvResolution(t *testing.T) {
	// VISUAL wins over EDITOR, and arguments in the value must stay separate
	// argv entries: quoting the whole value made the shell look for one
	// binary literally named "omarchy-launch-editor --inline" (exit 127).
	t.Setenv("VISUAL", "omarchy-launch-editor --inline")
	t.Setenv("EDITOR", "vim")
	argv, err := editorArgv()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv, "|") != "omarchy-launch-editor|--inline" {
		t.Fatalf("argv = %v", argv)
	}
	if p, err := exec.LookPath(argv[0]); err != nil {
		t.Fatalf("editor %q is not an executable: %v", argv[0], err)
	} else {
		t.Logf("resolves to %s", p)
	}

	// A blank value counts as unset.
	t.Setenv("VISUAL", "   ")
	t.Setenv("EDITOR", "nvim -u NONE")
	argv, err = editorArgv()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv, "|") != "nvim|-u|NONE" {
		t.Fatalf("argv = %v", argv)
	}

	// With neither set, the fallback must be a real executable.
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	argv, err = editorArgv()
	if err != nil {
		t.Skipf("no editor installed here: %v", err)
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		t.Fatalf("fallback editor %q is not executable: %v", argv[0], err)
	}
}

// The command must put the binary in argv[0] and the rest of $EDITOR plus the
// file path behind it. Treating the whole value as one program name is what
// produced "exit status 127".
func TestEditorCmdSplitsArguments(t *testing.T) {
	t.Setenv("VISUAL", "omarchy-launch-editor --inline")
	t.Setenv("EDITOR", "vim")
	c, name, err := editorCmd("/tmp/dae-tui-test.dns")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Args, "|"); got != "omarchy-launch-editor|--inline|/tmp/dae-tui-test.dns" {
		t.Fatalf("args = %q", got)
	}
	if c.Err != nil {
		t.Fatalf("command not runnable: %v", c.Err)
	}
	if name != "omarchy-launch-editor --inline" {
		t.Fatalf("display name = %q", name)
	}

	// No editor at all must be an error, not a bogus command.
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", "") // hide every fallback candidate
	if _, _, err := editorCmd("/tmp/x"); err == nil {
		t.Fatal("expected an error when no editor can be found")
	}
}

func TestConfigsDeleteProtection(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("5"))
	// Cursor on c1 which is the selected config → D is refused with a toast.
	m, cmd := m.Update(key("D"))
	if cmd == nil {
		t.Fatal("D on the selected profile should fire an error toast")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	if v := m.View(); !strings.Contains(v, "不可删除") {
		t.Fatalf("protection toast missing:\n%s", v)
	}
}

func TestConfigsProfileManagement(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("5"))

	// create in the dns section: two j's put the cursor on the DNS header.
	m, _ = m.Update(key("j")) // c1
	m, _ = m.Update(key("j")) // DNS 分区标题
	m, cmd := m.Update(key("c"))
	if v := m.View(); !strings.Contains(v, "新建") {
		t.Fatalf("create modal missing:\n%s", v)
	}
	// The hint must describe what CreateProfile really does: clone the
	// selected profile of the section (the stub's selected dns has a body),
	// not "created from the default template".
	if v := m.View(); !strings.Contains(v, "复制当前选中") || strings.Contains(v, "默认模板") {
		t.Fatalf("create hint should say it clones the selected profile:\n%s", v)
	}
	_ = cmd
	m, _ = m.Update(key("n"))
	m, _ = m.Update(key("2"))
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("create enter should fire profileMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}

	// only one cursor marker in the left pane
	if n := strings.Count(m.View(), "❯"); n != 1 {
		t.Fatalf("expected exactly one cursor marker, got %d:\n%s", n, m.View())
	}

	// rename (still on the DNS header → the section's selected profile).
	m, cmd = m.Update(key("R"))
	if v := m.View(); !strings.Contains(v, "重命名") {
		t.Fatalf("rename modal missing:\n%s", v)
	}
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("rename enter should fire profileMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}

	// delete with confirmation (the reloads leave the cursor on the DNS
	// header; two j's move past the selected 默认DNS to 备用DNS).
	m, _ = m.Update(key("j"))
	m, _ = m.Update(key("j"))
	m, _ = m.Update(key("D"))
	if v := m.View(); !strings.Contains(v, "确认删除") {
		t.Fatalf("delete confirm missing:\n%s", v)
	}
	m, cmd = m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y should fire profileMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
}

func TestHomeNetworkStateWarnsMissingInterface(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.Update(ifacesMsg{Ifaces: []driver.NetworkInterface{
		{Name: "eth0", Up: true, Default: true, Gateway: "192.168.1.1", IPs: []string{"192.168.1.5"}},
		{Name: "wlan0", Up: false},
	}})
	m = m2
	v := m.View()
	for _, want := range []string{"网络", "eth0", "192.168.1.5", "默认路由"} {
		if !strings.Contains(v, want) {
			t.Fatalf("home network state missing %q:\n%s", want, v)
		}
	}

	// A selected config binding to a NIC that no longer exists is flagged on
	// the home page and in the config detail.
	sel := mustSel(t)
	sel.Configs[0].Fields = append(sel.Configs[0].Fields,
		driver.ConfigField{Name: "wanInterface", Value: "ppp0", Type: "array"})
	m2, _ = m.Update(selectionsMsg{Sel: sel})
	m = m2
	if v := m.View(); !strings.Contains(v, "ppp0 不存在") {
		t.Fatalf("missing-interface warning missing on home:\n%s", v)
	}
	m2, _ = m.Update(key("5"))
	m = m2
	if v := m.View(); !strings.Contains(v, "ppp0 不存在") {
		t.Fatalf("missing-interface warning missing on configs page:\n%s", v)
	}

	// "auto" is dae's "detect it yourself" placeholder, not a NIC name: it
	// must never be flagged, alone or mixed with real names.
	sel = mustSel(t)
	for i := range sel.Configs[0].Fields {
		if sel.Configs[0].Fields[i].Name == "lanInterface" {
			sel.Configs[0].Fields[i].Value = "auto"
		}
	}
	sel.Configs[0].Fields = append(sel.Configs[0].Fields,
		driver.ConfigField{Name: "wanInterface", Value: "auto, eth0", Type: "array"})
	m2, _ = m.Update(selectionsMsg{Sel: sel})
	m = m2
	if v := m.View(); strings.Contains(v, "不存在") {
		t.Fatalf("auto must not be reported as a missing NIC:\n%s", v)
	}
	m2, _ = m.Update(key("5"))
	m = m2
	if v := m.View(); strings.Contains(v, "不存在") {
		t.Fatalf("auto must not be reported on the configs page:\n%s", v)
	}
}

func TestConfigsFieldEdit(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("5"))
	m, cmd := m.Update(key("e")) // field picker on the config row
	if v := m.View(); !strings.Contains(v, "选择要修改的字段") {
		t.Fatalf("field picker missing:\n%s", v)
	}
	if cmd != nil {
		t.Fatal("e on config should not fire a cmd directly")
	}
	// The cursor row shows the backend default next to the current value.
	if v := m.View(); !strings.Contains(v, "默认 info") {
		t.Fatalf("field picker should show the default of the cursor row:\n%s", v)
	}
	m, _ = m.Update(key("enter")) // first field (日志级别, preferred order)
	if v := m.View(); !strings.Contains(v, "新值") {
		t.Fatalf("field input missing:\n%s", v)
	}
	// The edit modal carries the backend documentation for the field.
	if v := m.View(); !strings.Contains(v, "Log level") {
		t.Fatalf("field input should show the field description:\n%s", v)
	}
	m, _ = m.Update(key("d"))
	m, _ = m.Update(key("e"))
	m, _ = m.Update(key("b"))
	m, _ = m.Update(key("u"))
	m, _ = m.Update(key("g"))
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("field enter should fire configFieldCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
}

// A field without a Chinese label (a backend key this build does not know)
// must still be reachable, falling back to the raw key.
// The field picker windows its rows: a config exposes every global field
// (31 on daed v2.1.1), far more than fit on screen, and the cursor must
// stay visible while scrolling.
func TestConfigsFieldPickerWindows(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("5"))
	// Grow the stub's config to a realistic field count.
	mm := m.(Model)
	var fields []driver.ConfigField
	for _, name := range []string{
		"tproxyPort", "tproxyPortProtect", "soMarkFromDae", "soMarkFromDaeSet", "logLevel",
		"tcpCheckUrl", "tcpCheckHttpMethod", "udpCheckDns", "checkInterval", "checkTolerance",
		"lanInterface", "wanInterface", "allowInsecure", "dialMode", "disableWaitingNetwork",
		"enableLocalTcpFastRedirect", "autoConfigKernelParameter", "autoConfigFirewallRule",
		"sniffingTimeout", "tlsImplementation", "utlsImitate", "tlsFragment", "tlsFragmentLength",
		"tlsFragmentInterval", "pprofPort", "mptcp", "bootstrapResolver", "fallbackResolver",
		"bandwidthMaxTx", "bandwidthMaxRx", "udphopInterval",
	} {
		fields = append(fields, driver.ConfigField{Name: name, Value: "v", Type: "string"})
	}
	mm.configs.sel.Configs[0].Fields = fields
	m = mm

	m, _ = m.Update(key("e"))
	v := m.View()
	if !strings.Contains(v, "(31)") {
		t.Fatalf("picker should report the field count:\n%s", v)
	}
	if !strings.Contains(v, "1-") || !strings.Contains(v, "/ 31") {
		t.Fatalf("picker should show the window position:\n%s", v)
	}
	// Preferred fields lead, so 日志级别 is on the first screen.
	if !strings.Contains(v, "日志级别") {
		t.Fatalf("preferred field should lead the picker:\n%s", v)
	}
	// Walk to the bottom; the window follows and the cursor stays visible.
	for i := 0; i < 40; i++ {
		m, _ = m.Update(key("j"))
	}
	v = m.View()
	if !strings.Contains(v, "❯") {
		t.Fatalf("cursor must stay visible after scrolling:\n%s", v)
	}
	if !strings.Contains(v, "UDP 跳变间隔") { // last preferred field
		t.Fatalf("scrolled window should reach the tail fields:\n%s", v)
	}
	if !strings.Contains(v, "/ 31，j/k 滚动") {
		t.Fatalf("scroll hint missing:\n%s", v)
	}
}

// A field without a Chinese label (a backend key this build does not know)
// must still be reachable, falling back to the raw key.
func TestConfigsFieldFallbackLabel(t *testing.T) {
	if got := fieldLabel(driver.ConfigField{Name: "someNewField"}); got != "someNewField" {
		t.Fatalf("fallback label = %q", got)
	}
	if got := fieldLabel(driver.ConfigField{Name: "someNewField", Label: "后端标签"}); got != "后端标签" {
		t.Fatalf("backend label = %q", got)
	}
	if got := fieldLabel(driver.ConfigField{Name: "logLevel"}); got != "日志级别" {
		t.Fatalf("known label = %q", got)
	}
	// Preferred fields lead, the rest keep backend order.
	fields := []driver.ConfigField{
		{Name: "zzzLast"}, {Name: "logLevel"}, {Name: "mptcp"}, {Name: "aaaFirst"},
	}
	ordered := orderedFields(fields)
	want := []string{"logLevel", "mptcp", "zzzLast", "aaaFirst"}
	for i, w := range want {
		if ordered[i].Name != w {
			t.Fatalf("ordered[%d] = %q, want %q (full: %+v)", i, ordered[i].Name, w, ordered)
		}
	}
}

// The routing/dns detail pane defaults to the raw DSL (what `e` edits); `v`
// switches to the backend's parsed structure. Showing both at once would
// print every rule twice for an already-canonical DSL.
func TestConfigsRoutingSummaryToggle(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("5"))
	// Row order: 全局配置, c1, DNS, d1, d2, 路由规则, r1 — G jumps to the last
	// row, the routing profile.
	m, _ = m.Update(key("G"))
	m, _ = m.Update(key("tab"))
	v := m.View()
	if !strings.Contains(v, "路由规则 (DSL 原文)") {
		t.Fatalf("raw DSL should be the default view:\n%s", v)
	}
	if strings.Contains(v, "结构概览") {
		t.Fatalf("summary must not stack on top of the DSL:\n%s", v)
	}
	// v switches to the parsed structure.
	m, _ = m.Update(key("v"))
	v = m.View()
	for _, want := range []string{"路由规则 (结构概览)", "a(domain: example.com) -> proxy", "fallback: direct"} {
		if !strings.Contains(v, want) {
			t.Fatalf("summary view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "DSL 原文") {
		t.Fatalf("summary view should replace the DSL, not join it:\n%s", v)
	}
	// v switches back.
	m, _ = m.Update(key("v"))
	if v := m.View(); !strings.Contains(v, "DSL 原文") || strings.Contains(v, "结构概览") {
		t.Fatalf("v should toggle back to the raw DSL:\n%s", v)
	}
	// The dns profile summarizes its upstreams the same way.
	m, _ = m.Update(key("h"))
	m, _ = m.Update(key("g"))
	for i := 0; i < 3; i++ {
		m, _ = m.Update(key("j")) // 全局配置 → c1 → DNS → d1
	}
	m, _ = m.Update(key("tab"))
	m, _ = m.Update(key("v"))
	if v := m.View(); !strings.Contains(v, "alidns: udp://223.5.5.5:53") {
		t.Fatalf("dns summary missing:\n%s", v)
	}
}

func TestConfigsEditorDoneSubmitsChange(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("5"))
	// Simulate $EDITOR having written new content for the dns profile.
	tmp := filepath.Join(t.TempDir(), "x.dns")
	os.WriteFile(tmp, []byte("upstream { alidns: 'udp://223.5.5.5:53' }"), 0o600)
	mm, cmd := m.Update(editorDoneMsg{Path: tmp, Section: "dns", ID: "d1", Old: "upstream {}"})
	m = mm
	if cmd == nil {
		t.Fatal("changed content should fire the syntax validation")
	}
	// The temp file must survive until the backend accepted the text.
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("edited file removed before validation: %v", err)
	}
	msg := cmd() // editorValidatedMsg (stub accepts everything)
	valMsg, ok := msg.(editorValidatedMsg)
	if !ok {
		t.Fatalf("msg = %T, want editorValidatedMsg", msg)
	}
	if valMsg.Err != nil {
		t.Fatalf("stub validation should pass: %v", valMsg.Err)
	}
	// A valid edit is shown as a diff and waits for confirmation instead of
	// submitting directly.
	m, cmd = m.Update(msg)
	if cmd != nil {
		t.Fatal("a validated edit should wait for the diff confirmation, not submit")
	}
	v := m.View()
	for _, want := range []string{"确认应用更改", "upstream {}", "alidns"} {
		if !strings.Contains(v, want) {
			t.Fatalf("diff confirm missing %q:\n%s", want, v)
		}
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("edited file removed before confirmation: %v", err)
	}
	// y applies the edit and finally removes the temp file.
	m, cmd = m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y should fire configTextCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("edited file should be removed after a successful submit")
	}

	// Declining the diff keeps the edit on disk and submits nothing.
	tmp2 := filepath.Join(t.TempDir(), "z.dns")
	os.WriteFile(tmp2, []byte("upstream { alidns: 'udp://223.5.5.5:53' }"), 0o600)
	m, cmd = m.Update(editorDoneMsg{Path: tmp2, Section: "dns", ID: "d1", Old: "upstream {}"})
	if cmd == nil {
		t.Fatal("changed content should fire the syntax validation")
	}
	m, _ = m.Update(cmd())
	m, cmd = m.Update(key("n"))
	if cmd == nil {
		t.Fatal("declining should at least surface a toast")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	if _, err := os.Stat(tmp2); err != nil {
		t.Fatalf("a declined edit must stay on disk for recovery: %v", err)
	}

	// Unchanged content must not fire.
	tmp3 := filepath.Join(t.TempDir(), "y.dns")
	os.WriteFile(tmp3, []byte("  fallback: direct  "), 0o600)
	m2, cmd2 := m.Update(editorDoneMsg{Path: tmp3, Section: "routing", ID: "r1", Old: "fallback: direct"})
	_ = m2
	if cmd2 != nil {
		t.Fatal("unchanged content should not fire a cmd")
	}
}

// A DSL the backend cannot parse must never reach updateDns/updateRouting:
// the edit is rejected, its error shown in the detail pane, and the edited
// file kept so the work is recoverable.
func TestConfigsValidationRejectsBrokenDsl(t *testing.T) {
	m := newTestModelWith(t, rejectingDriver{})
	m, _ = m.Update(key("5"))
	// Row order: 全局配置, c1, DNS, d1, d2, 路由规则, r1 — three j's put the
	// cursor on d1, the profile that gets edited.
	for i := 0; i < 3; i++ {
		m, _ = m.Update(key("j"))
	}
	// A short path (t.TempDir embeds the test name) so the assertion can
	// check the whole path survives the detail pane.
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("dae-tui-test-%d.dns", time.Now().UnixNano()))
	os.WriteFile(tmp, []byte("upstream {{{"), 0o600)
	defer os.Remove(tmp)
	mm, cmd := m.Update(editorDoneMsg{Path: tmp, Section: "dns", ID: "d1", Old: "upstream {}"})
	m = mm
	if cmd == nil {
		t.Fatal("changed content should fire the syntax validation")
	}
	msg := cmd() // editorValidatedMsg with the parser error
	valMsg, ok := msg.(editorValidatedMsg)
	if !ok || valMsg.Err == nil {
		t.Fatalf("msg = %+v, want a validation error", msg)
	}
	m, cmd = m.Update(msg)
	if cmd == nil {
		t.Fatal("rejection should surface a toast")
	}
	toast, ok := cmd().(opDoneMsg)
	if !ok || toast.Err == nil {
		t.Fatalf("expected an error toast, got %+v", toast)
	}
	m, _ = m.Update(toast)
	v := m.View()
	for _, want := range []string{"校验未通过，未保存", tmp, "mismatched input"} {
		if !strings.Contains(v, want) {
			t.Fatalf("rejection missing %q:\n%s", want, v)
		}
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("rejected edit must stay on disk: %v", err)
	}
}

func TestConfigsDetailPaneAndActions(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("5"))
	// Right pane shows the selected config's body.
	m, _ = m.Update(key("tab"))
	v := m.View()
	if !strings.Contains(v, "全局配置") {
		t.Fatalf("config body missing:\n%s", v)
	}
	// j scrolls the body.
	m, _ = m.Update(key("j"))
	// Back to left; select, dry-run, apply.
	m, _ = m.Update(key("tab"))
	m, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("select should return a cmd")
	}
	m, _ = m.Update(key("A"))
	m, cmd = m.Update(key("y"))
	if cmd == nil {
		t.Fatal("apply confirm should fire runCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	if v := m.View(); !strings.Contains(v, "配置方案") {
		t.Fatalf("configs view broken:\n%s", v)
	}
}

func TestLoginErrorPath(t *testing.T) {
	cfg := &config.Config{Endpoint: "http://x"}
	m := New(stubDriver{}, cfg, "/tmp/dae-tui-test.toml")
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m2, _ = m2.Update(bootMsg{Users: 1}) // no auth → login phase
	if v := m2.View(); !strings.Contains(v, "登录") {
		t.Fatalf("expected login view:\n%s", v)
	}
	m2, _ = m2.Update(key("a"))
	m2, _ = m2.Update(key("b"))
	m2, _ = m2.Update(key("tab"))
	m2, _ = m2.Update(key("c"))
	m2, cmd := m2.Update(key("enter"))
	if cmd == nil {
		t.Fatal("login submit should fire a cmd")
	}
	msg := cmd() // authMsg with error (stub rejects)
	m2, _ = m2.Update(msg)
	if v := m2.View(); !strings.Contains(v, "✗") {
		t.Fatalf("login error not displayed:\n%s", v)
	}
}

func TestSetupPhaseRenders(t *testing.T) {
	cfg := &config.Config{Endpoint: "http://x"}
	m := New(stubDriver{}, cfg, "/tmp/dae-tui-test.toml")
	m2, _ := m.Update(bootMsg{Users: 0})
	if v := m2.View(); !strings.Contains(v, "初始化") {
		t.Fatalf("expected setup view:\n%s", v)
	}
}

func TestFatalAndRetry(t *testing.T) {
	cfg := &config.Config{Endpoint: "http://x"}
	m := New(stubDriver{}, cfg, "/tmp/dae-tui-test.toml")
	m2, _ := m.Update(bootMsg{ConnErr: errors.New("connection refused")})
	if v := m2.View(); !strings.Contains(v, "无法连接") {
		t.Fatalf("expected fatal view:\n%s", v)
	}
	m2, _ = m2.Update(key("r")) // retry re-boots
	if v := m2.View(); !strings.Contains(v, "正在连接") && !strings.Contains(v, "无法连接") {
		t.Fatalf("unexpected view after retry:\n%s", v)
	}
}

// The home page's routing quick-switch: presets are listed, the current one
// is detected, and confirming shows the exact DSL before it replaces the
// selected routing profile.
func TestHomeRoutingPresetSwitch(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	for _, want := range []string{
		"路由快速切换", "默认路由", "代理组: proxy",
		"GFW 模式", "中国列表以外", "中国列表", "全局代理",
		"当前: 中国列表以外", // the stub routing matches the nonCn preset
	} {
		if !strings.Contains(v, want) {
			t.Fatalf("home preset section missing %q:\n%s", want, v)
		}
	}

	// j/k moves the preset cursor; enter opens the confirmation showing the
	// exact DSL y would write.
	m, _ = m.Update(key("j"))
	m, _ = m.Update(key("j")) // cursor on 中国列表
	m, cmd := m.Update(key("enter"))
	if cmd != nil {
		t.Fatal("enter should not fire a network cmd before confirmation")
	}
	v = m.View()
	for _, want := range []string{
		"替换为「中国列表」", "dip(geoip:cn) -> proxy",
		"domain(geosite:cn) -> proxy", "fallback: direct",
	} {
		if !strings.Contains(v, want) {
			t.Fatalf("confirmation missing %q:\n%s", want, v)
		}
	}
	// Global hotkeys must not fire while the confirmation is open.
	m, _ = m.Update(key("2"))
	if mm, _ := m.Update(key("A")); mm.(Model).page != pageHome {
		t.Fatal("page switched while a confirmation was open")
	}
	if v := m.View(); strings.Contains(v, "确认应用当前选中") {
		t.Fatalf("A fired while the preset confirmation was open:\n%s", v)
	}

	// g cycles the proxy group and re-renders the preview to match.
	m, _ = m.Update(key("g"))
	v = m.View()
	if !strings.Contains(v, "代理组: direct") {
		t.Fatalf("g should cycle the proxy group:\n%s", v)
	}
	if !strings.Contains(v, "dip(geoip:cn) -> direct") {
		t.Fatalf("preview should follow the new group:\n%s", v)
	}
	m, _ = m.Update(key("g")) // back to proxy

	// n cancels.
	m, _ = m.Update(key("n"))
	if v := m.View(); strings.Contains(v, "替换为") {
		t.Fatalf("confirmation should be gone:\n%s", v)
	}

	// y validates, then submits: presetValidatedMsg → configTextCmd.
	m, _ = m.Update(key("enter"))
	m, cmd = m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y should fire presetTextCmd")
	}
	msg, ok := cmd().(presetValidatedMsg)
	if !ok {
		t.Fatalf("msg = %T, want presetValidatedMsg", msg)
	}
	if msg.Err != nil || msg.Section != "routing" || msg.ID != "r1" {
		t.Fatalf("validated msg = %+v", msg)
	}
	m, cmd = m.Update(msg)
	if cmd == nil {
		t.Fatal("validated preset should fire configTextCmd")
	}
	if done := cmd(); done != nil {
		m, _ = m.Update(done) // selectionsMsg reload
	}
	// After the reload the mode is detected again (the stub's canned
	// detector still reports nonCn for its routing body).
	if v := m.View(); !strings.Contains(v, "当前: 中国列表以外") {
		t.Fatalf("mode not refreshed after submit:\n%s", v)
	}
}

// A preset the backend refuses (an unusable group name) must not be written;
// the error surfaces as a toast and nothing is submitted.
func TestHomeRoutingPresetRejected(t *testing.T) {
	m := newTestModelWith(t, presetRejectingDriver{})
	m, _ = m.Update(key("enter")) // cursor on gfw
	if v := m.View(); !strings.Contains(v, "替换为「GFW 模式」") {
		t.Fatalf("confirmation missing:\n%s", v)
	}
	m2, cmd := m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y should fire the validation cmd")
	}
	msg := cmd()
	m3, toast := m2.Update(msg)
	if toast == nil {
		t.Fatal("rejection should surface a toast")
	}
	done, ok := toast().(opDoneMsg)
	if !ok || done.Err == nil {
		t.Fatalf("expected an error toast, got %+v", done)
	}
	m3, _ = m3.Update(done) // the toast itself
	if v := m3.View(); !strings.Contains(v, "✗ 切换路由") {
		t.Fatalf("toast missing:\n%s", v)
	}
	if v := m3.View(); strings.Contains(v, "替换为") {
		t.Fatalf("confirmation should be closed after rejection:\n%s", v)
	}
}

// With no routing profile the section degrades to a hint instead of a dead
// Enter.
func TestHomeNoRoutingProfile(t *testing.T) {
	m := newTestModel(t)
	mm := m.(Model)
	mm.configs.sel.Routings = nil
	mm.home.handleSelections(mm.configs.sel, nil, stubDriver{})
	m = mm
	v := m.View()
	if !strings.Contains(v, "无路由方案") {
		t.Fatalf("missing no-routing hint:\n%s", v)
	}
	m, cmd := m.Update(key("enter"))
	if cmd != nil {
		t.Fatal("enter without a routing profile should do nothing")
	}
	if v := m.View(); strings.Contains(v, "替换为") {
		t.Fatalf("no confirmation without a routing profile:\n%s", v)
	}
}

// --- node list filter / sort ---

// The shared filter/sort: matching by any recognizable field, latency
// ordering with unmeasured nodes trailing, and the key contract of the
// filter box.
func TestNodeViewFilterAndSort(t *testing.T) {
	nodes := []driver.Node{
		{ID: "a", Name: "HK-01", Protocol: "vmess"},
		{ID: "b", Name: "东京-02", Protocol: "ss", Tag: "机场A"},
		{ID: "c", Name: "SG-03", Protocol: "trojan", Address: "sg.example.com:443"},
	}
	lat := map[string]driver.Latency{
		"a": {NodeID: "a", Ms: 300, Alive: true, TestedAt: time.Now()},
		"b": {NodeID: "b", Ms: 50, Alive: true, TestedAt: time.Now()},
		"c": {NodeID: "c", Alive: false, TestedAt: time.Now()}, // dead
	}
	ids := func(v nodeView) []string {
		out := []string{}
		for _, n := range v.visible(nodes, lat) {
			out = append(out, n.ID)
		}
		return out
	}

	v := newNodeView()
	if got := ids(v); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("no filter: %v", got)
	}
	// Match on name, protocol, tag, address — case-insensitively.
	for q, want := range map[string][]string{
		"hk":           {"a"},
		"SS":           {"a", "b"}, // substring: vmess contains "ss"
		"机场":           {"b"},
		"example.com":  {"c"},
		"":             {"a", "b", "c"},
		"nomatchatall": {},
	} {
		v = newNodeView()
		v.applied = q
		if got := ids(v); !reflect.DeepEqual(got, want) {
			t.Errorf("filter %q: %v, want %v", q, got, want)
		}
	}
	// Latency sort: measured first (asc/desc), dead last in both.
	v = newNodeView()
	v.sortBy = sortLatencyAsc
	if got := ids(v); !reflect.DeepEqual(got, []string{"b", "a", "c"}) {
		t.Fatalf("latency asc: %v", got)
	}
	v.sortBy = sortLatencyDesc
	if got := ids(v); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("latency desc: %v", got)
	}

	// Filter box keys: / opens, typing filters live, enter keeps, esc clears.
	v = newNodeView()
	if _, consumed := v.handleKey(key("j")); consumed {
		t.Fatal("j must not be consumed by the filter")
	}
	if cmd, consumed := v.handleKey(key("/")); !consumed || cmd == nil {
		t.Fatal("/ should open the box (and blink)")
	}
	if !v.open {
		t.Fatal("box should be open")
	}
	for _, r := range "hk" {
		v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if v.filter() != "hk" {
		t.Fatalf("live filter = %q", v.filter())
	}
	// While the box is open every key belongs to it: 'o' is typed into the
	// filter, not a sort toggle.
	if _, consumed := v.handleKey(key("o")); !consumed {
		t.Fatal("keys must be consumed by the open box")
	}
	if v.sortBy != sortBackend {
		t.Fatal("o must not toggle sort while the box is open")
	}
	if v.filter() != "hko" {
		t.Fatalf("o should have been typed: %q", v.filter())
	}
	v.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if v.filter() != "hk" {
		t.Fatalf("backspace should remove the o: %q", v.filter())
	}
	v.handleKey(key("enter"))
	if v.open || v.filter() != "hk" {
		t.Fatalf("enter should close and keep: open=%v filter=%q", v.open, v.filter())
	}
	v.handleKey(key("/")) // reopen with the text
	if v.input.Value() != "hk" {
		t.Fatalf("reopen should prefill: %q", v.input.Value())
	}
	v.handleKey(key("esc"))
	if v.open || v.filter() != "" {
		t.Fatalf("esc should close and clear: open=%v filter=%q", v.open, v.filter())
	}
	// o cycles the sort only while the box is closed.
	v = newNodeView()
	for _, want := range []int{sortLatencyAsc, sortLatencyDesc, sortBackend} {
		v.handleKey(key("o"))
		if v.sortBy != want {
			t.Fatalf("sortBy = %d, want %d", v.sortBy, want)
		}
	}

	// Titles and prompt.
	v = newNodeView()
	if got := v.countTitle(3, 10); got != " (10)" {
		t.Fatalf("countTitle = %q", got)
	}
	if got := v.sortTitle(); got != "" {
		t.Fatalf("sortTitle = %q", got)
	}
	v.applied = "hk"
	if got := v.countTitle(1, 10); got != " (1/10)" {
		t.Fatalf("filtered countTitle = %q", got)
	}
	if got := v.prompt(); got == "" || !strings.Contains(got, "hk") {
		t.Fatalf("prompt = %q", got)
	}
	v.sortBy = sortLatencyAsc
	if got := v.sortTitle(); got != " · 延迟↑" {
		t.Fatalf("sortTitle asc = %q", got)
	}
}

// Subscription page: the filter narrows the node list, `t` probes only what
// is visible, and the cursor cannot escape the filtered list.
func TestSubsNodeFilterAndSort(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("3"))
	m, cmd := m.Update(key("l")) // expand → fetch nodes
	if cmd == nil {
		t.Fatal("expanding should fetch nodes")
	}
	m, _ = m.Update(cmd()) // subNodesMsg
	v := m.View()
	for _, want := range []string{"机场A-01", "机场A-02"} {
		if !strings.Contains(v, want) {
			t.Fatalf("subscription nodes missing %q:\n%s", want, v)
		}
	}

	// Filter to the first node only.
	m, _ = m.Update(key("/"))
	for _, r := range "01" {
		mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mm
	}
	v = m.View()
	if !strings.Contains(v, "机场A-01") || strings.Contains(v, "机场A-02") {
		t.Fatalf("filter did not narrow the list:\n%s", v)
	}
	if !strings.Contains(v, "(1/2)") {
		t.Fatalf("pane title should show matched/total:\n%s", v)
	}
	// Global hotkeys must not fire while the box is open.
	if mm, _ := m.Update(key("3")); mm.(Model).page != pageSubs {
		t.Fatal("page switched while the filter box was open")
	}
	// enter closes the box but keeps the filter.
	m, _ = m.Update(key("enter"))
	if v := m.View(); !strings.Contains(v, "机场A-01") || strings.Contains(v, "机场A-02") {
		t.Fatalf("filter should survive closing the box:\n%s", v)
	}

	// t probes exactly the visible nodes.
	lastTestIDs = nil
	m, cmd = m.Update(key("t"))
	if cmd == nil {
		t.Fatal("t should fire a test")
	}
	cmd()
	if len(lastTestIDs) != 1 || lastTestIDs[0] != "x1" {
		t.Fatalf("tested %v, want only x1", lastTestIDs)
	}

	// The cursor cannot point past the filtered list.
	mm := m.(Model)
	mm.subs.nc = 99
	mm.subs.clampNodeCursor()
	if mm.subs.nc != 0 {
		t.Fatalf("nc = %d, want clamped to 0", mm.subs.nc)
	}
	m = mm
	// esc inside the box clears the filter and the full list is back.
	m, _ = m.Update(key("/"))
	m, _ = m.Update(key("esc"))
	if v := m.View(); !strings.Contains(v, "机场A-02") {
		t.Fatalf("esc should clear the filter:\n%s", v)
	}
}

// Manual nodes page: the same filter narrows the left list, and the detail
// pane follows the filtered cursor.
func TestManualNodesFilter(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("4"))
	m, _ = m.Update(key("/"))
	for _, r := range "sg" {
		mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mm
	}
	v := m.View()
	if !strings.Contains(v, "自建-SG") || strings.Contains(v, "自建-HK") {
		t.Fatalf("filter did not narrow the manual list:\n%s", v)
	}
	m, _ = m.Update(key("enter"))
	// The cursor now sits on the only visible node.
	m, _ = m.Update(key("tab"))
	if v := m.View(); !strings.Contains(v, "自建-SG") {
		t.Fatalf("detail should show the filtered node:\n%s", v)
	}
}

// Group page: the filter applies to the detail pane's nodes — sections
// without matches drop out, matched ones open — and to the `n` picker.
func TestGroupsNodeFilter(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("2"))
	m, _ = m.Update(key("l"))     // expand the group
	m, _ = m.Update(key("g"))     // rc=0: sub header
	m, _ = m.Update(key("enter")) // open the subscription section
	m, _ = m.Update(key("j"))     // 东京-01
	m, _ = m.Update(key("j"))     // SG-03
	m, _ = m.Update(key("j"))     // direct section header
	m, _ = m.Update(key("enter")) // open the direct section
	v := m.View()
	for _, want := range []string{"东京-01", "SG-03", "HK-02"} {
		if !strings.Contains(v, want) {
			t.Fatalf("group nodes missing %q:\n%s", want, v)
		}
	}

	// Filter "hk": only the direct node matches, so the subscription
	// section (no matches) disappears entirely.
	m, _ = m.Update(key("/"))
	for _, r := range "hk" {
		mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mm
	}
	v = m.View()
	if !strings.Contains(v, "HK-02") {
		t.Fatalf("matched node missing:\n%s", v)
	}
	for _, hidden := range []string{"东京-01", "SG-03", "机场A"} {
		if strings.Contains(v, hidden) {
			t.Fatalf("non-matching content should be hidden, found %q:\n%s", hidden, v)
		}
	}
	m, _ = m.Update(key("enter")) // close the box, keep the filter
	m, _ = m.Update(key("esc"))   // collapse back to the group list
	if v := m.View(); !strings.Contains(v, "HK-02") {
		t.Fatalf("filter should keep applying after collapsing:\n%s", v)
	}
	m, _ = m.Update(key("/"))
	m, _ = m.Update(key("esc")) // clear

	// The `n` picker filters its candidates too.
	m, cmd := m.Update(key("n"))
	if cmd == nil {
		t.Fatal("n should load candidates")
	}
	m, _ = m.Update(cmd())
	v = m.View()
	if !strings.Contains(v, "机场A-01") || !strings.Contains(v, "自建-HK") {
		t.Fatalf("picker should list manual + subscription nodes:\n%s", v)
	}
	m, _ = m.Update(key("/"))
	for _, r := range "机场" {
		mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mm
	}
	v = m.View()
	if !strings.Contains(v, "机场A-01") || strings.Contains(v, "自建-HK") {
		t.Fatalf("picker filter failed:\n%s", v)
	}
	// enter adds the filtered candidate.
	m, _ = m.Update(key("enter"))
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter in the picker should add the node")
	}
}

// A subscription's section expansion follows the subscription, not its
// position: a refresh that reorders the group's subscriptions must not open
// someone else's section.
func TestGroupSectionExpansionFollowsSubscriptionID(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("2"))
	m, _ = m.Update(key("l"))     // expand the group
	m, _ = m.Update(key("enter")) // open the subscription section
	if v := m.View(); !strings.Contains(v, "▾ 订阅") {
		t.Fatalf("subscription section should be open:\n%s", v)
	}
	// Reload with a second subscription in front of the opened one.
	gs := mustGroups(t)
	g := gs[0]
	g.Subscriptions = []driver.GroupSubscription{
		{SubscriptionID: "s9", Tag: "机场Z", MatchedCount: 1,
			Nodes: []driver.Node{{ID: "z1", Name: "Z-01", Protocol: "ss", SubscriptionID: "s9"}}},
		g.Subscriptions[0],
	}
	m, _ = m.Update(groupsMsg{Groups: []driver.Group{g, gs[1]}})
	v := m.View()
	if !strings.Contains(v, "▾ 订阅 机场A") {
		t.Fatalf("机场A section should stay open after the reorder:\n%s", v)
	}
	if !strings.Contains(v, "▸ 订阅 机场Z") {
		t.Fatalf("机场Z section should stay collapsed:\n%s", v)
	}
}

// Editing a subscription's tag or cron reloads the list but must not throw
// away the node caches; only an update (u), which re-fetches nodes, may.
func TestSubsNodeCacheSurvivesReload(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("3"))
	m, cmd := m.Update(key("l")) // expand s1 → fetch its nodes
	if cmd == nil {
		t.Fatal("expanding subscription should fetch its nodes")
	}
	m, _ = m.Update(cmd())
	if n := len(m.(Model).subs.subNodes); n != 1 {
		t.Fatalf("expected one cached node list, got %d", n)
	}
	// A plain reload (what a tag/cron edit returns) keeps the cache.
	m, _ = m.Update(subsMsg{Subs: mustSubs(t)})
	mm := m.(Model)
	if _, ok := mm.subs.subNodes["s1"]; !ok {
		t.Fatal("node cache dropped by a plain subsMsg")
	}
	if v := mm.View(); !strings.Contains(v, "机场A-01") {
		t.Fatalf("expanded nodes should render without a re-fetch:\n%s", v)
	}
	// An update invalidates just that subscription and re-fetches it.
	m, cmd = m.Update(key("u"))
	if cmd == nil {
		t.Fatal("u should fire subMutateCmd")
	}
	m, cmd = m.Update(cmd()) // subsMsg after the mutation
	if cmd == nil {
		t.Fatal("an updated subscription should be re-fetched")
	}
	if _, ok := m.(Model).subs.subNodes["s1"]; ok {
		t.Fatal("the updated subscription's cache should be dropped")
	}
}

// The in-flight window scales with the batch size, and the tab bar reports
// real progress instead of a fixed spinner.
func TestLatencyTestProgress(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("2"))
	m, cmd := m.Update(key("t")) // whole group: n1, n3, n2
	if cmd == nil {
		t.Fatal("t should fire a test")
	}
	m, _ = m.Update(cmd())
	if !m.(Model).groups.testing {
		t.Fatal("groups page should be in testing state")
	}
	if v := m.View(); !strings.Contains(v, "测速中 0/3") {
		t.Fatalf("tab bar should show progress:\n%s", v)
	}
	later := time.Now().Add(time.Second)
	m, _ = m.Update(latenciesMsg{Lats: []driver.Latency{
		{NodeID: "n1", Ms: 90, Alive: true, TestedAt: later},
		{NodeID: "n2", Ms: 300, Alive: true, TestedAt: later},
	}})
	if v := m.View(); !strings.Contains(v, "测速中 2/3") {
		t.Fatalf("progress should count the nodes that reported:\n%s", v)
	}
	m, _ = m.Update(latenciesMsg{Lats: []driver.Latency{
		{NodeID: "n3", Alive: false, TestedAt: later},
	}})
	if m.(Model).groups.testing {
		t.Fatal("testing should end once every probed node reported")
	}
	if v := m.View(); strings.Contains(v, "测速中") {
		t.Fatalf("progress indicator should be gone:\n%s", v)
	}
}

// A large batch gets a proportionally longer window; the cap matches the
// command's own context timeout.
func TestTestWindowScales(t *testing.T) {
	if got := testWindow(3); got != 15*time.Second+600*time.Millisecond {
		t.Fatalf("testWindow(3) = %v", got)
	}
	if got := testWindow(1000); got != 2*time.Minute {
		t.Fatalf("testWindow(1000) = %v, want the 2m cap", got)
	}
}

// Enter on a home-page group row jumps to the groups page with that group
// expanded — the short path to switching a group's node.
func TestHomeGroupRowJump(t *testing.T) {
	m := newTestModel(t)
	if v := m.View(); !strings.Contains(v, "各组当前节点") {
		t.Fatalf("home should list the groups:\n%s", v)
	}
	m, _ = m.Update(key("tab")) // focus the group list
	if v := m.View(); !strings.Contains(v, "Enter 跳到群组页并展开该组") {
		t.Fatalf("group-list focus hint missing:\n%s", v)
	}
	m, _ = m.Update(key("j")) // cursor onto the second group (direct)
	m, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter on a group row should fire gotoGroupMsg")
	}
	msg, ok := cmd().(gotoGroupMsg)
	if !ok {
		t.Fatalf("cmd = %T, want gotoGroupMsg", cmd())
	}
	if msg.ID != "g2" {
		t.Fatalf("gotoGroupMsg.ID = %q, want g2", msg.ID)
	}
	m, _ = m.Update(msg)
	v := m.View()
	if !strings.Contains(v, "路由组") {
		t.Fatalf("should land on the groups page:\n%s", v)
	}
	if !strings.Contains(v, "direct · 自动") {
		t.Fatalf("the chosen group should be expanded:\n%s", v)
	}
	// Coming back home, tab/esc leave the group list without touching the
	// routing picker.
	m, _ = m.Update(key("1"))
	m, _ = m.Update(key("tab"))
	m, _ = m.Update(key("esc"))
	if m.(Model).home.groupFocus {
		t.Fatal("esc should return focus to the routing picker")
	}
}

// Space ticks nodes for batch operations: t probes exactly the ticked ones,
// and x removes the ticked directly-attached nodes in one mutation.
func TestGroupsBatchSelection(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("2"))
	m, _ = m.Update(key("l"))     // expand the group
	m, _ = m.Update(key("enter")) // open the subscription section
	m, _ = m.Update(key("j"))     // 东京-01 (subscription node)
	m, _ = m.Update(key("space"))
	if v := m.View(); !strings.Contains(v, "✓") {
		t.Fatalf("ticked row should show a mark:\n%s", v)
	}
	m, _ = m.Update(key("j")) // SG-03 (subscription node)
	m, _ = m.Update(key("space"))
	lastTestIDs = nil
	m, cmd := m.Update(key("t"))
	if cmd == nil {
		t.Fatal("t with ticks should fire a test")
	}
	cmd()
	if len(lastTestIDs) != 2 {
		t.Fatalf("t should probe exactly the ticked nodes, got %v", lastTestIDs)
	}
	// Subscription-sourced nodes cannot be removed individually; say so.
	m, cmd = m.Update(key("x"))
	if cmd == nil {
		t.Fatal("x should explain why nothing can be removed")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	if v := m.View(); !strings.Contains(v, "均来自订阅挂载") {
		t.Fatalf("subscription-node removal should be explained:\n%s", v)
	}
	// Open the direct section and tick HK-02: the batch removal confirms.
	m, _ = m.Update(key("j"))     // direct section header
	m, _ = m.Update(key("enter")) // open it
	m, _ = m.Update(key("j"))     // HK-02 (directly attached)
	m, _ = m.Update(key("space"))
	if v := m.View(); !strings.Contains(v, "已选 3") {
		t.Fatalf("pane title should count the ticks:\n%s", v)
	}
	m, cmd = m.Update(key("x"))
	if cmd != nil {
		t.Fatal("x should open the confirmation, not fire a mutation")
	}
	// SG-03 is attached both ways in the stub, so the batch covers it too.
	if v := m.View(); !strings.Contains(v, "确认将 2 个节点从组中移除") {
		t.Fatalf("batch removal confirmation missing:\n%s", v)
	}
	m, cmd = m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y should fire the batch removal")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
}

// Ticked candidates in the add-node picker are attached in one mutation.
func TestGroupsPickerBatchAdd(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("2"))
	m, cmd := m.Update(key("n"))
	if cmd == nil {
		t.Fatal("n should load the attach candidates")
	}
	m, _ = m.Update(cmd())
	// Candidates: 自建-HK, 自建-SG, 机场A-01, 机场A-02.
	m, _ = m.Update(key("space"))
	m, _ = m.Update(key("j"))
	m, _ = m.Update(key("space"))
	if v := m.View(); !strings.Contains(v, "已选 2") {
		t.Fatalf("picker should count the ticks:\n%s", v)
	}
	lastAddNodeIDs = nil
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter should fire the batch attach")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	if len(lastAddNodeIDs) != 2 || lastAddNodeIDs[0] != "m1" || lastAddNodeIDs[1] != "m2" {
		t.Fatalf("batch attach ids = %v, want [m1 m2]", lastAddNodeIDs)
	}
}

// noCapsDriver reports a backend that supports nothing; the UI must degrade
// with feedback instead of leaving keys that silently do nothing.
type noCapsDriver struct{ stubDriver }

func (noCapsDriver) Capabilities() driver.Caps { return driver.Caps{} }

func TestCapabilityGating(t *testing.T) {
	m := newTestModelWith(t, noCapsDriver{})
	if v := m.View(); !strings.Contains(v, "当前后端不支持") {
		t.Fatalf("home should report the unsupported traffic stats:\n%s", v)
	}
	m, _ = m.Update(key("2"))
	m, cmd := m.Update(key("t"))
	if cmd == nil {
		t.Fatal("t should report the missing capability")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	if v := m.View(); !strings.Contains(v, "当前后端不支持该操作") {
		t.Fatalf("capability toast missing:\n%s", v)
	}
	m, _ = m.Update(key("3"))
	if v := m.View(); !strings.Contains(v, "当前后端不支持订阅管理") {
		t.Fatalf("subs page banner missing:\n%s", v)
	}
	m, cmd = m.Update(key("u"))
	if cmd == nil {
		t.Fatal("u should report the missing capability")
	}
	m, _ = m.Update(key("5"))
	if v := m.View(); !strings.Contains(v, "当前后端不支持配置管理") {
		t.Fatalf("configs page banner missing:\n%s", v)
	}
	m, cmd = m.Update(key("e"))
	if cmd == nil {
		t.Fatal("e should report the missing capability")
	}
}

// The setup form advertises "letters and digits" — that is the rule that
// runs, not just a length check.
func TestSetupPasswordRule(t *testing.T) {
	var m tea.Model = New(stubDriver{}, &config.Config{Endpoint: "http://127.0.0.1:2023/graphql"}, "/tmp/dae-tui-test.toml")
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m2, _ = m2.Update(bootMsg{Users: 0}) // no users yet → first-run setup
	m = m2
	if v := m.View(); !strings.Contains(v, "初始化 daed 账号") {
		t.Fatalf("expected the setup form:\n%s", v)
	}
	for _, k := range []string{"a", "d", "m", "i", "n"} {
		m, _ = m.Update(key(k))
	}
	m, _ = m.Update(key("tab"))
	for _, k := range []string{"1", "2", "3", "4", "5", "6"} { // digits only
		m, _ = m.Update(key(k))
	}
	m, _ = m.Update(key("tab"))
	for _, k := range []string{"1", "2", "3", "4", "5", "6"} {
		m, _ = m.Update(key(k))
	}
	m, cmd := m.Update(key("enter"))
	if cmd != nil {
		t.Fatal("a digits-only password must be refused client-side")
	}
	if v := m.View(); !strings.Contains(v, "需包含字母和数字") {
		t.Fatalf("password rule error missing:\n%s", v)
	}
	// Letters only is refused too… (two tabs walk focus from the confirm
	// field back to the password field).
	m, _ = m.Update(key("tab"))
	m, _ = m.Update(key("tab"))
	for i := 0; i < 6; i++ {
		m, _ = m.Update(key("backspace"))
	}
	for _, k := range []string{"a", "b", "c", "d", "e", "f"} {
		m, _ = m.Update(key(k))
	}
	m, _ = m.Update(key("tab"))
	for i := 0; i < 6; i++ {
		m, _ = m.Update(key("backspace"))
	}
	for _, k := range []string{"a", "b", "c", "d", "e", "f"} {
		m, _ = m.Update(key(k))
	}
	m, cmd = m.Update(key("enter"))
	if cmd != nil {
		t.Fatal("a letters-only password must be refused client-side")
	}
	// …and a mix passes.
	m, _ = m.Update(key("tab"))
	m, _ = m.Update(key("tab"))
	for i := 0; i < 6; i++ {
		m, _ = m.Update(key("backspace"))
	}
	for _, k := range []string{"a", "b", "c", "1", "2", "3"} {
		m, _ = m.Update(key(k))
	}
	m, _ = m.Update(key("tab"))
	for i := 0; i < 6; i++ {
		m, _ = m.Update(key("backspace"))
	}
	for _, k := range []string{"a", "b", "c", "1", "2", "3"} {
		m, _ = m.Update(key(k))
	}
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("a mixed password should submit")
	}
}

// The change-password form enforces the same rule it advertises.
func TestHomePasswordRule(t *testing.T) {
	cfg := &config.Config{Endpoint: "http://127.0.0.1:2023/graphql", Username: "admin"}
	var m tea.Model = New(stubDriver{}, cfg, "/tmp/dae-tui-test.toml")
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m2, _ = m2.Update(bootMsg{Users: 1, Status: driver.Status{Version: "v2.1.1", Running: true}})
	m = m2
	m, _ = m.Update(key("P"))
	m, _ = m.Update(key("enter")) // password form
	for _, k := range []string{"o", "l", "d", "p", "w", "1"} {
		m, _ = m.Update(key(k))
	}
	m, _ = m.Update(key("tab"))
	for _, k := range []string{"a", "b", "c", "d", "e", "f"} { // letters only
		m, _ = m.Update(key(k))
	}
	m, _ = m.Update(key("tab"))
	for _, k := range []string{"a", "b", "c", "d", "e", "f"} {
		m, _ = m.Update(key(k))
	}
	m, cmd := m.Update(key("enter"))
	if cmd != nil {
		t.Fatal("a letters-only new password must be refused")
	}
	if v := m.View(); !strings.Contains(v, "新密码至少 6 位，且需包含字母和数字") {
		t.Fatalf("password rule error missing:\n%s", v)
	}
}

// A field value is checked against the backend-declared type before the
// round trip.
func TestConfigsFieldTypeValidation(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("5"))
	m, cmd := m.Update(key("e")) // field picker on the config section
	if cmd != nil {
		t.Fatal("e on config should not fire a cmd directly")
	}
	m, _ = m.Update(key("j"))     // lanInterface (array), prefilled "eth0"
	m, _ = m.Update(key("enter")) // field input
	m, _ = m.Update(key(","))
	m, _ = m.Update(key(","))
	m, cmd = m.Update(key("enter"))
	if cmd != nil {
		t.Fatal("an array with an empty element must not submit")
	}
	if v := m.View(); !strings.Contains(v, "数组元素不能为空") {
		t.Fatalf("type error missing:\n%s", v)
	}
	// A valid value submits.
	m, _ = m.Update(key("esc")) // back to the picker
	m, _ = m.Update(key("enter"))
	m, _ = m.Update(key(","))
	m, _ = m.Update(key(" "))
	m, _ = m.Update(key("w"))
	m, _ = m.Update(key("l"))
	m, _ = m.Update(key("a"))
	m, _ = m.Update(key("n"))
	m, _ = m.Update(key("0"))
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("a valid array value should fire configFieldCmd")
	}
}

// The history keeps a bounded, per-node window of samples and evicts the
// least recently seen node when full.
func TestLatHistoryWindowAndEviction(t *testing.T) {
	h := newLatHistory()
	h.maxNodes = 2
	now := time.Now()
	for _, ms := range []int{100, 120, 140} {
		h.add(driver.Latency{NodeID: "n1", Ms: ms, Alive: true, TestedAt: now})
	}
	if got := h.series("n1"); len(got) != 3 || got[0] != 100 || got[2] != 140 {
		t.Fatalf("series = %v, want the three alive samples", got)
	}
	// Dead probes are skipped, not plotted as impossibly fast nodes.
	h.add(driver.Latency{NodeID: "n1", Alive: false, TestedAt: now})
	if got := h.series("n1"); len(got) != 3 {
		t.Fatalf("a dead probe should not extend the series: %v", got)
	}
	// The window trims to the newest samples (the dead probe occupies a
	// slot, it is just not plotted).
	h.window = 3
	h.add(driver.Latency{NodeID: "n1", Ms: 160, Alive: true, TestedAt: now})
	if got := h.series("n1"); len(got) != 2 || got[0] != 140 || got[1] != 160 {
		t.Fatalf("window should keep the newest samples: %v", got)
	}
	// Tracking a third node evicts the least recently updated one.
	h.add(driver.Latency{NodeID: "n2", Ms: 50, Alive: true, TestedAt: now})
	h.add(driver.Latency{NodeID: "n1", Ms: 170, Alive: true, TestedAt: now}) // touch n1
	h.add(driver.Latency{NodeID: "n3", Ms: 60, Alive: true, TestedAt: now})
	if got := h.series("n2"); got != nil {
		t.Fatalf("n2 should have been evicted, got %v", got)
	}
	if got := h.series("n1"); len(got) == 0 {
		t.Fatal("n1 was touched last and must survive")
	}
}

// The manual-node detail pane draws the accumulated trend once a node has
// at least two alive samples.
func TestNodesDetailTrend(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("4"))
	m, _ = m.Update(key("tab")) // detail pane
	now := time.Now()
	for _, ms := range []int{80, 120, 200, 160} {
		m, _ = m.Update(latenciesMsg{Lats: []driver.Latency{
			{NodeID: "m1", Ms: ms, Alive: true, TestedAt: now},
		}})
	}
	v := m.View()
	if !strings.Contains(v, "趋势") {
		t.Fatalf("detail pane should show the trend line:\n%s", v)
	}
}

func TestDiffLines(t *testing.T) {
	lines := diffLines("a\nb\nc\nd\ne", "a\nB\nc\nd\ne\nf")
	var adds, dels int
	for _, l := range lines {
		switch l.kind {
		case diffAdd:
			adds++
		case diffDel:
			dels++
		}
	}
	if adds != 2 || dels != 1 {
		t.Fatalf("adds = %d, dels = %d, want 2 and 1", adds, dels)
	}
	// A long unchanged run collapses into a gap marker.
	lines = diffLines(strings.Repeat("x\n", 10)+"tail", strings.Repeat("x\n", 10)+"TAIL")
	gap := false
	for _, l := range lines {
		if l.kind == diffGap {
			gap = true
		}
	}
	if !gap {
		t.Fatal("a long unchanged run should collapse into a gap")
	}
}

// y copies through OSC 52; off a terminal the command reports the failure
// instead of writing escape noise into a pipe.
func TestClipboardCopyFeedback(t *testing.T) {
	if stdoutIsTerminal() {
		t.Skip("stdout is a terminal; OSC 52 would be written for real")
	}
	m := newTestModel(t)
	m, _ = m.Update(key("3"))
	m, cmd := m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y on the subs page should copy the link")
	}
	msg, ok := cmd().(clipboardMsg)
	if !ok {
		t.Fatalf("cmd = %T, want clipboardMsg", msg)
	}
	if msg.OK {
		t.Fatal("off a terminal the copy must report failure")
	}
	m, _ = m.Update(msg)
	if v := m.View(); !strings.Contains(v, "复制失败") {
		t.Fatalf("copy feedback missing:\n%s", v)
	}
}

// L launches the journal viewer. The command itself is not executed here:
// on a machine with journalctl it would attach to the log stream and never
// return, which is exactly what it does in the TUI.
func TestLogsViewerLaunches(t *testing.T) {
	m := newTestModel(t)
	m, cmd := m.Update(key("L"))
	if cmd == nil {
		t.Fatal("L should launch the journal viewer (or report journalctl missing)")
	}
}

// Latency data is polled for what the current page shows — never the whole
// instance. The home page polls nothing: its per-group estimate runs on
// accumulated data rather than paying for every member of every group.
func TestLatencyPollScopedToVisibleNodes(t *testing.T) {
	m := newTestModel(t)
	if cmd := m.(Model).latencyPollCmd(); cmd != nil {
		t.Fatal("the home page should not poll latency data")
	}
	// Collapsed groups page: section headers only, no node rows.
	m, _ = m.Update(key("2"))
	if cmd := m.(Model).latencyPollCmd(); cmd != nil {
		t.Fatal("a collapsed group detail should not poll")
	}
	// Expand and open the subscription section: its node rows are polled.
	m, _ = m.Update(key("l"))
	m, _ = m.Update(key("enter"))
	lastLatencyIDs = nil
	cmd := m.(Model).latencyPollCmd()
	if cmd == nil {
		t.Fatal("an expanded group should poll its node rows")
	}
	cmd()
	if len(lastLatencyIDs) != 2 || lastLatencyIDs[0] != "n1" || lastLatencyIDs[1] != "n3" {
		t.Fatalf("polled ids = %v, want the two visible subscription nodes", lastLatencyIDs)
	}
	// A filter narrows the poll to what survives it.
	m, _ = m.Update(key("/"))
	for _, k := range []string{"S", "G"} {
		m, _ = m.Update(key(k))
	}
	m, _ = m.Update(key("enter")) // keep the filter
	lastLatencyIDs = nil
	cmd = m.(Model).latencyPollCmd()
	if cmd == nil {
		t.Fatal("a filtered list should still poll its visible nodes")
	}
	cmd()
	if len(lastLatencyIDs) != 1 || lastLatencyIDs[0] != "n3" {
		t.Fatalf("polled ids = %v, want only the matching node", lastLatencyIDs)
	}

	// Subs page polls only while a subscription is expanded.
	m, _ = m.Update(key("3"))
	if cmd := m.(Model).latencyPollCmd(); cmd != nil {
		t.Fatal("a collapsed subscription should not poll")
	}
	m, cmd = m.Update(key("l"))
	if cmd == nil {
		t.Fatal("expanding should fetch the nodes")
	}
	m, _ = m.Update(cmd())
	lastLatencyIDs = nil
	cmd = m.(Model).latencyPollCmd()
	if cmd == nil {
		t.Fatal("an expanded subscription should poll its visible nodes")
	}
	cmd()
	if len(lastLatencyIDs) != 2 || lastLatencyIDs[0] != "x1" {
		t.Fatalf("polled ids = %v, want the subscription's nodes", lastLatencyIDs)
	}

	// The manual node page is always showing its list.
	m, _ = m.Update(key("4"))
	lastLatencyIDs = nil
	cmd = m.(Model).latencyPollCmd()
	if cmd == nil {
		t.Fatal("the manual node page should poll its list")
	}
	cmd()
	if len(lastLatencyIDs) != 2 || lastLatencyIDs[0] != "m1" {
		t.Fatalf("polled ids = %v, want the manual nodes", lastLatencyIDs)
	}
}

// The add-node picker sorts by latency, so its candidates stay in the poll
// scope while it is open.
func TestLatencyPollCoversOpenPicker(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("2"))
	m, cmd := m.Update(key("n"))
	if cmd == nil {
		t.Fatal("n should load the attach candidates")
	}
	m, _ = m.Update(cmd())
	lastLatencyIDs = nil
	poll := m.(Model).latencyPollCmd()
	if poll == nil {
		t.Fatal("the open picker should poll its candidates")
	}
	poll()
	// 自建-HK, 自建-SG, 机场A-01, 机场A-02.
	if len(lastLatencyIDs) != 4 {
		t.Fatalf("polled ids = %v, want the picker candidates", lastLatencyIDs)
	}
}

// Startup must not probe the whole instance: measurements are created on
// demand (t/T) and kept fresh by the per-page poll.
func TestNoStartupLatencyTest(t *testing.T) {
	cfg := &config.Config{Endpoint: "http://127.0.0.1:2023/graphql"}
	var m tea.Model = New(stubDriver{}, cfg, "/tmp/dae-tui-test.toml")
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	lastTestIDs = nil
	m3, cmd := m2.Update(bootMsg{Users: 1, Status: driver.Status{Version: "v2.1.1", Running: true}})
	if cmd == nil {
		t.Fatal("boot should fire initialLoad")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd = %T, want tea.BatchMsg", cmd())
	}
	for _, c := range batch {
		c()
	}
	if lastTestIDs != nil {
		t.Fatalf("startup triggered a latency test: %v", lastTestIDs)
	}
	// The initial load still fetches the page data.
	if m3.(Model).phase != phaseMain {
		t.Fatal("boot should enter the main phase")
	}
}

// The home page's per-group line shows the estimated node without a
// millisecond figure — keeping one fresh for every group is exactly the
// full-instance cost the per-page poll avoids.
func TestHomeCurrentNodeHasNoMilliseconds(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	if !strings.Contains(v, "≈ 东京-01") {
		t.Fatalf("home should estimate the best measured node:\n%s", v)
	}
	if strings.Contains(v, "88ms") {
		t.Fatalf("home should not show per-node milliseconds:\n%s", v)
	}
}

// The help page must describe how latency data actually works now: on-demand
// probing plus a poll scoped to the visible nodes, no startup-wide test.
func TestHelpDescribesLatencyModel(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("?"))
	v := m.View()
	for _, want := range []string{"测速按需触发", "每 3 秒轮询当前页可见节点", "≈ 已测最优节点"} {
		if !strings.Contains(v, want) {
			t.Fatalf("help missing %q:\n%s", want, v)
		}
	}
}

// The groups page sorts each section's nodes by latency: rebuild applies the
// shared filter+sort per section, so the section structure survives.
func TestGroupsSortByLatency(t *testing.T) {
	m := newTestModel(t)
	// A group whose node order differs from the latency order.
	groups := []driver.Group{{
		ID: "g1", Name: "proxy", Policy: "min_moving_avg",
		Subscriptions: []driver.GroupSubscription{{
			SubscriptionID: "s1", Tag: "机场A", MatchedCount: 2,
			Nodes: []driver.Node{
				{ID: "slow", Name: "慢-01", Protocol: "ss", SubscriptionID: "s1"},
				{ID: "fast", Name: "快-02", Protocol: "ss", SubscriptionID: "s1"},
			},
		}},
	}}
	m, _ = m.Update(groupsMsg{Groups: groups})
	m, _ = m.Update(latenciesMsg{Lats: []driver.Latency{
		{NodeID: "slow", Ms: 900, Alive: true, TestedAt: time.Now()},
		{NodeID: "fast", Ms: 50, Alive: true, TestedAt: time.Now()},
	}})
	m, _ = m.Update(key("2"))
	m, _ = m.Update(key("l"))     // expand the group
	m, _ = m.Update(key("enter")) // open the subscription section
	v := m.View()
	if i, j := strings.Index(v, "慢-01"), strings.Index(v, "快-02"); i > j {
		t.Fatalf("backend order expected before sorting:\n%s", v)
	}
	m, _ = m.Update(key("o")) // latency ↑
	v = m.View()
	if !strings.Contains(v, "延迟↑") {
		t.Fatalf("the pane title should name the sort mode:\n%s", v)
	}
	if i, j := strings.Index(v, "慢-01"), strings.Index(v, "快-02"); i < j {
		t.Fatalf("latency sort should put the fast node first:\n%s", v)
	}
}

// Automatic-policy groups without any measurement say so, rather than
// showing a bare policy label that reads like a rendering gap.
func TestHomeUntestedGroupsAreMarked(t *testing.T) {
	cfg := &config.Config{Endpoint: "http://127.0.0.1:2023/graphql"}
	var m tea.Model = New(stubDriver{}, cfg, "/tmp/dae-tui-test.toml")
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m2, _ = m2.Update(bootMsg{Users: 1, Status: driver.Status{Version: "v2.1.1", Running: true}})
	m2, _ = m2.Update(groupsMsg{Groups: mustGroups(t)}) // no latenciesMsg: nothing measured
	m = m2
	v := m.View()
	if !strings.Contains(v, "未测速") {
		t.Fatalf("unmeasured automatic groups should be marked:\n%s", v)
	}
	if strings.Contains(v, "≈") {
		t.Fatalf("no estimate without measurements:\n%s", v)
	}
}

// r reloads every list, not just the current page's: the pages share data
// (home shows groups, the groups page shows subscription tags, the home
// routing section comes from selections), so a per-page refresh left the
// views you were not looking at stale.
func TestForceRefreshReloadsEverything(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("3")) // the subs page — the old behavior reloaded only this
	m, cmd := m.Update(key("r"))
	if cmd == nil {
		t.Fatal("r should fire a refresh")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd = %T, want tea.BatchMsg", cmd())
	}
	var sawGroups, sawSubs, sawSel, sawStatus, sawManual bool
	for _, c := range batch {
		switch c().(type) {
		case groupsMsg:
			sawGroups = true
		case subsMsg:
			sawSubs = true
		case selectionsMsg:
			sawSel = true
		case statusMsg:
			sawStatus = true
		case manualNodesMsg:
			sawManual = true
		}
	}
	if !sawGroups || !sawSubs || !sawSel || !sawStatus || !sawManual {
		t.Fatalf("refresh should reload every list: groups=%v subs=%v selections=%v status=%v manual=%v",
			sawGroups, sawSubs, sawSel, sawStatus, sawManual)
	}
}
