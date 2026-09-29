package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/config"
	"dae-tui/internal/driver"
	"dae-tui/internal/i18n"
	"dae-tui/internal/ui"
)

// Pinned-node support, built on a dedicated daed group instead of mutating
// the group the node came from (the old fixed-node flow silently rebuilt
// whole groups; see AGENTS.md for why that was removed).
//
// Pinning rewrites the managed group's membership to the one node and
// re-points the selected routing's references from the source group to the
// managed group. The source group keeps its members and subscriptions, so
// unpinning is a plain routing rewrite back — nothing to restore server-side.
//
// daed facts this relies on (verified in the daed-wing source):
//   - the fixed single-member constraint is checked at reload time, and only
//     for groups the routing references; an unreferenced group is inert;
//   - a fixed group with exactly one member is the only order-stable fixed
//     configuration (multi-member groups render via map iteration);
//   - subscription refresh keeps nodes that are attached to a group, so the
//     pin survives provider churn; deleting the subscription deletes them
//     unconditionally, which the home page flags as the empty-pinned-group
//     reload blocker.

// pinGroupName is the canonical name of the managed group. Collision with a
// user-created group of the same name falls back to pinned2, pinned3, …
const pinGroupName = "pinned"

// pinState mirrors the persisted pin (config.Pin) plus runtime flags the
// root model re-derives whenever groups or selections arrive. No translated
// copy lives here: pinState is long-lived, so it stores data only.
type pinState struct {
	groupID string // daed ID of the managed group; survives unpin
	restore string // outbound the pin displaced; "" = no pin recorded

	nodeID string
	node   string // display name of the pinned node; "" = unknown/missing
	subID  string // subscription the pinned node belongs to ("" = manual)

	active bool // the selected routing still references the managed group
	empty  bool // active pin whose group has no member left: reload would fail
}

type pinRequestedMsg struct {
	NodeID    string
	NodeName  string
	FromGroup string
}

type unpinRequestedMsg struct{}

// switchGroupRequestedMsg re-points the selected routing's group references
// at another group (home `S` / groups page `S`). From is the group the
// routing currently references — the root model's rederive-mapped answer,
// i.e. the managed pinned group while a pin is active.
type switchGroupRequestedMsg struct {
	From, To string
}

// referencedGroups turns a routing's reference list into the set of group
// names worth marking as "in use" (builtins dropped: a group sharing a
// builtin's name is shadowed by it in the DSL anyway).
func referencedGroups(refs []string) map[string]bool {
	out := make(map[string]bool, len(refs))
	for _, r := range refs {
		if !isBuiltinOutbound(r) {
			out[r] = true
		}
	}
	return out
}

// switchGroupCmd re-points the selected routing's references from one group
// to another — the pin machinery without the pinned group. The managed group
// on either end doubles the switch as pin bookkeeping: as From (a live pin)
// the rewrite dissolves it — the state is cleared, the group stays; as To the
// switch re-arms the recorded pin with From as the new restore, re-using the
// fixed(0) node that never left the group (S beats re-pinning by hand).
func switchGroupCmd(d driver.Driver, cfgr *config.Config, cfgPath, from, to, managed string) tea.Cmd {
	label := i18n.T("切换代理组 ") + from + " → " + to
	return withCtxT(30*time.Second, func(ctx context.Context) tea.Msg {
		if from == to {
			return opDoneMsg{Op: label, Err: errors.New(i18n.T("目标组就是当前组"))}
		}
		sel, err := d.ListSelections(ctx)
		if err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		rt, ok := selectedRouting(sel)
		if !ok {
			return opDoneMsg{Op: label, Err: errors.New(i18n.T("没有选中的路由方案"))}
		}
		next, n := rewriteOutboundRefs(rt.Body, from, to)
		if n == 0 {
			return opDoneMsg{Op: label, Err: fmt.Errorf(
				i18n.T("路由方案 %s 未引用组 %s，切换不会改变流量走向"), rt.Name, from)}
		}
		if err := d.ValidateRouting(ctx, next); err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		if err := d.UpdateRoutingText(ctx, rt.ID, next); err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		// Pin bookkeeping, direction-aware: switching to the managed group
		// re-arms the pin (From becomes the restore home's x points back at);
		// any other switch while a state is recorded dissolves it, so home
		// stops showing a pin the routing no longer has. Only write on change.
		nextRestore := cfgr.Pin.Restore
		switch {
		case managed != "" && to == managed:
			nextRestore = from
		case cfgr.Pin.Restore != "":
			nextRestore = ""
		}
		if nextRestore != cfgr.Pin.Restore {
			if err := cfgr.UpdatePin(cfgPath, cfgr.Pin.GroupID, nextRestore); err != nil {
				return opDoneMsg{Op: label, Err: fmt.Errorf(
					i18n.T("切换已生效，但写入 config.toml 失败: %v"), err)}
			}
		}
		return tea.BatchMsg{
			func() tea.Msg {
				return opDoneMsg{Op: i18n.T("已切换到 %s（A 重载后生效）", to)}
			},
			loadGroupsCmd(d),
			loadSelectionsCmd(d),
			reloadNoteCmd(label),
		}
	})
}

// outboundOf extracts a line's outbound token: the target after the last
// "->", or a fallback line's value. Comments and blank lines report ok=false.
func outboundOf(line string) (out string, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", false
	}
	var tail string
	switch {
	case strings.Contains(t, "->"):
		tail = line[strings.LastIndex(line, "->")+2:]
	case strings.HasPrefix(t, "fallback:"):
		tail = line[strings.Index(line, "fallback:")+len("fallback:"):]
	default:
		return "", false
	}
	out = strings.TrimSpace(tail)
	return out, out != ""
}

// rewriteOutboundRefs rewrites routing DSL lines whose outbound is exactly
// `from` — or its must_-prefixed form, which carries the same "no fallback"
// modifier over to the replacement — to `to`. Everything else is returned
// verbatim; the changed-reference count lets callers refuse a rewrite that
// would do nothing. Builtin outbounds are refused on either side: a group
// named like a builtin is shadowed in the DSL anyway, and rewriting e.g.
// must_direct would change unrelated semantics.
func rewriteOutboundRefs(raw, from, to string) (string, int) {
	if from == "" || to == "" || from == to ||
		isBuiltinOutbound(from) || isBuiltinOutbound(to) {
		return raw, 0
	}
	var b strings.Builder
	n := 0
	for i, line := range strings.Split(raw, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		newLine, changed := rewriteOutboundLine(line, from, to)
		if changed {
			n++
		}
		b.WriteString(newLine)
	}
	return b.String(), n
}

// rewriteOutboundLine rewrites one line's outbound token in place, keeping
// the line's own indentation and the spacing around the token. The match is
// anchored strictly to the outbound slot: a plain substring replace would
// also hit the same word inside a condition (group "cn" vs dip(geoip:cn)).
// A trailing comment on the line suppresses the rewrite rather than risk
// mangling it — the confirm overlay shows the reference count, so a skipped
// line is visible before anything is applied.
func rewriteOutboundLine(line, from, to string) (string, bool) {
	out, ok := outboundOf(line)
	if !ok {
		return line, false
	}
	repl := ""
	switch out {
	case from:
		repl = to
	case "must_" + from:
		repl = "must_" + to
	default:
		return line, false
	}
	tailStart := strings.LastIndex(line, "->")
	prefixLen := tailStart + 2
	if tailStart < 0 {
		prefixLen = strings.Index(line, "fallback:") + len("fallback:")
	}
	tail := line[prefixLen:]
	i := strings.Index(tail, out)
	return line[:prefixLen] + tail[:i] + repl + tail[i+len(out):], true
}

// countOutboundRefs counts routing DSL references to `from` (plain and
// must_-prefixed) without rewriting anything.
func countOutboundRefs(raw, from string) int {
	if from == "" || isBuiltinOutbound(from) {
		return 0
	}
	n := 0
	for _, line := range strings.Split(raw, "\n") {
		if out, ok := outboundOf(line); ok && (out == from || out == "must_"+from) {
			n++
		}
	}
	return n
}

// resolvePinGroup finds the managed group by its stored ID only. Adopting a
// same-named stranger is deliberately not done: rewriting some unrelated
// group's membership because it happens to be called "pinned" is exactly the
// kind of silent destruction this feature exists to avoid.
func resolvePinGroup(groups []driver.Group, storedID string) *driver.Group {
	if storedID == "" {
		return nil
	}
	for i := range groups {
		if groups[i].ID == storedID {
			return &groups[i]
		}
	}
	return nil
}

// freePinGroupName picks the name createPinGroup will use: the canonical
// one, or the first suffixed fallback that is not taken.
func freePinGroupName(groups []driver.Group) string {
	taken := map[string]bool{}
	for _, g := range groups {
		taken[g.Name] = true
	}
	for i := 0; i < 10; i++ {
		name := pinGroupName
		if i > 0 {
			name = fmt.Sprintf("%s%d", pinGroupName, i+1)
		}
		if !taken[name] {
			return name
		}
	}
	return ""
}

func selectedRouting(sel driver.Selections) (driver.ConfigItem, bool) {
	for _, r := range sel.Routings {
		if r.Selected {
			return r, true
		}
	}
	return driver.ConfigItem{}, false
}

func opErrCmd(op string, err error) tea.Cmd {
	return func() tea.Msg { return opDoneMsg{Op: op, Err: err} }
}

// pinGuardCmd is the toast every managed-group mutation attempt earns. The
// daed API would accept most of them; refusing is about not leaving the pin
// in a state our own unpin flow no longer understands.
func pinGuardCmd(what string) tea.Cmd {
	return opErrCmd(what, errors.New(i18n.T(
		"固定节点组由 TUI 管理：群组页 f 换固定节点，首页 x 解除固定")))
}

// pinNodeCmd pins one node. Steps run inside a single command because each
// depends on the previous one's success (a tea.Batch would race them).
// Ordering: compute+validate the routing rewrite first (a syntax problem
// aborts before anything changes server-side), then membership/policy, and
// the routing update last — until it lands, the pin simply is not active,
// which is the safe intermediate state. Reload stays with the global A
// confirm; daed's version bump makes the top bar flag it.
func pinNodeCmd(d driver.Driver, cfgr *config.Config, cfgPath, nodeID, nodeName, fromGroup string) tea.Cmd {
	label := i18n.T("固定节点 ") + ui.SpaceAfterFlag(nodeName)
	return withCtxT(30*time.Second, func(ctx context.Context) tea.Msg {
		groups, err := d.ListGroups(ctx)
		if err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		g := resolvePinGroup(groups, cfgr.Pin.GroupID)
		target := pinGroupName
		if g != nil {
			target = g.Name
		} else if free := freePinGroupName(groups); free != "" {
			target = free
		}

		sel, err := d.ListSelections(ctx)
		if err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		rt, ok := selectedRouting(sel)
		if !ok {
			return opDoneMsg{Op: label, Err: errors.New(i18n.T("没有选中的路由方案，固定无处指路"))}
		}
		raw := rt.Body
		// A pin is already recorded: put its references back before pointing
		// the new group's lines at the managed group, otherwise unpinning
		// later would re-route the previous group's traffic to the new one.
		if cfgr.Pin.Restore != "" && g != nil {
			raw, _ = rewriteOutboundRefs(raw, target, cfgr.Pin.Restore)
		}
		next, n := rewriteOutboundRefs(raw, fromGroup, target)
		if n == 0 {
			return opDoneMsg{Op: label, Err: fmt.Errorf(
				i18n.T("路由方案 %s 未引用组 %s，固定不会改变流量走向"), rt.Name, fromGroup)}
		}
		if err := d.ValidateRouting(ctx, next); err != nil {
			return opDoneMsg{Op: label, Err: err}
		}

		if g == nil {
			if err := d.CreateGroup(ctx, target, "min_moving_avg"); err != nil {
				return opDoneMsg{Op: label, Err: err}
			}
			gs, err := d.ListGroups(ctx)
			if err != nil {
				return opDoneMsg{Op: label, Err: err}
			}
			for i := range gs {
				if gs[i].Name == target {
					g = &gs[i]
					break
				}
			}
			if g == nil {
				return opDoneMsg{Op: label, Err: errors.New(i18n.T("创建固定组后未在群组列表中找到它"))}
			}
		}
		// Membership swap: drop every other member and any subscription
		// attached outside the TUI, then make the target the single direct
		// node. daed only checks fixed groups at reload, and only the ones
		// the routing references, so these intermediate states are inert.
		var drop []string
		has := false
		for _, m := range g.Nodes {
			if m.ID == nodeID {
				has = true
			} else {
				drop = append(drop, m.ID)
			}
		}
		if len(drop) > 0 {
			if err := d.RemoveGroupNodes(ctx, g.ID, drop); err != nil {
				return opDoneMsg{Op: label, Err: err}
			}
		}
		for _, s := range g.Subscriptions {
			if err := d.RemoveGroupSubscriptions(ctx, g.ID, []string{s.SubscriptionID}); err != nil {
				return opDoneMsg{Op: label, Err: err}
			}
		}
		if !has {
			if err := d.AddGroupNodes(ctx, g.ID, []string{nodeID}); err != nil {
				return opDoneMsg{Op: label, Err: err}
			}
		}
		if err := d.SetGroupPolicy(ctx, g.ID, driver.Policy{Name: "fixed", FixedIndex: 0}); err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		if err := d.UpdateRoutingText(ctx, rt.ID, next); err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		if err := cfgr.UpdatePin(cfgPath, g.ID, fromGroup); err != nil {
			return opDoneMsg{Op: label, Err: fmt.Errorf(
				i18n.T("固定已生效，但写入 config.toml 失败: %v（解除固定前请在 5 配置页手动改回路由）"), err)}
		}
		return tea.BatchMsg{
			func() tea.Msg {
				return opDoneMsg{Op: i18n.T("已固定到 %s（A 重载后生效）", ui.SpaceAfterFlag(nodeName))}
			},
			loadGroupsCmd(d),
			loadSelectionsCmd(d),
			reloadNoteCmd(i18n.T("固定节点 %s", ui.SpaceAfterFlag(nodeName))),
		}
	})
}

// unpinNodeCmd points the selected routing's references back to the group
// the pin displaced and clears the recorded state. The managed group and its
// member are left alone: unreferenced groups are inert, and keeping the ID
// turns the next pin into a pure membership swap.
func unpinNodeCmd(d driver.Driver, cfgr *config.Config, cfgPath string) tea.Cmd {
	label := i18n.T("解除固定")
	return withCtxT(30*time.Second, func(ctx context.Context) tea.Msg {
		restore := cfgr.Pin.Restore
		if restore != "" {
			sel, err := d.ListSelections(ctx)
			if err != nil {
				return opDoneMsg{Op: label, Err: err}
			}
			groups, err := d.ListGroups(ctx)
			if err != nil {
				return opDoneMsg{Op: label, Err: err}
			}
			if rt, ok := selectedRouting(sel); ok {
				if g := resolvePinGroup(groups, cfgr.Pin.GroupID); g != nil {
					// Zero matches is the stale case (routing was switched or
					// hand-edited away from the managed group): the rewrite is
					// a no-op and unpin degrades to clearing the state.
					if next, _ := rewriteOutboundRefs(rt.Body, g.Name, restore); next != rt.Body {
						if err := d.ValidateRouting(ctx, next); err != nil {
							return opDoneMsg{Op: label, Err: err}
						}
						if err := d.UpdateRoutingText(ctx, rt.ID, next); err != nil {
							return opDoneMsg{Op: label, Err: err}
						}
					}
				}
			}
		}
		if err := cfgr.UpdatePin(cfgPath, cfgr.Pin.GroupID, ""); err != nil {
			return opDoneMsg{Op: label, Err: err}
		}
		return tea.BatchMsg{
			func() tea.Msg { return opDoneMsg{Op: i18n.T("已解除固定（A 重载后生效）")} },
			loadGroupsCmd(d),
			loadSelectionsCmd(d),
			reloadNoteCmd(i18n.T("解除固定节点")),
		}
	})
}
