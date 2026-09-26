// dae-tui: a terminal UI for the dae eBPF proxy, speaking the daed GraphQL
// API (Phase 1). See README.md for architecture and security notes.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/app"
	"dae-tui/internal/config"
	"dae-tui/internal/driver"
	daeddrv "dae-tui/internal/driver/daed"
)

func main() {
	cfgPathFlag := flag.String("config", "", "配置文件路径 (默认 ~/.config/dae-tui/config.toml)")
	endpointFlag := flag.String("endpoint", "", "daed GraphQL 端点 (默认 http://127.0.0.1:2023/graphql)")
	probe := flag.Bool("probe", false, "非交互自检：连接后端并打印诊断信息后退出")
	version := flag.Bool("version", false, "打印版本")
	cmdFlag := flag.String("cmd", "", "非交互子命令: status（打印后端状态）| test（触发测速，见 -g/-n）")
	groupFlag := flag.String("g", "", "-cmd test: 按组名测速（逗号分隔多个组）")
	nodeFlag := flag.String("n", "", "-cmd test: 按节点 ID 测速（逗号分隔多个 ID）")
	flag.Parse()

	if *version {
		fmt.Println("dae-tui 0.1.0")
		return
	}

	cfgPath := *cfgPathFlag
	if cfgPath == "" {
		p, err := config.Path()
		if err != nil {
			fatal("确定配置路径失败: %v", err)
		}
		cfgPath = p
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fatal("加载配置失败: %v", err)
	}
	if *endpointFlag != "" {
		cfg.Endpoint = *endpointFlag
	}

	// Credential persistence goes through Config's locked mutators: these
	// hooks fire on background request goroutines (silent re-auth) while
	// the UI may be logging out on the tea goroutine, and each save is an
	// atomic write-then-rename.
	warnSave := func(err error) {
		if err != nil {
			fmt.Fprintln(os.Stderr, "警告: 保存配置失败:", err)
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
	// Mirror credentials into the config after interactive login so the
	// 30-day JWT can be renewed silently on expiry.
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
		fatal("未知 -cmd %q（可用: status, test）", *cmdFlag)
	}

	p := tea.NewProgram(app.New(drv, cfg, cfgPath), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fatal("启动 TUI 失败: %v", err)
	}
}

// splitCSV splits a comma-separated flag value, dropping blanks.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// runStatus prints a compact, read-only summary of the backend — what a
// script or a desktop keybinding needs to decide its next step. Like probe
// it never calls run(): on daed, run(dry:true) stops the proxy.
func runStatus(d *daeddrv.Driver) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	st, err := d.Connect(ctx)
	if err != nil {
		fatal("连接失败: %v", err)
	}
	run := "已停止"
	if st.Running {
		run = "运行中"
	}
	mod := ""
	if st.Modified {
		mod = "，有未应用改动"
	}
	fmt.Printf("dae %s  代理%s%s\n", st.Version, run, mod)

	groups, err := d.ListGroups(ctx)
	if err != nil {
		fmt.Println("ListGroups:", err)
	} else {
		for _, g := range groups {
			cur := "自动"
			if sel := g.SelectedNode(); sel != nil {
				cur = sel.Name
			}
			fmt.Printf("组 %-16s 策略=%-14s 成员=%-4d 当前=%s\n", g.Name, g.Policy, len(g.Members()), cur)
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

// runTest triggers a latency probe: every node by default, the members of
// the groups named with -g, or the node IDs listed with -n. Switching a
// group's node is deliberately not a CLI subcommand — a daed v2 fixed group
// holds exactly one member, so pinning silently rebuilds the whole group.
func runTest(d *daeddrv.Driver, groups, nodes string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if _, err := d.Connect(ctx); err != nil {
		fatal("连接失败: %v", err)
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
			fmt.Fprintf(os.Stderr, "dae-tui: 警告: 组 %q 不存在\n", name)
		}
		if len(ids) == 0 {
			fatal("指定的组没有成员节点")
		}
	}

	if err := d.TestLatency(ctx, ids); err != nil {
		fatal("测速失败: %v", err)
	}
	if len(ids) == 0 {
		fmt.Println("已触发全部节点测速")
		return
	}
	fmt.Printf("已触发 %d 个节点测速\n", len(ids))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dae-tui: "+format+"\n", args...)
	os.Exit(1)
}

// runProbe exercises the driver end to end without a TTY. Everything it
// calls is read-only, except testNodeLatencies (a harmless probe trigger),
// which only runs with DAE_TUI_PROBE_MUTATE=1. It deliberately never calls
// run(): on daed, run(dry:true) stops the proxy, so a self-check must not
// do it.
func runProbe(d *daeddrv.Driver) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pr := func(format string, args ...any) { fmt.Printf("· "+format+"\n", args...) }

	users, err := d.NumberUsers(ctx)
	if err != nil {
		fmt.Println("numberUsers:", err)
		return
	}
	pr("endpoint 可达, users=%d", users)

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
	fmt.Println("probe 完成")
}

var _ driver.Driver = (*daeddrv.Driver)(nil)
