package app

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yhyld/dae-tui/internal/driver"
	"github.com/yhyld/dae-tui/internal/i18n"
)

// The profiles backup: a TOML document of every stored config/dns/routing
// profile, built exclusively from ListSelections data. dae-tui's own
// credentials (config.toml's password/JWT) never flow through Selections, so
// the export is structurally incapable of leaking them — subscriptions and
// nodes are left out for the same reason: their links embed server
// credentials, and a backup is meant to be shareable.
type exportDoc struct {
	Generated time.Time
	Note      string
	Configs   []exportItem `toml:"config"`
	Dns       []exportItem `toml:"dns"`
	Routings  []exportItem `toml:"routing"`
}

type exportItem struct {
	Name     string
	Selected bool
	Global   []exportField `toml:"global,omitempty"` // config section: field table
	Body     string        `toml:"body,omitempty"`   // dns/routing section: raw DSL
}

type exportField struct {
	Name  string
	Value string
}

func buildExport(sel driver.Selections, now time.Time) string {
	doc := exportDoc{Generated: now, Note: i18n.T(
		"dae-tui 导出的 daed 配置方案备份：selected = 当前选中。不含订阅与节点（链接内嵌服务器凭据），也不含 dae-tui 账户信息。")}
	for _, it := range sel.Configs {
		item := exportItem{Name: it.Name, Selected: it.Selected}
		for _, f := range it.Fields {
			item.Global = append(item.Global, exportField{Name: f.Name, Value: f.Value})
		}
		doc.Configs = append(doc.Configs, item)
	}
	for _, it := range sel.Dns {
		doc.Dns = append(doc.Dns, exportItem{Name: it.Name, Selected: it.Selected, Body: it.Body})
	}
	for _, it := range sel.Routings {
		doc.Routings = append(doc.Routings, exportItem{Name: it.Name, Selected: it.Selected, Body: it.Body})
	}
	var sb strings.Builder
	if err := toml.NewEncoder(&sb).Encode(doc); err != nil {
		// Only strings from the backend are encoded; the encoder cannot fail
		// on them. If it ever does, an empty document is still honest.
		return ""
	}
	return sb.String()
}

// exportFileCmd writes the backup into dir with a timestamped name and
// reports the full path back through the toast. 0600 out of conservatism —
// no dae-tui secrets inside, but dns/routing DSL still names the user's
// infrastructure.
func exportFileCmd(dir, content string) tea.Cmd {
	return func() tea.Msg {
		path := filepath.Join(dir, "dae-tui-profiles-"+time.Now().Format("20060102-150405")+".toml")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return opDoneMsg{Op: i18n.T("导出备份"), Err: err}
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return opDoneMsg{Op: i18n.T("导出备份"), Err: err}
		}
		return opDoneMsg{Op: i18n.T("已导出 %s", path)}
	}
}
