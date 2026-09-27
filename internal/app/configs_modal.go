package app

// configsPage's in-right-pane modals, split out of configs.go: the modes
// share no state beyond the page itself, and each mode's key handling reads
// better on its own than as a case in a 200-line switch. The numeric modes
// are documented on configsPage.mode.

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/driver"
)

// modalKey routes the current modal's keys. Modes 0 (list) never reach here:
// handleKey checks mode != 0 first.
func (p *configsPage) modalKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch p.mode {
	case 1: // field picker (config global fields)
		return p.fieldPickerKey(msg, d)
	case 2: // field input
		return p.fieldInputKey(msg, d)
	case 3, 4: // create / rename input
		return p.nameInputKey(msg, d)
	case 5: // delete confirm
		return p.deleteConfirmKey(msg, d)
	case 6: // DSL diff confirm
		return p.diffConfirmKey(msg, d)
	case 7: // builtin DSL editor
		return p.builtinEditorKey(msg, d)
	}
	return nil
}

func (p *configsPage) fieldPickerKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	r := p.curRow()
	if r == nil {
		p.mode = 0
		return nil
	}
	it := p.item(*r)
	fields := orderedFields(it.Fields)
	// The field list can change while the picker is open (the refreshed
	// selection after a submit); keep the cursor on a real row.
	if p.pickCursor >= len(fields) {
		p.pickCursor = max0(len(fields) - 1)
	}
	switch msg.String() {
	case "esc":
		p.mode = 0
	case "j", "down":
		if p.pickCursor < len(fields)-1 {
			p.pickCursor++
		}
	case "k", "up":
		if p.pickCursor > 0 {
			p.pickCursor--
		}
	case "enter":
		if p.pickCursor < len(fields) {
			p.editField = fields[p.pickCursor]
			p.fieldErr = ""
			p.mode = 2
			return p.openInput("新值 ("+p.editField.Type+")", p.editField.Value)
		}
	}
	return nil
}

func (p *configsPage) fieldInputKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "esc":
		p.mode = 1
		p.input.Blur()
		return nil
	case "enter":
		val := strings.TrimSpace(p.input.Value())
		f := p.editField
		r := p.curRow()
		if r == nil {
			p.mode = 0
			p.input.Blur()
			return nil
		}
		if val == "" || val == f.Value {
			// Nothing to submit: back to the picker, which stays open
			// for the next field.
			p.mode = 1
			p.input.Blur()
			return nil
		}
		// Catch a type mismatch here: the backend would reject it too,
		// but only after a round trip and with a less pointed message.
		if err := validateFieldValue(f, val); err != nil {
			p.fieldErr = err.Error()
			return nil
		}
		// Stay in the picker once the edit lands: multi-field sessions
		// (checkInterval + checkTolerance + …) are the common case, the
		// refreshed value shows up on the picker row itself, and esc is
		// the explicit way out.
		p.mode = 1
		p.input.Blur()
		it := p.item(*r)
		return configFieldCmd(d, it.ID, f, val, "修改 "+fieldLabel(f))
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

// nameInputKey drives the create (3) / rename (4) input. `c` clones the
// profile under the cursor (a section header means its selected one) — the
// same item e/R/D act on, not always the section's selected profile.
func (p *configsPage) nameInputKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "esc":
		p.mode = 0
		p.input.Blur()
		return nil
	case "enter":
		name := strings.TrimSpace(p.input.Value())
		kind := p.mode
		r := p.curRow()
		p.mode = 0
		p.input.Blur()
		if name == "" || r == nil {
			return nil
		}
		if kind == 3 {
			return profileMutateCmd(d, profileMutation{kind: 0, section: r.section, name: name,
				src: p.item(*r)},
				"创建"+sectionName(r.section)+" "+name)
		}
		it := p.item(*r)
		return profileMutateCmd(d, profileMutation{kind: 1, section: r.section, id: it.ID, name: name},
			"重命名为 "+name)
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

func (p *configsPage) deleteConfirmKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "y":
		r := p.curRow()
		p.mode = 0
		if r == nil {
			return nil
		}
		it := p.item(*r)
		return profileMutateCmd(d, profileMutation{kind: 2, section: r.section, id: it.ID},
			"删除"+sectionName(r.section)+" "+it.Name)
	case "n", "esc", "enter":
		p.mode = 0
	}
	return nil
}

// diffConfirmKey answers the validated-edit confirmation. Declining keeps
// the edit's only copy: the temp file ($EDITOR path) or the floating editor
// itself (builtin).
func (p *configsPage) diffConfirmKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "y":
		st := p.diff
		p.diff, p.mode = nil, 0
		p.scroll = 0 // the diff's scroll offset must not follow the pane
		if st == nil {
			return nil
		}
		if !st.Builtin {
			os.Remove(st.Path)
		}
		delete(p.validateErr, st.ID)
		return configTextCmd(d, st.Section, st.ID, st.Text, "更新"+sectionName(st.Section)+" 内容")
	case "n", "esc":
		st := p.diff
		p.diff, p.mode = nil, 0
		p.scroll = 0
		if st == nil {
			return nil
		}
		if st.Builtin {
			// Back into the floating editor with the edited text: the
			// in-app editor IS the copy that survives a decline.
			return p.reopenBuiltinEditor(st)
		}
		// The edit is declined, but the temp file is the only copy of
		// it — keep it and say where.
		return func() tea.Msg {
			return opDoneMsg{Op: "编辑", Err: fmt.Errorf("已取消，编辑内容保留在 %s", st.Path)}
		}
	case "j", "down":
		p.scroll++
	case "k", "up":
		if p.scroll > 0 {
			p.scroll--
		}
	}
	return nil
}

func (p *configsPage) builtinEditorKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "esc":
		if !p.edEscArm {
			if text := strings.TrimSpace(p.ed.Value()); text != "" && text != strings.TrimSpace(p.edOld) {
				// The editor holds the only copy of the edit until
				// ctrl+s; a reflexive esc must not drop a big paste
				// without one explicit confirmation.
				p.edEscArm = true
				return nil
			}
		}
		p.edEscArm = false
		p.mode = 0
		p.ed.Blur()
		return nil
	case "ctrl+s":
		p.edEscArm = false
		text := strings.TrimSpace(p.ed.Value())
		if text == "" || text == strings.TrimSpace(p.edOld) {
			p.mode = 0
			p.ed.Blur()
			return nil
		}
		p.edErr = ""
		return validateTextCmd(d, p.edSection, p.edID, text, p.edOld, "")
	}
	// Any other key means the user kept editing; the guard disarms.
	p.edEscArm = false
	var cmd tea.Cmd
	p.ed, cmd = p.ed.Update(msg)
	return cmd
}
