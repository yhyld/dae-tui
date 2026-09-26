// Package config loads and persists the dae-tui application config
// (~/.config/dae-tui/config.toml).
package config

import (
	"fmt"
	"os"
	"path/filepath"

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

// Save writes the config back with 0600 permissions (it contains
// credentials and a bearer token).
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}
