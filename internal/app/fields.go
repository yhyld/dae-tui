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
	"logLevel":                   i18n.T("日志级别"),
	"lanInterface":               i18n.T("LAN 接口"),
	"wanInterface":               i18n.T("WAN 接口"),
	"dialMode":                   i18n.T("拨号模式"),
	"checkInterval":              i18n.T("检查间隔"),
	"checkTolerance":             i18n.T("检查容差"),
	"allowInsecure":              i18n.T("允许不安全 TLS"),
	"tcpCheckUrl":                i18n.T("TCP 检查 URL"),
	"tcpCheckHttpMethod":         i18n.T("TCP 检查方法"),
	"udpCheckDns":                i18n.T("UDP 检查 DNS"),
	"sniffingTimeout":            i18n.T("探测超时"),
	"mptcp":                      "MPTCP",
	"pprofPort":                  i18n.T("pprof 端口"),
	"autoConfigKernelParameter":  i18n.T("自动内核参数"),
	"autoConfigFirewallRule":     i18n.T("自动防火墙规则"),
	"tproxyPort":                 i18n.T("tproxy 端口"),
	"tproxyPortProtect":          i18n.T("tproxy 端口保护"),
	"soMarkFromDae":              "SO_MARK",
	"soMarkFromDaeSet":           i18n.T("SO_MARK 已设置"),
	"disableWaitingNetwork":      i18n.T("禁用等待网络"),
	"enableLocalTcpFastRedirect": i18n.T("TCP 快速重定向"),
	"tlsImplementation":          i18n.T("TLS 实现"),
	"utlsImitate":                i18n.T("uTLS 伪装"),
	"tlsFragment":                i18n.T("TLS 分片"),
	"tlsFragmentLength":          i18n.T("TLS 分片长度"),
	"tlsFragmentInterval":        i18n.T("TLS 分片间隔"),
	"bootstrapResolver":          i18n.T("引导解析器"),
	"fallbackResolver":           i18n.T("备用解析器"),
	"bandwidthMaxTx":             i18n.T("最大上行带宽"),
	"bandwidthMaxRx":             i18n.T("最大下行带宽"),
	"udphopInterval":             i18n.T("UDP 跳变间隔"),
}

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
