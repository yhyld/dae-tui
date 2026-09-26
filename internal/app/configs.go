package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/driver"
	"dae-tui/internal/ui"
)

// configsPage: master-detail. Left lists the three profile sections
// (config / dns / routing) flat; the right pane shows the selected item's
// content (global fields or DSL text), scrollable.
type configsPage struct {
	sel   driver.Selections
	err   error
	focus int // 0 left, 1 right (scroll)

	groups []driver.Group // for reference-existence checks in routing profiles
	ifaces []driver.NetworkInterface

	rows   []rowRef
	cur    int
	scroll int // right pane line offset

	// summaryView shows the parsed structure of a dns/routing profile
	// instead of its raw DSL (v toggles). Both at once would print every
	// rule twice for an already-canonical DSL.
	summaryView bool

	// diff is an $EDITOR edit that passed backend validation and awaits the
	// user's y/n. Showing the diff before committing is what makes "the
	// backend accepted it" distinguishable from "this is what I meant to
	// change"; the temp file stays on disk until the edit is applied, so a
	// cancelled edit is still recoverable.
	diff *diffState

	// validateErr holds the backend parser's rejection of the last $EDITOR
	// session, per profile ID, so it follows the cursor.
	validateErr map[string]editRejection

	// modal state
	mode       int // 0 list, 1 fieldPicker, 2 fieldInput, 3 createInput, 4 renameInput, 5 deleteConfirm, 6 diffConfirm, 7 builtinEditor
	pickCursor int
	input      textinput.Model
	editField  driver.ConfigField
	fieldErr   string // client-side type rejection of the field input

	// builtin DSL editor (mode 7): the in-app alternative to $EDITOR for
	// dns/routing text. edCtx identifies what is being edited; edErr is the
	// backend parser's last rejection, shown inside the floating editor so
	// the text stays put while it is fixed.
	builtin   bool
	ed        textarea.Model
	edSection string
	edID      string
	edOld     string
	edErr     string

	caps driver.Caps

	leftW, rightW, height int
}

// diffState is a validated $EDITOR edit awaiting confirmation.
type diffState struct {
	Section string // config | dns | routing
	ID      string
	Name    string
	Old     string
	Text    string
	Path    string // temp file, kept until the edit is applied or cancelled
	Builtin bool   // produced by the in-app editor: no file, cancel returns to it
}

type rowRef struct {
	kind    int // rowItem | rowHeader
	section string
	index   int // item index within its section (rowItem only)
}

// Section headers are cursor rows: the flat list jumps between sections
// without the header ever being selected, which made the cursor position and
// the visual grouping drift apart.
const (
	rowItem = iota
	rowHeader
)

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

// editRejection is the backend parser's rejection of an $EDITOR session: the
// temp file is kept so the edit can be recovered.
type editRejection struct {
	Path string
	Err  string // flattened parser error, one display line
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
	for _, section := range []string{"config", "dns", "routing"} {
		items := itemsOf(p.sel, section)
		if len(items) == 0 {
			continue
		}
		p.rows = append(p.rows, rowRef{kind: rowHeader, section: section})
		for i := range items {
			p.rows = append(p.rows, rowRef{kind: rowItem, section: section, index: i})
		}
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
	p.validateErr = map[string]editRejection{} // a reload retires stale edit errors
	p.diff = nil                               // …and any pending diff confirmation
	p.rebuild()
}

// setGroups keeps the group list for reference checks: a routing profile
// that references a group which no longer exists has silently dead rules.
func (p *configsPage) setGroups(groups []driver.Group) { p.groups = groups }

// setInterfaces keeps the NIC list for lan/wan interface hints.
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

// ifaceHint lists the backend's NICs for the lan/wan interface fields, where
// a mistyped name is the classic "config applies but nothing is proxied"
// misconfiguration.
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
			s += "(默认路由)"
		}
		parts = append(parts, s)
	}
	return "可用: " + strings.Join(parts, ", ")
}

// ifaceWarning flags a configured interface name the backend does not have.
func (p configsPage) ifaceWarning(f driver.ConfigField) string {
	if !isIfaceField(f.Name) || len(p.ifaces) == 0 {
		return ""
	}
	for _, n := range configuredIfaces(f.Value) {
		if !p.hasIface(n) {
			return "⚠ 接口 " + n + " 不存在"
		}
	}
	return ""
}

func isIfaceField(name string) bool {
	return name == "lanInterface" || name == "wanInterface"
}

// item resolves a row to its profile. A header row stands for the section's
// selected profile (or its first), so Enter/e/R/D on a header act on the
// profile the user last chose in that section.
func (p *configsPage) item(r rowRef) *driver.ConfigItem {
	items := itemsOf(p.sel, r.section)
	if r.kind == rowHeader {
		if src := p.srcProfile(r.section); src != nil {
			return src
		}
		return nil
	}
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

	case 7: // builtin DSL editor
		switch msg.String() {
		case "esc":
			p.mode = 0
			p.ed.Blur()
			return nil
		case "ctrl+s":
			text := strings.TrimSpace(p.ed.Value())
			if text == "" || text == strings.TrimSpace(p.edOld) {
				p.mode = 0
				p.ed.Blur()
				return nil
			}
			p.edErr = ""
			return validateTextCmd(d, p.edSection, p.edID, text, p.edOld, "")
		}
		var cmd tea.Cmd
		p.ed, cmd = p.ed.Update(msg)
		return cmd

	case 6: // DSL diff confirm
		switch msg.String() {
		case "y":
			st := p.diff
			p.diff, p.mode = nil, 0
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

	case 1: // field picker (config global fields)
		r := p.curRow()
		if r == nil {
			p.mode = 0
			return nil
		}
		it := p.item(*r)
		fields := orderedFields(it.Fields)
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
			if val == "" || val == f.Value || r == nil {
				p.mode = 0
				p.input.Blur()
				return nil
			}
			// Catch a type mismatch here: the backend would reject it too,
			// but only after a round trip and with a less pointed message.
			if err := validateFieldValue(f, val); err != nil {
				p.fieldErr = err.Error()
				return nil
			}
			p.mode = 0
			p.input.Blur()
			it := p.item(*r)
			return configFieldCmd(d, it.ID, f, val, "修改 "+fieldLabel(f))
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

// editorArgv resolves which editor to launch for DSL editing. POSIX
// precedence is VISUAL over EDITOR; when neither is set we fall back to the
// first editor that exists on this machine, so a minimal image without vi
// still works. The value may carry arguments ("omarchy-launch-editor
// --inline"), so it is split into words and exec'd directly: going through a
// shell with the value quoted looks for one binary whose name contains the
// spaces and fails with exit 127.
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
	return nil, errors.New("未找到可用的编辑器：请设置 $EDITOR（如 EDITOR=nvim）")
}

// editorCmd builds the command that opens path in the resolved editor, plus
// the editor as a display string for error messages.
func editorCmd(path string) (*exec.Cmd, string, error) {
	argv, err := editorArgv()
	if err != nil {
		return nil, "", err
	}
	args := append(append([]string{}, argv[1:]...), path)
	return exec.Command(argv[0], args...), strings.Join(argv, " "), nil
}

// openBuiltinEditor starts the in-app floating editor with the profile's
// current DSL. Same contract as editInEditor minus the file: ctrl+s
// validates, a rejection keeps the editor open with the error, and a
// cancelled diff returns here instead of to a temp file.
func (p *configsPage) openBuiltinEditor(r rowRef, it driver.ConfigItem) tea.Cmd {
	delete(p.validateErr, it.ID)
	ta := textarea.New()
	ta.Placeholder = "dae DSL"
	ta.SetWidth(min(76, max(40, p.rightW)))
	ta.SetHeight(10)
	ta.CharLimit = 0
	ta.SetValue(it.Body)
	ta.Focus()
	p.ed = ta
	p.edSection, p.edID, p.edOld, p.edErr = r.section, it.ID, it.Body, ""
	p.mode = 7
	return textarea.Blink
}

// editInEditor hands the raw DSL to $VISUAL/$EDITOR via tea.ExecProcess.
func (p *configsPage) editInEditor(d driver.Driver, r rowRef, it driver.ConfigItem) tea.Cmd {
	delete(p.validateErr, it.ID) // a new session supersedes the last rejection
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

	c, editor, err := editorCmd(f.Name())
	if err != nil {
		os.Remove(f.Name())
		return func() tea.Msg { return opDoneMsg{Op: "编辑", Err: err} }
	}
	old := it.Body
	path, section, id := f.Name(), r.section, it.ID
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorDoneMsg{Path: path, Section: section, ID: id, Old: old, Err: err,
			Editor: editor}
	})
}

// handleEditorDone reads the edited file back. Changed content is parsed by
// the backend before it is submitted; the temp file stays on disk until the
// new text is known to be valid, so a rejected edit is never lost.
func (p *configsPage) handleEditorDone(msg editorDoneMsg, d driver.Driver) tea.Cmd {
	if msg.Err != nil {
		os.Remove(msg.Path)
		return func() tea.Msg { return opDoneMsg{Op: "编辑 " + msg.Editor, Err: msg.Err} }
	}
	raw, err := os.ReadFile(msg.Path)
	if err != nil {
		os.Remove(msg.Path)
		return func() tea.Msg { return opDoneMsg{Op: "编辑", Err: err} }
	}
	text := strings.TrimSpace(string(raw))
	if text == "" || text == strings.TrimSpace(msg.Old) {
		os.Remove(msg.Path)
		return nil // unchanged
	}
	// The file is only removed once the new text is known to be valid (see
	// handleValidated).
	return validateTextCmd(d, msg.Section, msg.ID, text, msg.Old, msg.Path)
}

// reopenBuiltinEditor puts a declined diff back into the floating editor.
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
	p.mode = 7
	return textarea.Blink
}

// handleValidated shows the diff of a backend-accepted edit and submits it
// only after confirmation. A rejection keeps the temp file and the error
// stays visible in the detail pane — unless the builtin editor is open, in
// which case the editor itself is the surviving copy and stays open.
func (p *configsPage) handleValidated(msg editorValidatedMsg, d driver.Driver) tea.Cmd {
	if msg.Err != nil {
		if p.mode == 7 {
			p.edErr = flattenErr(msg.Err)
			return func() tea.Msg {
				return opDoneMsg{Op: sectionName(msg.Section) + " 校验", Err: errors.New("校验未通过（编辑器保持打开）")}
			}
		}
		if p.validateErr == nil {
			p.validateErr = map[string]editRejection{}
		}
		p.validateErr[msg.ID] = editRejection{Path: msg.Path, Err: flattenErr(msg.Err)}
		return func() tea.Msg {
			return opDoneMsg{Op: sectionName(msg.Section) + " 校验", Err: errors.New("校验未通过，未保存")}
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

// profileName resolves a profile ID to its display name, for the diff title.
func (p *configsPage) profileName(section, id string) string {
	for _, it := range itemsOf(p.sel, section) {
		if it.ID == id {
			return it.Name
		}
	}
	return ""
}

// flattenErr squeezes a multi-line parser error (daed points at the
// offending token with a caret line) into a single display line.
func flattenErr(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}

func (p *configsPage) bodyLines() []string {
	r := p.curRow()
	if r == nil {
		return []string{ui.HelpStyle.Render("（无配置）")}
	}
	it := p.item(*r)
	if r.section == "config" {
		lines := []string{ui.SelectedStyle.Render("全局配置 · " + it.Name)}
		// The editable surface is the field list; rendering it here keeps
		// labels a UI concern (the driver supplies keys and values only).
		if fields := orderedFields(it.Fields); len(fields) > 0 {
			for _, f := range fields {
				// Values can be long (URL lists); never bleed past the pane.
				lines = append(lines, ui.Truncate(" "+ui.PadRight(fieldLabel(f), 18)+f.Value,
					max0(p.rightW-4)))
				if warn := p.ifaceWarning(f); warn != "" {
					lines = append(lines, ui.ErrorStyle.Render("   "+warn))
				}
			}
			return lines
		}
		return append(lines, fallbackBody(it)...)
	}
	title := sectionTitles[r.section]
	lines := []string{}
	if r.section == "routing" && len(it.References) > 0 {
		// daed's referenceGroups lists every name the DSL targets — dae
		// built-ins (direct, must_direct, …) included. A referenced group
		// that no longer exists means every rule aimed at it is dead weight,
		// so say so; a built-in is valid without any group, so label it
		// instead of flagging it.
		parts := make([]string, 0, len(it.References))
		for _, g := range it.References {
			switch {
			case p.hasGroup(g):
				parts = append(parts, g)
			case isBuiltinOutbound(g):
				parts = append(parts, ui.HelpStyle.Render(g+" (内置)"))
			case len(p.groups) > 0:
				parts = append(parts, ui.ErrorStyle.Render(g+" (已不存在)"))
			default:
				parts = append(parts, g) // group list not loaded yet
			}
		}
		lines = append(lines, ui.Truncate(ui.SelectedStyle.Render("引用组 ")+
			strings.Join(parts, ", "), max0(p.rightW-4)))
	}
	if p.summaryView && len(it.Summary) > 0 {
		lines = append(lines, ui.SelectedStyle.Render(title+" (结构概览) · "+it.Name))
		for _, s := range it.Summary {
			lines = append(lines, "  "+ui.Truncate(s, max0(p.rightW-6)))
		}
		return lines
	}
	lines = append(lines, ui.SelectedStyle.Render(title+" (DSL 原文) · "+it.Name))
	return append(lines, fallbackBody(it)...)
}

// fallbackBody renders a profile's stored text, used when there is nothing
// better to show (no fields, no parsed structure, or the raw view).
func fallbackBody(it *driver.ConfigItem) []string {
	body := it.Body
	if body == "" {
		body = it.Detail
	}
	if body == "" {
		return []string{ui.HelpStyle.Render(" （无内容：编辑请在 daed 完成）")}
	}
	lines := make([]string, 0, 8)
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
	switch msg.String() {
	case "v": // toggle raw DSL / parsed structure
		p.summaryView = !p.summaryView
		p.scroll = 0
		return nil
	case "y": // copy the profile's DSL to the clipboard (OSC 52)
		if r := p.curRow(); r != nil {
			if body := p.item(*r).Body; body != "" {
				return osc52CopyCmd(body)
			}
		}
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
			if !p.caps.ConfigMgmt {
				return unsupportedCmd("新建配置")
			}
			r := p.curRow()
			if r != nil {
				p.mode = 3
				return p.openInput("名称（将复制当前选中配置）", "")
			}
		case "R": // rename
			if !p.caps.ConfigMgmt {
				return unsupportedCmd("重命名配置")
			}
			if r := p.curRow(); r != nil {
				it := p.item(*r)
				p.mode = 4
				return p.openInput("新名称", it.Name)
			}
		case "D": // delete (confirm)
			if !p.caps.ConfigMgmt {
				return unsupportedCmd("删除配置")
			}
			if r := p.curRow(); r != nil {
				it := p.item(*r)
				if err := p.deletable(r.section, it); err != nil {
					return func() tea.Msg { return opDoneMsg{Op: "删除", Err: err} }
				}
				p.mode = 5
			}
		case "e": // edit content
			if !p.caps.ConfigMgmt {
				return unsupportedCmd("编辑配置")
			}
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
				if p.builtin {
					return p.openBuiltinEditor(*r, *it)
				}
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
	// The "modified" banner takes one row of the pane budget — appended
	// below the panes it would be the first line the frame's clamp cuts.
	h := p.height
	var banner string
	if modified {
		h--
		banner = ui.ErrorStyle.Render(" ⚠ 运行配置与选中项不一致（A 应用 / 首页 o 重启）")
	}
	lv := ui.Pane(false, " 配置方案 ", p.focus == 0, p.leftW, h, p.leftLines())
	rv := ui.Pane(true, " 内容 ", p.focus == 1, p.rightW, h, p.rightLines())
	body := lipgloss.JoinHorizontal(lipgloss.Top, lv, " ", rv)
	if banner != "" {
		body = banner + "\n" + body
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
		fields := orderedFields(p.item(*r).Fields)
		lines := []string{ui.TitleStyle.Render(" 选择要修改的字段") +
			ui.HelpStyle.Render(fmt.Sprintf("  (%d)", len(fields)))}
		// Window the picker: a config exposes every global field, far more
		// than fit on screen.
		rowsH := max0(p.height - 6)
		start := 0
		if p.pickCursor >= rowsH {
			start = p.pickCursor - rowsH + 1
		}
		end := min(start+rowsH, len(fields))
		for i := start; i < end; i++ {
			f := fields[i]
			mark, style := "  ", ui.HelpStyle
			if i == p.pickCursor {
				mark, style = "❯ ", ui.CursorStyle
			}
			line := style.Render(mark+ui.PadRight(fieldLabel(f), 16)) +
				ui.HelpStyle.Render(ui.PadRight("["+f.Type+"]", 11)) + f.Value
			if i == p.pickCursor && f.Default != "" {
				line += ui.HelpStyle.Render("  默认 " + f.Default)
			}
			if warn := p.ifaceWarning(f); warn != "" {
				line += ui.ErrorStyle.Render("  " + warn)
			}
			if i == p.pickCursor {
				if hint := p.ifaceHint(f); hint != "" {
					line += ui.HelpStyle.Render("  " + hint)
				}
			}
			// Long values (URL lists) must not bleed past the pane.
			lines = append(lines, ui.Truncate(line, max0(p.rightW-4)))
		}
		if start > 0 || end < len(fields) {
			lines = append(lines, ui.HelpStyle.Render(fmt.Sprintf(" … %d-%d / %d，j/k 滚动", start+1, end, len(fields))))
		}
		return append(lines, ui.HelpStyle.Render(" Enter 编辑  esc 取消"))
	case 5: // delete confirm
		if r := p.curRow(); r != nil {
			it := p.item(*r)
			return ui.BoxLines(true, "确认删除"+sectionName(r.section)+" \""+it.Name+"\"?  (y/n)")
		}
	case 6: // DSL diff confirm
		if p.diff == nil {
			return []string{ui.HelpStyle.Render("（无待确认的更改）")}
		}
		st := p.diff
		// The decision line lives in a red box of its own: when everything
		// around it is +/- diff noise, a dim hint row is too easy to miss.
		head := ui.BoxLines(true,
			ui.TitleStyle.Render(" 确认应用更改 · "+sectionName(st.Section)+" "+st.Name),
			ui.OKStyle.Render(" y 应用")+"    "+
				ui.ErrorStyle.Render("n / esc 取消")+"    "+
				ui.HelpStyle.Render("j/k 滚动 diff"),
			ui.HelpStyle.Render(" 取消后编辑内容保留在 "+ui.TruncateHead(st.Path, max0(p.rightW-6))),
			"")
		lines := append([]string{}, head...)
		for _, dl := range diffLines(st.Old, st.Text) {
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
		// Window the diff: a large edit can easily exceed the pane, and the
		// clamp is best-effort (the stored scroll is re-based on entry).
		rowsH := max0(p.height - 6)
		if p.scroll > max0(len(lines)-rowsH) {
			p.scroll = max0(len(lines) - rowsH)
		}
		if p.scroll < 0 {
			p.scroll = 0
		}
		end := p.scroll + rowsH
		if end > len(lines) {
			end = len(lines)
		}
		return lines[p.scroll:end]
	}
	return nil
}

// overlay returns the page's floating windows: the field input, the
// create/rename forms and the builtin DSL editor. The field picker, the
// delete confirmation and the diff confirmation stay in the right pane.
func (p configsPage) overlay() *overlaySpec {
	switch p.mode {
	case 2:
		f := p.editField
		lines := []string{
			ui.TitleStyle.Render(" 修改 " + fieldLabel(f)),
			"",
			" 当前值  " + ui.HelpStyle.Render(ui.Truncate(f.Value, 56)),
			" 新值    " + p.input.View(),
		}
		if f.Default != "" {
			lines = append(lines, " 默认值  "+ui.HelpStyle.Render(f.Default))
		}
		if hint := p.ifaceHint(f); hint != "" {
			lines = append(lines, ui.HelpStyle.Render(" "+hint))
		}
		if warn := p.ifaceWarning(f); warn != "" {
			lines = append(lines, ui.ErrorStyle.Render(" "+warn))
		}
		if f.Desc != "" {
			lines = append(lines, ui.HelpStyle.Render(" 说明    "+
				ui.Truncate(firstLine(f.Desc), 56)))
		}
		if p.fieldErr != "" {
			lines = append(lines, ui.ErrorStyle.Render(" ✗ "+p.fieldErr))
		}
		return &overlaySpec{lines: append(lines, "",
			ui.HelpStyle.Render(" 类型 "+f.Type+"（数组用逗号分隔）  Enter 提交  esc 返回"))}
	case 3, 4:
		r := p.curRow()
		title := "重命名"
		if p.mode == 3 {
			title = "新建"
		}
		if r != nil {
			title += sectionName(r.section)
		}
		hint := " Enter 确认  esc 取消"
		if p.mode == 3 && r != nil {
			// Mirror what CreateProfile actually does: it clones the
			// selected profile of the section, and only falls back to the
			// built-in template when there is nothing to clone.
			what := "默认模板"
			if src := p.srcProfile(r.section); src != nil && (src.Body != "" || len(src.Fields) > 0) {
				what = "当前选中" + sectionName(r.section) + "的内容"
			}
			hint = " 将复制" + what + "，之后可 e 编辑  Enter 确认  esc 取消"
		}
		return &overlaySpec{lines: []string{
			ui.TitleStyle.Render(" " + title),
			"",
			" 名称  " + p.input.View(),
			"",
			ui.HelpStyle.Render(hint),
		}}
	case 7:
		lines := []string{
			ui.TitleStyle.Render(" 编辑 " + sectionName(p.edSection) + " DSL · " +
				p.profileName(p.edSection, p.edID)),
			"",
		}
		lines = append(lines, strings.Split(p.ed.View(), "\n")...)
		if p.edErr != "" {
			lines = append(lines, "", ui.ErrorStyle.Render(" ✗ "+ui.Truncate(p.edErr, 76)))
		}
		return &overlaySpec{lines: append(lines, "",
			ui.HelpStyle.Render(" ctrl+s 校验并预览 diff  esc 取消"),
			ui.HelpStyle.Render(" 想用 vim/nano 等编辑器：config.toml 里 editor = \"external\""))}
	}
	return nil
}

// leftClick parks the left-pane cursor on the row-th row (section headers
// included — they are valid cursor positions, the same as moving with j/k).
func (p *configsPage) leftClick(row int) {
	if row < 0 || row >= len(p.rows) || row == p.cur {
		return
	}
	p.cur = row
	p.scroll = 0
}

func (p configsPage) leftLines() []string {
	var lines []string
	if !p.caps.ConfigMgmt {
		lines = append(lines, ui.ErrorStyle.Render("✗ 当前后端不支持配置管理"))
	}
	if p.err != nil {
		lines = append(lines, ui.ErrorStyle.Render("✗ "+shortErr(p.err)))
	}
	for i, r := range p.rows {
		// The cursor marker renders on header rows too, so the position is
		// always visible even while parked on a section title.
		cursor := "  "
		if i == p.cur && p.focus == 0 {
			cursor = ui.CursorStyle.Render("❯")
		}
		if r.kind == rowHeader {
			lines = append(lines, cursor+" "+ui.SelectedStyle.Render(sectionTitles[r.section]))
			continue
		}
		it := itemsOf(p.sel, r.section)[r.index]
		mark := "  "
		if it.Selected {
			mark = ui.OKStyle.Render("● ")
		}
		lines = append(lines, cursor+" "+mark+ui.PadRight(it.Name, max0(p.leftW-10)))
	}
	return lines
}

func (p configsPage) rightLines() []string {
	if p.mode == 1 || p.mode == 5 || p.mode == 6 {
		return p.modalLines()
	}
	var lines []string
	// A rejected $EDITOR session stays visible until it is superseded.
	if r := p.curRow(); r != nil {
		if rej := p.validateErr[p.item(*r).ID]; rej.Err != "" {
			lines = append(lines,
				ui.ErrorStyle.Render(" ✗ "+sectionName(r.section)+" 校验未通过，未保存"),
				ui.HelpStyle.Render("  编辑内容保留在 "+
					ui.TruncateHead(rej.Path, max0(p.rightW-18))),
				ui.ErrorStyle.Render("  "+ui.Truncate(rej.Err, max0(p.rightW-4))),
				"",
			)
		}
	}
	lines = append(lines, p.bodyLines()...)
	rowsH := max0(p.height - 2)
	if p.scroll >= len(lines) {
		p.scroll = len(lines) - 1
	}
	if p.scroll < 0 {
		p.scroll = 0
	}
	end := p.scroll + rowsH
	if end > len(lines) {
		end = len(lines)
	}
	return lines[p.scroll:end]
}
