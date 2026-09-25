package app

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/driver"
)

// --- messages ---

type tickMsg struct{ n int }

// bootMsg is the result of the initial connectivity/authentication probe.
type bootMsg struct {
	Users   int
	Status  driver.Status
	ConnErr error // endpoint unreachable / HTTP error
	AuthErr error // driver.ErrNeedAuth (credentials missing or rejected)
}

// authMsg is the result of a login or first-run account creation attempt.
type authMsg struct {
	Setup bool // true when this was a createUser attempt
	Err   error
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

// subNodes is one subscription's node list, for the attach picker.
type subNodes struct {
	Tag   string
	Nodes []driver.Node
}

// attachCandidatesMsg carries everything pickable via `n`: manual nodes
// plus every subscription's nodes.
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
}

type subsMsg struct {
	Subs []driver.Subscription
	Err  error
}

type selectionsMsg struct {
	Sel driver.Selections
	Err error
}

// opDoneMsg reports completion of a fire-and-forget mutation.
type opDoneMsg struct {
	Op  string // human label for the toast
	Err error
}

// --- commands ---

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

// nodeMutation manages manual (subscription-less) nodes.
type nodeMutation struct {
	kind      int // 0 import, 1 remove
	link, tag string
	ids       []string
}

func nodeMutateCmd(d driver.Driver, nm nodeMutation, label string) tea.Cmd {
	return withCtxT(30*time.Second, func(ctx context.Context) tea.Msg {
		var err error
		switch nm.kind {
		case 0:
			err = d.ImportNode(ctx, nm.link, nm.tag)
		case 1:
			err = d.RemoveNodes(ctx, nm.ids)
		}
		if err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		nodes, lerr := d.ListManualNodes(ctx)
		if lerr != nil {
			return opDoneMsg{Op: label, Err: lerr}
		}
		return manualNodesMsg{Nodes: nodes}
	})
}

func loadManualNodesCmd(d driver.Driver) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		ns, err := d.ListManualNodes(ctx)
		return manualNodesMsg{Nodes: ns, Err: err}
	})
}

// loadAttachCandidatesCmd fetches manual nodes plus all subscriptions'
// nodes in one go (subscriptions are capped defensively).
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
			if i >= 10 { // defensive cap on subscription count
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
	kind    int // 0 addSubs, 1 delSubs, 2 addNodes, 3 create, 4 delete, 5 rename, 6 setPolicy, 7 delNodes
	groupID string
	ids     []string
	filter  string
	name    string // create/rename
	policy  string // create/setPolicy
}

func groupMutateCmd(d driver.Driver, gm groupMutation, label string) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
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
	})
}

// switchNodeCmd applies a policy to a group and reloads the group list.
func switchNodeCmd(d driver.Driver, groupID string, p driver.Policy) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		if err := d.SetGroupPolicy(ctx, groupID, p); err != nil {
			return opDoneMsg{Op: "切换节点", Err: err}
		}
		gs, err := d.ListGroups(ctx)
		if err != nil {
			return opDoneMsg{Op: "切换节点", Err: err}
		}
		return groupsMsg{Groups: gs}
	})
}

func testLatencyCmd(d driver.Driver, ids []string) tea.Cmd {
	return withCtxT(2*time.Minute, func(ctx context.Context) tea.Msg {
		if err := d.TestLatency(ctx, ids); err != nil {
			return opDoneMsg{Op: "触发测速", Err: err}
		}
		return opDoneMsg{Op: "测速已触发"}
	})
}

func latenciesCmd(d driver.Driver, ids []string) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		lats, err := d.Latencies(ctx, ids)
		return latenciesMsg{Lats: lats, Err: err}
	})
}

func trafficCmd(d driver.Driver) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		snap, err := d.Traffic(ctx, 10, 60)
		return trafficMsg{Snap: snap, Err: err}
	})
}

func loadSubsCmd(d driver.Driver) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		subs, err := d.ListSubscriptions(ctx)
		return subsMsg{Subs: subs, Err: err}
	})
}

type subMutation struct {
	kind      int // 0 add, 1 update, 2 remove, 3 cron
	id        string
	ids       []string
	link, tag string
	cronExp   string
	cronOn    bool
}

func subMutateCmd(d driver.Driver, m subMutation, label string) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
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
		}
		if err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		subs, lerr := d.ListSubscriptions(ctx)
		if lerr != nil {
			return opDoneMsg{Op: label, Err: lerr}
		}
		return subsMsg{Subs: subs}
	})
}

func loadSelectionsCmd(d driver.Driver) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		sel, err := d.ListSelections(ctx)
		return selectionsMsg{Sel: sel, Err: err}
	})
}

func selectCmd(d driver.Driver, section, id string) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
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
			return opDoneMsg{Op: "切换选择", Err: err}
		}
		sel, lerr := d.ListSelections(ctx)
		if lerr != nil {
			return opDoneMsg{Op: "切换选择", Err: lerr}
		}
		return selectionsMsg{Sel: sel}
	})
}

// profileMutation manages config/dns/routing profiles.
type profileMutation struct {
	kind    int // 0 create, 1 rename, 2 remove
	section string
	id      string
	name    string
	src     *driver.ConfigItem // clone source for create
}

func profileMutateCmd(d driver.Driver, pm profileMutation, label string) tea.Cmd {
	return withCtxT(30*time.Second, func(ctx context.Context) tea.Msg {
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
	})
}

func configTextCmd(d driver.Driver, section, id, text, label string) tea.Cmd {
	return withCtxT(30*time.Second, func(ctx context.Context) tea.Msg {
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
	})
}

func configFieldCmd(d driver.Driver, id string, field driver.ConfigField, value, label string) tea.Cmd {
	return withCtxT(30*time.Second, func(ctx context.Context) tea.Msg {
		if err := d.UpdateConfigField(ctx, id, field, value); err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		sel, lerr := d.ListSelections(ctx)
		if lerr != nil {
			return opDoneMsg{Op: label, Err: lerr}
		}
		return selectionsMsg{Sel: sel}
	})
}

// editorDoneMsg is emitted after $EDITOR exits (dns/routing DSL editing).
type editorDoneMsg struct {
	Path    string
	Section string
	ID      string
	Old     string
	Err     error
	Editor  string // resolved editor command, for error messages
}

// editorValidatedMsg is the result of parsing edited DSL through the backend
// before it replaces a stored profile. Path names the temp file, which is
// still on disk when Err != nil so a rejected edit is never lost.
type editorValidatedMsg struct {
	Path    string
	Section string
	ID      string
	Text    string
	Err     error
}

// validateTextCmd runs the edited DSL through the backend parser. It is a
// separate step (not folded into the submit) so a syntax error can be shown
// without the edited file having been deleted yet.
func validateTextCmd(d driver.Driver, section, id, text, path string) tea.Cmd {
	return withCtx(func(ctx context.Context) tea.Msg {
		var err error
		switch section {
		case "dns":
			err = d.ValidateDns(ctx, text)
		case "routing":
			err = d.ValidateRouting(ctx, text)
		}
		return editorValidatedMsg{Path: path, Section: section, ID: id, Text: text, Err: err}
	})
}

// presetValidatedMsg is the result of validating a rendered routing preset
// before it replaces a stored profile. Unlike the $EDITOR flow there is no
// temp file to keep: the text is regenerated on demand.
type presetValidatedMsg struct {
	Section string
	ID      string
	Text    string
	Label   string // toast label for the submit
	Err     error
}

// presetTextCmd validates rendered preset DSL through the backend parser and
// submits it on success. Presets are generated, not typed, so a rejection
// here means the proxy group name cannot be interpolated — the error goes
// back to the page that started the switch.
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
	label := "应用配置 (run)"
	if dry {
		label = "校验配置 (dry-run)"
	}
	return withCtx(func(ctx context.Context) tea.Msg {
		err := d.Run(ctx, dry)
		return opDoneMsg{Op: label, Err: err}
	})
}

// runToggleCmd flips the proxy: daed's run(dry:true) stops the proxy,
// run(dry:false) starts it with the selected config+dns+routing.
func runToggleCmd(d driver.Driver, running bool) tea.Cmd {
	label := "启动代理"
	dry := false
	if running {
		label = "停止代理 (run dry)"
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
