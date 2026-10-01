// Package config loads and persists the dae-tui application config
// (~/.config/dae-tui/config.toml).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

type Config struct {
	// Endpoint of the daed GraphQL API. For remote daed instances use an
	// SSH tunnel (ssh -L 2023:127.0.0.1:2023 host) and keep this loopback.
	Endpoint string `toml:"endpoint"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	Token    string `toml:"token"`

	// Editor picks how DNS/routing DSL is edited: "external" (default)
	// hands the terminal to $VISUAL/$EDITOR; "builtin" opens an in-app
	// floating textarea (no editor keybindings, but works without an
	// editor installed and keeps the TUI on screen).
	Editor string `toml:"editor"`

	// Accent overrides the UI accent color (titles, cursor, active tab):
	// an ANSI-256 index ("0"-"255") or a "#rrggbb" hex string. Empty or
	// invalid keeps the default. Read once at startup.
	Accent string `toml:"accent"`

	// Border and Dim retune the neutral colors for the terminal's
	// background. The defaults (238 / 245) are tuned for dark backgrounds;
	// on a light one they nearly vanish, so a light-terminal user wants
	// something like border = "250", dim = "240". Same format as Accent;
	// empty or invalid keeps the default. Read once at startup.
	Border string `toml:"border"`
	Dim    string `toml:"dim"`

	// OK/Warn/Err theme the semantic trio: success/warning/error text and
	// the latency ramp (good/mid/bad) share one slot each. Defaults 42/214/
	// 203. Same format as Accent; empty or invalid keeps the default.
	OK   string `toml:"ok"`
	Warn string `toml:"warn"`
	Err  string `toml:"err"`

	// Gray themes the untested/dead latency tier (default 241); light
	// terminals want something like "249". Same format as Accent; empty or
	// invalid keeps the default.
	Gray string `toml:"gray"`

	// SelBG overrides the selected-row bar's background. Empty (the
	// default) derives it from the accent blended into a dark base; light
	// terminals want an explicit light bar, e.g. "#d0d0d0".
	SelBG string `toml:"sel_bg"`

	// Lang selects the UI language: "en" for English, empty/"zh" for
	// Chinese (the source language). Switchable live from the settings
	// window, which rewrites this field and saves.
	Lang string `toml:"lang"`

	// Theme selects a named color scheme from the built-ins or a
	// theme/*.toml file (config.Theme). When set it wins over the inline
	// Accent/Border/Dim above, which remain the fallback for slots the
	// theme leaves empty. Read once at startup; switched live from the
	// settings window (which rewrites this field and saves).
	Theme string `toml:"theme"`

	// Pin is the TUI-managed pinned-node state: a dedicated fixed group
	// ("pinned") whose single member carries the pinned node, plus the
	// outbound the pin displaced in the routing DSL (what unpin restores).
	// GroupID survives unpin — the group is deliberately kept around: an
	// unreferenced group is inert in daed's apply path, and the stable ID
	// makes re-pinning a pure membership swap.
	Pin Pin `toml:"pin"`

	// AutoReload reloads daed automatically (2s after the last change this
	// TUI made) instead of waiting for the manual A. Off by default: every
	// reload is a cold restart of the control plane — established
	// connections drop for about a second — and a failed rollback kills the
	// daed process, so it must be a deliberate opt-in. External changes
	// (daed web UI) never trigger it; a failed auto-reload never retries.
	AutoReload bool `toml:"auto_reload"`

	// mu serializes mutations + saves: driver hooks persist new tokens and
	// credentials from background request goroutines while the UI's logout
	// clears the session on the tea goroutine. Unexported, so the struct
	// must stay behind a pointer (it is everywhere).
	mu sync.Mutex
}

// DefaultEndpoint is the daed default GraphQL address.
const DefaultEndpoint = "http://127.0.0.1:2023/graphql"

// InlineTheme returns the config's inline color tuning as a Theme — the
// cascade's middle tier between a named theme and the built-in palette.
func (c *Config) InlineTheme() Theme {
	return Theme{
		Accent: c.Accent, Border: c.Border, Dim: c.Dim,
		OK: c.OK, Warn: c.Warn, Err: c.Err, Gray: c.Gray, SelBG: c.SelBG,
	}
}

// Pin is the persisted pinned-node state (Config.Pin).
type Pin struct {
	GroupID string `toml:"group_id"`
	Restore string `toml:"restore"` // outbound the pin displaced; "" = no pin
}

func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dae-tui", "config.toml"), nil
}

// Load reads the config file, applying defaults. A missing file is not an
// error.
func Load(path string) (*Config, error) {
	cfg := &Config{Endpoint: DefaultEndpoint}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if _, err := toml.Decode(string(raw), cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	if cfg.Editor == "" {
		cfg.Editor = "external"
	}
	return cfg, nil
}

// HasAuth reports whether the stored credentials could authenticate a
// session. Locked: background token refreshes may write concurrently.
func (c *Config) HasAuth() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Token != "" || (c.Username != "" && c.Password != "")
}

// Save writes the config back with 0600 permissions (it contains
// credentials and a bearer token).
func (c *Config) Save(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked(path)
}

// UpdateToken replaces the bearer token and persists it. Driver hooks call
// this from background request goroutines whenever a refresh produces a new
// token.
func (c *Config) UpdateToken(path, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Token = token
	return c.saveLocked(path)
}

// UpdateCredentials replaces the stored username/password and persists them.
func (c *Config) UpdateCredentials(path, username, password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Username, c.Password = username, password
	return c.saveLocked(path)
}

// UpdateTheme replaces the named theme preference and persists it. The field
// write must share the lock with saveLocked's marshal: background token
// refreshes save the whole config concurrently, so an unlocked write races
// their read of every field.
func (c *Config) UpdateTheme(path, theme string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Theme = theme
	return c.saveLocked(path)
}

// UpdateLang replaces the UI language preference and persists it. Locked for
// the same reason as UpdateTheme.
func (c *Config) UpdateLang(path, lang string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Lang = lang
	return c.saveLocked(path)
}

// UpdatePin replaces the pinned-node state and persists it. Locked like
// UpdateTheme: background token refreshes save the whole config file
// concurrently with pin/unpin completions on the tea goroutine.
func (c *Config) UpdatePin(path, groupID, restore string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Pin.GroupID, c.Pin.Restore = groupID, restore
	return c.saveLocked(path)
}

// PinSnapshot returns the persisted pin as a consistent pair. Readers must
// use this instead of touching c.Pin directly: the pin cmds (pinNodeCmd,
// unpinNodeCmd, switchGroupCmd) read and rewrite the pair from tea cmd
// goroutines while the root model's rederivePin reads it on the tea
// goroutine, so an unlocked read can observe the new GroupID next to the
// old Restore — a half-applied pin that neither re-pinning nor unpinning
// understands. Locked like UpdatePin.
func (c *Config) PinSnapshot() Pin {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Pin
}

// UpdateAutoReload flips the auto-reload preference and persists it. Locked
// like UpdateTheme (whole-file saves race the background token refresh).
func (c *Config) UpdateAutoReload(path string, on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.AutoReload = on
	return c.saveLocked(path)
}

// ClearSession wipes every stored credential and persists (logout).
func (c *Config) ClearSession(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Username, c.Password, c.Token = "", "", ""
	return c.saveLocked(path)
}

// saveLocked persists the config atomically: a temp file in the same
// directory is written, synced and renamed over the target, so a crash
// mid-save can never truncate the only copy of the stored credentials, and
// the final file always ends up 0600 (CreateTemp creates it that way;
// os.WriteFile would keep a pre-existing file's looser mode).
func (c *Config) saveLocked(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeded
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// Persist the rename itself: on some filesystems a crash right after
	// the rename can otherwise lose the directory entry. Best-effort — a
	// failure to sync the dir is not worth failing the save over.
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
