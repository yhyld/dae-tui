// Theme files: named color schemes in ~/.config/dae-tui/theme/*.toml,
// selectable via config.toml's `theme` key and from the settings window.
package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/yhyld/dae-tui/internal/ui"
)

// Theme is one named color scheme: the slots ui.ApplyPalette takes. In a
// theme file Name is the file stem (theme = "nord" loads nord.toml), so
// the display name and the config value can never drift apart. Every slot
// but Name may be empty — Resolved cascades theme → inline config → the
// built-in palette, so a theme file only spells out what it actually
// retunes.
type Theme struct {
	Name   string `toml:"name"`
	Accent string `toml:"accent"`
	Border string `toml:"border"`
	Dim    string `toml:"dim"`
	OK     string `toml:"ok"`
	Warn   string `toml:"warn"`
	Err    string `toml:"err"`
	Gray   string `toml:"gray"`
	// SelBG is the selected-row bar's background; empty means "derive
	// from the accent" (the historical default), so only light themes
	// spell it out.
	SelBG string `toml:"sel_bg"`
}

// Palette converts the resolved theme into ui.ApplyPalette's input.
func (t Theme) Palette() ui.Palette {
	return ui.Palette{
		Accent: t.Accent, Border: t.Border, Dim: t.Dim,
		OK: t.OK, Warn: t.Warn, Err: t.Err, Gray: t.Gray, SelBG: t.SelBG,
	}
}

// ThemeDir is the folder user theme files live in.
func ThemeDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "dae-tui", "theme"), nil
}

// BuiltinThemes ship with the binary so the picker has content before any
// user file exists. 默认 leaves every slot empty — resolved over the
// config's inline colors, i.e. exactly the pre-theme-file behavior; 浅色
// retunes the neutrals for a light terminal and stays at index 1 (the
// settings test previews it with one j from 默认). The rest cover one color
// family each and fill the neutral + semantic slots with that family's
// palette, so they render the same whatever the inline tuning says; their
// accents stay clear of the semantic hues (green/yellow/red region) so
// delay colors never read as "selected". 水墨 is the monochrome take: the
// semantic trio too goes grayscale, keeping its ink identity end to end.
func BuiltinThemes() []Theme {
	return []Theme{
		{Name: "默认"},
		{Name: "浅色", Border: "250", Dim: "240", Gray: "249", SelBG: "#d0d0d0"},
		{Name: "北欧", Accent: "#88c0d0", Border: "#4c566a", Dim: "#7b88a1", OK: "#a3be8c", Warn: "#ebcb8b", Err: "#bf616a"},                                // Nord
		{Name: "夜航", Accent: "#7aa2f7", Border: "#292e42", Dim: "#565f89", OK: "#9ece6a", Warn: "#e0af68", Err: "#f7768e"},                                // Tokyo Night
		{Name: "布丁", Accent: "#cba6f7", Border: "#45475a", Dim: "#9399b2", OK: "#a6e3a1", Warn: "#f9e2af", Err: "#f38ba8"},                                // Catppuccin Mocha
		{Name: "玫瑰", Accent: "#ebbcba", Border: "#403d52", Dim: "#908caa", OK: "#9cc3bd", Warn: "#f6c177", Err: "#eb6f92"},                                // Rosé Pine
		{Name: "青竹", Accent: "#a7c080", Border: "#4b565c", Dim: "#859289", OK: "#83c09c", Warn: "#dbbc7f", Err: "#e67e80"},                                // Everforest
		{Name: "石青", Accent: "#56b6c2", Border: "#3e4451", Dim: "#5c6370", OK: "#98c379", Warn: "#e5c07b", Err: "#e06c75"},                                // One Dark
		{Name: "暖橙", Accent: "#fe8019", Border: "#504945", Dim: "#928374", OK: "#b8bb26", Warn: "#fabd2f", Err: "#fb4934"},                                // Gruvbox
		{Name: "水墨", Accent: "#e4e4e4", Border: "#4e4e4e", Dim: "#808080", OK: "#a8a8a8", Warn: "#d0d0d0", Err: "#ffffff"},                                // monochrome ink
		{Name: "晨光", Accent: "#268bd2", Border: "#93a1a1", Dim: "#657b83", OK: "#859900", Warn: "#b58900", Err: "#dc322f", Gray: "249", SelBG: "#eee8d5"}, // Solarized Light
	}
}

// Resolved fills t's empty color slots: first from the config's inline
// colors (a partial theme file keeps the terminal's neutral tuning instead
// of resetting it), then from the built-in palette. The result is always
// concrete colors — ui.ApplyPalette treats "" as "keep the previous value",
// so an empty slot reaching it would silently stick to whichever theme was
// previewed before. SelBG is the exception on purpose: empty is meaningful
// there (derive from the accent), so it passes through unresolved.
func (t Theme) Resolved(inline Theme) Theme {
	out := t
	slots := []struct {
		dst *string
		val string
	}{
		{&out.Accent, inline.Accent},
		{&out.Border, inline.Border},
		{&out.Dim, inline.Dim},
		{&out.OK, inline.OK},
		{&out.Warn, inline.Warn},
		{&out.Err, inline.Err},
		{&out.Gray, inline.Gray},
	}
	for _, s := range slots {
		if *s.dst == "" {
			*s.dst = s.val
		}
	}
	if out.Accent == "" {
		out.Accent = ui.DefaultAccent
	}
	if out.Border == "" {
		out.Border = ui.DefaultBorder
	}
	if out.Dim == "" {
		out.Dim = ui.DefaultDim
	}
	if out.OK == "" {
		out.OK = ui.DefaultOK
	}
	if out.Warn == "" {
		out.Warn = ui.DefaultWarn
	}
	if out.Err == "" {
		out.Err = ui.DefaultErr
	}
	if out.Gray == "" {
		out.Gray = ui.DefaultGray
	}
	return out
}

// ResolveTheme resolves the config's active theme. `theme = "<name>"` picks
// a builtin or a theme-dir file; no name means the inline colors themselves.
// The bool reports whether the named theme was found — false means the
// caller fell back to the inline colors and should say so.
func ResolveTheme(name string, inline Theme) (Theme, bool) {
	resolved := (Theme{Name: "默认"}).Resolved(inline)
	if name == "" || name == "默认" {
		return resolved, true
	}
	candidates := BuiltinThemes()
	if dir, err := ThemeDir(); err == nil {
		if user, _, err := LoadThemes(dir); err == nil {
			candidates = append(candidates, user...)
		}
	}
	// Later candidates win: a user file may deliberately shadow a builtin.
	for i := len(candidates) - 1; i >= 0; i-- {
		if candidates[i].Name == name {
			return candidates[i].Resolved(inline), true
		}
	}
	return resolved, false
}

// LoadThemes reads every *.toml in dir, Name taken from the file stem. A
// missing dir is not an error (no user themes yet); one unparsable file is
// skipped and reported as a note so the picker can say so instead of the
// theme silently never appearing.
func LoadThemes(dir string) (themes []Theme, notes []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			notes = append(notes, e.Name()+": 读取失败")
			continue
		}
		var f Theme
		if _, err := toml.Decode(string(raw), &f); err != nil {
			notes = append(notes, e.Name()+": 解析失败")
			continue
		}
		f.Name = strings.TrimSuffix(e.Name(), ".toml")
		themes = append(themes, f)
	}
	return themes, notes, nil
}
