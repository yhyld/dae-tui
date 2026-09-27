// Package keymap remaps the app's keyboard shortcuts. The catalog is
// per-scope: an action is identified by its DEFAULT key inside a scope
// ("global/q", "groups/j"), and a binding file maps that default to a new
// key. Dispatch keeps switching on the default names — Translate rewrites
// what the user pressed into the canonical name before the switch, so the
// remap layer stays out of the page code.
package keymap

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

// Fixed keys are never valid remap targets: they are the universal
// conventions every modal relies on (esc backs out, enter submits, tab
// moves focus, arrows are always-working synonyms of the letter nav,
// ctrl+c must survive everything).
var Fixed = map[string]bool{
	"esc": true, "enter": true, "tab": true, "shift+tab": true,
	"up": true, "down": true, "left": true, "right": true,
	"ctrl+c": true,
}

// dead is what Translate returns for a default key the user moved away:
// a switch label no live key produces.
const dead = "\x00"

// Scopes the UI dispatches through. The catalog lives in the app package;
// the keymap only needs names.
const (
	Global  = "global"
	Home    = "home"
	Groups  = "groups"
	Subs    = "subs"
	Nodes   = "nodes"
	Configs = "configs"
)

// Keymap is the loaded binding set. Read-only after Load; Reload swaps the
// whole value behind the pointer.
type Keymap struct {
	mu   sync.RWMutex
	over map[string]map[string]string // scope -> default key -> new key
	rev  map[string]map[string]string // scope -> new key -> default key
	dead map[string]map[string]bool   // scope -> default keys moved away
	notes []string                    // load warnings, shown in the keys viewer
}

// New returns a keymap with no overrides (every key at its default).
func New() *Keymap {
	return &Keymap{
		over: map[string]map[string]string{},
		rev:  map[string]map[string]string{},
		dead: map[string]map[string]bool{},
	}
}

// Path is the binding file's location: keys.toml next to config.toml.
func Path(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "keys.toml")
}

// Load reads the binding file. A missing file is the default keymap; a
// broken one is the default keymap plus a note — a typo in keys.toml must
// never leave the app unkeyable. One bad binding is dropped with a note and
// the rest apply.
func Load(path string) *Keymap {
	km := New()
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			km.notes = append(km.notes, "读取 keys.toml 失败: "+err.Error())
		}
		return km
	}
	var file map[string]map[string]string
	if _, err := toml.Decode(string(raw), &file); err != nil {
		km.notes = append(km.notes, "keys.toml 解析失败: "+err.Error())
		return km
	}
	for scope, binds := range file {
		seen := map[string]string{} // new key -> default that claimed it
		for def, neu := range binds {
			if neu == def {
				continue // binding a key to itself is a no-op, not a note
			}
			if Fixed[neu] {
				km.notes = append(km.notes,
					scope+"/"+def+" → "+neu+"：固定键不能作为新键位，已忽略")
				continue
			}
			if prev, dup := seen[neu]; dup {
				km.notes = append(km.notes,
					scope+"/"+def+" 与 "+scope+"/"+prev+" 都映射到 "+neu+"，两个绑定都已忽略")
				delete(km.over[scope], prev)
				delete(km.rev[scope], neu)
				delete(km.dead[scope], prev)
				continue
			}
			if km.over[scope] == nil {
				km.over[scope] = map[string]string{}
				km.rev[scope] = map[string]string{}
				km.dead[scope] = map[string]bool{}
			}
			km.over[scope][def] = neu
			km.rev[scope][neu] = def
			km.dead[scope][def] = true
			seen[neu] = def
		}
	}
	return km
}

// Reload re-reads path and swaps the bindings in place, keeping the same
// pointer so every dispatch site sees the new map.
func (km *Keymap) Reload(path string) {
	fresh := Load(path)
	km.mu.Lock()
	defer km.mu.Unlock()
	km.over, km.rev, km.dead, km.notes = fresh.over, fresh.rev, fresh.dead, fresh.notes
}

// Translate rewrites a pressed key into the canonical (default) name for
// the scope's dispatch switch: an override target yields the default it
// covers, a moved-away default yields dead, everything else passes through.
func (km *Keymap) Translate(scope, pressed string) string {
	km.mu.RLock()
	defer km.mu.RUnlock()
	if def, ok := km.rev[scope][pressed]; ok {
		return def
	}
	if km.dead[scope][pressed] {
		return dead
	}
	return pressed
}

// Key returns the key the action currently lives on (for footers, the
// frame help strip and the keys viewer).
func (km *Keymap) Key(scope, def string) string {
	km.mu.RLock()
	defer km.mu.RUnlock()
	if neu, ok := km.over[scope][def]; ok {
		return neu
	}
	return def
}

// Notes returns the load warnings (nil when the file was clean).
func (km *Keymap) Notes() []string {
	km.mu.RLock()
	defer km.mu.RUnlock()
	return append([]string(nil), km.notes...)
}

// Override reports whether the scope has any user binding at all — the
// viewer uses it to show a "defaults" hint.
func (km *Keymap) Override(scope string) bool {
	km.mu.RLock()
	defer km.mu.RUnlock()
	return len(km.over[scope]) > 0
}
