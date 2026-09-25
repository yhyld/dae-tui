package app

import (
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/driver"
	"dae-tui/internal/ui"
)

// configsPage: master-detail. Left lists the three profile sections
// (config / dns / routing) flat; the right pane shows the selected item's
// content (global summary or DSL text), scrollable.
type configsPage struct {
	sel   driver.Selections
	err   error
	focus int // 0 left, 1 right (scroll)

	rows   []rowRef
	cur    int
	scroll int // right pane line offset

	// modal state
	mode       int // 0 list, 1 fieldPicker, 2 fieldInput, 3 createInput, 4 renameInput, 5 deleteConfirm
	pickCursor int
	input      textinput.Model
	editField  driver.ConfigField

	leftW, rightW, height int
}

type rowRef struct {
	section string // config | dns | routing
	index   int
}

// items returns the profiles of a section.
func itemsOf(sel driver.Selections, section string) []driver.ConfigItem {
	switch section {
	case "config":
		return sel.Configs
	case "dns":
		return sel.Dns
	case "routing":
		return sel.Routings
	}
	return nil
}

func (p *configsPage) setSize(leftW, rightW, h int) {
	p.leftW, p.rightW, p.height = leftW, rightW, h
}

// openInput (re)initializes the shared text input for create/rename/field
// editing modals.
func (p *configsPage) openInput(placeholder, value string) tea.Cmd {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.CharLimit = 512
	ti.Width = 40
	ti.SetValue(value)
	ti.Focus()
	p.input = ti
	return textinput.Blink
}

func (p *configsPage) rebuild() {
	p.rows = p.rows[:0]
	for i := range p.sel.Configs {
		p.rows = append(p.rows, rowRef{"config", i})
	}
	for i := range p.sel.Dns {
		p.rows = append(p.rows, rowRef{"dns", i})
	}
	for i := range p.sel.Routings {
		p.rows = append(p.rows, rowRef{"routing", i})
	}
	if p.cur >= len(p.rows) {
		p.cur = max0(len(p.rows) - 1)
	}
	p.scroll = 0
}

func (p *configsPage) handleSelections(sel driver.Selections, err error) {
	if err != nil {
		p.err = err
		return
	}
	p.err = nil
	p.sel = sel
	p.rebuild()
}

func (p *configsPage) item(r rowRef) *driver.ConfigItem {
	var it driver.ConfigItem
	switch r.section {
	case "config":
		it = p.sel.Configs[r.index]
	case "dns":
		it = p.sel.Dns[r.index]
	case "routing":
		it = p.sel.Routings[r.index]
	default:
		return nil
	}
	return &it
}

func (p *configsPage) curRow() *rowRef {
	if p.cur < 0 || p.cur >= len(p.rows) {
		return nil
	}
	return &p.rows[p.cur]
}

// sectionName returns a Chinese label for a section.
func sectionName(section string) string {
	return sectionTitles[section]
}

// srcProfile returns the profile a new one should be cloned from: the
// currently selected one in the section, else the first.
func (p *configsPage) srcProfile(section string) *driver.ConfigItem {
	items := itemsOf(p.sel, section)
	for i := range items {
		if items[i].Selected {
			return &items[i]
		}
	}
	if len(items) > 0 {
		return &items[0]
	}
	return nil
}

// deletable reports whether a profile may be deleted: the selected one and
// the last remaining one in a section are protected.
func (p *configsPage) deletable(section string, it *driver.ConfigItem) error {
	items := itemsOf(p.sel, section)
	if it.Selected {
		return errors.New("选中的配置不可删除（先 Enter 切换到其他配置）")
	}
	if len(items) <= 1 {
		return errors.New("该分区仅剩一个配置，不可删除")
	}
	return nil
}

// modalKey drives the field picker / inputs / confirm modals.
func (p *configsPage) modalKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch p.mode {
	case 5: // delete confirm
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

	case 1: // field picker (config global fields)
		r := p.curRow()
		if r == nil {
			p.mode = 0
			return nil
		}
		it := p.item(*r)
		switch msg.String() {
		case "esc":
			p.mode = 0
		case "j", "down":
			if p.pickCursor < len(it.Fields)-1 {
				p.pickCursor++
			}
		case "k", "up":
			if p.pickCursor > 0 {
				p.pickCursor--
			}
		case "enter":
			if p.pickCursor < len(it.Fields) {
				p.editField = it.Fields[p.pickCursor]
				p.mode = 2
				return p.openInput("新值 ("+p.editField.Type+")", p.editField.Value)
			}
		}
		return nil

	case 2: // field input
		switch msg.String() {
		case "esc":
			p.mode = 1
			p.input.Blur()
			return nil
		case "enter":
			val := strings.TrimSpace(p.input.Value())
			f := p.editField
			r := p.curRow()
			p.mode = 0
			p.input.Blur()
			if val == "" || val == f.Value || r == nil {
				return nil
			}
			it := p.item(*r)
			return configFieldCmd(d, it.ID, f, val, "修改 "+f.Label)
		}
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return cmd

	case 3, 4: // create / rename input
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
					src: p.srcProfile(r.section)},
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
	return nil
}

// editInEditor hands the raw DSL to $EDITOR via tea.ExecProcess.
func (p *configsPage) editInEditor(d driver.Driver, r rowRef, it driver.ConfigItem) tea.Cmd {
	ext := ".conf"
	if r.section == "dns" {
		ext = ".dns"
	}
	f, err := os.CreateTemp("", "dae-tui-*"+ext)
	if err != nil {
		return func() tea.Msg { return opDoneMsg{Op: "编辑", Err: err} }
	}
	if _, err := f.WriteString(it.Body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return func() tea.Msg { return opDoneMsg{Op: "编辑", Err: err} }
	}
	f.Close()

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	old := it.Body
	path, section, id := f.Name(), r.section, it.ID
	// $EDITOR may carry arguments (e.g. "omarchy-launch-editor --inline"),
	// so run it through a shell for word splitting.
	c := exec.Command("sh", "-c", `exec "$EDITOR" "$@"`, "dae-tui-edit", path)
	c.Env = append(os.Environ(), "EDITOR="+editor)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorDoneMsg{Path: path, Section: section, ID: id, Old: old, Err: err}
	})
}

// handleEditorDone reads the edited file back and submits when changed.
func (p *configsPage) handleEditorDone(msg editorDoneMsg, d driver.Driver) tea.Cmd {
	defer os.Remove(msg.Path)
	if msg.Err != nil {
		return func() tea.Msg { return opDoneMsg{Op: "编辑", Err: msg.Err} }
	}
	raw, err := os.ReadFile(msg.Path)
	if err != nil {
		return func() tea.Msg { return opDoneMsg{Op: "编辑", Err: err} }
	}
	text := strings.TrimSpace(string(raw))
	if text == "" || text == strings.TrimSpace(msg.Old) {
		return nil // unchanged
	}
	return configTextCmd(d, msg.Section, msg.ID, text, "更新"+sectionName(msg.Section)+" 内容")
}

func (p *configsPage) bodyLines() []string {
	r := p.curRow()
	if r == nil {
		return []string{ui.HelpStyle.Render("（无配置）")}
	}
	it := p.item(*r)
	var title string
	switch r.section {
	case "config":
		title = "全局配置"
	case "dns":
		title = "DNS (DSL 原文)"
	case "routing":
		title = "路由规则 (DSL 原文)"
	}
	lines := []string{ui.SelectedStyle.Render(title + " · " + it.Name)}
	body := it.Body
	if body == "" {
		body = it.Detail
	}
	if body == "" {
		body = "（无内容：编辑请在 daed 完成）"
	}
	for _, l := range strings.Split(body, "\n") {
		lines = append(lines, " "+l)
	}
	return lines
}

func (p *configsPage) handleKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	// `A` (apply) is handled globally by the app model.
	if p.mode != 0 {
		return p.modalKey(msg, d)
	}
	if p.focus == 0 {
		switch msg.String() {
		case "j", "down":
			if p.cur < len(p.rows)-1 {
				p.cur++
				p.scroll = 0
			}
		case "k", "up":
			if p.cur > 0 {
				p.cur--
				p.scroll = 0
			}
		case "g":
			p.cur = 0
			p.scroll = 0
		case "G":
			p.cur = len(p.rows) - 1
			p.scroll = 0
		case "tab", "l", "right":
			p.focus = 1
		case "enter":
			if r := p.curRow(); r != nil {
				it := p.item(*r)
				return selectCmd(d, r.section, it.ID)
			}
		case "c": // clone the selected profile of the section under the cursor
			r := p.curRow()
			if r != nil {
				p.mode = 3
				return p.openInput("名称（将复制当前选中配置）", "")
			}
		case "R": // rename
			if r := p.curRow(); r != nil {
				it := p.item(*r)
				p.mode = 4
				return p.openInput("新名称", it.Name)
			}
		case "D": // delete (confirm)
			if r := p.curRow(); r != nil {
				it := p.item(*r)
				if err := p.deletable(r.section, it); err != nil {
					return func() tea.Msg { return opDoneMsg{Op: "删除", Err: err} }
				}
				p.mode = 5
			}
		case "e": // edit content
			r := p.curRow()
			if r == nil {
				return nil
			}
			it := p.item(*r)
			switch r.section {
			case "config":
				if len(it.Fields) == 0 {
					return nil
				}
				p.mode = 1
				p.pickCursor = 0
				return nil
			case "dns", "routing":
				return p.editInEditor(d, *r, *it)
			}
		}
		return nil
	}

	body := p.bodyLines()
	switch msg.String() {
	case "j", "down":
		if p.scroll < len(body)-1 {
			p.scroll++
		}
	case "k", "up":
		if p.scroll > 0 {
			p.scroll--
		}
	case "g":
		p.scroll = 0
	case "G":
		p.scroll = len(body) - 1
	case "tab", "h", "left", "esc":
		p.focus = 0
	}
	return nil
}

var sectionTitles = map[string]string{
	"config":  "全局配置",
	"dns":     "DNS",
	"routing": "路由规则",
}

func (p configsPage) View(modified bool) string {
	lv := ui.Pane(" 配置方案 ", p.focus == 0, p.leftW, p.height, p.leftLines())
	rv := ui.Pane(" 内容 ", p.focus == 1, p.rightW, p.height, p.rightLines())
	body := lipgloss.JoinHorizontal(lipgloss.Top, lv, " ", rv)
	if modified {
		body += "\n" + ui.ErrorStyle.Render(" ⚠ 运行配置与选中项不一致（A 应用 / 首页 o 重启）")
	}
	return body
}

// modalLines renders the active modal inside the right pane.
func (p configsPage) modalLines() []string {
	switch p.mode {
	case 1: // field picker
		r := p.curRow()
		if r == nil {
			return []string{ui.HelpStyle.Render("（无配置）")}
		}
		it := p.item(*r)
		lines := []string{ui.TitleStyle.Render(" 选择要修改的字段")}
		for i, f := range it.Fields {
			mark, style := "  ", ui.HelpStyle
			if i == p.pickCursor {
				mark, style = "❯ ", ui.CursorStyle
			}
			lines = append(lines, style.Render(mark+ui.PadRight(f.Label, 16))+
				ui.HelpStyle.Render(ui.PadRight("["+f.Type+"]", 10))+f.Value)
		}
		return append(lines, ui.HelpStyle.Render(" Enter 编辑  esc 取消"))
	case 2: // field input
		f := p.editField
		return []string{
			ui.TitleStyle.Render(" 修改 " + f.Label),
			"",
			" 当前值  " + ui.HelpStyle.Render(f.Value),
			" 新值    " + p.input.View(),
			"",
			ui.HelpStyle.Render(" 类型 " + f.Type + "（数组用逗号分隔）  Enter 提交  esc 返回"),
		}
	case 3, 4: // create / rename
		r := p.curRow()
		title := "重命名"
		if p.mode == 3 {
			title = "新建"
		}
		if r != nil {
			title += sectionName(r.section)
		}
		hint := " Enter 确认  esc 取消"
		if p.mode == 3 && r != nil && r.section != "config" {
			hint = " DNS/路由 将以默认模板创建，之后 e 编辑  Enter 确认  esc 取消"
		}
		return []string{
			ui.TitleStyle.Render(" " + title),
			"",
			" 名称  " + p.input.View(),
			"",
			ui.HelpStyle.Render(hint),
		}
	case 5: // delete confirm
		if r := p.curRow(); r != nil {
			it := p.item(*r)
			return []string{ui.ErrorStyle.Render(" 确认删除" + sectionName(r.section) + " \"" + it.Name + "\"?  (y/n)")}
		}
	}
	return nil
}

func (p configsPage) leftLines() []string {
	var lines []string
	if p.err != nil {
		lines = append(lines, ui.ErrorStyle.Render("✗ "+shortErr(p.err)))
	}
	line := 0
	renderSection := func(section string, items []driver.ConfigItem) {
		lines = append(lines, ui.SelectedStyle.Render(sectionTitles[section]))
		for _, it := range items {
			// `line` is this item's flat index across all sections —
			// compare it with the cursor, not with row existence.
			isCur := line == p.cur
			cursor := "  "
			if isCur && p.focus == 0 {
				cursor = ui.CursorStyle.Render("❯")
			}
			mark := "  "
			if it.Selected {
				mark = ui.OKStyle.Render("● ")
			}
			lines = append(lines, cursor+" "+mark+ui.PadRight(it.Name, max0(p.leftW-10)))
			line++
		}
		lines = append(lines, "")
	}
	renderSection("config", p.sel.Configs)
	renderSection("dns", p.sel.Dns)
	renderSection("routing", p.sel.Routings)
	return lines
}

func (p configsPage) rightLines() []string {
	if p.mode != 0 {
		return p.modalLines()
	}
	body := p.bodyLines()
	rowsH := max0(p.height - 2)
	if p.scroll >= len(body) {
		p.scroll = len(body) - 1
	}
	if p.scroll < 0 {
		p.scroll = 0
	}
	end := p.scroll + rowsH
	if end > len(body) {
		end = len(body)
	}
	return body[p.scroll:end]
}
