// Theme files: named color schemes in ~/.config/dae-tui/theme/*.toml,
// selectable via config.toml's `theme` key and from the settings window.
package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"dae-tui/internal/ui"
)

// Theme is one named color scheme: the three knobs ui.ApplyTheme takes.
// In a theme file Name is the file stem (theme = "nord" loads nord.toml),
// so the display name and the config value can never drift apart.
type Theme struct {
	Name   string `toml:"name"`
	Accent string `toml:"accent"`
	Border string `toml:"border"`
	Dim    string `toml:"dim"`
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
// retunes the neutrals for a light terminal.
func BuiltinThemes() []Theme {
	return []Theme{
		{Name: "默认"},
		{Name: "浅色", Border: "250", Dim: "240"},
	}
}

// Resolved fills t's empty color slots: first from the config's inline
// accent/border/dim (a partial theme file keeps the terminal's neutral
// tuning instead of resetting it), then from the built-in palette. The
// result is always three concrete colors — ui.ApplyTheme treats "" as
// "keep the previous value", so an empty slot reaching it would silently
// stick to whichever theme was previewed before.
func (t Theme) Resolved(accent, border, dim string) Theme {
	out := t
	if out.Accent == "" {
		out.Accent = accent
	}
	if out.Border == "" {
		out.Border = border
	}
	if out.Dim == "" {
		out.Dim = dim
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
	return out
}

// ResolveTheme resolves the config's active theme. `theme = "<name>"` picks
// a builtin or a theme-dir file; no name means the inline colors themselves.
// The bool reports whether the named theme was found — false means the
// caller fell back to the inline colors and should say so.
func ResolveTheme(name, accent, border, dim string) (Theme, bool) {
	inline := (Theme{Name: "默认"}).Resolved(accent, border, dim)
	if name == "" || name == "默认" {
		return inline, true
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
			return candidates[i].Resolved(accent, border, dim), true
		}
	}
	return inline, false
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
		var f struct {
			Accent string `toml:"accent"`
			Border string `toml:"border"`
			Dim    string `toml:"dim"`
		}
		if _, err := toml.Decode(string(raw), &f); err != nil {
			notes = append(notes, e.Name()+": 解析失败")
			continue
		}
		themes = append(themes, Theme{
			Name:   strings.TrimSuffix(e.Name(), ".toml"),
			Accent: f.Accent,
			Border: f.Border,
			Dim:    f.Dim,
		})
	}
	return themes, notes, nil
}
