// Package keymap remaps the app's keyboard shortcuts. The catalog is
// per-scope: an action is identified by its DEFAULT key inside a scope
// ("global/q", "groups/j"), and a binding file maps that default to a new
// key. Dispatch keeps switching on the default names — Translate rewrites
// what the user pressed into the canonical name before the switch, so the
// remap layer stays out of the page code.
package keymap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// defaults is the per-scope set of DEFAULT dispatch keys, registered by the
// app at startup (the catalog lives there; the keymap only needs the key
// names). Load and Set consult it so a binding whose new key would silently
// shadow a still-live action — in the same scope or in the global layer
// that dispatches ahead of it — is rejected with a note instead.
var (
	defaultsMu sync.RWMutex
	defaults   = map[string]map[string]bool{}
)

// RegisterDefaults records the default keys a scope dispatches on. Call
// once at startup, before the first Load.
func RegisterDefaults(scope string, keys []string) {
	defaultsMu.Lock()
	defer defaultsMu.Unlock()
	m := defaults[scope]
	if m == nil {
		m = map[string]bool{}
		defaults[scope] = m
	}
	for _, k := range keys {
		m[k] = true
	}
}

// shadowsDefault reports whether binding newKey in scope would silently
// cover a still-live default action: either another action of the same
// scope, or a global action (global dispatch runs ahead of the page's, so
// a page binding on a live global key never fires). A default that has
// itself been moved away is not live — that is how key swaps are expressed.
func shadowsDefault(scope, newKey string, scopeDead, globalDead map[string]bool) bool {
	defaultsMu.RLock()
	inScope := defaults[scope][newKey]
	inGlobal := scope != Global && defaults[Global][newKey]
	defaultsMu.RUnlock()
	if inScope && !scopeDead[newKey] {
		return true
	}
	return inGlobal && !globalDead[newKey]
}

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
	// Second pass, after every binding of the file is applied so true swaps
	// (a→b plus b→a) pass: a new key that is still the live default of
	// another action would silently shadow it — Translate maps the pressed
	// key to this override before the dispatch switch ever sees the default.
	for scope, binds := range km.over {
		for def, neu := range binds {
			if shadowsDefault(scope, neu, km.dead[scope], km.dead[Global]) {
				km.notes = append(km.notes,
					scope+"/"+def+" → "+neu+"：该键仍是其它未改走动作的默认键，绑定已忽略（先把原键改走可实现交换）")
				delete(km.over[scope], def)
				delete(km.rev[scope], neu)
				delete(km.dead[scope], def)
			}
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

// Set binds def to newKey in scope and persists the whole binding set to
// path. newKey == def removes the override (restore the default); the
// caller validates newKey first (fixed keys, conflicts) — Set applies.
// The file is rewritten from the live state in deterministic order, so
// hand-written comments do not survive an edit made here, but every
// binding — including scopes the UI never shows — does.
func (km *Keymap) Set(path, scope, def, newKey string) error {
	km.mu.Lock()
	defer km.mu.Unlock()
	// Same shadow rule the loader enforces, checked before anything is
	// mutated so a rejection leaves the previous binding intact; the UI
	// validates first, this is the depth for direct Set callers.
	if newKey != def && shadowsDefault(scope, newKey, km.dead[scope], km.dead[Global]) {
		return fmt.Errorf("%s 仍是未改走动作的默认键，绑定被拒绝", newKey)
	}
	if km.over[scope] == nil {
		km.over[scope] = map[string]string{}
		km.rev[scope] = map[string]string{}
		km.dead[scope] = map[string]bool{}
	}
	if old, ok := km.over[scope][def]; ok {
		delete(km.rev[scope], old)
	}
	delete(km.over[scope], def)
	delete(km.dead[scope], def)
	if newKey != def {
		km.over[scope][def] = newKey
		km.rev[scope][newKey] = def
		km.dead[scope][def] = true
	}
	return km.saveLocked(path)
}

// saveLocked writes the binding set atomically (temp file + fsync +
// rename, like config.toml): a crash mid-save must not leave a truncated
// keys.toml that silently drops bindings on the next start.
func (km *Keymap) saveLocked(path string) error {
	scopes := make([]string, 0, len(km.over))
	for sc := range km.over {
		if len(km.over[sc]) > 0 {
			scopes = append(scopes, sc)
		}
	}
	sort.Strings(scopes)
	var b strings.Builder
	for _, sc := range scopes {
		fmt.Fprintf(&b, "[%s]\n", sc)
		defs := make([]string, 0, len(km.over[sc]))
		for d := range km.over[sc] {
			defs = append(defs, d)
		}
		sort.Strings(defs)
		for _, d := range defs {
			fmt.Fprintf(&b, "%s = %q\n", d, km.over[sc][d])
		}
		b.WriteString("\n")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".keys-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
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
	// Best-effort directory sync so the rename survives a crash, matching
	// config.toml's atomic save.
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// BoundTo reports the default key that newKey currently triggers in scope
// through an override (a default key's own liveness is the caller's to
// judge against its catalog — this only sees the override table).
func (km *Keymap) BoundTo(scope, newKey string) (string, bool) {
	km.mu.RLock()
	defer km.mu.RUnlock()
	def, ok := km.rev[scope][newKey]
	return def, ok
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
