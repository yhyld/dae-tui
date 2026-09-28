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

func TestSetPersistsAndRestores(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.toml")
	km := Load(path) // missing file: defaults
	if err := km.Set(path, Groups, "j", "ctrl+n"); err != nil {
		t.Fatal(err)
	}
	// The override applies and survives a fresh load of the file.
	if got := km.Key(Groups, "j"); got != "ctrl+n" {
		t.Fatalf("binding lost: %q", got)
	}
	km2 := Load(path)
	if got := km2.Translate(Groups, "ctrl+n"); got != "j" {
		t.Fatalf("binding did not persist: %q", got)
	}
	// A second Set on the same action replaces, not stacks.
	if err := km2.Set(path, Groups, "j", "ctrl+x"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `j = "ctrl+x"`) || strings.Contains(string(raw), "ctrl+n") {
		t.Fatalf("old override must be replaced, file: %s", raw)
	}
	// Restoring the default drops the override from the file entirely.
	if err := km2.Set(path, Groups, "j", "j"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "j =") {
		t.Fatalf("default restore must drop the entry, file: %s", raw)
	}
}

// A binding whose new key is the still-live default of another action in
// the same scope would silently shadow it — Translate maps the pressed key
// to the override before the dispatch switch sees the default — so the
// loader drops it with a note. The same rule protects the global layer,
// which dispatches ahead of every page.
func TestShadowOfLiveDefaultRejected(t *testing.T) {
	RegisterDefaults(Groups, []string{"j", "k", "c"})
	RegisterDefaults(Global, []string{"q", "r"})
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.toml")

	os.WriteFile(path, []byte("[groups]\nc = \"j\"\n"), 0o600)
	km := Load(path)
	if got := km.Translate(Groups, "c"); got != "c" {
		t.Fatalf("shadowing binding must be dropped: %q", got)
	}
	if got := km.Translate(Groups, "j"); got != "j" {
		t.Fatalf("j must stay the live default: %q", got)
	}
	if len(km.Notes()) != 1 || !strings.Contains(km.Notes()[0], "groups/c") {
		t.Fatalf("a note should explain the shadow rejection: %v", km.Notes())
	}

	os.WriteFile(path, []byte("[subs]\nu = \"r\"\n"), 0o600)
	km = Load(path)
	if got := km.Translate(Subs, "u"); got != "u" {
		t.Fatalf("binding onto a live global key must be dropped: %q", got)
	}
}

// Swapping two keys is expressed by moving both away; once the target is
// dead the second half is legal.
func TestKeySwapAllowed(t *testing.T) {
	RegisterDefaults(Nodes, []string{"e", "x"})
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.toml")
	os.WriteFile(path, []byte("[nodes]\ne = \"x\"\nx = \"e\"\n"), 0o600)
	km := Load(path)
	if got := km.Translate(Nodes, "x"); got != "e" {
		t.Fatalf("swap half 1 broken: %q", got)
	}
	if got := km.Translate(Nodes, "e"); got != "x" {
		t.Fatalf("swap half 2 broken: %q", got)
	}
	if len(km.Notes()) != 0 {
		t.Fatalf("a clean swap must not warn: %v", km.Notes())
	}
}

// Set enforces the same shadow rule, rejecting before anything is mutated
// so a failed call leaves the previous binding intact. A swap through Set
// needs an intermediate key (move s away, then L onto s, then s onto L);
// a file listing both halves at once swaps directly.
func TestSetRejectsLiveDefault(t *testing.T) {
	RegisterDefaults(Home, []string{"s", "L"})
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.toml")
	km := Load(path)
	if err := km.Set(path, Home, "s", "L"); err == nil {
		t.Fatal("binding onto a live default must be rejected")
	}
	if got := km.Key(Home, "s"); got != "s" {
		t.Fatalf("a rejected Set must leave the default intact: %q", got)
	}
	if err := km.Set(path, Home, "s", "Z"); err != nil {
		t.Fatal(err)
	}
	if err := km.Set(path, Home, "L", "s"); err != nil {
		t.Fatal(err)
	}
	if err := km.Set(path, Home, "s", "L"); err != nil {
		t.Fatalf("completing the swap must be allowed: %v", err)
	}
	if got := km.Translate(Home, "s"); got != "L" {
		t.Fatalf("swap half: pressing s should trigger L, got %q", got)
	}
}
