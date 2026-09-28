package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/app"
	"dae-tui/internal/config"
	"dae-tui/internal/driver"
	daeddrv "dae-tui/internal/driver/daed"
	"dae-tui/internal/i18n"
	"dae-tui/internal/ui"
)

var version = "dev"

func versionString() string {
	if version != "dev" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	var rev, t string
	dirty := false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			t = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return version
	}
	if len(rev) > 10 {
		rev = rev[:10]
	}
	v := "dev-" + rev
	if dirty {
		v += "+dirty"
	}
	if t != "" && len(t) >= 10 {
		v += " " + t[:10]
	}
	return v
}

func main() {
	// The flag help strings below render at flag definition time, so the
	// language must come from the config file before the flags are
	// declared. Only the -config path is pre-scanned from the raw command
	// line; the full load happens after flag.Parse as before.
	if lang := preScanLang(); lang != "" {
		i18n.SetLang(lang)
	}
	cfgPathFlag := flag.String("config", "", i18n.T("配置文件路径 (默认 ~/.config/dae-tui/config.toml)"))
	endpointFlag := flag.String("endpoint", "", i18n.T("daed GraphQL 端点 (默认 http://127.0.0.1:2023/graphql)"))
	probe := flag.Bool("probe", false, i18n.T("非交互自检：连接后端并打印诊断信息后退出"))
	version := flag.Bool("version", false, i18n.T("打印版本"))
	cmdFlag := flag.String("cmd", "", i18n.T("非交互子命令: status（打印后端状态）| test（触发测速，见 -g/-n）"))
	groupFlag := flag.String("g", "", i18n.T("-cmd test: 按组名测速（逗号分隔多个组）"))
	nodeFlag := flag.String("n", "", i18n.T("-cmd test: 按节点 ID 测速（逗号分隔多个 ID）"))
	flag.Parse()

	if *version {
		fmt.Println("dae-tui " + versionString())
		return
	}

	cfgPath := *cfgPathFlag
	if cfgPath == "" {
		p, err := config.Path()
		if err != nil {
			fatal(i18n.T("确定配置路径失败: %v"), err)
		}
		cfgPath = p
	}
	cfg, err := loadCfg(cfgPath, *endpointFlag)
	if err != nil {
		fatal(i18n.T("加载配置失败: %v"), err)
	}
	// Authoritative application after the real load; the pre-scan above only
	// existed to localize the flag help strings.
	i18n.SetLang(cfg.Lang)

	theme, found := config.ResolveTheme(cfg.Theme, cfg.Accent, cfg.Border, cfg.Dim)
	if cfg.Theme != "" && !found {
		fmt.Fprintf(os.Stderr, i18n.T("警告: 主题 %q 未找到（内置或 ~/.config/dae-tui/theme/），使用内联颜色\n"), cfg.Theme)
	}
	ui.ApplyTheme(theme.Accent, theme.Border, theme.Dim)

	warnSave := func(err error) {
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.T("警告: 保存配置失败:"), err)
		}
	}

	drv := daeddrv.New(daeddrv.Options{
		Endpoint: cfg.Endpoint,
		Username: cfg.Username,
		Password: cfg.Password,
		Token:    cfg.Token,
		SaveToken: func(t string) {
			warnSave(cfg.UpdateToken(cfgPath, t))
		},
	})

	drv.SetCredHook(func(u, pw string) {
		warnSave(cfg.UpdateCredentials(cfgPath, u, pw))
	})

	if *probe {
		runProbe(drv)
		return
	}

	switch *cmdFlag {
	case "":
	case "status":
		runStatus(drv)
		return
	case "test":
		runTest(drv, *groupFlag, *nodeFlag)
		return
	default:
		fatal(i18n.T("未知 -cmd %q（可用: status, test）"), *cmdFlag)
	}

	app.Version = versionString()
	p := tea.NewProgram(app.New(drv, cfg, cfgPath), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fatal(i18n.T("启动 TUI 失败: %v"), err)
	}
}

// preScanLang reads the language from the config file before the flags are
// declared, so -h help text comes out in the configured language. Load is
// side-effect free (a missing file just yields defaults), so a failed
// pre-scan silently falls back to Chinese — the full load below reports
// real errors.
func preScanLang() string {
	path := ""
	args := os.Args[1:]
	for i, a := range args {
		var val string
		switch {
		case a == "-config" || a == "--config":
			if i+1 < len(args) {
				val = args[i+1]
			}
		case strings.HasPrefix(a, "-config="):
			val = strings.TrimPrefix(a, "-config=")
		case strings.HasPrefix(a, "--config="):
			val = strings.TrimPrefix(a, "--config=")
		default:
			continue
		}
		if val != "" {
			path = val
			break
		}
	}
	if path == "" {
		p, err := config.Path()
		if err != nil {
			return ""
		}
		path = p
	}
	cfg, err := config.Load(path)
	if err != nil {
		return ""
	}
	return cfg.Lang
}

func loadCfg(path, endpoint string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if endpoint != "" {
		cfg.Endpoint = endpoint
	}
	return cfg, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func testTargets(all []driver.Group, groups string) (ids, missing []string) {
	want := map[string]bool{}
	for _, name := range splitCSV(groups) {
		want[name] = true
	}
	seen := map[string]bool{}
	for _, g := range all {
		if !want[g.Name] {
			continue
		}
		delete(want, g.Name)
		for _, n := range g.Members() {
			if !seen[n.ID] {
				seen[n.ID] = true
				ids = append(ids, n.ID)
			}
		}
	}
	for name := range want {
		missing = append(missing, name)
	}
	return ids, missing
}

func runStatus(d *daeddrv.Driver) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	st, err := d.Connect(ctx)
	if err != nil {
		fatal(i18n.T("连接失败: %v"), err)
	}
	run := i18n.T("已停止")
	if st.Running {
		run = i18n.T("运行中")
	}
	mod := ""
	if st.Modified {
		mod = i18n.T("，需重载")
	}
	fmt.Printf(i18n.T("dae %s  代理%s%s\n"), st.Version, run, mod)

	groups, err := d.ListGroups(ctx)
	if err != nil {
		fmt.Println("ListGroups:", err)
	} else {
		for _, g := range groups {
			cur := i18n.T("自动")
			if sel := g.SelectedNode(); sel != nil {
				cur = sel.Name
			}
			fmt.Printf(i18n.T("组 %-16s 策略=%-14s 成员=%-4d 当前=%s\n"), g.Name, g.Policy, len(g.Members()), cur)
		}
	}

	sel, err := d.ListSelections(ctx)
	if err != nil {
		fmt.Println("ListSelections:", err)
		return
	}
	for _, c := range sel.Configs {
		if c.Selected {
			fmt.Printf("config  %s\n", c.Name)
		}
	}
	for _, x := range sel.Dns {
		if x.Selected {
			fmt.Printf("dns     %s\n", x.Name)
		}
	}
	for _, x := range sel.Routings {
		if x.Selected {
			fmt.Printf("routing %s\n", x.Name)
		}
	}
}

func runTest(d *daeddrv.Driver, groups, nodes string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if _, err := d.Connect(ctx); err != nil {
		fatal(i18n.T("连接失败: %v"), err)
	}

	var ids []string
	switch {
	case nodes != "":
		ids = splitCSV(nodes)
	case groups != "":
		all, err := d.ListGroups(ctx)
		if err != nil {
			fatal("ListGroups: %v", err)
		}
		ids, missing := testTargets(all, groups)
		for _, name := range missing {
			fmt.Fprintf(os.Stderr, i18n.T("dae-tui: 警告: 组 %q 不存在\n"), name)
		}
		if len(ids) == 0 {
			fatal(i18n.T("指定的组没有成员节点"))
		}
	}

	if err := d.TestLatency(ctx, ids); err != nil {
		fatal(i18n.T("测速失败: %v"), err)
	}
	if len(ids) == 0 {
		fmt.Println(i18n.T("已触发全部节点测速"))
		return
	}
	fmt.Printf(i18n.T("已触发 %d 个节点测速\n"), len(ids))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dae-tui: "+format+"\n", args...)
	os.Exit(1)
}

func runProbe(d *daeddrv.Driver) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pr := func(format string, args ...any) { fmt.Printf("· "+format+"\n", args...) }

	users, err := d.NumberUsers(ctx)
	if err != nil {
		fmt.Println("numberUsers:", err)
		return
	}
	pr(i18n.T("endpoint 可达, users=%d"), users)

	st, err := d.Connect(ctx)
	if err != nil {
		fmt.Println("connect:", err)
		return
	}
	pr("dae version=%s running=%v modified=%v", st.Version, st.Running, st.Modified)

	if groups, err := d.ListGroups(ctx); err != nil {
		fmt.Println("ListGroups:", err)
	} else {
		for _, g := range groups {
			pr("group %q policy=%s nodes=%d fixed=%d", g.Name, g.Policy, len(g.Nodes), g.FixedIndex())
		}
	}

	if subs, err := d.ListSubscriptions(ctx); err != nil {
		fmt.Println("ListSubscriptions:", err)
	} else {
		for _, s := range subs {
			pr("sub %q nodes=%d updated=%s", s.Tag, s.NodeCount, s.UpdatedAt.Format(time.RFC3339))
		}
	}

	if ifaces, err := d.Interfaces(ctx); err != nil {
		fmt.Println("Interfaces:", err)
	} else {
		for _, i := range ifaces {
			pr("iface %s up=%v default=%v ips=%v", i.Name, i.Up, i.Default, i.IPs)
		}
	}

	sel, err := d.ListSelections(ctx)
	if err != nil {
		fmt.Println("ListSelections:", err)
	} else {
		for _, c := range sel.Configs {
			pr("config %q selected=%v %s", c.Name, c.Selected, c.Detail)
		}
		for _, x := range sel.Dns {
			pr("dns %q selected=%v", x.Name, x.Selected)
		}
		for _, x := range sel.Routings {
			pr("routing %q selected=%v refs=%v", x.Name, x.Selected, x.References)
		}
	}

	if _, err := d.Traffic(ctx, 10, 60); err != nil {
		fmt.Println("Traffic:", err)
	} else {
		pr("runtimeOverview OK")
	}

	if os.Getenv("DAE_TUI_PROBE_MUTATE") == "1" {
		if err := d.TestLatency(ctx, nil); err != nil {
			fmt.Println("TestLatency(all):", err)
		} else {
			pr("testNodeLatencies triggered")
		}
	}
	fmt.Println(i18n.T("probe 完成"))
}

var _ driver.Driver = (*daeddrv.Driver)(nil)
