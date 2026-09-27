package keymap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsPassThrough(t *testing.T) {
	km := New()
	if got := km.Translate(Global, "r"); got != "r" {
		t.Fatalf("default keys pass through, got %q", got)
	}
	if got := km.Key(Global, "q"); got != "q" {
		t.Fatalf("Key reports the default, got %q", got)
	}
}

func TestOverrideAndRetire(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.toml")
	os.WriteFile(path, []byte("[groups]\nj = \"ctrl+n\"\nk = \"ctrl+p\"\n"), 0o600)
	km := Load(path)
	if got := km.Translate(Groups, "ctrl+n"); got != "j" {
		t.Fatalf("override target should translate to the default: %q", got)
	}
	if got := km.Translate(Groups, "j"); got != dead {
		t.Fatalf("a moved-away default must be dead: %q", got)
	}
	if got := km.Translate(Groups, "down"); got != "down" {
		t.Fatalf("fixed synonyms still pass through: %q", got)
	}
	// Other scopes are untouched.
	if got := km.Translate(Subs, "j"); got != "j" {
		t.Fatalf("other scopes keep defaults: %q", got)
	}
	if got := km.Key(Groups, "j"); got != "ctrl+n" {
		t.Fatalf("Key reports the live binding: %q", got)
	}
}

func TestFixedTargetsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.toml")
	os.WriteFile(path, []byte("[global]\nq = \"esc\"\n"), 0o600)
	km := Load(path)
	if got := km.Translate(Global, "esc"); got != "esc" {
		t.Fatalf("esc must stay esc: %q", got)
	}
	if got := km.Translate(Global, "q"); got != "q" {
		t.Fatalf("the binding must be dropped, q stays: %q", got)
	}
	if len(km.Notes()) != 1 {
		t.Fatalf("a note should explain the rejection: %v", km.Notes())
	}
}

func TestCollisionDropsBoth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.toml")
	os.WriteFile(path, []byte("[home]\nj = \"x\"\ne = \"x\"\n"), 0o600)
	km := Load(path)
	if got := km.Translate(Home, "x"); got != "x" {
		t.Fatalf("x must stay itself after a collision: %q", got)
	}
	if got := km.Translate(Home, "j"); got != "j" {
		t.Fatalf("both bindings fall back to defaults: %q", got)
	}
	if len(km.Notes()) != 1 || !strings.Contains(km.Notes()[0], "home/e") {
		t.Fatalf("a collision note should name both actions: %v", km.Notes())
	}
}

func TestBrokenFileIsDefaultsWithNote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.toml")
	os.WriteFile(path, []byte("[global]\nj = [unclosed\n"), 0o600)
	km := Load(path)
	if got := km.Translate(Global, "j"); got != "j" {
		t.Fatalf("a broken file leaves defaults: %q", got)
	}
	if len(km.Notes()) != 1 {
		t.Fatalf("a note should report the parse failure: %v", km.Notes())
	}
	// A missing file is silently default.
	if km := Load(filepath.Join(dir, "absent.toml")); len(km.Notes()) != 0 {
		t.Fatalf("missing file should be silent: %v", km.Notes())
	}
}

func TestReloadSwapsBindings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.toml")
	os.WriteFile(path, []byte("[global]\nq = \"Q\"\n"), 0o600)
	km := Load(path)
	if got := km.Key(Global, "q"); got != "Q" {
		t.Fatalf("initial binding: %q", got)
	}
	os.WriteFile(path, []byte("[global]\nr = \"F5\"\n"), 0o600)
	km.Reload(path)
	if got := km.Key(Global, "q"); got != "q" {
		t.Fatalf("reload drops the old binding: %q", got)
	}
	if got := km.Key(Global, "r"); got != "F5" {
		t.Fatalf("reload applies the new one: %q", got)
	}
}
