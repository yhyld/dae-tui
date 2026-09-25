package daed

import (
	"fmt"
	"strings"

	"dae-tui/internal/driver"
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

// builtinOutbounds are not proxy groups: a routing target equal to one of
// these does not count as "the proxy group" when detecting a preset.
var builtinOutbounds = map[string]bool{
	"direct": true, "must_direct": true, "block": true, "must_proxy": true,
}

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
func validGroupName(name string) bool {
	if name == "" {
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
// recognizes the exact rule sets the presets emit, so a hand-written routing
// that merely looks similar is reported as custom rather than mislabeled.
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
	// The prelude, semantically: any pname(...) -> must_direct line plus the
	// private-IP rule. daed's own default template spells the pname arguments
	// differently from our presets, and it is still a nonCn routing.
	hasMustDirect, hasPrivate := false, false
	for _, l := range lines {
		if l == "dip(geoip:private)->direct" {
			hasPrivate = true
			continue
		}
		if cond, target, ok := splitRule(l); ok &&
			strings.HasPrefix(cond, "pname(") && target == "must_direct" {
			hasMustDirect = true
		}
	}
	if !hasMustDirect || !hasPrivate {
		return "" // without the prelude it is not one of ours
	}

	fallback := ""
	cnDirect, cnProxy, gfwProxy := 0, 0, false
	proxyTargets := map[string]bool{}
	for _, l := range lines {
		if strings.HasPrefix(l, "fallback:") {
			fallback = strings.TrimPrefix(l, "fallback:")
			continue
		}
		cond, target, ok := splitRule(l)
		if !ok {
			continue
		}
		isProxy := !builtinOutbounds[target]
		switch cond {
		case "dip(geoip:cn)", "domain(geosite:cn)":
			switch {
			case target == "direct":
				cnDirect++
			case isProxy:
				cnProxy++
				proxyTargets[target] = true
			}
		case "domain(geosite:gfw)":
			if isProxy {
				gfwProxy = true
			}
		}
	}

	switch {
	case gfwProxy && fallback == "direct":
		return "gfw"
	case cnDirect == 2 && !builtinOutbounds[fallback]:
		return "nonCn"
	case cnProxy == 2 && len(proxyTargets) == 1 && fallback == "direct":
		return "cnOnly"
	case !gfwProxy && cnDirect == 0 && cnProxy == 0 && !builtinOutbounds[fallback]:
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
