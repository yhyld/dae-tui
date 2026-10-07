package daed

import (
	"fmt"
	"strings"

	"github.com/yhyld/dae-tui/internal/driver"
)

// Routing presets: ready-made routing profiles rendered as dae DSL. They
// mirror the templates daed's own web UI offers in its "simple mode", so a
// user switching between the two tools sees the same rules.
//
// Every preset starts from the same prelude, which keeps local and private
// traffic out of the proxy regardless of the mode chosen.
const routingPrelude = `pname(NetworkManager, systemd-resolved, dnsmasq) -> must_direct
dip(geoip:private) -> direct`

// routingPresetOrder is the order the UI lists the presets in.
var routingPresetOrder = []string{"gfw", "nonCn", "cnOnly", "global"}

func (d *Driver) RoutingPresets() []driver.RoutingPreset {
	out := make([]driver.RoutingPreset, 0, len(routingPresetOrder))
	for _, id := range routingPresetOrder {
		out = append(out, driver.RoutingPreset{ID: id, Group: true})
	}
	return out
}

// BuildRoutingPreset renders the DSL for a preset. proxyGroup is the group
// non-direct traffic is sent to; it must be a plain identifier, because it
// is interpolated into the DSL verbatim.
func (d *Driver) BuildRoutingPreset(id, proxyGroup string) (string, error) {
	if !validGroupName(proxyGroup) {
		return "", fmt.Errorf("代理组名 %q 无法写入 DSL（仅限字母、数字、-、_、.）", proxyGroup)
	}
	var rules string
	switch id {
	case "gfw":
		rules = "domain(geosite:gfw) -> " + proxyGroup + "\nfallback: direct"
	case "nonCn":
		rules = "dip(geoip:cn) -> direct\ndomain(geosite:cn) -> direct\nfallback: " + proxyGroup
	case "cnOnly":
		rules = "dip(geoip:cn) -> " + proxyGroup +
			"\ndomain(geosite:cn) -> " + proxyGroup + "\nfallback: direct"
	case "global":
		rules = "fallback: " + proxyGroup
	default:
		return "", fmt.Errorf("未知路由预设 %q", id)
	}
	return routingPrelude + "\n" + rules, nil
}

// validGroupName reports whether name can appear as a DSL outbound. Group
// names come from the backend and are usually plain, but a name with spaces
// or quotes would silently produce unparseable DSL — refuse it here instead.
// A name equal to a builtin outbound is refused too: the generated
// "-> name" would route to the builtin instead of the group, and the result
// would read back as custom rather than as the preset.
func validGroupName(name string) bool {
	if name == "" || driver.BuiltinOutbounds[name] {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// DetectRoutingPreset reports which preset a stored routing DSL corresponds
// to, or "" when it is custom. It requires the shared prelude and only
// recognizes the exact rule sets the presets emit: a hand-written routing
// that merely contains a preset's rules (plus extras) reads as custom, so
// "apply preset" can never silently drop the extras.
func (d *Driver) DetectRoutingPreset(raw string) string {
	lines := presetLines(raw)
	if len(lines) == 0 {
		return ""
	}
	for _, l := range lines {
		if !presetLine(l) {
			return "" // a rule no preset emits → custom
		}
	}
	// Count every line by category; the switch below then requires the
	// totals to equal one preset's output exactly. Missing a count anywhere
	// (a second pname rule, a duplicated cn rule, two fallbacks, …) makes
	// the routing custom.
	var pnames, privates, fallbacks int
	fallback := ""
	hasMustDirect, hasPrivate := false, false
	var dipCnDirect, domCnDirect, dipCnProxy, domCnProxy int
	var gfwProxy, gfwOther, cnOther int
	proxyTargets := map[string]bool{}
	for _, l := range lines {
		if strings.HasPrefix(l, "fallback:") {
			fallbacks++
			fallback = strings.TrimPrefix(l, "fallback:")
			continue
		}
		cond, target, ok := splitRule(l)
		if !ok {
			return "" // presetLine passed but there is no rule arrow
		}
		isProxy := !driver.BuiltinOutbounds[target]
		switch {
		case strings.HasPrefix(cond, "pname("):
			pnames++
			if target == "must_direct" {
				// daed's own default template spells the pname arguments
				// differently from our presets and is still a nonCn routing,
				// so the prelude rule matches semantically, not verbatim.
				hasMustDirect = true
			}
		case cond == "dip(geoip:private)":
			privates++
			if target == "direct" {
				hasPrivate = true
			}
		case cond == "dip(geoip:cn)":
			switch {
			case target == "direct":
				dipCnDirect++
			case isProxy:
				dipCnProxy++
				proxyTargets[target] = true
			default:
				cnOther++ // e.g. dip(geoip:cn) -> block: no preset emits it
			}
		case cond == "domain(geosite:cn)":
			switch {
			case target == "direct":
				domCnDirect++
			case isProxy:
				domCnProxy++
				proxyTargets[target] = true
			default:
				cnOther++
			}
		case cond == "domain(geosite:gfw)":
			if isProxy {
				gfwProxy++
			} else {
				gfwOther++ // gfw -> direct: no preset emits it
			}
		default:
			return "" // not preset output despite the allowed prefix
		}
	}
	// The prelude is exactly one pname rule and one private-IP rule.
	if pnames != 1 || privates != 1 || !hasMustDirect || !hasPrivate {
		return ""
	}
	if fallbacks != 1 {
		return "" // a preset always ends with exactly one fallback
	}

	cnRules := dipCnDirect + domCnDirect + dipCnProxy + domCnProxy + cnOther
	gfwRules := gfwProxy + gfwOther
	switch {
	case gfwProxy == 1 && gfwOther == 0 && cnRules == 0 && fallback == "direct":
		return "gfw"
	case dipCnDirect == 1 && domCnDirect == 1 && dipCnProxy == 0 && domCnProxy == 0 &&
		cnOther == 0 && gfwRules == 0 && !driver.BuiltinOutbounds[fallback]:
		return "nonCn"
	case dipCnProxy == 1 && domCnProxy == 1 && len(proxyTargets) == 1 &&
		dipCnDirect == 0 && domCnDirect == 0 && cnOther == 0 &&
		gfwRules == 0 && fallback == "direct":
		return "cnOnly"
	case cnRules == 0 && gfwRules == 0 && !driver.BuiltinOutbounds[fallback]:
		return "global"
	}
	return ""
}

// presetLines splits a routing DSL into comparable lines: comments and
// blanks dropped, all whitespace removed so any spacing the user (or the
// generator) chose compares equal.
func presetLines(raw string) []string {
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, strings.Join(strings.Fields(l), ""))
	}
	return out
}

// presetLine reports whether a whitespace-free line is one the presets can
// emit. Anything else means the routing was hand-edited.
func presetLine(l string) bool {
	for _, p := range []string{
		"pname(", "dip(geoip:private)", "dip(geoip:cn)", "domain(geosite:cn)",
		"domain(geosite:gfw)", "fallback:",
	} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// splitRule splits "condition -> outbound" into its parts.
func splitRule(l string) (cond, target string, ok bool) {
	i := strings.Index(l, "->")
	if i < 0 {
		return "", "", false
	}
	return l[:i], l[i+2:], true
}
