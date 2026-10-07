package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yhyld/dae-tui/internal/config"
	"github.com/yhyld/dae-tui/internal/driver"
)

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{"a, b , ,c,", []string{"a", "b", "c"}},
		{",,", nil},
	}
	for _, c := range cases {
		if got := splitCSV(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitCSV(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

// A missing config file means defaults, and -endpoint must override whatever
// the file (or the default) provides.
func TestLoadCfgEndpointOverride(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.toml")
	cfg, err := loadCfg(missing, "")
	if err != nil {
		t.Fatalf("loadCfg(missing): %v", err)
	}
	if cfg.Endpoint != config.DefaultEndpoint {
		t.Fatalf("default endpoint = %q, want %q", cfg.Endpoint, config.DefaultEndpoint)
	}
	const custom = "http://127.0.0.1:9999/graphql"
	cfg, err = loadCfg(missing, custom)
	if err != nil {
		t.Fatalf("loadCfg(override): %v", err)
	}
	if cfg.Endpoint != custom {
		t.Fatalf("endpoint = %q, want the -endpoint override %q", cfg.Endpoint, custom)
	}
}

func TestLoadCfgRejectsBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(path, []byte("not [valid toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCfg(path, ""); err == nil {
		t.Fatal("expected a parse error for a broken config file")
	}
}

func TestTestTargetsExpandsGroupsAndDedups(t *testing.T) {
	all := []driver.Group{
		{Name: "proxy", Nodes: []driver.Node{{ID: "n1"}, {ID: "n2"}}},
		{Name: "backup", Nodes: []driver.Node{{ID: "n2"}, {ID: "n3"}}},
	}
	ids, missing := testTargets(all, "proxy, backup")
	if missing != nil {
		t.Fatalf("missing = %#v, want none", missing)
	}
	// A node in two named groups must be probed once.
	if want := []string{"n1", "n2", "n3"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %#v, want %#v", ids, want)
	}
}

func TestTestTargetsReportsUnknownGroups(t *testing.T) {
	all := []driver.Group{{Name: "proxy", Nodes: []driver.Node{{ID: "n1"}}}}
	ids, missing := testTargets(all, "proxy, nope")
	if want := []string{"n1"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %#v, want %#v", ids, want)
	}
	if len(missing) != 1 || missing[0] != "nope" {
		t.Fatalf("missing = %#v, want [nope]", missing)
	}
	// No match at all: empty ids (runTest turns that into an error) and the
	// whole request reported missing.
	ids, missing = testTargets(all, "ghost")
	if ids != nil || len(missing) != 1 || missing[0] != "ghost" {
		t.Fatalf("ids = %#v missing = %#v", ids, missing)
	}
}

// runSwitch resolves by name, not by the backend's ID — the CLI is given
// names and the miss error should list what was available.
func TestResolveSelectionByName(t *testing.T) {
	items := []driver.ConfigItem{
		{ID: "7", Name: "gfw"},
		{ID: "8", Name: "cnOnly"},
	}
	it, _, ok := resolveSelection(items, "cnOnly")
	if !ok || it.ID != "8" {
		t.Fatalf("resolve(cnOnly) = %v %s %v, want id 8", it, it.ID, ok)
	}
	_, names, ok := resolveSelection(items, "global")
	if ok || !reflect.DeepEqual(names, []string{"gfw", "cnOnly"}) {
		t.Fatalf("resolve(global) miss = %v %v, want the name list", ok, names)
	}
}
