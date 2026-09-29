// Package ui centralizes visual primitives: styles, latency color ramp,
// value formatting and a braille sparkline chart.
package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The built-in palette. Theme resolution (config package) cascades down to
// these as the floor, so ApplyPalette always receives concrete colors — it
// treats "" as "keep the current value" and must never see it (SelBG alone
// treats "" as "derive from the accent").
const (
	DefaultAccent = "62" // soft blue
	DefaultBorder = "238"
	DefaultDim    = "245"
	DefaultOK     = "42"  // good latency / success
	DefaultWarn   = "214" // medium latency / warning
	DefaultErr    = "203" // high latency / error
	DefaultGray   = "241" // dead / unknown
)

var (
	// Base colors.
	Accent = lipgloss.Color(DefaultAccent)
	// OK/Warn/Err are one semantic trio serving two readers at once: the
	// latency ramp (good/mid/bad) and status text (success/warning/error).
	// They were split-background colors once (latency vs semantics) but the
	// hues overlap by design — "green means fine" reads the same either
	// way — so the palette exposes one themed slot per tier.
	OK      = lipgloss.Color(DefaultOK)
	Warn    = lipgloss.Color(DefaultWarn)
	Err     = lipgloss.Color(DefaultErr)
	Gray    = lipgloss.Color(DefaultGray)
	DimText = lipgloss.Color(DefaultDim)

	// BorderCol is the unfocused box/frame edge; BorderDim renders it.
	BorderCol = lipgloss.Color(DefaultBorder)
	// SelBG is the selected-row background bar: the accent blended into a
	// dark neutral base — a faint theme-colored bar, not a flat gray (too
	// lifeless) and not a solid accent block (too loud). ApplyPalette
	// re-derives it whenever the accent changes, unless the theme set an
	// explicit sel_bg (light terminals want a light bar).
	SelBG = selBGFromAccent(Accent)

	TitleStyle = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	// Inactive tabs ride the dim color — one neutral tier for all secondary
	// text. The active tab stays text-on-a-line (bold + accent + underline),
	// so the active/inactive contrast comes from weight and hue, never from
	// a filled chip.
	TabStyle = lipgloss.NewStyle().Foreground(DimText).Padding(0, 1)
	// TabActive recolors only — same padding/width as TabStyle so tabs never
	// shift and the click-span math in the app stays trivial. The underline
	// is the emphasis: it rides the border line the same way the label does,
	// where a filled chip would break the line-art language every other box
	// speaks. It also degrades to plain bold accent text on terminals that
	// ignore SGR 4.
	TabActive     = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(Accent).Padding(0, 1)
	HelpStyle     = lipgloss.NewStyle().Foreground(DimText)
	ErrorStyle    = lipgloss.NewStyle().Foreground(Err)
	OKStyle       = lipgloss.NewStyle().Foreground(OK)
	WarnStyle     = lipgloss.NewStyle().Foreground(Warn)
	SelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	CursorStyle   = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	BorderDim     = lipgloss.NewStyle().Foreground(BorderCol)
)

// The latency ramp's thresholds: under LatGoodMs is green, under LatMidMs
// yellow, above red. LatencyBar fills on the same scale (LatMidMs = full).
const (
	LatGoodMs = 200
	LatMidMs  = 500
)

// LatencyStyle maps a latency in ms to a style.
func LatencyStyle(ms int, alive, tested bool) lipgloss.Style {
	if !tested {
		return lipgloss.NewStyle().Foreground(Gray)
	}
	if !alive {
		return lipgloss.NewStyle().Foreground(Gray)
	}
	if ms <= 0 {
		// Alive but without a figure: a probe in flight or a record without
		// a measurement — untested, not worst-latency.
		return lipgloss.NewStyle().Foreground(Gray)
	}
	switch {
	case ms > 0 && ms < LatGoodMs:
		return lipgloss.NewStyle().Foreground(OK)
	case ms >= LatGoodMs && ms < LatMidMs:
		return lipgloss.NewStyle().Foreground(Warn)
	default:
		return lipgloss.NewStyle().Foreground(Err)
	}
}

// selAccentMix is how much of the accent the selection bar carries, the
// rest being the dark neutral base. 0.4 keeps latency colors and dim
// suffixes readable while the bar clearly reads as theme-tinted.
const selAccentMix = 0.4

// selBGBase is the bar's neutral floor (ANSI 236): dark enough to sit
// behind the latency ramp, light enough to show the blended accent.
var selBGBase = [3]uint8{0x30, 0x30, 0x30}

// colorRGB8 converts a lipgloss color to 8-bit RGB. ANSI-256 indexes go
// through x/ansi's palette table (which covers 0-15 too); hex strings parse
// directly; anything else falls back to the neutral base.
func colorRGB8(c lipgloss.Color) (r, g, b uint8) {
	s := string(c)
	if len(s) == 7 && s[0] == '#' {
		v, err := strconv.ParseUint(s[1:], 16, 24)
		if err != nil {
			return selBGBase[0], selBGBase[1], selBGBase[2]
		}
		return uint8(v >> 16), uint8(v >> 8), uint8(v)
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 255 {
		return selBGBase[0], selBGBase[1], selBGBase[2]
	}
	r16, g16, b16, _ := ansi.IndexedColor(uint8(n)).RGBA()
	return uint8(r16 >> 8), uint8(g16 >> 8), uint8(b16 >> 8)
}

// selBGFromAccent derives the selection-bar background from the accent: the
// accent mixed into the dark base, so the bar carries a faint theme tint
// and follows ApplyTheme's accent changes.
func selBGFromAccent(accent lipgloss.Color) lipgloss.Color {
	ar, ag, ab := colorRGB8(accent)
	mix := func(a uint8, i int) uint8 {
		v := selAccentMix*float64(a) + (1-selAccentMix)*float64(selBGBase[i])
		return uint8(v + 0.5)
	}
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x",
		mix(ar, 0), mix(ag, 1), mix(ab, 2)))
}

// selBGOpen returns the SGR sequence that turns the selection background on
// ("" on colorless profiles, so tests and pipes get plain text). It is
// peeled off a one-space render rather than written by hand because
// lipgloss applies the terminal's color profile at render time — a
// hand-written escape would bypass that and leak raw sequences into tests.
func selBGOpen() string {
	s := lipgloss.NewStyle().Background(SelBG).Render(" ")
	return strings.TrimSuffix(strings.TrimSuffix(s, "\x1b[0m"), " ")
}

// reassertBG re-arms the background after every embedded full reset. A
// styled row ends each inner style with ESC[0m, which would also clear a
// background applied by wrapping the whole row — the reset has to be
// followed by the background again for the bar to survive the row's own
// colors.
func reassertBG(row, open string) string {
	if open == "" {
		return row
	}
	return open + strings.ReplaceAll(row, "\x1b[0m", "\x1b[0m"+open) + "\x1b[0m"
}

// HiRow paints row as the selected line: a full-width background bar across
// the pane's content width w. Rows are styled text (latency colors, dim
// suffixes), so truncation and padding happen first and the background is
// re-asserted after every embedded reset — wrapping the row in a
// Background style instead would lose the bar at the first inner ESC[0m.
// on=false returns the row untouched; width math is ANSI-aware, CJK
// included. The selected-row rule: call this at the same place the ❯
// cursor is drawn, with the box's inner width.
func HiRow(row string, w int, on bool) string {
	if !on || w < 1 {
		return row
	}
	row = Truncate(row, w)
	if d := w - width(row); d > 0 {
		row += spaces(d)
	}
	return reassertBG(row, selBGOpen())
}

// Palette is the full theme surface, one slot per themed color: accent
// (titles, cursor, active tab), border (every box/frame edge), dim
// (secondary text), the semantic trio ok/warn/err (success/warning/error
// text and the latency ramp together) and gray (untested/dead). Each value
// is an ANSI-256 index ("0"-"255") or a "#rrggbb" hex string; anything
// else (including "") keeps the current value — a half-valid config still
// themes what it could parse. SelBG is the one slot where "" is
// meaningful: it means "derive from the accent" (the dark-base blend), so
// themes only spell it out when they want the opposite — light terminals
// set a light bar.
type Palette struct {
	Accent, Border, Dim string
	OK, Warn, Err, Gray string
	SelBG               string
}

// ApplyPalette overrides the theme colors from config at startup (and from
// the settings theme picker's live preview). Styles built from these at
// package init are rebuilt here; call sites that read the vars at render
// time pick the new colors up on their own.
func ApplyPalette(p Palette) {
	if c, ok := parseColor(p.Accent); ok {
		Accent = c
	}
	if c, ok := parseColor(p.Border); ok {
		BorderCol = c
	}
	if c, ok := parseColor(p.Dim); ok {
		DimText = c
	}
	if c, ok := parseColor(p.OK); ok {
		OK = c
	}
	if c, ok := parseColor(p.Warn); ok {
		Warn = c
	}
	if c, ok := parseColor(p.Err); ok {
		Err = c
	}
	if c, ok := parseColor(p.Gray); ok {
		Gray = c
	}
	if p.SelBG == "" {
		SelBG = selBGFromAccent(Accent)
	} else if c, ok := parseColor(p.SelBG); ok {
		SelBG = c
	}
	TitleStyle = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	TabStyle = lipgloss.NewStyle().Foreground(DimText).Padding(0, 1)
	TabActive = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(Accent).Padding(0, 1)
	HelpStyle = lipgloss.NewStyle().Foreground(DimText)
	ErrorStyle = lipgloss.NewStyle().Foreground(Err)
	OKStyle = lipgloss.NewStyle().Foreground(OK)
	WarnStyle = lipgloss.NewStyle().Foreground(Warn)
	SelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	CursorStyle = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	BorderDim = lipgloss.NewStyle().Foreground(BorderCol)
}

// ApplyTheme retunes the three neutral slots only — the compat entry point
// for callers (and tests) predating the palette, kept so a partial retune
// never wipes the semantic trio.
func ApplyTheme(accent, border, dim string) {
	ApplyPalette(Palette{Accent: accent, Border: border, Dim: dim})
}

// parseColor accepts an ANSI-256 index ("0"-"255") or a "#rrggbb" hex
// string (case-insensitive). Anything else reports false, so the caller
// keeps its current value instead of blanking a color.
func parseColor(s string) (lipgloss.Color, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 || n > 255 {
			return "", false
		}
		return lipgloss.Color(strconv.Itoa(n)), true
	}
	if len(s) != 7 || s[0] != '#' {
		return "", false
	}
	if _, err := strconv.ParseUint(s[1:], 16, 24); err != nil {
		return "", false
	}
	return lipgloss.Color(strings.ToLower(s)), true
}

// PadRight pads s with spaces to display width w (CJK-aware).
func PadRight(s string, w int) string {
	d := w - width(s)
	if d < 0 {
		return truncate(s, w)
	}
	return s + spaces(d)
}

// PadLeft right-aligns s in display width w (CJK-aware).
func PadLeft(s string, w int) string {
	d := w - width(s)
	if d < 0 {
		return truncate(s, w)
	}
	return spaces(d) + s
}

// BoxLines wraps lines in a rounded border — the shared look for in-pane
// confirmations and small forms. destructive=true draws the border red.
func BoxLines(destructive bool, lines ...string) []string {
	if len(lines) == 0 {
		return nil
	}
	st := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if destructive {
		st = st.BorderForeground(Err)
	} else {
		st = st.BorderForeground(Accent)
	}
	return strings.Split(st.Render(strings.Join(lines, "\n")), "\n")
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func spaces(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}
