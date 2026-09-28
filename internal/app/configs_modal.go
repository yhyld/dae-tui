package app

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/driver"
	"dae-tui/internal/i18n"
)

func (p *configsPage) modalKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch p.mode {
	case 2:
		return p.fieldInputKey(msg, d)
	case 3, 4:
		return p.nameInputKey(msg, d)
	case 5:
		return p.deleteConfirmKey(msg, d)
	case 6:
		return p.diffConfirmKey(msg, d)
	case 7:
		return p.builtinEditorKey(msg, d)
	case 8:
		return p.exportKey(msg)
	}
	return nil
}

// exportKey drives the E overlay: w writes the backup file, y copies the
// document to the clipboard (OSC 52). Both channels produce the same
// content; esc just closes. Keys stay literal like every modal confirm —
// they are not remappable actions.
func (p *configsPage) exportKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "w":
		p.mode = 0
		return exportFileCmd(p.exportDir, buildExport(p.sel, time.Now()))
	case "y":
		p.mode = 0
		return osc52CopyCmd(buildExport(p.sel, time.Now()), i18n.T("方案备份"))
	case "n", "esc":
		p.mode = 0
	}
	return nil
}

func (p *configsPage) fieldInputKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "esc":

		p.mode = 0
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

			p.mode = 0
			p.input.Blur()
			return nil
		}

		if err := validateFieldValue(f, val); err != nil {
			p.fieldErr = err.Error()
			return nil
		}

		p.mode = 0
		p.input.Blur()
		it := p.item(*r)
		return configFieldCmd(d, it.ID, f, val, i18n.T("修改 ")+fieldLabel(f))
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

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
				i18n.T("创建")+sectionName(r.section)+" "+name)
		}
		it := p.item(*r)
		return profileMutateCmd(d, profileMutation{kind: 1, section: r.section, id: it.ID, name: name},
			i18n.T("重命名为 ")+name)
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
			i18n.T("删除")+sectionName(r.section)+" "+it.Name)
	case "n", "esc", "enter":
		p.mode = 0
	}
	return nil
}

func (p *configsPage) diffConfirmKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "y":
		st := p.diff
		p.diff, p.mode = nil, 0
		p.scroll = 0
		if st == nil {
			return nil
		}
		if !st.Builtin {
			os.Remove(st.Path)
		}
		delete(p.validateErr, st.ID)
		return configTextCmd(d, st.Section, st.ID, st.Text, i18n.T("更新")+sectionName(st.Section)+i18n.T(" 内容"))
	case "n", "esc":
		st := p.diff
		p.diff, p.mode = nil, 0
		p.scroll = 0
		if st == nil {
			return nil
		}
		if st.Builtin {

			return p.reopenBuiltinEditor(st)
		}

		return func() tea.Msg {
			return opDoneMsg{Op: i18n.T("编辑"), Err: fmt.Errorf(i18n.T("已取消，编辑内容保留在 %s"), st.Path)}
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

	p.edEscArm = false
	var cmd tea.Cmd
	p.ed, cmd = p.ed.Update(msg)
	return cmd
}
