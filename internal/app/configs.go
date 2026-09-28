package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/driver"
	"dae-tui/internal/i18n"
	"dae-tui/internal/keymap"
	"dae-tui/internal/ui"
)

type configsPage struct {
	sel   driver.Selections
	err   error
	focus int

	groups []driver.Group
	ifaces []driver.NetworkInterface

	rows []rowRef
	cur  int

	sec      int
	secCur   [3]int
	secRange [3][2]int

	scroll int

	fieldCur int

	summaryView bool

	diff *diffState

	validateErr map[string]editRejection

	mode      int
	input     textinput.Model
	editField driver.ConfigField
	fieldErr  string

	builtin   bool
	ed        textarea.Model
	edSection string
	edID      string
	edOld     string
	edErr     string
	edEscArm  bool

	caps driver.Caps

	leftW, rightW, height int
}

type diffState struct {
	Section string
	ID      string
	Name    string
	Old     string
	Text    string
	Path    string
	Builtin bool

	lines []diffLine
}

type rowRef struct {
	section string
	index   int
}

var configSections = []string{"config", "dns", "routing"}

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

func newConfigsPage(caps driver.Caps) configsPage {
	return configsPage{validateErr: map[string]editRejection{}, caps: caps}
}

type editRejection struct {
	Path string
	Err  string
	Line int
}

func (p *configsPage) setSize(leftW, rightW, h int) {
	p.leftW, p.rightW, p.height = leftW, rightW, h
}

func (p *configsPage) openInput(placeholder, value string) tea.Cmd {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.CharLimit = 4096
	ti.Width = 40
	ti.SetValue(value)
	ti.Focus()
	p.input = ti
	return textinput.Blink
}

func (p *configsPage) rebuild() {
	p.rows = p.rows[:0]
	p.secRange = [3][2]int{{-1, -1}, {-1, -1}, {-1, -1}}
	for si, section := range configSections {
		items := itemsOf(p.sel, section)
		if n := len(items); p.secCur[si] >= n {
			p.secCur[si] = max0(n - 1)
		}
		if len(items) == 0 {
			continue
		}
		p.secRange[si][0] = len(p.rows)
		for i := range items {
			p.rows = append(p.rows, rowRef{section: section, index: i})
		}
		p.secRange[si][1] = len(p.rows) - 1
	}

	if p.secRange[p.sec][0] < 0 {
		for si := range configSections {
			if p.secRange[si][0] >= 0 {
				p.sec = si
				break
			}
		}
	}
	p.cur = p.rowAt(configSections[p.sec], p.secCur[p.sec])
	if p.cur < 0 && len(p.rows) > 0 {
		p.cur = 0
	}
	p.scroll = 0

	if fields, ok := p.curFields(); ok && p.fieldCur >= len(fields) {
		p.fieldCur = len(fields) - 1
	}
}

func (p *configsPage) handleSelections(sel driver.Selections, err error) {
	if err != nil {
		p.err = err
		return
	}
	p.err = nil
	p.sel = sel
	for _, rej := range p.validateErr {

		os.Remove(rej.Path)
	}
	p.validateErr = map[string]editRejection{}
	p.diff = nil
	if p.mode == 6 {

		p.mode = 0
	}
	p.rebuild()
}

func (p *configsPage) setGroups(groups []driver.Group) { p.groups = groups }

func (p *configsPage) setInterfaces(ifaces []driver.NetworkInterface) { p.ifaces = ifaces }

func (p *configsPage) hasGroup(name string) bool {
	for _, g := range p.groups {
		if g.Name == name {
			return true
		}
	}
	return false
}

func (p *configsPage) hasIface(name string) bool {
	for _, i := range p.ifaces {
		if i.Name == name {
			return true
		}
	}
	return false
}

func (p configsPage) ifaceHint(f driver.ConfigField) string {
	if !isIfaceField(f.Name) || len(p.ifaces) == 0 {
		return ""
	}
	parts := make([]string, 0, len(p.ifaces))
	for _, i := range p.ifaces {
		s := i.Name
		if !i.Up {
			s += "(down)"
		}
		if i.Default {
			s += i18n.T("(默认路由)")
		}
		parts = append(parts, s)
	}
	return i18n.T("可用: ") + strings.Join(parts, ", ")
}

func (p configsPage) ifaceWarning(f driver.ConfigField) string {
	if !isIfaceField(f.Name) || len(p.ifaces) == 0 {
		return ""
	}
	for _, n := range configuredIfaces(f.Value) {
		if !p.hasIface(n) {
			return i18n.T("⚠ 接口 ") + n + i18n.T(" 不存在")
		}
	}
	return ""
}

func isIfaceField(name string) bool {
	return name == "lanInterface" || name == "wanInterface"
}

func (p *configsPage) item(r rowRef) *driver.ConfigItem {
	items := itemsOf(p.sel, r.section)
	if r.index < 0 || r.index >= len(items) {
		return nil
	}
	it := items[r.index]
	return &it
}

func (p *configsPage) curRow() *rowRef {
	if p.cur < 0 || p.cur >= len(p.rows) {
		return nil
	}
	return &p.rows[p.cur]
}

func sectionName(section string) string {
	return i18n.T(sectionTitles[section])
}

func (p *configsPage) deletable(section string, it *driver.ConfigItem) error {
	items := itemsOf(p.sel, section)
	if it.Selected {
		return errors.New(i18n.T("选中的配置不可删除（先 Enter 切换到其他配置）"))
	}
	if len(items) <= 1 {
		return errors.New(i18n.T("该分区仅剩一个配置，不可删除"))
	}
	return nil
}

func editorArgv() ([]string, error) {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return strings.Fields(v), nil
		}
	}
	for _, name := range []string{"sensible-editor", "editor", "nano", "vim", "vi", "nvim", "micro", "hx"} {
		if p, err := exec.LookPath(name); err == nil {
			return []string{p}, nil
		}
	}
	return nil, errors.New(i18n.T("未找到可用的编辑器：请设置 $EDITOR（如 EDITOR=nvim）"))
}

func editorCmd(path string) (*exec.Cmd, string, error) {
	argv, err := editorArgv()
	if err != nil {
		return nil, "", err
	}
	args := append(append([]string{}, argv[1:]...), path)
	return exec.Command(argv[0], args...), strings.Join(argv, " "), nil
}

func (p *configsPage) retireRejection(id string) {
	if rej, ok := p.validateErr[id]; ok {
		os.Remove(rej.Path)
		delete(p.validateErr, id)
	}
}

func (p *configsPage) openBuiltinEditor(r rowRef, it driver.ConfigItem) tea.Cmd {
	p.retireRejection(it.ID)
	ta := textarea.New()
	ta.Placeholder = "dae DSL"
	ta.SetWidth(min(76, max(40, p.rightW)))
	ta.SetHeight(10)
	ta.CharLimit = 0
	ta.SetValue(it.Body)
	ta.Focus()
	p.ed = ta
	p.edSection, p.edID, p.edOld, p.edErr = r.section, it.ID, it.Body, ""
	p.edEscArm = false
	p.mode = 7
	return textarea.Blink
}

func (p *configsPage) editInEditor(d driver.Driver, r rowRef, it driver.ConfigItem) tea.Cmd {
	p.retireRejection(it.ID)
	ext := ".conf"
	if r.section == "dns" {
		ext = ".dns"
	}
	f, err := os.CreateTemp("", "dae-tui-*"+ext)
	if err != nil {
		return func() tea.Msg { return opDoneMsg{Op: i18n.T("编辑"), Err: err} }
	}
	if _, err := f.WriteString(it.Body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return func() tea.Msg { return opDoneMsg{Op: i18n.T("编辑"), Err: err} }
	}
	f.Close()

	c, editor, err := editorCmd(f.Name())
	if err != nil {
		os.Remove(f.Name())
		return func() tea.Msg { return opDoneMsg{Op: i18n.T("编辑"), Err: err} }
	}
	old := it.Body
	path, section, id := f.Name(), r.section, it.ID
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorDoneMsg{Path: path, Section: section, ID: id, Old: old, Err: err,
			Editor: editor}
	})
}

func (p *configsPage) handleEditorDone(msg editorDoneMsg, d driver.Driver) tea.Cmd {
	if msg.Err != nil {
		os.Remove(msg.Path)
		return func() tea.Msg { return opDoneMsg{Op: i18n.T("编辑 ") + msg.Editor, Err: msg.Err} }
	}
	raw, err := os.ReadFile(msg.Path)
	if err != nil {
		os.Remove(msg.Path)
		return func() tea.Msg { return opDoneMsg{Op: i18n.T("编辑"), Err: err} }
	}
	text := strings.TrimSpace(string(raw))
	if text == "" || text == strings.TrimSpace(msg.Old) {
		os.Remove(msg.Path)
		return nil
	}

	return validateTextCmd(d, msg.Section, msg.ID, text, msg.Old, msg.Path)
}

func (p *configsPage) reopenBuiltinEditor(st *diffState) tea.Cmd {
	ta := textarea.New()
	ta.Placeholder = "dae DSL"
	ta.SetWidth(min(76, max(40, p.rightW)))
	ta.SetHeight(10)
	ta.CharLimit = 0
	ta.SetValue(st.Text)
	ta.Focus()
	p.ed = ta
	p.edSection, p.edID, p.edOld = st.Section, st.ID, st.Old
	p.edErr, p.edEscArm = "", false
	p.mode = 7
	return textarea.Blink
}

func (p *configsPage) handleValidated(msg editorValidatedMsg, d driver.Driver) tea.Cmd {
	if msg.Err != nil {
		if p.mode == 7 {
			p.edErr = flattenErr(msg.Err)
			return func() tea.Msg {
				return opDoneMsg{Op: sectionName(msg.Section) + i18n.T(" 校验"), Err: errors.New(i18n.T("校验未通过（编辑器保持打开）"))}
			}
		}
		if p.validateErr == nil {
			p.validateErr = map[string]editRejection{}
		}
		p.validateErr[msg.ID] = editRejection{Path: msg.Path, Err: flattenErr(msg.Err),
			Line: errLineNo(msg.Err)}
		return func() tea.Msg {
			return opDoneMsg{Op: sectionName(msg.Section) + i18n.T(" 校验"), Err: errors.New(i18n.T("校验未通过，未保存"))}
		}
	}
	p.diff = &diffState{
		Section: msg.Section, ID: msg.ID, Name: p.profileName(msg.Section, msg.ID),
		Old: msg.Old, Text: msg.Text, Path: msg.Path, Builtin: p.mode == 7,
	}
	p.mode = 6
	p.scroll = 0
	return nil
}

func (p *configsPage) profileName(section, id string) string {
	for _, it := range itemsOf(p.sel, section) {
		if it.ID == id {
			return it.Name
		}
	}
	return ""
}

func flattenErr(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}

func errLineNo(err error) int {
	m := regexp.MustCompile(`(?i)line (\d+)`).FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	n, convErr := strconv.Atoi(m[1])
	if convErr != nil || n < 1 || n > 100000 {
		return 0
	}
	return n
}

func (p *configsPage) bodyLines() []string {
	r := p.curRow()
	if r == nil {
		return []string{ui.HelpStyle.Render(i18n.T("（无配置）"))}
	}
	it := p.item(*r)
	if r.section == "config" {
		lines := []string{ui.SelectedStyle.Render(i18n.T("全局配置 · ") + it.Name)}

		if fields := orderedFields(it.Fields); len(fields) > 0 {
			for i, f := range fields {
				sel := p.focus == 1 && i == p.fieldCur
				cur := " "
				if sel {
					cur = ui.CursorStyle.Render("❯")
				}

				lines = append(lines, ui.HiRow(ui.Truncate(cur+" "+ui.PadRight(fieldLabel(f), 18)+f.Value,
					max0(p.rightW-6)), p.rightW-4, sel))
				if warn := p.ifaceWarning(f); warn != "" {
					lines = append(lines, ui.ErrorStyle.Render("   "+warn))
				}
			}
			return lines
		}
		return append(lines, fallbackBody(it, 0)...)
	}
	title := i18n.T(sectionTitles[r.section])
	lines := []string{}
	if r.section == "routing" && len(it.References) > 0 {

		parts := make([]string, 0, len(it.References))
		for _, g := range it.References {
			switch {
			case p.hasGroup(g):
				parts = append(parts, g)
			case isBuiltinOutbound(g):
				parts = append(parts, ui.HelpStyle.Render(g+i18n.T(" (内置)")))
			case len(p.groups) > 0:
				parts = append(parts, ui.ErrorStyle.Render(g+i18n.T(" (已不存在)")))
			default:
				parts = append(parts, g)
			}
		}
		lines = append(lines, ui.Truncate(ui.SelectedStyle.Render(i18n.T("引用组 "))+
			strings.Join(parts, ", "), max0(p.rightW-6)))
	}
	if p.summaryView && len(it.Summary) > 0 {
		lines = append(lines, ui.SelectedStyle.Render(title+i18n.T(" (结构概览) · ")+it.Name))
		for _, s := range it.Summary {
			lines = append(lines, "  "+ui.Truncate(s, max0(p.rightW-8)))
		}
		return lines
	}
	lines = append(lines, ui.SelectedStyle.Render(title+i18n.T(" (DSL 原文) · ")+it.Name))

	return append(lines, fallbackBody(it, p.validateErr[it.ID].Line)...)
}

func fallbackBody(it *driver.ConfigItem, badLine int) []string {
	body := it.Body
	if body == "" {
		body = it.Detail
	}
	if body == "" {
		return []string{ui.HelpStyle.Render(i18n.T(" （无内容：编辑请在 daed 完成）"))}
	}
	lines := make([]string, 0, 8)
	for i, l := range strings.Split(body, "\n") {
		if badLine > 0 && i+1 == badLine {
			lines = append(lines, ui.ErrorStyle.Render("✗ "+l))
			continue
		}
		lines = append(lines, " "+l)
	}
	return lines
}

func (p *configsPage) handleKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {

	if p.mode != 0 {
		return p.modalKey(msg, d)
	}
	switch msg.String() {
	case "v":
		p.summaryView = !p.summaryView
		p.scroll = 0
		return nil
	case "y":
		if r := p.curRow(); r != nil {
			if body := p.item(*r).Body; body != "" {
				return osc52CopyCmd(body, i18n.T("方案 DSL"))
			}

			return func() tea.Msg {
				return opDoneMsg{Op: i18n.T("复制"), Err: errors.New(i18n.T("该方案没有 DSL 可复制（全局配置是字段表）"))}
			}
		}
	}
	if p.focus == 0 {
		switch msg.String() {
		case "j", "down":

			if last := p.secRange[p.sec][1]; last >= 0 && p.cur < last {
				p.setCursor(p.cur + 1)
			}
		case "k", "up":
			if first := p.secRange[p.sec][0]; first >= 0 && p.cur > first {
				p.setCursor(p.cur - 1)
			}
		case "g":
			if first := p.secRange[p.sec][0]; first >= 0 {
				p.setCursor(first)
			}
		case "G":
			if last := p.secRange[p.sec][1]; last >= 0 {
				p.setCursor(last)
			}
		case "tab":
			p.switchSection((p.sec + 1) % len(configSections))
		case "shift+tab":
			p.switchSection((p.sec + len(configSections) - 1) % len(configSections))
		case "l", "right":
			p.focus = 1
		case "enter":
			if r := p.curRow(); r != nil {
				it := p.item(*r)
				return selectCmd(d, r.section, it.ID)
			}
		case "c":
			if !p.caps.ConfigMgmt {
				return unsupportedCmd(i18n.T("新建配置"))
			}
			r := p.curRow()
			if r != nil {
				p.mode = 3
				return p.openInput(i18n.T("名称（将复制光标所指方案）"), "")
			}
		case "R":
			if !p.caps.ConfigMgmt {
				return unsupportedCmd(i18n.T("重命名配置"))
			}
			if r := p.curRow(); r != nil {
				it := p.item(*r)
				p.mode = 4
				return p.openInput(i18n.T("新名称"), it.Name)
			}
		case "D":
			if !p.caps.ConfigMgmt {
				return unsupportedCmd(i18n.T("删除配置"))
			}
			if r := p.curRow(); r != nil {
				it := p.item(*r)
				if err := p.deletable(r.section, it); err != nil {
					return func() tea.Msg { return opDoneMsg{Op: i18n.T("删除"), Err: err} }
				}
				p.mode = 5
			}
		case "e":
			if !p.caps.ConfigMgmt {
				return unsupportedCmd(i18n.T("编辑配置"))
			}
			r := p.curRow()
			if r == nil {
				return nil
			}
			it := p.item(*r)
			switch r.section {
			case "config":
				if len(it.Fields) == 0 {
					return func() tea.Msg {
						return opDoneMsg{Op: i18n.T("编辑"), Err: errors.New(i18n.T("该配置没有可编辑字段"))}
					}
				}

				p.focus = 1
				return nil
			case "dns", "routing":
				if p.builtin {
					return p.openBuiltinEditor(*r, *it)
				}
				return p.editInEditor(d, *r, *it)
			}
		}
		return nil
	}

	if fields, ok := p.curFields(); ok {
		if p.fieldCur >= len(fields) {
			p.fieldCur = len(fields) - 1
		}
		switch msg.String() {
		case "j", "down":
			if p.fieldCur < len(fields)-1 {
				p.fieldCur++
			}
		case "k", "up":
			if p.fieldCur > 0 {
				p.fieldCur--
			}
		case "g":
			p.fieldCur = 0
		case "G":
			p.fieldCur = len(fields) - 1
		case "enter":
			f := fields[p.fieldCur]
			p.editField = f
			p.fieldErr = ""
			p.mode = 2
			return p.openInput(i18n.T("新值 (")+f.Type+")", f.Value)
		case "tab", "shift+tab", "h", "left", "esc":
			p.focus = 0
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
	case "tab", "shift+tab", "h", "left", "esc":
		p.focus = 0
	}
	return nil
}

var sectionTitles = map[string]string{
	"config":  "全局配置",
	"dns":     "DNS",
	"routing": "路由规则",
}

func (p configsPage) View() string {

	h := p.height

	rightFooter := i18n.T("j/k 滚动")
	if _, ok := p.curFields(); ok {
		rightFooter = K(keymap.Configs, "j") + "/" + K(keymap.Configs, "k") + " " + i18n.T("选择 · Enter 编辑")
	}
	switch p.mode {
	case 5:
		rightFooter = i18n.T("y 确认 · n/esc 取消")
	case 6:
		rightFooter = i18n.T("y 提交 · n/esc 取消 · j/k 滚动")
	case 2, 3, 4, 7:

		rightFooter = ""
	}

	return strings.Join(ui.JoinBoxes(p.leftBoxes(h), p.rightBox(h, rightFooter)), "\n")
}

func (p configsPage) rightBox(h int, footer string) []string {
	lines := p.rightLines()
	if len(lines) > h-2 {
		lines = lines[:h-2]
	}
	for len(lines) < h-2 {
		lines = append(lines, "")
	}
	title := i18n.T("内容")
	if r := p.curRow(); r != nil {
		if it := p.item(*r); it != nil && it.Name != "" {
			title = it.Name
		}
	}
	return ui.TitledBoxFooter(title, footer, p.focus == 1, p.rightW, lines)
}

func (p configsPage) modalLines() []string {
	switch p.mode {
	case 5:
		if r := p.curRow(); r != nil {
			it := p.item(*r)
			return ui.BoxLines(true, i18n.T("确认删除")+sectionName(r.section)+" \""+it.Name+"\"?  (y/n)")
		}
	case 6:
		if p.diff == nil {
			return []string{ui.HelpStyle.Render(i18n.T("（无待确认的更改）"))}
		}
		st := p.diff

		head := ui.BoxLines(true,
			ui.TitleStyle.Render(i18n.T(" 确认提交更改 · ")+sectionName(st.Section)+" "+st.Name),
			ui.OKStyle.Render(i18n.T(" y 提交"))+"    "+
				ui.ErrorStyle.Render(i18n.T("n / esc 取消"))+"    "+
				ui.HelpStyle.Render(i18n.T("j/k 滚动 diff")),
			ui.HelpStyle.Render(i18n.T(" 取消后编辑内容保留在 ")+ui.TruncateHead(st.Path, max0(p.rightW-8))),
			"")
		lines := append([]string{}, head...)
		if st.lines == nil {
			st.lines = diffLines(st.Old, st.Text)
		}
		for _, dl := range st.lines {
			var line string
			switch dl.kind {
			case diffAdd:
				line = ui.OKStyle.Render("+ " + dl.text)
			case diffDel:
				line = ui.ErrorStyle.Render("- " + dl.text)
			case diffGap:
				line = ui.HelpStyle.Render("  " + dl.text)
			default:
				line = ui.HelpStyle.Render("  " + dl.text)
			}
			lines = append(lines, ui.Truncate(line, max0(p.rightW-4)))
		}

		rowsH := max0(p.height - 8)
		// Local clamp: this is a value receiver, so writing p.scroll here
		// only mutated the render copy — the stored one could stay past the
		// end until a keypress re-clamped it.
		scroll := p.scroll
		if scroll > max0(len(lines)-rowsH) {
			scroll = max0(len(lines) - rowsH)
		}
		if scroll < 0 {
			scroll = 0
		}
		end := scroll + rowsH
		if end > len(lines) {
			end = len(lines)
		}
		return lines[scroll:end]
	}
	return nil
}

func (p configsPage) overlay() *overlaySpec {
	switch p.mode {
	case 2:
		f := p.editField
		lines := []string{
			ui.TitleStyle.Render(i18n.T(" 修改 ") + fieldLabel(f)),
			"",
			i18n.T(" 当前值  ") + ui.HelpStyle.Render(ui.Truncate(f.Value, 56)),
			i18n.T(" 新值    ") + p.input.View(),
		}
		if f.Default != "" {
			lines = append(lines, i18n.T(" 默认值  ")+ui.HelpStyle.Render(f.Default))
		}
		if hint := p.ifaceHint(f); hint != "" {
			lines = append(lines, ui.HelpStyle.Render(" "+hint))
		}
		if warn := p.ifaceWarning(f); warn != "" {
			lines = append(lines, ui.ErrorStyle.Render(" "+warn))
		}
		if f.Desc != "" {
			lines = append(lines, ui.HelpStyle.Render(i18n.T(" 说明    ")+
				ui.Truncate(firstLine(f.Desc), 56)))
		}
		if p.fieldErr != "" {
			lines = append(lines, ui.ErrorStyle.Render(" ✗ "+p.fieldErr))
		}
		return &overlaySpec{lines: append(lines, "",
			ui.HelpStyle.Render(i18n.T(" 类型 ")+f.Type+i18n.T("（数组用逗号分隔）  Enter 提交  esc 返回")))}
	case 3, 4:
		r := p.curRow()
		title := i18n.T("重命名")
		if p.mode == 3 {
			title = i18n.T("新建")
		}
		if r != nil {
			title += sectionName(r.section)
		}
		hint := i18n.T(" Enter 确认  esc 取消")
		if p.mode == 3 && r != nil {

			switch src := p.item(*r); {
			case src != nil && (src.Body != "" || len(src.Fields) > 0):
				hint = i18n.T(" 将复制「") + src.Name + i18n.T("」的内容，之后可 e 编辑  Enter 确认  esc 取消")
			default:
				hint = i18n.T(" 将使用默认模板，之后可 e 编辑  Enter 确认  esc 取消")
			}
		}
		return &overlaySpec{lines: []string{
			ui.TitleStyle.Render(" " + title),
			"",
			i18n.T(" 名称  ") + p.input.View(),
			"",
			ui.HelpStyle.Render(hint),
		}}
	case 7:
		lines := []string{
			ui.TitleStyle.Render(i18n.T(" 编辑 ") + sectionName(p.edSection) + " DSL · " +
				p.profileName(p.edSection, p.edID)),
			"",
		}
		lines = append(lines, strings.Split(p.ed.View(), "\n")...)
		if p.edErr != "" {
			lines = append(lines, "", ui.ErrorStyle.Render(" ✗ "+ui.Truncate(p.edErr, 76)))
		}
		if p.edEscArm {
			lines = append(lines, "", ui.ErrorStyle.Render(i18n.T(" ⚠ 修改尚未提交：再按一次 esc 放弃，或 ctrl+s 校验保存")))
		}
		return &overlaySpec{lines: append(lines, "",
			ui.HelpStyle.Render(i18n.T(" ctrl+s 校验并预览 diff  esc 取消")),
			ui.HelpStyle.Render(i18n.T(" 想用 vim/nano 等编辑器：config.toml 里 editor = \"external\"")))}
	}
	return nil
}

func (p *configsPage) leftClick(row int) {
	if row < 0 || row >= p.height {
		return
	}
	if p.height < 9 {

		rowsH := max0(p.height - 2)
		extra := p.leftLinesExtra()
		within := row - extra
		if within < 0 || within >= rowsH-extra {
			return
		}
		i := max0(p.cur-(rowsH-extra)+1) + within
		if i < len(p.rows) {
			for si, section := range configSections {
				if section == p.rows[i].section {
					p.sec = si
					break
				}
			}
			p.setCursor(i)
			p.focus = 0
		}
		return
	}
	shares := leftShares(p.height)

	b := row + 1
	for si, section := range configSections {
		if b >= shares[si] {
			b -= shares[si]
			continue
		}
		within := b - 1
		rowsH := shares[si] - 2
		if within < 0 || within >= rowsH {
			return
		}
		if si == 0 {
			within -= p.leftLinesExtra()
			if within < 0 {
				return
			}
		}
		i := p.leftWindow(si, rowsH) + within
		if i >= 0 && i < len(itemsOf(p.sel, section)) {
			if si != p.sec {
				p.switchSection(si)
			}
			p.setCursor(p.rowAt(section, i))
			p.focus = 0
		}
		return
	}
}

func (p *configsPage) switchSection(si int) {
	if si == p.sec || p.secRange[si][0] < 0 {
		return
	}
	if r := p.curRow(); r != nil {
		p.secCur[p.sec] = r.index
	}
	p.sec = si
	p.scroll = 0
	p.fieldCur = 0
	p.cur = p.rowAt(configSections[si], p.secCur[si])
	if p.cur < 0 {
		p.cur = p.secRange[si][0]
	}
}

func (p *configsPage) setCursor(i int) {
	if i < 0 || i >= len(p.rows) || i == p.cur {
		return
	}
	p.cur = i
	if r := p.curRow(); r != nil && r.section == configSections[p.sec] {
		p.secCur[p.sec] = r.index
	}
	p.scroll = 0
	p.fieldCur = 0
}

func (p configsPage) curFields() ([]driver.ConfigField, bool) {
	r := p.curRow()
	if r == nil || r.section != "config" {
		return nil, false
	}
	fields := orderedFields(p.item(*r).Fields)
	if len(fields) == 0 {
		return nil, false
	}
	return fields, true
}

func (p configsPage) fieldLineOf(fields []driver.ConfigField, i int) int {
	if i >= len(fields) {
		i = len(fields) - 1
	}
	n := 1
	for j := 0; j < i; j++ {
		n++
		if p.ifaceWarning(fields[j]) != "" {
			n++
		}
	}
	return n
}

func (p configsPage) fieldWindowStart(banner, rowsH int, fields []driver.ConfigField) int {
	line := banner + p.fieldLineOf(fields, p.fieldCur)
	if line >= rowsH {
		return line - rowsH + 1
	}
	return 0
}

func (p *configsPage) rightClick(row int) {
	if p.mode != 0 || row < 0 {
		return
	}
	r := p.curRow()
	if r == nil {
		return
	}
	rowsH := max0(p.height - 2)
	if row >= rowsH {
		return
	}
	p.focus = 1
	if r.section != "config" {
		return
	}
	fields := orderedFields(p.item(*r).Fields)
	if len(fields) == 0 {
		return
	}

	banner := 0
	if rej := p.validateErr[p.item(*r).ID]; rej.Err != "" {
		banner = 4
	}
	target := p.fieldWindowStart(banner, rowsH, fields) + row
	for i := range fields {
		line := banner + p.fieldLineOf(fields, i)
		if target == line {
			p.fieldCur = i
			return
		}

		if target == line+1 && p.ifaceWarning(fields[i]) != "" {
			p.fieldCur = i
			return
		}
	}

}

// leftFooter is the left column's key strip, translated at render time.
func leftFooter() string {
	return i18n.T("Tab 切区 · ") + kb(keymap.Configs, "e", "编辑") + " · " +
		K(keymap.Configs, "c") + "/" + K(keymap.Configs, "R") + "/" + K(keymap.Configs, "D") +
		" " + i18n.T("管理")
}

func leftShares(h int) (s [3]int) {
	base, rem := h/3, h%3
	for i := range s {
		s[i] = base
		if i < rem {
			s[i]++
		}
	}
	return s
}

func (p configsPage) rowAt(section string, index int) int {
	for i, r := range p.rows {
		if r.section == section && r.index == index {
			return i
		}
	}
	return -1
}

func (p configsPage) itemLine(section string, i int) string {
	row := p.rowAt(section, i)
	selected := row == p.cur && p.focus == 0
	cursor := "  "
	if selected {
		cursor = ui.CursorStyle.Render("❯")
	}
	it := itemsOf(p.sel, section)[i]
	mark := "  "
	if it.Selected {
		mark = ui.OKStyle.Render("● ")
	}
	return ui.HiRow(cursor+" "+mark+ui.PadRight(it.Name, max0(p.leftW-12)), p.leftW-4, selected)
}

func (p configsPage) leftLinesExtra() int {
	n := 0
	if !p.caps.ConfigMgmt {
		n++
	}
	if p.err != nil {
		n++
	}
	return n
}

func (p configsPage) leftWindow(si, rowsH int) int {
	extra := 0
	if si == 0 {
		extra = p.leftLinesExtra()
	}
	if si != p.sec || p.cur < 0 || p.cur >= len(p.rows) {
		return 0
	}
	if cur := p.rows[p.cur].index; cur >= rowsH-extra {
		return cur - (rowsH - extra) + 1
	}
	return 0
}

func (p configsPage) leftBoxes(h int) []string {
	if h < 9 {

		return p.flatLeftBox(h)
	}
	shares := leftShares(h)
	out := make([]string, 0, h)
	for si, section := range configSections {
		items := itemsOf(p.sel, section)
		rowsH := shares[si] - 2
		focused := p.focus == 0 && si == p.sec
		footer := ""
		if focused {
			footer = leftFooter()
		}
		lines := make([]string, 0, rowsH)
		if si == 0 {
			if !p.caps.ConfigMgmt {
				lines = append(lines, ui.ErrorStyle.Render(i18n.T("✗ 当前后端不支持配置管理")))
			}
			if p.err != nil {
				lines = append(lines, ui.ErrorStyle.Render("✗ "+shortErr(p.err)))
			}
		}
		start := p.leftWindow(si, rowsH)
		var itemRows []string
		for i := start; i < len(items) && len(itemRows) < rowsH-len(lines); i++ {
			itemRows = append(itemRows, p.itemLine(section, i))
		}
		itemRows = ui.WithScrollbar(itemRows, p.leftW-4, len(items), start, focused)
		lines = append(lines, itemRows...)
		for len(lines) < rowsH {
			lines = append(lines, "")
		}
		title := fmt.Sprintf("%s %d", i18n.T(sectionTitles[section]), len(items))
		out = append(out, ui.TitledBoxFooter(title, footer, focused, p.leftW, lines)...)
	}
	return out
}

func (p configsPage) flatLeftBox(h int) []string {
	rowsH := max0(h - 2)
	extra := p.leftLinesExtra()
	lines := make([]string, 0, rowsH)
	if !p.caps.ConfigMgmt {
		lines = append(lines, ui.ErrorStyle.Render(i18n.T("✗ 当前后端不支持配置管理")))
	}
	if p.err != nil {
		lines = append(lines, ui.ErrorStyle.Render("✗ "+shortErr(p.err)))
	}
	start := max0(p.cur - (rowsH - extra) + 1)
	for i := start; i < len(p.rows) && len(lines) < rowsH; i++ {
		lines = append(lines, p.itemLine(p.rows[i].section, p.rows[i].index))
	}
	for len(lines) < rowsH {
		lines = append(lines, "")
	}
	return ui.TitledBoxFooter(i18n.T("配置方案"), leftFooter(), p.focus == 0, p.leftW, lines)
}

func (p configsPage) rightLines() []string {
	if p.mode == 5 || p.mode == 6 {
		return p.modalLines()
	}
	var lines []string
	banner := 0

	if r := p.curRow(); r != nil {
		if rej := p.validateErr[p.item(*r).ID]; rej.Err != "" {
			banner = 4
			lines = append(lines,
				ui.ErrorStyle.Render(" ✗ "+sectionName(r.section)+i18n.T(" 校验未通过，未保存")),
				ui.HelpStyle.Render(i18n.T("  编辑内容保留在 ")+
					ui.TruncateHead(rej.Path, max0(p.rightW-20))),
				ui.ErrorStyle.Render("  "+ui.Truncate(rej.Err, max0(p.rightW-6))),
				"",
			)
		}
	}
	lines = append(lines, p.bodyLines()...)
	rowsH := max0(p.height - 2)

	if fields, ok := p.curFields(); ok {
		start := p.fieldWindowStart(banner, rowsH, fields)
		end := min(start+rowsH, len(lines))
		return ui.WithScrollbar(lines[start:end], p.rightW-4, len(lines), start, p.focus == 1)
	}
	// Local clamp, same reason as modalLines: value receiver.
	scroll := p.scroll
	if scroll >= len(lines) {
		scroll = len(lines) - 1
	}
	if scroll < 0 {
		scroll = 0
	}
	end := scroll + rowsH
	if end > len(lines) {
		end = len(lines)
	}
	return lines[scroll:end]
}
