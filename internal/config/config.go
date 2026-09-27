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

	// mu serializes mutations + saves: driver hooks persist new tokens and
	// credentials from background request goroutines while the UI's logout
	// clears the session on the tea goroutine. Unexported, so the struct
	// must stay behind a pointer (it is everywhere).
	mu sync.Mutex
}

// DefaultEndpoint is the daed default GraphQL address.
const DefaultEndpoint = "http://127.0.0.1:2023/graphql"

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
	return os.Rename(tmp.Name(), path)
}
