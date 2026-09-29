package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dae-tui/internal/ui"
)

// The save must leave a parseable file with 0600 permissions even when the
// previous file was looser: it stores the password and the bearer token.
func TestSaveIsAtomicAndEnforces0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	c := &Config{Endpoint: "http://127.0.0.1:2023/graphql", Username: "u", Password: "p", Token: "t1"}
	if err := c.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	// os.WriteFile would keep a pre-existing file's mode; the write-then-
	// rename replaces the file, so the mode is enforced again here.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	c.Token = "t2"
	if err := c.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", fi.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Token != "t2" || got.Username != "u" {
		t.Fatalf("reload = %+v", got)
	}
	// No temp files left behind by the rename.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries after save, want 1", len(entries))
	}
}

// Driver hooks persist tokens/credentials from background goroutines while
// the UI may log out concurrently; the mutators serialize and every save is
// atomic, so the file always parses. Meaningful under -race.
func TestConcurrentMutationsAndSaves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	c := &Config{Endpoint: "http://127.0.0.1:2023/graphql", Username: "u", Password: "p"}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				_ = c.UpdateToken(path, "tok")
				_ = c.UpdateCredentials(path, "u", "p2")
				_ = c.Save(path)
			}
		}(i)
	}
	wg.Wait()
	if _, err := Load(path); err != nil {
		t.Fatalf("config corrupted by concurrent saves: %v", err)
	}
}

// The theme knobs are plain config fields: they survive a save/load
// round-trip like everything else, and an absent file leaves them empty
// (which ui.ApplyTheme reads as "keep the default").
func TestThemeFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	c := &Config{Endpoint: "http://127.0.0.1:2023/graphql", Accent: "62", Border: "250", Dim: "#d0d0d0",
		OK: "82", Warn: "220", Err: "196", Gray: "245", SelBG: "#d8d8d8"}
	if err := c.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Accent != "62" || got.Border != "250" || got.Dim != "#d0d0d0" {
		t.Fatalf("theme fields = %q/%q/%q, want 62/250/#d0d0d0", got.Accent, got.Border, got.Dim)
	}
	if got.OK != "82" || got.Warn != "220" || got.Err != "196" || got.Gray != "245" || got.SelBG != "#d8d8d8" {
		t.Fatalf("semantic slots = %q/%q/%q/%q/%q", got.OK, got.Warn, got.Err, got.Gray, got.SelBG)
	}
	empty, err := Load(filepath.Join(dir, "missing.toml"))
	if err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if empty.Accent != "" || empty.Border != "" || empty.Dim != "" {
		t.Fatalf("missing file should leave the theme empty, got %q/%q/%q",
			empty.Accent, empty.Border, empty.Dim)
	}
}

func TestResolvedCascadesToInlineThenDefaults(t *testing.T) {
	// A full theme file wins outright.
	got := (Theme{Name: "nord", Accent: "#001", Border: "10", Dim: "20"}).
		Resolved(Theme{Accent: "#x", Border: "1", Dim: "2", OK: "#3"})
	if got.Accent != "#001" || got.Border != "10" || got.Dim != "20" || got.OK != "#3" {
		t.Fatalf("full theme must keep its colors: %+v", got)
	}
	// Empty slots inherit the inline values…
	got = (Theme{Name: "partial", Accent: "#001", OK: "#0a0"}).
		Resolved(Theme{Accent: "200", Border: "250", Dim: "240", OK: "#0b0", Warn: "#0e0"})
	if got.Accent != "#001" || got.Border != "250" || got.Dim != "240" || got.OK != "#0a0" || got.Warn != "#0e0" {
		t.Fatalf("partial theme must inherit inline colors: %+v", got)
	}
	// …and empty inline values fall through to the built-in palette.
	got = (Theme{Name: "bare"}).Resolved(Theme{})
	if got.Accent != ui.DefaultAccent || got.Border != ui.DefaultBorder || got.Dim != ui.DefaultDim ||
		got.OK != ui.DefaultOK || got.Warn != ui.DefaultWarn || got.Err != ui.DefaultErr || got.Gray != ui.DefaultGray {
		t.Fatalf("bare theme must land on the built-in palette: %+v", got)
	}
	// SelBG deliberately does NOT resolve: empty is meaningful there
	// (ApplyPalette derives it from the accent).
	if got.SelBG != "" {
		t.Fatalf("SelBG must pass through unresolved, got %q", got.SelBG)
	}
}

func TestResolveTheme(t *testing.T) {
	// No name: the inline colors are the theme.
	got, ok := ResolveTheme("", Theme{Accent: "62", Border: "250", Dim: "240"})
	if !ok || got.Name != "默认" || got.Border != "250" {
		t.Fatalf("no name should resolve to inline: %+v ok=%v", got, ok)
	}
	// Builtins resolve by name.
	got, ok = ResolveTheme("浅色", Theme{})
	if !ok || got.Border != "250" || got.Dim != "240" || got.SelBG != "#d0d0d0" {
		t.Fatalf("builtin 浅色 should resolve: %+v ok=%v", got, ok)
	}
	// 默认 over inline: picking it keeps the inline tuning.
	got, ok = ResolveTheme("默认", Theme{Accent: "99"})
	if !ok || got.Accent != "99" {
		t.Fatalf("默认 should resolve over inline: %+v ok=%v", got, ok)
	}
	// Unknown name: inline fallback, found=false.
	if _, ok := ResolveTheme("nope", Theme{Accent: "62", Border: "238", Dim: "245"}); ok {
		t.Fatal("unknown theme must report found=false")
	}
}

// Builtins must survive the real theme pipeline: a typo'd color applies as
// "keep the current value" and would render whatever theme was active
// before. 默认 must stay slot-empty (it means "the inline colors"), and
// 默认/浅色 keep their leading positions — the settings test previews 浅色
// with a single j from 默认.
func TestBuiltinThemesResolve(t *testing.T) {
	themes := BuiltinThemes()
	if len(themes) < 2 || themes[0].Name != "默认" || themes[1].Name != "浅色" {
		t.Fatalf("默认/浅色 must lead the builtin list: %+v", themes)
	}
	if themes[0].Accent != "" || themes[0].Border != "" || themes[0].Dim != "" {
		t.Fatalf("默认 must leave every slot empty: %+v", themes[0])
	}
	if themes[1].Border != "250" || themes[1].Dim != "240" {
		t.Fatalf("浅色 must keep the light-terminal neutrals: %+v", themes[1])
	}
	if themes[1].SelBG != "#d0d0d0" {
		t.Fatalf("浅色 must carry a light selection bar: %+v", themes[1])
	}
	seen := map[string]bool{}
	reset := func() {
		ui.ApplyPalette(ui.Palette{
			Accent: ui.DefaultAccent, Border: ui.DefaultBorder, Dim: ui.DefaultDim,
			OK: ui.DefaultOK, Warn: ui.DefaultWarn, Err: ui.DefaultErr, Gray: ui.DefaultGray,
		})
	}
	defer reset()
	for _, th := range themes {
		if seen[th.Name] {
			t.Fatalf("duplicate builtin theme name: %q", th.Name)
		}
		seen[th.Name] = true
		reset()
		ui.ApplyPalette(th.Palette())
		for _, slot := range []struct{ name, want, got string }{
			{"accent", th.Accent, string(ui.Accent)},
			{"border", th.Border, string(ui.BorderCol)},
			{"dim", th.Dim, string(ui.DimText)},
			{"ok", th.OK, string(ui.OK)},
			{"warn", th.Warn, string(ui.Warn)},
			{"err", th.Err, string(ui.Err)},
			{"gray", th.Gray, string(ui.Gray)},
			{"sel_bg", th.SelBG, string(ui.SelBG)},
		} {
			if slot.want != "" && slot.got != slot.want {
				t.Fatalf("%s: %s %q did not parse (got %q)", th.Name, slot.name, slot.want, slot.got)
			}
		}
	}
}

func TestLoadThemes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("nord.toml", "accent = \"#88C0D0\"\nborder = \"241\"\nok = \"#a3be8c\"\nwarn = \"#ebcb8b\"\nerr = \"#bf616a\"\ngray = \"244\"\nsel_bg = \"#2e3440\"\n")
	write("broken.toml", "accent = [unclosed\n")
	write("readme.txt", "not a theme")
	themes, notes, err := LoadThemes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(themes) != 1 || themes[0].Name != "nord" || themes[0].Accent != "#88C0D0" || themes[0].Dim != "" {
		t.Fatalf("nord.toml should load: %+v", themes)
	}
	if themes[0].OK != "#a3be8c" || themes[0].Warn != "#ebcb8b" || themes[0].Err != "#bf616a" ||
		themes[0].Gray != "244" || themes[0].SelBG != "#2e3440" {
		t.Fatalf("nord.toml semantic slots should load: %+v", themes[0])
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "broken.toml") {
		t.Fatalf("broken file should produce a note: %v", notes)
	}
	// A missing directory is not an error.
	if themes, notes, err := LoadThemes(filepath.Join(dir, "absent")); err != nil || themes != nil || notes != nil {
		t.Fatalf("missing dir should be empty and nil: %v %v %v", themes, notes, err)
	}
}
