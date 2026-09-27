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

	rows []rowRef
	cur  int
	// sec is the active section box; Tab cycles it. secCur remembers each
	// box's cursor item index (kept in sync with cur for the active box, so
	// a refresh re-lands on the same row), secRange is each box's
	// [first,last] row span in rows (-1 when the section is empty).
	sec      int
	secCur   [3]int
	secRange [3][2]int

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
	// the text stays put while it is fixed. edEscArm is the discard guard:
	// until ctrl+s the editor holds the only copy of the edit, so the first
	// esc on changed text arms it and the second one actually discards.
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

// diffState is a validated $EDITOR edit awaiting confirmation.
type diffState struct {
	Section string // config | dns | routing
	ID      string
	Name    string
	Old     string
	Text    string
	Path    string // temp file, kept until the edit is applied or cancelled
	Builtin bool   // produced by the in-app editor: no file, cancel returns to it

	// lines is the computed diff, filled on first render: the LCS is
	// quadratic and View re-runs on every tick and spinner frame while the
	// confirmation is open. A fresh diffState starts nil (uncached).
	lines []diffLine
}

// rowRef points at one profile in the flat cursor list. The left column
// renders as three stacked section boxes (config/dns/routing); the cursor
// walks the items across box boundaries and the box holding it lights up —
// the old "section header is a cursor row" special case is gone with it.
type rowRef struct {
	section string
	index   int // item index within its section
}

// configSections is the left column's stacking order and rebuild order.
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

// editRejection is the backend parser's rejection of an $EDITOR session: the
// temp file is kept so the edit can be recovered.
type editRejection struct {
	Path string
	Err  string // flattened parser error, one display line
	Line int    // 1-based line the parser pointed at ("line 3:24 …"), 0 unknown
}

func (p *configsPage) setSize(leftW, rightW, h int) {
	p.leftW, p.rightW, p.height = leftW, rightW, h
}

// openInput (re)initializes the shared text input for create/rename/field
// editing modals. The limit only bounds a runaway paste: array field values
// (URL lists) legitimately outrun any name by a lot.
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
	// The active box must hold items; daed guarantees one profile per
	// section, this only guards a weird backend.
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
}

func (p *configsPage) handleSelections(sel driver.Selections, err error) {
	if err != nil {
		p.err = err
		return
	}
	p.err = nil
	p.sel = sel
	for _, rej := range p.validateErr {
		// The reload retires the rejection banners; their temp files are
		// the only copy of those edits and nothing references them anymore,
		// so leaving them behind leaks one /tmp/dae-tui-* per retry.
		os.Remove(rej.Path)
	}
	p.validateErr = map[string]editRejection{} // a reload retires stale edit errors
	p.diff = nil                               // …and any pending diff confirmation
	if p.mode == 6 {
		// A refresh can land after the diff was armed (the field submit and
		// its refresh are independent requests with no ordering), which
		// would otherwise leave an empty diff modal waiting for a keypress.
		p.mode = 0
	}
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

// item resolves a row to its profile. Every row is a real profile: with the
// left column zoned into one box per section there is no header row to
// resolve, and all keys act on exactly what the cursor is on.
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

// sectionName returns a Chinese label for a section.
func sectionName(section string) string {
	return sectionTitles[section]
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

// modalKey drives the field picker / inputs / confirm modals; the per-mode
// handlers live in configs_modal.go.

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

// retireRejection drops a stored $EDITOR rejection together with its temp
// file: the banner is the only thing that referenced the path, so deleting
// the record alone would orphan the file (one leak per edit-retry cycle).
func (p *configsPage) retireRejection(id string) {
	if rej, ok := p.validateErr[id]; ok {
		os.Remove(rej.Path)
		delete(p.validateErr, id)
	}
}

// openBuiltinEditor starts the in-app floating editor with the profile's
// current DSL. Same contract as editInEditor minus the file: ctrl+s
// validates, a rejection keeps the editor open with the error, and a
// cancelled diff returns here instead of to a temp file.
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

// editInEditor hands the raw DSL to $VISUAL/$EDITOR via tea.ExecProcess.
func (p *configsPage) editInEditor(d driver.Driver, r rowRef, it driver.ConfigItem) tea.Cmd {
	p.retireRejection(it.ID) // a new session supersedes the last rejection
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
	p.edErr, p.edEscArm = "", false
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
		p.validateErr[msg.ID] = editRejection{Path: msg.Path, Err: flattenErr(msg.Err),
			Line: errLineNo(msg.Err)}
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

// errLineNo extracts the 1-based line number a daed parse error points at
// ("line 3:24 …"), so the raw DSL view can mark that line; 0 when the
// message names no line.
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
					max0(p.rightW-6)))
				if warn := p.ifaceWarning(f); warn != "" {
					lines = append(lines, ui.ErrorStyle.Render("   "+warn))
				}
			}
			return lines
		}
		return append(lines, fallbackBody(it, 0)...)
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
			strings.Join(parts, ", "), max0(p.rightW-6)))
	}
	if p.summaryView && len(it.Summary) > 0 {
		lines = append(lines, ui.SelectedStyle.Render(title+" (结构概览) · "+it.Name))
		for _, s := range it.Summary {
			lines = append(lines, "  "+ui.Truncate(s, max0(p.rightW-8)))
		}
		return lines
	}
	lines = append(lines, ui.SelectedStyle.Render(title+" (DSL 原文) · "+it.Name))
	// A rejected edit highlights the exact line the parser pointed at, so
	// "line 3" is somewhere to look instead of something to count.
	return append(lines, fallbackBody(it, p.validateErr[it.ID].Line)...)
}

// fallbackBody renders a profile's stored text, used when there is nothing
// better to show (no fields, no parsed structure, or the raw view). badLine
// (1-based) is the line a rejected edit's parse error pointed at; it renders
// marked so the error's "line N" is directly findable.
func fallbackBody(it *driver.ConfigItem, badLine int) []string {
	body := it.Body
	if body == "" {
		body = it.Detail
	}
	if body == "" {
		return []string{ui.HelpStyle.Render(" （无内容：编辑请在 daed 完成）")}
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
			// The config section is a field table with no DSL; say so
			// instead of silently doing nothing.
			return func() tea.Msg {
				return opDoneMsg{Op: "复制", Err: errors.New("该方案没有 DSL 可复制（全局配置是字段表）")}
			}
		}
	}
	if p.focus == 0 {
		switch msg.String() {
		case "j", "down":
			// j/k stay inside the active box; Tab is how you cross sections.
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
		case "c": // clone the profile under the cursor
			if !p.caps.ConfigMgmt {
				return unsupportedCmd("新建配置")
			}
			r := p.curRow()
			if r != nil {
				p.mode = 3
				return p.openInput("名称（将复制光标所指方案）", "")
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
					return func() tea.Msg {
						return opDoneMsg{Op: "编辑", Err: errors.New("该配置没有可编辑字段")}
					}
				}
				// Re-opening the picker keeps the cursor where the user left
				// it: editing several fields in a row must not restart the
				// hunt through every global field each time.
				if n := len(orderedFields(it.Fields)); p.pickCursor >= n {
					p.pickCursor = n - 1
				}
				p.mode = 1
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
	// Whether the running config is stale is the top bar's "⚠ 需重载" hint;
	// this page carries no banner of its own.
	h := p.height
	// Key hints ride the edges of the boxes they belong to: the left box
	// lists profile management, the right box the content view's keys (and
	// the open modal's keys); page-wide keys stay on the app frame.
	rightFooter := "j/k 滚动"
	switch p.mode {
	case 1:
		rightFooter = "Enter 编辑 · j/k 移动 · esc 取消"
	case 5:
		rightFooter = "y 确认 · n/esc 取消"
	case 6:
		rightFooter = "y 提交 · n/esc 取消 · j/k 滚动"
	case 2, 3, 4, 7:
		// A floating window owns every keystroke; the pane behind it
		// advertises none.
		rightFooter = ""
	}
	// Left column: three stacked section boxes; right: the detail box. Both
	// sides are exactly h rows so JoinBoxes keeps them flush.
	return strings.Join(ui.JoinBoxes(p.leftBoxes(h), p.rightBox(h, rightFooter)), "\n")
}

// rightBox renders the detail pane at exactly h rows — the content clamped
// and padded, what PaneRow did for the boxes before the left column grew
// its own stacked composition.
func (p configsPage) rightBox(h int, footer string) []string {
	lines := p.rightLines()
	if len(lines) > h-2 {
		lines = lines[:h-2]
	}
	for len(lines) < h-2 {
		lines = append(lines, "")
	}
	return ui.TitledBoxFooter("内容", footer, p.focus == 1, p.rightW, lines)
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
		// than fit on screen (its Enter/esc hints ride the box's footer).
		rowsH := max0(p.height - 4)
		start := 0
		if p.pickCursor >= rowsH {
			start = p.pickCursor - rowsH + 1
		}
		end := min(start+rowsH, len(fields))
		var window []string
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
			window = append(window, ui.HiRow(ui.Truncate(line, max0(p.rightW-6)), p.rightW-4, i == p.pickCursor))
		}
		window = ui.WithScrollbar(window, p.rightW-4, len(fields), start, p.focus == 1)
		lines = append(lines, window...)
		if start > 0 || end < len(fields) {
			lines = append(lines, ui.HelpStyle.Render(fmt.Sprintf(" … %d-%d / %d，j/k 滚动", start+1, end, len(fields))))
		}
		return lines
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
			ui.TitleStyle.Render(" 确认提交更改 · "+sectionName(st.Section)+" "+st.Name),
			ui.OKStyle.Render(" y 提交")+"    "+
				ui.ErrorStyle.Render("n / esc 取消")+"    "+
				ui.HelpStyle.Render("j/k 滚动 diff"),
			ui.HelpStyle.Render(" 取消后编辑内容保留在 "+ui.TruncateHead(st.Path, max0(p.rightW-8))),
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
		// Window the diff: a large edit can easily exceed the pane, and the
		// clamp is best-effort (the stored scroll is re-based on entry).
		// The decision box above costs 6 bordered lines of the h-2 budget.
		rowsH := max0(p.height - 8)
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
			// Mirror what CreateProfile actually does: it clones the profile
			// under the cursor (a section header means its selected one),
			// and only falls back to the built-in template when there is
			// nothing to clone.
			switch src := p.item(*r); {
			case src != nil && (src.Body != "" || len(src.Fields) > 0):
				hint = " 将复制「" + src.Name + "」的内容，之后可 e 编辑  Enter 确认  esc 取消"
			default:
				hint = " 将使用默认模板，之后可 e 编辑  Enter 确认  esc 取消"
			}
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
		if p.edEscArm {
			lines = append(lines, "", ui.ErrorStyle.Render(" ⚠ 修改尚未提交：再按一次 esc 放弃，或 ctrl+s 校验保存"))
		}
		return &overlaySpec{lines: append(lines, "",
			ui.HelpStyle.Render(" ctrl+s 校验并预览 diff  esc 取消"),
			ui.HelpStyle.Render(" 想用 vim/nano 等编辑器：config.toml 里 editor = \"external\""))}
	}
	return nil
}

// leftClick parks the cursor on the clicked row of the stacked section
// boxes. Box borders and dead space are no-ops; a content row maps back
// through the same windowing leftBoxes used.
func (p *configsPage) leftClick(row int) {
	if row < 0 || row >= p.height {
		return
	}
	if p.height < 9 {
		// The flat fallback box (see flatLeftBox): skip its extra lines,
		// then map through the same window. The clicked row's section
		// becomes the active one.
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
		}
		return
	}
	shares := leftShares(p.height)
	// row is a content row (the boxes' shared top border sits at row -1, the
	// root's y<5 guard already ate it), so the body row the three-box stack
	// is laid out in is row+1; each box then charges its own top border back
	// before mapping onto items.
	b := row + 1
	for si, section := range configSections {
		if b >= shares[si] {
			b -= shares[si]
			continue
		}
		within := b - 1 // the box's top border
		rowsH := shares[si] - 2
		if within < 0 || within >= rowsH {
			return // top border, bottom border or footer edge
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
				p.switchSection(si) // clicking a box activates it
			}
			p.setCursor(p.rowAt(section, i))
		}
		return
	}
}

// switchSection activates another section box: the cursor jumps to that
// box's remembered position (or its first item) and the right pane follows.
func (p *configsPage) switchSection(si int) {
	if si == p.sec || p.secRange[si][0] < 0 {
		return
	}
	if r := p.curRow(); r != nil {
		p.secCur[p.sec] = r.index // remember where we leave the cursor
	}
	p.sec = si
	p.scroll = 0
	p.cur = p.rowAt(configSections[si], p.secCur[si])
	if p.cur < 0 {
		p.cur = p.secRange[si][0]
	}
}

// setCursor parks the cursor on a flat row of the active box and rewinds
// the right pane; secCur stays in sync so a refresh re-lands on the row.
func (p *configsPage) setCursor(i int) {
	if i < 0 || i >= len(p.rows) || i == p.cur {
		return
	}
	p.cur = i
	if r := p.curRow(); r != nil && r.section == configSections[p.sec] {
		p.secCur[p.sec] = r.index
	}
	p.scroll = 0
}

// rightClick opens the field editor whose row the user clicked in the right
// pane. Only the config section renders one line per editable field; the
// dns/routing panes are scroll views, so a click there is ignored.
func (p *configsPage) rightClick(row int) tea.Cmd {
	if p.mode != 0 || row < 0 {
		return nil
	}
	r := p.curRow()
	if r == nil || r.section != "config" {
		return nil
	}
	fields := orderedFields(p.item(*r).Fields)
	if len(fields) == 0 {
		return nil
	}
	// bodyLines renders the title line, then one line per field with an
	// optional interface-warning line after some of them; rightLines windows
	// those lines by p.scroll — but it also prepends the 4-line rejection
	// banner when the last $EDITOR session was refused, and the walk below
	// must start after it or every field row maps to the wrong field.
	head := 0
	if rej := p.validateErr[p.item(*r).ID]; rej.Err != "" {
		head = 4
	}
	line := p.scroll + row - 1 - head // -1: the title line
	if line < 0 {
		return nil
	}
	for i := range fields {
		if line == 0 {
			f := fields[i]
			p.editField = f
			p.fieldErr = ""
			p.mode = 2
			p.focus = 1
			return p.openInput("新值 ("+f.Type+")", f.Value)
		}
		line--
		if p.ifaceWarning(fields[i]) != "" {
			line--
		}
	}
	return nil
}

// leftFooter is the left column's key hint. The keys work in every section
// box, so they ride the bottom edge of the active one — the lit zone —
// instead of repeating on all three.
const leftFooter = "Tab 切区 · e 编辑 · c/R/D 管理"

// leftShares splits the left column's height into three equal box heights
// (btop zoning: equal shares, dead space stays inside the box); the
// remainder goes to the top boxes so the stack is exactly h rows.
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

// rowAt maps a (section, item index) pair back to its flat cursor row, -1
// when there is none.
func (p configsPage) rowAt(section string, index int) int {
	for i, r := range p.rows {
		if r.section == section && r.index == index {
			return i
		}
	}
	return -1
}

// itemLine renders one left-column profile row: cursor marker, selected
// mark, name.
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

// leftLinesExtra counts the non-item lines heading the first box (caps and
// load errors) — both the renderer and the click mapping skip them.
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

// leftWindow returns the first item index shown in box si: boxes other than
// the active one start at the top, the active box windows so the cursor
// stays visible (the same rule the field picker uses).
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

// leftBoxes renders the left column: one titled box per section, stacked to
// exactly h rows. The box holding the cursor lights up and carries the
// column's key hints.
func (p configsPage) leftBoxes(h int) []string {
	if h < 9 {
		// Too short to give every box one content row: fall back to a
		// single flat box (the right pane's title still says what is shown).
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
			footer = leftFooter
		}
		lines := make([]string, 0, rowsH)
		if si == 0 {
			if !p.caps.ConfigMgmt {
				lines = append(lines, ui.ErrorStyle.Render("✗ 当前后端不支持配置管理"))
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
		title := fmt.Sprintf("%s %d", sectionTitles[section], len(items))
		out = append(out, ui.TitledBoxFooter(title, footer, focused, p.leftW, lines)...)
	}
	return out
}

// flatLeftBox is the tiny-terminal fallback: one box, all sections' items in
// cursor order, no zoning.
func (p configsPage) flatLeftBox(h int) []string {
	rowsH := max0(h - 2)
	extra := p.leftLinesExtra()
	lines := make([]string, 0, rowsH)
	if !p.caps.ConfigMgmt {
		lines = append(lines, ui.ErrorStyle.Render("✗ 当前后端不支持配置管理"))
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
	return ui.TitledBoxFooter("配置方案", leftFooter, p.focus == 0, p.leftW, lines)
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
					ui.TruncateHead(rej.Path, max0(p.rightW-20))),
				ui.ErrorStyle.Render("  "+ui.Truncate(rej.Err, max0(p.rightW-6))),
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
