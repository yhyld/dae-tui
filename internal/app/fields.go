package app

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"dae-tui/internal/driver"
	"dae-tui/internal/i18n"
)

func validateFieldValue(f driver.ConfigField, val string) error {
	switch f.Type {
	case "int":
		if _, err := strconv.Atoi(val); err != nil {
			return fmt.Errorf(i18n.T("需要整数，得到 %q"), val)
		}
	case "bool":
		switch strings.ToLower(val) {
		case "true", "false":
		default:
			return fmt.Errorf(i18n.T("需要 true 或 false，得到 %q"), val)
		}
	case "duration":
		if _, err := time.ParseDuration(val); err != nil {
			return fmt.Errorf(i18n.T("需要时长（如 30s、5m），得到 %q"), val)
		}
	case "array":

		for _, item := range strings.Split(val, ",") {
			if strings.TrimSpace(item) == "" {
				return errors.New(i18n.T("数组元素不能为空（用逗号分隔）"))
			}
		}
	}
	return nil
}

func configuredIfaces(value string) []string {
	var out []string
	for _, n := range strings.Split(value, ",") {
		if n = strings.TrimSpace(n); n == "" || strings.EqualFold(n, "auto") {
			continue
		}
		out = append(out, n)
	}
	return out
}

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

var preferredFieldOrder = []string{
	"logLevel", "lanInterface", "wanInterface", "dialMode",
	"checkInterval", "checkTolerance", "allowInsecure",
	"tcpCheckUrl", "udpCheckDns", "sniffingTimeout",
	"mptcp", "pprofPort", "autoConfigKernelParameter", "autoConfigFirewallRule",
}

func fieldLabel(f driver.ConfigField) string {
	if l, ok := configFieldLabels[f.Name]; ok {
		return i18n.T(l)
	}
	if f.Label != "" {
		return f.Label
	}
	return f.Name
}

var fieldRank = func() map[string]int {
	rank := make(map[string]int, len(preferredFieldOrder))
	for i, k := range preferredFieldOrder {
		rank[k] = i
	}
	return rank
}()

func orderedFields(fields []driver.ConfigField) []driver.ConfigField {
	out := append([]driver.ConfigField(nil), fields...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		ra, oka := fieldRank[a.Name]
		rb, okb := fieldRank[b.Name]
		switch {
		case oka && okb:
			return ra < rb
		case oka:
			return true
		case okb:
			return false
		default:
			return false
		}
	})
	return out
}
