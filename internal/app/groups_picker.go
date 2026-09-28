package app

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/driver"
	"dae-tui/internal/i18n"
	"dae-tui/internal/ui"
)

func (p *groupsPage) pickerKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch p.mode {
	case pickDetach:
		return p.detachConfirmKey(msg, d)
	case pickDeleteGroup:
		return p.deleteGroupConfirmKey(msg, d)
	case pickRemoveNode:
		return p.removeNodeConfirmKey(msg, d)
	case pickRemoveNodes:
		return p.removeNodesConfirmKey(msg, d)
	case inputCreate, inputRename:
		return p.groupNameInputKey(msg, d)
	case pickSub, pickNode, pickPolicy:
		return p.pickerNavKey(msg, d)
	}
	return nil
}

func (p *groupsPage) detachConfirmKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "y":
		r := p.cur()
		p.mode = pickNone
		if r == nil || r.gi >= len(p.groups) || r.si >= len(p.groups[r.gi].Subscriptions) {
			return nil
		}
		sub := p.groups[r.gi].Subscriptions[r.si]
		return groupMutateCmd(d, groupMutation{kind: 1, groupID: p.groups[r.gi].ID,
			ids: []string{sub.SubscriptionID}}, i18n.T("移除组内订阅 ")+sub.Tag)
	case "n", "esc", "enter":
		p.mode = pickNone
	}
	return nil
}

func (p *groupsPage) deleteGroupConfirmKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "y":
		g := p.curGroup()
		p.mode = pickNone
		if g == nil {
			return nil
		}
		return groupMutateCmd(d, groupMutation{kind: 4, groupID: g.ID}, i18n.T("删除群组 ")+g.Name)
	case "n", "esc", "enter":
		p.mode = pickNone
	}
	return nil
}

func (p *groupsPage) removeNodeConfirmKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "y":
		r := p.cur()
		p.mode = pickNone
		if r == nil || r.kind != rowNode {
			return nil
		}
		return groupMutateCmd(d, groupMutation{kind: 7, groupID: p.groups[r.gi].ID,
			ids: []string{r.node.ID}}, i18n.T("移除组内节点 ")+ui.SpaceAfterFlag(r.node.Name))
	case "n", "esc", "enter":
		p.mode = pickNone
	}
	return nil
}

func (p *groupsPage) removeNodesConfirmKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "y":
		nodes := p.markedDirectNodes()
		p.mode = pickNone
		p.marked = map[string]bool{}
		if len(nodes) == 0 {
			return nil
		}
		ids := make([]string, len(nodes))
		for i, n := range nodes {
			ids[i] = n.ID
		}
		return groupMutateCmd(d, groupMutation{kind: 7, groupID: p.groups[p.gi].ID, ids: ids},
			i18n.T("移除组内 ")+strconv.Itoa(len(ids))+i18n.T(" 个节点"))
	case "n", "esc", "enter":
		p.mode = pickNone
	}
	return nil
}

func (p *groupsPage) groupNameInputKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "esc":
		p.mode = pickNone
		p.input.Blur()
		return nil
	case "enter":
		name := strings.TrimSpace(p.input.Value())
		isCreate := p.mode == inputCreate
		p.mode = pickNone
		p.input.Blur()
		if name == "" {
			return nil
		}
		if isCreate {
			return groupMutateCmd(d, groupMutation{kind: 3, name: name, policy: "min_moving_avg"},
				i18n.T("创建群组 ")+name)
		}
		g := p.curGroup()
		if g == nil {
			return nil
		}
		return groupMutateCmd(d, groupMutation{kind: 5, groupID: g.ID, name: name},
			i18n.T("重命名群组 ")+g.Name+" → "+name)
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

func (p *groupsPage) pickerNavKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	var n int
	switch p.mode {
	case pickSub:
		if g := p.curGroup(); g != nil {
			n = len(p.pickableSubs(g))
		}
	case pickNode:
		n = len(p.visibleCandidates())
	case pickPolicy:
		n = len(policyChoices)
	}
	switch msg.String() {
	case "esc":
		p.mode = pickNone
		p.candBusy = false

		p.pickMarked = map[string]bool{}
	case " ":

		if p.mode == pickNode {
			rows := p.visibleCandidates()
			if p.pickCursor < len(rows) {
				id := rows[p.pickCursor].node.ID
				if p.pickMarked[id] {
					delete(p.pickMarked, id)
				} else {
					p.pickMarked[id] = true
				}
			}
		}
	case "j", "down":
		if p.pickCursor < n-1 {
			p.pickCursor++
		}
	case "k", "up":
		if p.pickCursor > 0 {
			p.pickCursor--
		}
	case "enter":
		g := p.curGroup()
		if g == nil || p.pickCursor >= n {
			return nil
		}
		switch p.mode {
		case pickSub:
			sub := p.pickableSubs(g)[p.pickCursor]
			p.mode = pickNone
			return groupMutateCmd(d, groupMutation{kind: 0, groupID: g.ID, ids: []string{sub.ID}},
				i18n.T("添加订阅 ")+sub.Tag+i18n.T(" 到组 ")+g.Name)
		case pickNode:
			rows := p.visibleCandidates()
			if p.pickCursor >= len(rows) {
				p.mode = pickNone
				return nil
			}

			ids := p.markedCandidateIDs(rows)
			if len(ids) == 0 {
				ids = []string{rows[p.pickCursor].node.ID}
			}
			p.mode = pickNone
			p.pickMarked = map[string]bool{}
			return groupMutateCmd(d, groupMutation{kind: 2, groupID: g.ID, ids: ids},
				i18n.T("添加 ")+strconv.Itoa(len(ids))+i18n.T(" 个节点到组 ")+g.Name)
		case pickPolicy:
			choice := policyChoices[p.pickCursor]
			p.mode = pickNone
			return groupMutateCmd(d, groupMutation{kind: 6, groupID: g.ID, policy: choice.name},
				g.Name+i18n.T(" 策略改为 ")+i18n.T(choice.label))
		}
	}
	return nil
}
