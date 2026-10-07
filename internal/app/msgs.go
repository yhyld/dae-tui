package app

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yhyld/dae-tui/internal/driver"
	"github.com/yhyld/dae-tui/internal/i18n"
)

type tickMsg struct{ n int }

type spinnerMsg struct{}

type refreshDoneMsg struct{}

type bootMsg struct {
	Users   int
	Status  driver.Status
	ConnErr error
	AuthErr error
}

type authMsg struct {
	Setup bool
	Err   error
}

type logoutMsg struct{}

type gotoGroupMsg struct{ ID string }

// reloadNoteMsg reports a mutation that bumps the backend's config version:
// the label names what changed, and lands in the pending-reload list the A
// confirm shows ("why is a reload pending?"). Only successful mutations
// emit one; the note is a translated-at-mutation-time string — entries are
// a historical log of what was done, so a mid-session language switch
// leaves older entries in the old language (same allowance as toasts).
type reloadNoteMsg struct{ Note string }

// autoReloadToggledMsg reports the settings toggle's outcome; the dedicated
// type keeps the menu's displayed state honest even when the config write
// fails (the root model only flips settings.autoReload on success).
type autoReloadToggledMsg struct {
	On  bool
	Err error
}

func reloadNoteCmd(note string) tea.Cmd {
	return func() tea.Msg { return reloadNoteMsg{Note: note} }
}

// withReloadNote tags a mutation cmd's success messages with its label as a
// pending-reload note. Failure paths (opDoneMsg carrying Err) and the node
// import report (imports alone don't touch group versions) pass through
// untouched.
func withReloadNote(cmd tea.Cmd, label string) tea.Cmd {
	return func() tea.Msg {
		msg := cmd()
		switch m := msg.(type) {
		case opDoneMsg:
			if m.Err != nil {
				return m
			}
		case importDoneMsg:
			return m
		case tea.BatchMsg:
			return append(m, reloadNoteCmd(label))
		}
		return tea.BatchMsg{func() tea.Msg { return msg }, reloadNoteCmd(label)}
	}
}

type statusMsg struct {
	Status driver.Status
	Err    error
}

type groupsMsg struct {
	Groups []driver.Group
	Err    error
}

type manualNodesMsg struct {
	Nodes []driver.Node
	Err   error
}

type importDoneMsg struct {
	Results []driver.NodeImportResult
	Nodes   []driver.Node
	Err     error
}

type nodesChangedMsg struct {
	Nodes  []driver.Node
	Groups []driver.Group
	Err    error
}

type subNodes struct {
	Tag   string
	Nodes []driver.Node
}

type attachCandidatesMsg struct {
	Manual []driver.Node
	Subs   []subNodes
	Err    error
}

type subNodesMsg struct {
	SubID string
	Nodes []driver.Node
	Err   error
}

type latenciesMsg struct {
	Lats []driver.Latency
	Err  error
}

type trafficMsg struct {
	Snap driver.TrafficSnapshot
	Err  error

	Took time.Duration
}

type subsMsg struct {
	Subs []driver.Subscription
	Err  error
}

type selectionsMsg struct {
	Sel driver.Selections
	Err error
}

type ifacesMsg struct {
	Ifaces []driver.NetworkInterface
	Err    error
}

type opDoneMsg struct {
	Op  string
	Err error

	Idle busyOwner
}

type busyOwner int

const (
	busyNone busyOwner = iota
	busySubs
	busyNodes
)

const ctxTimeout = 12 * time.Second

func withCtx(fn func(ctx context.Context) tea.Msg) tea.Cmd {
	return withCtxT(ctxTimeout, fn)
}

func withCtxT(timeout time.Duration, fn func(ctx context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return fn(ctx)
	}
}

func bootCmd(d driver.Driver, tryAuth bool) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		m := bootMsg{}
		users, err := d.NumberUsers(ctx)
		if err != nil {
			m.ConnErr = err
			return m
		}
		m.Users = users
		if tryAuth && users > 0 {
			st, err := d.Connect(ctx)
			if err != nil {
				m.AuthErr = err
			} else {
				m.Status = st
			}
		}
		return m
	})
}

func loginCmd(d driver.Driver, setup bool, username, password string) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		var err error
		if setup {
			_, err = d.CreateUser(ctx, username, password)
		} else {
			_, err = d.Login(ctx, username, password)
		}
		return authMsg{Setup: setup, Err: err}
	})
}

func passwordCmd(d driver.Driver, currentPassword, newPassword string) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		err := d.UpdatePassword(ctx, currentPassword, newPassword)
		return opDoneMsg{Op: i18n.T("修改密码"), Err: err}
	})
}

func statusCmd(d driver.Driver) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		st, err := d.Connect(ctx)
		return statusMsg{Status: st, Err: err}
	})
}

func loadGroupsCmd(d driver.Driver) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		gs, err := d.ListGroups(ctx)
		return groupsMsg{Groups: gs, Err: err}
	})
}

type nodeMutation struct {
	kind   int
	links  []string
	link   string
	tag    string
	ids    []string
	doLink bool
}

func nodeMutateCmd(d driver.Driver, nm nodeMutation, label string) tea.Cmd {
	return withReloadNote(withCtxT(30*time.Second, func(ctx context.Context) tea.Msg {
		if nm.kind == 0 {
			report, err := d.ImportNodes(ctx, nm.links, nm.tag)
			if err != nil {
				return opDoneMsg{Op: label, Err: err, Idle: busyNodes}
			}
			nodes, lerr := d.ListManualNodes(ctx)
			if lerr != nil {

				return importDoneMsg{Results: report, Err: lerr}
			}
			return importDoneMsg{Results: report, Nodes: nodes}
		}
		var err error
		switch nm.kind {
		case 1:
			err = d.RemoveNodes(ctx, nm.ids)
		case 2:

			if err = d.TagNode(ctx, nm.ids[0], nm.tag); err == nil && nm.doLink {
				err = d.UpdateNode(ctx, nm.ids[0], nm.link)
			}
		}
		if err != nil {
			return opDoneMsg{Op: label, Err: err, Idle: busyNodes}
		}
		nodes, lerr := d.ListManualNodes(ctx)
		if lerr != nil {
			return opDoneMsg{Op: label, Err: lerr, Idle: busyNodes}
		}
		groups, gerr := d.ListGroups(ctx)
		if gerr != nil {
			return opDoneMsg{Op: label, Err: gerr, Idle: busyNodes}
		}
		return nodesChangedMsg{Nodes: nodes, Groups: groups}
	}), label)
}

func loadManualNodesCmd(d driver.Driver) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		ns, err := d.ListManualNodes(ctx)
		return manualNodesMsg{Nodes: ns, Err: err}
	})
}

func loadAttachCandidatesCmd(d driver.Driver) tea.Cmd {
	return withCtxT(60*time.Second, func(ctx context.Context) tea.Msg {
		msg := attachCandidatesMsg{}
		manual, err := d.ListManualNodes(ctx)
		if err != nil {
			msg.Err = err
			return msg
		}
		msg.Manual = manual
		subs, err := d.ListSubscriptions(ctx)
		if err != nil {
			msg.Err = err
			return msg
		}
		for i, sub := range subs {
			if i >= 10 {
				break
			}
			nodes, err := d.SubscriptionNodes(ctx, sub.ID)
			if err != nil {
				msg.Err = err
				return msg
			}
			msg.Subs = append(msg.Subs, subNodes{Tag: sub.Tag, Nodes: nodes})
		}
		return msg
	})
}

func loadSubNodesCmd(d driver.Driver, subID string) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		ns, err := d.SubscriptionNodes(ctx, subID)
		return subNodesMsg{SubID: subID, Nodes: ns, Err: err}
	})
}

type groupMutation struct {
	kind    int
	groupID string
	ids     []string
	filter  string
	name    string
	policy  string
}

func groupMutateCmd(d driver.Driver, gm groupMutation, label string) tea.Cmd {
	return withReloadNote(withCtx(func(ctx context.Context) tea.Msg {
		var err error
		switch gm.kind {
		case 0:
			err = d.AddGroupSubscriptions(ctx, gm.groupID, gm.ids, gm.filter)
		case 1:
			err = d.RemoveGroupSubscriptions(ctx, gm.groupID, gm.ids)
		case 2:
			err = d.AddGroupNodes(ctx, gm.groupID, gm.ids)
		case 3:
			err = d.CreateGroup(ctx, gm.name, gm.policy)
		case 4:
			err = d.RemoveGroup(ctx, gm.groupID)
		case 5:
			err = d.RenameGroup(ctx, gm.groupID, gm.name)
		case 6:
			err = d.SetGroupPolicy(ctx, gm.groupID, driver.Policy{Name: gm.policy})
		case 7:
			err = d.RemoveGroupNodes(ctx, gm.groupID, gm.ids)
		}
		if err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		gs, lerr := d.ListGroups(ctx)
		if lerr != nil {
			return opDoneMsg{Op: label, Err: lerr}
		}
		return groupsMsg{Groups: gs}
	}), label)
}

func switchNodeCmd(d driver.Driver, groupID string, p driver.Policy) tea.Cmd {
	return withReloadNote(withCtx(func(ctx context.Context) tea.Msg {
		if err := d.SetGroupPolicy(ctx, groupID, p); err != nil {
			return opDoneMsg{Op: i18n.T("切换节点"), Err: err}
		}
		gs, err := d.ListGroups(ctx)
		if err != nil {
			return opDoneMsg{Op: i18n.T("切换节点"), Err: err}
		}
		return groupsMsg{Groups: gs}
	}), i18n.T("切换节点"))
}

func testLatencyCmd(d driver.Driver, ids []string) tea.Cmd {
	return withCtxT(2*time.Minute, func(ctx context.Context) tea.Msg {
		if err := d.TestLatency(ctx, ids); err != nil {
			return opDoneMsg{Op: i18n.T("触发测速"), Err: err}
		}
		return opDoneMsg{Op: i18n.T("测速已触发")}
	})
}

func testWindow(nodeCount int) time.Duration {
	w := 15*time.Second + time.Duration(nodeCount)*200*time.Millisecond
	if w > 2*time.Minute {
		w = 2 * time.Minute
	}
	return w
}

func latenciesCmd(d driver.Driver, ids []string) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		lats, err := d.Latencies(ctx, ids)
		return latenciesMsg{Lats: lats, Err: err}
	})
}

func trafficCmd(d driver.Driver) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		start := time.Now()
		snap, err := d.Traffic(ctx, 10, 60)
		return trafficMsg{Snap: snap, Err: err, Took: time.Since(start)}
	})
}

func loadSubsCmd(d driver.Driver) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		subs, err := d.ListSubscriptions(ctx)
		return subsMsg{Subs: subs, Err: err}
	})
}

type subMutation struct {
	kind      int
	id        string
	ids       []string
	link, tag string
	cronExp   string
	cronOn    bool
	doLink    bool
}

func subMutateCmd(d driver.Driver, m subMutation, label string) tea.Cmd {
	return withReloadNote(withCtxT(30*time.Second, func(ctx context.Context) tea.Msg {
		var err error
		switch m.kind {
		case 0:
			err = d.AddSubscription(ctx, m.link, m.tag)
		case 1:
			err = d.UpdateSubscription(ctx, m.id)
		case 2:
			err = d.RemoveSubscriptions(ctx, m.ids)
		case 3:
			err = d.UpdateSubscriptionCron(ctx, m.id, m.cronExp, m.cronOn)
		case 4:

			if err = d.TagSubscription(ctx, m.id, m.tag); err == nil && m.doLink {
				err = d.UpdateSubscriptionLink(ctx, m.id, m.link)
			}
		}
		if err != nil {
			return opDoneMsg{Op: label, Err: err, Idle: busySubs}
		}
		subs, lerr := d.ListSubscriptions(ctx)
		if lerr != nil {
			return opDoneMsg{Op: label, Err: lerr, Idle: busySubs}
		}
		return subsMsg{Subs: subs}
	}), label)
}

func loadSelectionsCmd(d driver.Driver) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		sel, err := d.ListSelections(ctx)
		return selectionsMsg{Sel: sel, Err: err}
	})
}

func loadInterfacesCmd(d driver.Driver) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		ifs, err := d.Interfaces(ctx)
		return ifacesMsg{Ifaces: ifs, Err: err}
	})
}

// selectCmd switches the selected profile of one section; the label names
// the section so the pending-reload note reads like a change, not a verb.
func selectCmd(d driver.Driver, section, id string) tea.Cmd {
	label := i18n.T("切换选择")
	switch section {
	case "config":
		label = i18n.T("切换全局配置方案")
	case "dns":
		label = i18n.T("切换 DNS 方案")
	case "routing":
		label = i18n.T("切换路由方案")
	}
	return withReloadNote(withCtx(func(ctx context.Context) tea.Msg {
		var err error
		switch section {
		case "config":
			err = d.SelectConfig(ctx, id)
		case "dns":
			err = d.SelectDns(ctx, id)
		case "routing":
			err = d.SelectRouting(ctx, id)
		}
		if err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		sel, lerr := d.ListSelections(ctx)
		if lerr != nil {
			return opDoneMsg{Op: label, Err: lerr}
		}
		return selectionsMsg{Sel: sel}
	}), label)
}

type profileMutation struct {
	kind    int
	section string
	id      string
	name    string
	src     *driver.ConfigItem
}

func profileMutateCmd(d driver.Driver, pm profileMutation, label string) tea.Cmd {
	return withReloadNote(withCtxT(30*time.Second, func(ctx context.Context) tea.Msg {
		var err error
		switch pm.kind {
		case 0:
			err = d.CreateProfile(ctx, pm.section, pm.name, pm.src)
		case 1:
			err = d.RenameProfile(ctx, pm.section, pm.id, pm.name)
		case 2:
			err = d.RemoveProfile(ctx, pm.section, pm.id)
		}
		if err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		sel, lerr := d.ListSelections(ctx)
		if lerr != nil {
			return opDoneMsg{Op: label, Err: lerr}
		}
		return selectionsMsg{Sel: sel}
	}), label)
}

func configTextCmd(d driver.Driver, section, id, text, label string) tea.Cmd {
	return withReloadNote(withCtxT(30*time.Second, func(ctx context.Context) tea.Msg {
		var err error
		switch section {
		case "dns":
			err = d.UpdateDnsText(ctx, id, text)
		case "routing":
			err = d.UpdateRoutingText(ctx, id, text)
		}
		if err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		sel, lerr := d.ListSelections(ctx)
		if lerr != nil {
			return opDoneMsg{Op: label, Err: lerr}
		}
		return selectionsMsg{Sel: sel}
	}), label)
}

func configFieldCmd(d driver.Driver, id string, field driver.ConfigField, value, label string) tea.Cmd {
	return withReloadNote(withCtxT(30*time.Second, func(ctx context.Context) tea.Msg {
		if err := d.UpdateConfigField(ctx, id, field, value); err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		sel, lerr := d.ListSelections(ctx)
		if lerr != nil {
			return opDoneMsg{Op: label, Err: lerr}
		}
		return selectionsMsg{Sel: sel}
	}), label)
}

type editorDoneMsg struct {
	Path    string
	Section string
	ID      string
	Old     string
	Err     error
	Editor  string
}

type editorValidatedMsg struct {
	Path    string
	Section string
	ID      string
	Old     string
	Text    string
	Err     error
}

func validateTextCmd(d driver.Driver, section, id, text, old, path string) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		var err error
		switch section {
		case "dns":
			err = d.ValidateDns(ctx, text)
		case "routing":
			err = d.ValidateRouting(ctx, text)
		}
		return editorValidatedMsg{Path: path, Section: section, ID: id, Old: old, Text: text, Err: err}
	})
}

type presetValidatedMsg struct {
	Section string
	ID      string
	Text    string
	Label   string
	Err     error
}

func presetTextCmd(d driver.Driver, section, id, text, label string) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		var err error
		switch section {
		case "dns":
			err = d.ValidateDns(ctx, text)
		case "routing":
			err = d.ValidateRouting(ctx, text)
		}
		return presetValidatedMsg{Section: section, ID: id, Text: text, Label: label, Err: err}
	})
}

func runCmd(d driver.Driver, dry bool) tea.Cmd {
	label := i18n.T("重载 (run)")
	if dry {
		label = i18n.T("校验配置 (dry-run)")
	}
	return withCtx(func(ctx context.Context) tea.Msg {
		err := d.Run(ctx, dry)
		return opDoneMsg{Op: label, Err: err}
	})
}

func runToggleCmd(d driver.Driver, running bool) tea.Cmd {
	label := i18n.T("启动代理")
	dry := false
	if running {
		label = i18n.T("停止代理 (run dry)")
		dry = true
	}
	return withCtx(func(ctx context.Context) tea.Msg {
		err := d.Run(ctx, dry)
		if err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		st, serr := d.Connect(ctx)
		return statusMsg{Status: st, Err: serr}
	})
}

func spinnerTickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinnerMsg{} })
}

func tickCmd(n int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return tickMsg{n: n + 1}
	})
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
