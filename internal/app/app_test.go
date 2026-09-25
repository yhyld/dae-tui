package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
func (stubDriver) RemoveNodes(_ context.Context, nodeIDs []string) error           { return nil }
func (stubDriver) AddGroupSubscriptions(_ context.Context, groupID string, subIDs []string, filter string) error {
	return nil
}
func (stubDriver) RemoveGroupSubscriptions(_ context.Context, groupID string, subIDs []string) error {
	return nil
}
func (stubDriver) AddGroupNodes(_ context.Context, groupID string, nodeIDs []string) error {
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
func (stubDriver) Latencies(_ context.Context, ids []string) ([]driver.Latency, error) {
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
func (stubDriver) UpdateConfigField(_ context.Context, id string, field driver.ConfigField, value string) error {
	return nil
}
func (stubDriver) UpdateSubscription(_ context.Context, id string) error     { return nil }
func (stubDriver) RemoveSubscriptions(_ context.Context, ids []string) error { return nil }
func (stubDriver) ListSelections(context.Context) (driver.Selections, error) {
	return driver.Selections{
		Configs: []driver.ConfigItem{{ID: "c1", Name: "默认", Selected: true, Detail: "log=info",
			Body: "日志级别:       info",
			Fields: []driver.ConfigField{
				{Name: "logLevel", Label: "日志级别", Value: "info", Type: "string"},
				{Name: "checkInterval", Label: "检查间隔", Value: "30s", Type: "duration"},
			}}},
		Dns: []driver.ConfigItem{
			{ID: "d1", Name: "默认DNS", Selected: true, Body: "upstream {}"},
			{ID: "d2", Name: "备用DNS", Selected: false, Body: "upstream {}"},
		},
		Routings: []driver.ConfigItem{{ID: "r1", Name: "默认路由", Selected: true, Body: "fallback: direct"}},
	}, nil
}
func (stubDriver) SelectConfig(_ context.Context, id string) error  { return nil }
func (stubDriver) SelectDns(_ context.Context, id string) error     { return nil }
func (stubDriver) SelectRouting(_ context.Context, id string) error { return nil }
func (stubDriver) Run(ctx context.Context, dry bool) error          { return nil }

func newTestModel(t *testing.T) tea.Model {
	t.Helper()
	cfg := &config.Config{Endpoint: "http://127.0.0.1:2023/graphql"}
	m := New(stubDriver{}, cfg, "/tmp/dae-tui-test.toml")
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

	// rc is on the sub header; j reaches n1; Enter opens the pin confirmation.
	m, _ = m.Update(key("j"))
	m, cmd := m.Update(key("enter"))
	if v := m.View(); !strings.Contains(v, "固定到节点") {
		t.Fatalf("pin confirmation missing:\n%s", v)
	}
	m, _ = m.Update(key("n")) // decline
	// Do it again and confirm: y fires pinNodeCmd.
	m, _ = m.Update(key("enter"))
	m, cmd = m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y should fire pinNodeCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}

	// g jumps to top (sub row), j to a node, T single-node test.
	m, _ = m.Update(key("g"))
	m, _ = m.Update(key("j"))
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

	// a opens the import form; x asks for delete confirmation.
	m, _ = m.Update(key("h"))
	m, _ = m.Update(key("a"))
	m, _ = m.Update(key("s"))
	m, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("import submit should fire nodeMutateCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
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

	// create (dns section: move cursor to the dns row first).
	m, _ = m.Update(key("j")) // cursor: config c1 → dns d1
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

	// rename.
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

	// delete with confirmation (cursor sits on d1 after the reloads; one
	// j moves to the unselected 备用DNS row).
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
	m, _ = m.Update(key("enter")) // first field (日志级别)
	if v := m.View(); !strings.Contains(v, "新值") {
		t.Fatalf("field input missing:\n%s", v)
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

func TestConfigsEditorDoneSubmitsChange(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("5"))
	// Simulate $EDITOR having written new content for the dns profile.
	tmp := filepath.Join(t.TempDir(), "x.dns")
	os.WriteFile(tmp, []byte("upstream { alidns: 'udp://223.5.5.5:53' }"), 0o600)
	mm, cmd := m.Update(editorDoneMsg{Path: tmp, Section: "dns", ID: "d1", Old: "upstream {}"})
	m = mm
	if cmd == nil {
		t.Fatal("changed content should fire configTextCmd")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.Update(msg)
	}
	// Unchanged content must not fire.
	tmp2 := filepath.Join(t.TempDir(), "y.dns")
	os.WriteFile(tmp2, []byte("  fallback: direct  "), 0o600)
	m2, cmd2 := m.Update(editorDoneMsg{Path: tmp2, Section: "routing", ID: "r1", Old: "fallback: direct"})
	_ = m2
	if cmd2 != nil {
		t.Fatal("unchanged content should not fire a cmd")
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
