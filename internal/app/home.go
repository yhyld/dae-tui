package app

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/driver"
	"dae-tui/internal/ui"
)

// homePage is page 1: proxy on/off switch, live traffic charts, and the
// currently used node of every group (fixed → exact, auto → inferred from
// the best latency we have seen).
type homePage struct {
	groups []driver.Group
	lat    map[string]driver.Latency
	trafficPage

	confirmSwitch bool
	width, height int
}

func newHomePage() homePage {
	return homePage{lat: map[string]driver.Latency{}}
}

func (p *homePage) setSize(w, h int) {
	p.width, p.height = w, h
	p.trafficPage.setSize(w, h)
}

func (p *homePage) handleGroups(groups []driver.Group, err error) {
	if err != nil {
		return
	}
	p.groups = groups
}

func (p *homePage) handleLatencies(lats []driver.Latency) {
	for _, l := range lats {
		p.lat[l.NodeID] = l
	}
}

func (p *homePage) handleKey(msg tea.KeyMsg, d driver.Driver, running bool) tea.Cmd {
	if p.confirmSwitch {
		switch msg.String() {
		case "y":
			p.confirmSwitch = false
			return runToggleCmd(d, running)
		case "n", "esc":
			p.confirmSwitch = false
		}
		return nil
	}
	switch msg.String() {
	case "o":
		p.confirmSwitch = true
	}
	return nil
}

// currentNode describes what a group is using right now.
func (p *homePage) currentNode(g driver.Group) (label string, style lipgloss.Style) {
	if sel := g.SelectedNode(); sel != nil && g.Policy == "fixed" {
		s := "手动: " + ui.SpaceAfterFlag(sel.Name)
		if l, ok := p.lat[sel.ID]; ok && l.Alive && l.Ms > 0 {
			s += fmt.Sprintf("  (%dms)", l.Ms)
		}
		return s, ui.OKStyle
	}
	// Auto policy: the actual pick lives inside the dae core and is not
	// exposed via the API — show the best-latency node as an estimate.
	best, bestMs := "", -1
	for _, n := range g.Members() {
		if l, ok := p.lat[n.ID]; ok && l.Alive && l.Ms > 0 && (bestMs < 0 || l.Ms < bestMs) {
			best, bestMs = ui.SpaceAfterFlag(n.Name), l.Ms
		}
	}
	auto := map[string]string{
		"min_moving_avg": "自动(移动平均)", "min_avg10": "自动(平均)", "min": "自动(最新)",
	}[g.Policy]
	if auto == "" {
		auto = g.Policy
	}
	if best != "" {
		return fmt.Sprintf("%s ≈ %s  (%dms)", auto, best, bestMs), ui.SelectedStyle
	}
	return auto, ui.HelpStyle
}

func (p homePage) View(status driver.Status) string {
	var b strings.Builder

	// --- status & switch line ---
	run := ui.OKStyle.Render("● 代理运行中")
	hint := ui.HelpStyle.Render("  o 停止")
	if !status.Running {
		run = ui.ErrorStyle.Render("○ 代理已停止")
		hint = ui.HelpStyle.Render("  o 启动")
	}
	mod := ""
	if status.Modified {
		mod = ui.ErrorStyle.Render("  ⚠ 配置改动未应用（4 配置页 A 应用）")
	}
	b.WriteString(" " + run + hint + ui.HelpStyle.Render("   dae "+status.Version) + mod + "\n")

	if p.confirmSwitch {
		verb := "启动"
		if status.Running {
			verb = "停止"
		}
		b.WriteString(ui.ErrorStyle.Render(" ▸ 确认"+verb+"代理? (y/n)") + "\n")
	}
	b.WriteString("\n")

	// --- traffic charts (smaller than the old full page) ---
	chartW := max0(p.width/2 - 16)
	if chartW < 20 {
		chartW = 20
	}
	chartH := 4
	green := lipgloss.NewStyle().Foreground(ui.Green)
	yellow := lipgloss.NewStyle().Foreground(ui.Yellow)
	s := p.snap
	up := ui.Sparkline(s.UpSeries, chartW, chartH, green, "↑")
	down := ui.Sparkline(s.DownSeries, chartW, chartH, yellow, "↓")
	left := "↑ 上行  " + green.Render(ui.Rate(s.UpRate)) + "\n" + up +
		"\n\n↓ 下行  " + yellow.Render(ui.Rate(s.DownRate)) + "\n" + down
	right := "连接 " + strconv.Itoa(s.Conns) + "   UDP " + strconv.Itoa(s.UDPSessions) +
		"\n累计 ↑ " + ui.Bytes(s.UpTotal) + "\n累计 ↓ " + ui.Bytes(s.DownTotal) +
		"\n\n" + ui.HelpStyle.Render("每秒自动刷新 (runtimeOverview)")
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left, "    ", right))
	b.WriteString("\n\n")

	// --- current node per group ---
	b.WriteString(ui.TitleStyle.Render(" 各组当前节点") + "\n")
	if len(p.groups) == 0 {
		b.WriteString(ui.HelpStyle.Render(" （加载中…）") + "\n")
	}
	for _, g := range p.groups {
		label, style := p.currentNode(g)
		b.WriteString("  " + ui.PadRight(g.Name, 16) + style.Render(label) + "\n")
	}

	return b.String()
}
