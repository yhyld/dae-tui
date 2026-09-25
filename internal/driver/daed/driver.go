// Package daed implements the driver.Driver interface on top of the daed
// GraphQL API (daed v2.1.1, the final release of the archived project; the
// schema is frozen, see schema.graphql in this package).
package daed

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"dae-tui/internal/driver"
)

// Options configures a daed driver instance.
type Options struct {
	Endpoint string
	// Username/Password are retained (with the caller's consent — they are
	// persisted by the app config) to refresh the 30-day JWT silently.
	Username string
	Password string
	Token    string
	// SaveToken is called whenever a new token is obtained, so the app can
	// persist it.
	SaveToken func(token string)
}

type Driver struct {
	client *Client
	opts   Options
	// credHook is notified whenever credentials are established via
	// Login/CreateUser so the caller can persist them.
	credHook func(username, password string)
	// groupsFallback is set when the backend's schema lacks the
	// GroupSubscription type (daed < 2026-04): ListGroups then permanently
	// uses the minimal query without subscription info.
	groupsFallback bool
}

var _ driver.Driver = (*Driver)(nil)

func New(opts Options) *Driver {
	if opts.Endpoint == "" {
		opts.Endpoint = "http://127.0.0.1:2023/graphql"
	}
	d := &Driver{client: NewClient(opts.Endpoint), opts: opts}
	d.client.SetToken(opts.Token)
	d.client.SetReAuth(func(c *Client) error { return d.reAuth(c) })
	return d
}

func (d *Driver) reAuth(c *Client) error {
	if d.opts.Username == "" || d.opts.Password == "" {
		return errors.New("no stored credentials")
	}
	tok, err := c.FetchToken(context.Background(), d.opts.Username, d.opts.Password)
	if err != nil {
		return err
	}
	c.SetToken(tok)
	if d.opts.SaveToken != nil {
		d.opts.SaveToken(tok)
	}
	return nil
}

func (d *Driver) Name() string { return "daed" }
func (d *Driver) Capabilities() driver.Caps {
	return driver.Caps{
		SwitchNode:    true,
		TestLatency:   true,
		TrafficStats:  true,
		Subscriptions: true,
		ConfigMgmt:    true,
	}
}

func (d *Driver) Connect(ctx context.Context) (driver.Status, error) {
	if err := d.client.HealthCheck(ctx); err != nil {
		return driver.Status{}, err
	}
	var out struct {
		General rawGeneral `json:"general"`
	}
	if err := d.client.Do(ctx, qGeneral, nil, &out); err != nil {
		return driver.Status{}, err
	}
	g := out.General.Dae
	return driver.Status{Version: g.Version, Running: g.Running, Modified: g.Modified, UpdatedAt: time.Now()}, nil
}

func (d *Driver) NumberUsers(ctx context.Context) (int, error) {
	return d.client.NumberUsers(ctx)
}

// SetCredHook installs a callback invoked with credentials after a
// successful Login or CreateUser.
func (d *Driver) SetCredHook(f func(username, password string)) { d.credHook = f }

func (d *Driver) Login(ctx context.Context, username, password string) (driver.Status, error) {
	tok, err := d.client.FetchToken(ctx, username, password)
	if err != nil {
		return driver.Status{}, err
	}
	d.opts.Username, d.opts.Password = username, password
	if d.credHook != nil {
		d.credHook(username, password)
	}
	d.client.SetToken(tok)
	if d.opts.SaveToken != nil {
		d.opts.SaveToken(tok)
	}
	return d.Connect(ctx)
}

func (d *Driver) CreateUser(ctx context.Context, username, password string) (driver.Status, error) {
	tok, err := d.client.CreateUser(ctx, username, password)
	if err != nil {
		return driver.Status{}, err
	}
	d.opts.Username, d.opts.Password = username, password
	if d.credHook != nil {
		d.credHook(username, password)
	}
	d.client.SetToken(tok)
	if d.opts.SaveToken != nil {
		d.opts.SaveToken(tok)
	}
	return d.Connect(ctx)
}

func (d *Driver) ListGroups(ctx context.Context) ([]driver.Group, error) {
	var out struct {
		Groups []rawGroup `json:"groups"`
	}
	// Prefer the rich query (group→subscriptions→matched nodes); fall back
	// to the minimal one on old daed builds that lack GroupSubscription.
	if !d.groupsFallback {
		err := d.client.Do(ctx, qGroupsRich, nil, &out)
		if err == nil {
			return mapGroups(out.Groups), nil
		}
		if !strings.Contains(err.Error(), "Cannot query field") {
			return nil, err
		}
		d.groupsFallback = true
		out = struct {
			Groups []rawGroup `json:"groups"`
		}{}
	}
	if err := d.client.Do(ctx, qGroups, nil, &out); err != nil {
		return nil, err
	}
	return mapGroups(out.Groups), nil
}

func mapGroups(raw []rawGroup) []driver.Group {
	groups := make([]driver.Group, 0, len(raw))
	for _, g := range raw {
		grp := driver.Group{
			ID:     g.ID,
			Name:   g.Name,
			Policy: g.Policy,
		}
		for _, p := range g.PolicyParams {
			grp.PolicyParams = append(grp.PolicyParams, driver.Param{Key: p.Key, Val: p.Val})
		}
		for _, n := range g.Nodes {
			grp.Nodes = append(grp.Nodes, mapNode(n))
		}
		for _, s := range g.Subscriptions {
			gs := driver.GroupSubscription{
				SubscriptionID:  s.Subscription.ID,
				Tag:             s.Subscription.Tag,
				MatchedCount:    s.MatchedCount,
				NameFilterRegex: s.NameFilterRegex,
			}
			for _, n := range s.MatchedNodes {
				gs.Nodes = append(gs.Nodes, mapNode(n))
			}
			grp.Subscriptions = append(grp.Subscriptions, gs)
		}
		groups = append(groups, grp)
	}
	return groups
}

func mapNode(n rawNode) driver.Node {
	return driver.Node{
		ID: n.ID, Name: n.Name, Tag: n.Tag, Protocol: n.Protocol,
		Address: n.Address, SubscriptionID: n.SubscriptionID,
	}
}

func (d *Driver) AddGroupSubscriptions(ctx context.Context, groupID string, subIDs []string, nameFilterRegex string) error {
	vars := map[string]any{"id": groupID, "subscriptionIDs": subIDs}
	if nameFilterRegex != "" {
		vars["nameFilterRegex"] = nameFilterRegex
	}
	return d.client.Do(ctx, mGroupAddSubscriptions, vars, nil)
}

func (d *Driver) RemoveGroupSubscriptions(ctx context.Context, groupID string, subIDs []string) error {
	return d.client.Do(ctx, mGroupDelSubscriptions, map[string]any{"id": groupID, "subscriptionIDs": subIDs}, nil)
}

func (d *Driver) AddGroupNodes(ctx context.Context, groupID string, nodeIDs []string) error {
	return d.client.Do(ctx, mGroupAddNodes, map[string]any{"id": groupID, "nodeIDs": nodeIDs}, nil)
}

func (d *Driver) RemoveGroupNodes(ctx context.Context, groupID string, nodeIDs []string) error {
	return d.client.Do(ctx, mGroupDelNodes, map[string]any{"id": groupID, "nodeIDs": nodeIDs}, nil)
}

// ListManualNodes walks the global nodes connection and keeps only
// subscription-less nodes (the ones eligible for groupAddNodes).
func (d *Driver) ListManualNodes(ctx context.Context) ([]driver.Node, error) {
	var out struct {
		Nodes rawNodesConn `json:"nodes"`
	}
	nodes := make([]driver.Node, 0, 16)
	after := ""
	for page := 0; ; page++ {
		vars := map[string]any{"first": 200}
		if after != "" {
			vars["after"] = after
		}
		if err := d.client.Do(ctx, qAllNodes, vars, &out); err != nil {
			return nil, err
		}
		for _, n := range out.Nodes.Edges {
			if n.SubscriptionID == "" {
				nodes = append(nodes, mapNode(n))
			}
		}
		if !out.Nodes.PageInfo.HasNextPage || len(out.Nodes.Edges) == 0 || page > 20 {
			return nodes, nil
		}
		after = out.Nodes.PageInfo.EndCursor
		if after == "" {
			return nodes, nil
		}
	}
}

func (d *Driver) SetGroupPolicy(ctx context.Context, groupID string, p driver.Policy) error {
	vars := map[string]any{"id": groupID, "policy": p.Name}
	if p.Name == "fixed" {
		// daed passes params through verbatim into the generated dae DSL;
		// an empty key renders as the positional argument of fixed(<n>).
		vars["policyParams"] = []map[string]any{{"val": strconv.Itoa(p.FixedIndex)}}
	}
	return d.client.Do(ctx, mGroupSetPolicy, vars, nil)
}

// TestLatency triggers latency probes. Large ID lists are chunked: probing
// hundreds of nodes in one mutation regularly exceeds sane HTTP timeouts.
func (d *Driver) TestLatency(ctx context.Context, nodeIDs []string) error {
	const batch = 100
	if len(nodeIDs) == 0 || len(nodeIDs) <= batch {
		vars := map[string]any{}
		if len(nodeIDs) > 0 {
			vars["ids"] = nodeIDs
		}
		return d.client.Do(ctx, mTestNodeLatencies, vars, nil)
	}
	for start := 0; start < len(nodeIDs); start += batch {
		end := start + batch
		if end > len(nodeIDs) {
			end = len(nodeIDs)
		}
		vars := map[string]any{"ids": nodeIDs[start:end]}
		if err := d.client.Do(ctx, mTestNodeLatencies, vars, nil); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) CreateGroup(ctx context.Context, name string, policy string) error {
	vars := map[string]any{"name": name, "policy": policy}
	return d.client.Do(ctx, mCreateGroup, vars, nil)
}

func (d *Driver) RemoveGroup(ctx context.Context, groupID string) error {
	return d.client.Do(ctx, mRemoveGroup, map[string]any{"id": groupID}, nil)
}

func (d *Driver) RenameGroup(ctx context.Context, groupID, name string) error {
	return d.client.Do(ctx, mRenameGroup, map[string]any{"id": groupID, "name": name}, nil)
}

func (d *Driver) ImportNode(ctx context.Context, link, tag string) error {
	arg := map[string]any{"link": link}
	if tag != "" {
		arg["tag"] = tag
	}
	vars := map[string]any{"rollbackError": true, "args": []any{arg}}
	return d.client.Do(ctx, mImportNodes, vars, nil)
}

func (d *Driver) RemoveNodes(ctx context.Context, nodeIDs []string) error {
	return d.client.Do(ctx, mRemoveNodes, map[string]any{"ids": nodeIDs}, nil)
}

// SubscriptionNodes walks the nodes connection (cursor = node ID) until
// exhausted. 200 per page keeps requests small while limiting round trips
// for typical subscription sizes.
func (d *Driver) SubscriptionNodes(ctx context.Context, subscriptionID string) ([]driver.Node, error) {
	var out struct {
		Nodes rawNodesConn `json:"nodes"`
	}
	nodes := make([]driver.Node, 0, 64)
	after := ""
	for page := 0; ; page++ {
		vars := map[string]any{"subscriptionId": subscriptionID, "first": 200}
		if after != "" {
			vars["after"] = after
		}
		if err := d.client.Do(ctx, qNodesBySubscription, vars, &out); err != nil {
			return nil, err
		}
		for _, n := range out.Nodes.Edges {
			nodes = append(nodes, driver.Node{
				ID: n.ID, Name: n.Name, Tag: n.Tag, Protocol: n.Protocol,
				Address: n.Address, SubscriptionID: n.SubscriptionID,
			})
		}
		if !out.Nodes.PageInfo.HasNextPage || len(out.Nodes.Edges) == 0 || page > 100 {
			return nodes, nil
		}
		after = out.Nodes.PageInfo.EndCursor
		if after == "" {
			return nodes, nil
		}
	}
}

func (d *Driver) Latencies(ctx context.Context, nodeIDs []string) ([]driver.Latency, error) {
	vars := map[string]any{}
	if len(nodeIDs) > 0 {
		vars["ids"] = nodeIDs
	}
	var out struct {
		NodeLatencies []rawNodeLatency `json:"nodeLatencies"`
	}
	if err := d.client.Do(ctx, qNodeLatencies, vars, &out); err != nil {
		return nil, err
	}
	lats := make([]driver.Latency, 0, len(out.NodeLatencies))
	for _, l := range out.NodeLatencies {
		lat := driver.Latency{NodeID: l.ID, Alive: l.Alive, TestedAt: parseTime(l.TestedAt)}
		if l.LatencyMs != nil {
			lat.Ms = *l.LatencyMs
		}
		if l.Message != nil {
			lat.Message = *l.Message
		}
		lats = append(lats, lat)
	}
	return lats, nil
}

func (d *Driver) Traffic(ctx context.Context, windowSec, maxPoints int) (driver.TrafficSnapshot, error) {
	var out struct {
		General rawGeneralTraffic `json:"general"`
	}
	vars := map[string]any{"windowSec": windowSec, "maxPoints": maxPoints}
	if err := d.client.Do(ctx, qRuntimeOverview, vars, &out); err != nil {
		return driver.TrafficSnapshot{}, err
	}
	r := out.General.RuntimeOverview
	snap := driver.TrafficSnapshot{
		UpRate:      r.UploadRate,
		DownRate:    r.DownloadRate,
		UpTotal:     parseInt64(r.UploadTotal),
		DownTotal:   parseInt64(r.DownloadTotal),
		Conns:       r.ActiveConnections,
		UDPSessions: r.UdpSessions,
		UpdatedAt:   parseTime(r.UpdatedAt),
	}
	for _, s := range r.Samples {
		snap.UpSeries = append(snap.UpSeries, s.UploadRate)
		snap.DownSeries = append(snap.DownSeries, s.DownloadRate)
	}
	return snap, nil
}

func (d *Driver) ListSubscriptions(ctx context.Context) ([]driver.Subscription, error) {
	var out struct {
		Subscriptions []rawSubscription `json:"subscriptions"`
	}
	if err := d.client.Do(ctx, qSubscriptions, nil, &out); err != nil {
		return nil, err
	}
	subs := make([]driver.Subscription, 0, len(out.Subscriptions))
	for _, s := range out.Subscriptions {
		subs = append(subs, driver.Subscription{
			ID: s.ID, Tag: s.Tag, Link: s.Link, Status: s.Status, Info: s.Info,
			NodeCount: s.Nodes.TotalCount, CronExp: s.CronExp, CronEnable: s.CronEnable,
			UpdatedAt: parseTime(s.UpdatedAt),
		})
	}
	return subs, nil
}

func (d *Driver) AddSubscription(ctx context.Context, link, tag string) error {
	arg := map[string]any{"link": link}
	if tag != "" {
		arg["tag"] = tag
	}
	vars := map[string]any{"rollbackError": true, "arg": arg}
	return d.client.Do(ctx, mImportSubscription, vars, nil)
}

func (d *Driver) UpdateSubscription(ctx context.Context, id string) error {
	return d.client.Do(ctx, mUpdateSubscription, map[string]any{"id": id}, nil)
}

func (d *Driver) RemoveSubscriptions(ctx context.Context, ids []string) error {
	return d.client.Do(ctx, mRemoveSubscriptions, map[string]any{"ids": ids}, nil)
}

func (d *Driver) UpdateSubscriptionCron(ctx context.Context, id, cronExp string, enable bool) error {
	return d.client.Do(ctx, mUpdateSubscriptionCron,
		map[string]any{"id": id, "cronExp": cronExp, "cronEnable": enable}, nil)
}

func (d *Driver) ListSelections(ctx context.Context) (driver.Selections, error) {
	var out struct {
		Configs  []rawConfig      `json:"configs"`
		Dnss     []rawDnsItem     `json:"dnss"`
		Routings []rawRoutingItem `json:"routings"`
	}
	if err := d.client.Do(ctx, qSelections, nil, &out); err != nil {
		return driver.Selections{}, err
	}
	sel := driver.Selections{}
	for _, c := range out.Configs {
		g := c.Global
		fields := []driver.ConfigField{
			{Name: "logLevel", Label: "日志级别", Value: g.LogLevel, Type: "string"},
			{Name: "lanInterface", Label: "LAN 接口", Value: strings.Join(g.LanInterface, ","), Type: "array"},
			{Name: "wanInterface", Label: "WAN 接口", Value: strings.Join(g.WanInterface, ","), Type: "array"},
			{Name: "dialMode", Label: "拨号模式", Value: g.DialMode, Type: "string"},
			{Name: "checkInterval", Label: "检查间隔", Value: g.CheckInterval, Type: "duration"},
			{Name: "checkTolerance", Label: "检查容差", Value: g.CheckTolerance, Type: "duration"},
			{Name: "tcpCheckUrl", Label: "TCP 检查 URL", Value: strings.Join(g.TcpCheckUrl, ","), Type: "array"},
			{Name: "tcpCheckHttpMethod", Label: "TCP 检查方法", Value: g.TcpCheckHttpMethod, Type: "string"},
			{Name: "udpCheckDns", Label: "UDP 检查 DNS", Value: strings.Join(g.UdpCheckDns, ","), Type: "array"},
			{Name: "sniffingTimeout", Label: "探测超时", Value: g.SniffingTimeout, Type: "duration"},
			{Name: "allowInsecure", Label: "允许不安全 TLS", Value: strconv.FormatBool(g.AllowInsecure), Type: "bool"},
			{Name: "mptcp", Label: "MPTCP", Value: strconv.FormatBool(g.Mptcp), Type: "bool"},
			{Name: "autoConfigKernelParameter", Label: "自动内核参数", Value: strconv.FormatBool(g.AutoConfigKernelParam), Type: "bool"},
			{Name: "autoConfigFirewallRule", Label: "自动防火墙规则", Value: strconv.FormatBool(g.AutoConfigFirewallRule), Type: "bool"},
			{Name: "pprofPort", Label: "pprof 端口", Value: strconv.Itoa(g.PprofPort), Type: "int"},
		}
		var b strings.Builder
		for _, f := range fields {
			line := f.Label + ": "
			for len([]rune(line)) < 20 {
				line += " "
			}
			b.WriteString(line + f.Value + "\n")
		}
		detail := fmt.Sprintf("log=%s lan=%v wan=%v", g.LogLevel, g.LanInterface, g.WanInterface)
		sel.Configs = append(sel.Configs, driver.ConfigItem{ID: c.ID, Name: c.Name, Selected: c.Selected,
			Detail: detail, Body: strings.TrimRight(b.String(), "\n"), Fields: fields})
	}
	for _, x := range out.Dnss {
		sel.Dns = append(sel.Dns, driver.ConfigItem{ID: x.ID, Name: x.Name, Selected: x.Selected, Body: x.Dns.String})
	}
	for _, x := range out.Routings {
		sel.Routings = append(sel.Routings, driver.ConfigItem{ID: x.ID, Name: x.Name, Selected: x.Selected, Body: x.Routing.String})
	}
	return sel, nil
}

func (d *Driver) SelectConfig(ctx context.Context, id string) error {
	return d.client.Do(ctx, mSelectConfig, map[string]any{"id": id}, nil)
}

func (d *Driver) SelectDns(ctx context.Context, id string) error {
	return d.client.Do(ctx, mSelectDns, map[string]any{"id": id}, nil)
}

func (d *Driver) SelectRouting(ctx context.Context, id string) error {
	return d.client.Do(ctx, mSelectRouting, map[string]any{"id": id}, nil)
}

// defaultDnsTemplate / defaultRoutingTemplate are minimal valid DSL bodies
// used when creating new profiles (matching the daed web UI's templates).
const (
	defaultDnsTemplate = `upstream {
	alidns: 'udp://223.5.5.5:53'
}
routing {
	request {
		fallback: alidns
	}
}`
	defaultRoutingTemplate = `fallback: direct`
)

func (d *Driver) CreateProfile(ctx context.Context, section, name string, src *driver.ConfigItem) error {
	switch section {
	case "config":
		if err := d.client.Do(ctx, mCreateConfig, map[string]any{"name": name}, nil); err != nil {
			return err
		}
		if src == nil || len(src.Fields) == 0 {
			return nil
		}
		// Clone: fetch the new id, then copy all fields in one update.
		sel, err := d.ListSelections(ctx)
		if err != nil {
			return err
		}
		var id string
		for _, c := range sel.Configs {
			if c.Name == name && !c.Selected {
				id = c.ID
			}
		}
		if id == "" {
			return fmt.Errorf("新建后未找到配置 %q", name)
		}
		return d.UpdateConfigFields(ctx, id, src.Fields)
	case "dns":
		content := defaultDnsTemplate
		if src != nil && src.Body != "" {
			content = src.Body
		}
		return d.client.Do(ctx, mCreateDns, map[string]any{"name": name, "dns": content}, nil)
	case "routing":
		content := defaultRoutingTemplate
		if src != nil && src.Body != "" {
			content = src.Body
		}
		return d.client.Do(ctx, mCreateRouting, map[string]any{"name": name, "routing": content}, nil)
	}
	return fmt.Errorf("unknown section %q", section)
}

func (d *Driver) RenameProfile(ctx context.Context, section, id, name string) error {
	q := map[string]any{"id": id, "name": name}
	switch section {
	case "config":
		return d.client.Do(ctx, mRenameConfig, q, nil)
	case "dns":
		return d.client.Do(ctx, mRenameDns, q, nil)
	case "routing":
		return d.client.Do(ctx, mRenameRouting, q, nil)
	}
	return fmt.Errorf("unknown section %q", section)
}

func (d *Driver) RemoveProfile(ctx context.Context, section, id string) error {
	q := map[string]any{"id": id}
	switch section {
	case "config":
		return d.client.Do(ctx, mRemoveConfig, q, nil)
	case "dns":
		return d.client.Do(ctx, mRemoveDns, q, nil)
	case "routing":
		return d.client.Do(ctx, mRemoveRouting, q, nil)
	}
	return fmt.Errorf("unknown section %q", section)
}

func (d *Driver) UpdateDnsText(ctx context.Context, id, text string) error {
	return d.client.Do(ctx, mUpdateDnsText, map[string]any{"id": id, "dns": text}, nil)
}

func (d *Driver) UpdateRoutingText(ctx context.Context, id, text string) error {
	return d.client.Do(ctx, mUpdateRoutingText, map[string]any{"id": id, "routing": text}, nil)
}

// typedValue converts a text value per field type for globalInput.
func typedValue(f driver.ConfigField, value string) (any, error) {
	switch f.Type {
	case "int":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("%s 需要整数: %w", f.Label, err)
		}
		return n, nil
	case "bool":
		b, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("%s 需要 true/false: %w", f.Label, err)
		}
		return b, nil
	case "array":
		parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' })
		if len(parts) == 0 {
			return nil, fmt.Errorf("%s 需要至少一个值", f.Label)
		}
		return parts, nil
	default: // string / duration pass through
		return strings.TrimSpace(value), nil
	}
}

// UpdateConfigField converts the text value per field type and sends a
// partial globalInput update.
func (d *Driver) UpdateConfigField(ctx context.Context, id string, f driver.ConfigField, value string) error {
	typed, err := typedValue(f, value)
	if err != nil {
		return err
	}
	return d.client.Do(ctx, mUpdateConfigGlobal,
		map[string]any{"id": id, "global": map[string]any{f.Name: typed}}, nil)
}

// UpdateConfigFields sends several converted field values in one request.
func (d *Driver) UpdateConfigFields(ctx context.Context, id string, fields []driver.ConfigField) error {
	global := map[string]any{}
	for _, f := range fields {
		typed, err := typedValue(f, f.Value)
		if err != nil {
			return err
		}
		global[f.Name] = typed
	}
	return d.client.Do(ctx, mUpdateConfigGlobal, map[string]any{"id": id, "global": global}, nil)
}

func (d *Driver) Run(ctx context.Context, dry bool) error {
	return d.client.Do(ctx, mRun, map[string]any{"dry": dry}, nil)
}

// --- helpers ---

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// parseTime tolerates zero times and odd encodings; on failure it returns
// the zero time.
func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999 -0700 MST"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
