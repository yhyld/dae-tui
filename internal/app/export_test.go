package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"dae-tui/internal/driver"
)

// The backup document carries every profile — names, selected flags, global
// fields and raw DSL — and stays valid TOML, because a backup that cannot be
// parsed back is a corrupt backup.
func TestBuildExportRoundTrip(t *testing.T) {
	sel := driver.Selections{
		Configs: []driver.ConfigItem{{ID: "c1", Name: "默认", Selected: true,
			Fields: []driver.ConfigField{
				{Name: "logLevel", Value: "info"},
				{Name: "lanInterface", Value: "eth0"},
			}}},
		Dns: []driver.ConfigItem{
			{ID: "d1", Name: "默认DNS", Selected: true, Body: "upstream {}"},
			{ID: "d2", Name: "备用DNS", Body: "upstream {}"},
		},
		Routings: []driver.ConfigItem{{ID: "r1", Name: "默认路由", Selected: true,
			Body: "pname(x) -> must_direct\nfallback: proxy"}},
	}
	out := buildExport(sel, time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC))
	for _, want := range []string{
		"默认", "logLevel", "eth0", "默认DNS", "备用DNS",
		"pname(x) -> must_direct", "fallback: proxy",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("export missing %q:\n%s", want, out)
		}
	}

	var back exportDoc
	if err := toml.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("export is not valid TOML: %v\n%s", err, out)
	}
	if len(back.Configs) != 1 || back.Configs[0].Name != "默认" || !back.Configs[0].Selected {
		t.Fatalf("config round-trip = %+v", back.Configs)
	}
	if len(back.Dns) != 2 || back.Dns[1].Name != "备用DNS" || back.Dns[1].Selected {
		t.Fatalf("dns round-trip = %+v", back.Dns)
	}
	if back.Routings[0].Body != sel.Routings[0].Body {
		t.Fatalf("routing body round-trip = %q", back.Routings[0].Body)
	}
	if len(back.Configs[0].Global) != 2 || back.Configs[0].Global[1].Value != "eth0" {
		t.Fatalf("global fields round-trip = %+v", back.Configs[0].Global)
	}
}

// E opens the export overlay (modal — global keys swallowed, destination
// named), esc closes it clean, w writes the file and toasts its path.
func TestConfigsExportFlow(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.Update(key("5"))
	m, _ = m.Update(key("E"))
	mm := m.(Model)
	if mm.configs.mode != 8 {
		t.Fatal("E should open the export overlay")
	}
	if !mm.anyModal() {
		t.Fatal("the export overlay must count as a modal")
	}
	if v := m.View(); !strings.Contains(v, mm.configs.exportDir) {
		t.Errorf("overlay should name the destination dir:\n%s", v)
	}
	m, _ = m.Update(key("esc"))
	if m.(Model).configs.mode != 0 {
		t.Fatal("esc should close the export overlay")
	}

	dir := t.TempDir()
	m, _ = m.Update(key("E"))
	mm = m.(Model)
	mm.configs.exportDir = dir
	m = mm
	m, cmd := m.Update(key("w"))
	if cmd == nil {
		t.Fatal("w should write the export file")
	}
	msgs := execCmds(cmd)
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) != 1 {
		t.Fatalf("export dir should hold one file: err=%v n=%d", err, len(ents))
	}
	data, err := os.ReadFile(filepath.Join(dir, ents[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"默认", "默认DNS", "备用DNS", "默认路由"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("export file missing %q", want)
		}
	}
	found := false
	for _, msg := range msgs {
		if od, ok := msg.(opDoneMsg); ok && od.Err == nil && strings.Contains(od.Op, ents[0].Name()) {
			found = true
		}
	}
	if !found {
		t.Errorf("opDoneMsg should name the written file: %v", msgs)
	}
}
