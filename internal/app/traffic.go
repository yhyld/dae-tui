package app

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/driver"
	"dae-tui/internal/i18n"
	"dae-tui/internal/ui"
)

type trafficPage struct {
	snap   driver.TrafficSnapshot
	err    error
	width  int
	height int
}

func (p *trafficPage) setSize(w, h int) {
	p.width, p.height = w, h
}

func (p *trafficPage) update(snap driver.TrafficSnapshot) {
	p.snap = snap
	p.err = nil
}

func (p trafficPage) View() string {
	if p.err != nil {
		return ui.ErrorStyle.Render(i18n.T(" ✗ 获取流量数据失败: ") + shortErr(p.err))
	}
	chartW := p.width - 16
	if chartW < 20 {
		chartW = 20
	}
	chartH := (p.height - 8) / 2
	if chartH < 2 {
		chartH = 2
	}
	if chartH > 8 {
		chartH = 8
	}

	green := lipgloss.NewStyle().Foreground(ui.Green)
	yellow := lipgloss.NewStyle().Foreground(ui.Yellow)
	up := ui.Sparkline(p.snap.UpSeries, chartW, chartH, green, "↑")
	down := ui.Sparkline(p.snap.DownSeries, chartW, chartH, yellow, "↓")

	s := p.snap
	stats := []string{
		i18n.T("活跃连接 ") + strconv.Itoa(s.Conns),
		i18n.T("UDP 会话 ") + strconv.Itoa(s.UDPSessions),
		i18n.T("累计 ↑ ") + ui.Bytes(s.UpTotal),
		i18n.T("累计 ↓ ") + ui.Bytes(s.DownTotal),
		i18n.T("更新于 ") + ui.TimeAgo(s.UpdatedAt),
	}

	var b strings.Builder
	b.WriteString(ui.TitleStyle.Render(i18n.T(" 实时流量")) +
		ui.HelpStyle.Render(i18n.T("  (runtimeOverview: 窗口10s, 60采样点)")) + "\n\n")
	b.WriteString(i18n.T("↑ 上行  ") + green.Render(ui.Rate(s.UpRate)) + "\n" + up + "\n\n")
	b.WriteString(i18n.T("↓ 下行  ") + yellow.Render(ui.Rate(s.DownRate)) + "\n" + down + "\n\n")
	b.WriteString(ui.HelpStyle.Render(" " + strings.Join(stats, "   ")))
	return b.String()
}
