package app

import (
	"sort"

	"dae-tui/internal/driver"
)

// configFieldLabels maps globalInput keys to Chinese labels. The editable
// field set is discovered from the backend's field metadata, so keys this
// table does not know fall back to the backend label and then the raw key —
// a new daed field stays reachable without a code change here.
var configFieldLabels = map[string]string{
	"logLevel":                   "日志级别",
	"lanInterface":               "LAN 接口",
	"wanInterface":               "WAN 接口",
	"dialMode":                   "拨号模式",
	"checkInterval":              "检查间隔",
	"checkTolerance":             "检查容差",
	"allowInsecure":              "允许不安全 TLS",
	"tcpCheckUrl":                "TCP 检查 URL",
	"tcpCheckHttpMethod":         "TCP 检查方法",
	"udpCheckDns":                "UDP 检查 DNS",
	"sniffingTimeout":            "探测超时",
	"mptcp":                      "MPTCP",
	"pprofPort":                  "pprof 端口",
	"autoConfigKernelParameter":  "自动内核参数",
	"autoConfigFirewallRule":     "自动防火墙规则",
	"tproxyPort":                 "tproxy 端口",
	"tproxyPortProtect":          "tproxy 端口保护",
	"soMarkFromDae":              "SO_MARK",
	"soMarkFromDaeSet":           "SO_MARK 已设置",
	"disableWaitingNetwork":      "禁用等待网络",
	"enableLocalTcpFastRedirect": "TCP 快速重定向",
	"tlsImplementation":          "TLS 实现",
	"utlsImitate":                "uTLS 伪装",
	"tlsFragment":                "TLS 分片",
	"tlsFragmentLength":          "TLS 分片长度",
	"tlsFragmentInterval":        "TLS 分片间隔",
	"bootstrapResolver":          "引导解析器",
	"fallbackResolver":           "备用解析器",
	"bandwidthMaxTx":             "最大上行带宽",
	"bandwidthMaxRx":             "最大下行带宽",
	"udphopInterval":             "UDP 跳变间隔",
}

// preferredFieldOrder lists the fields users touch most; they lead the
// picker and the detail pane, everything else follows in backend order.
var preferredFieldOrder = []string{
	"logLevel", "lanInterface", "wanInterface", "dialMode",
	"checkInterval", "checkTolerance", "allowInsecure",
	"tcpCheckUrl", "udpCheckDns", "sniffingTimeout",
	"mptcp", "pprofPort", "autoConfigKernelParameter", "autoConfigFirewallRule",
}

func fieldLabel(f driver.ConfigField) string {
	if l, ok := configFieldLabels[f.Name]; ok {
		return l
	}
	if f.Label != "" {
		return f.Label
	}
	return f.Name
}

// orderedFields returns the config's editable fields with the commonly used
// ones first, preserving backend order inside each group.
func orderedFields(fields []driver.ConfigField) []driver.ConfigField {
	rank := map[string]int{}
	for i, k := range preferredFieldOrder {
		rank[k] = i
	}
	out := append([]driver.ConfigField(nil), fields...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		ra, oka := rank[a.Name]
		rb, okb := rank[b.Name]
		switch {
		case oka && okb:
			return ra < rb
		case oka:
			return true
		case okb:
			return false
		default:
			return false // keep backend order for the rest
		}
	})
	return out
}
