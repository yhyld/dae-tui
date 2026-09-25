// Package daed implements the driver.Driver interface on top of the daed
// GraphQL API (daed v2.1.1, the final release of the archived project; the
// schema is frozen, see schema.graphql in this package).
package daed

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
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
	// uses the minimal query without subscription info. It is atomic
	// because tea.Batch runs commands concurrently, so several ListGroups
	// calls may be in flight at once.
	groupsFallback atomic.Bool
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
	if !d.groupsFallback.Load() {
		err := d.client.Do(ctx, qGroupsRich, nil, &out)
		if err == nil {
			return mapGroups(out.Groups), nil
		}
		if !strings.Contains(err.Error(), "Cannot query field") {
			return nil, err
		}
		d.groupsFallback.Store(true)
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
		Address: n.Address, Link: n.Link, SubscriptionID: n.SubscriptionID,
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
	_, err := d.ImportNodes(ctx, []string{link}, tag)
	return err
}

// ImportNodes parses a batch of share links. rollbackError is deliberately
// false: a batch is usually a paste of many links, and one malformed entry
// must not discard the good ones — the per-link results say which failed.
func (d *Driver) ImportNodes(ctx context.Context, links []string, tag string) ([]driver.NodeImportResult, error) {
	if len(links) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(links))
	for _, link := range links {
		arg := map[string]any{"link": link}
		if tag != "" {
			arg["tag"] = tag
		}
		args = append(args, arg)
	}
	var out struct {
		ImportNodes []rawNodeImportResult `json:"importNodes"`
	}
	if err := d.client.Do(ctx, mImportNodes,
		map[string]any{"rollbackError": false, "args": args}, &out); err != nil {
		return nil, err
	}
	results := make([]driver.NodeImportResult, 0, len(out.ImportNodes))
	for _, r := range out.ImportNodes {
		res := driver.NodeImportResult{Link: r.Link}
		if r.Error != nil {
			// daed echoes raw parser bytes for malformed links; they are
			// unprintable in a terminal, so keep only printable runes.
			res.Error = printable(*r.Error)
		}
		if r.Node != nil {
			n := mapNode(*r.Node)
			res.Node = &n
		}
		results = append(results, res)
	}
	return results, nil
}

func (d *Driver) RemoveNodes(ctx context.Context, nodeIDs []string) error {
	return d.client.Do(ctx, mRemoveNodes, map[string]any{"ids": nodeIDs}, nil)
}

// TagNode / UpdateNode edit a manual node in place. Keeping the node ID is
// the whole point: group memberships reference nodes by ID, so the
// remove-and-re-import workaround silently detached the node from its
// groups (the new node carries a fresh ID).
func (d *Driver) TagNode(ctx context.Context, id, tag string) error {
	return d.client.Do(ctx, mTagNode, map[string]any{"id": id, "tag": tag}, nil)
}

func (d *Driver) UpdateNode(ctx context.Context, id, newLink string) error {
	return d.client.Do(ctx, mUpdateNode, map[string]any{"id": id, "newLink": newLink}, nil)
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

// TagSubscription / UpdateSubscriptionLink edit a subscription in place.
// Groups attach to subscriptions by ID, so editing (instead of removing and
// re-adding) is what keeps those attachments alive.
func (d *Driver) TagSubscription(ctx context.Context, id, tag string) error {
	return d.client.Do(ctx, mTagSubscription, map[string]any{"id": id, "tag": tag}, nil)
}

func (d *Driver) UpdateSubscriptionLink(ctx context.Context, id, link string) error {
	return d.client.Do(ctx, mUpdateSubscriptionLink, map[string]any{"id": id, "link": link}, nil)
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
	descs := d.configFieldDescs(ctx)
	sel := driver.Selections{}
	for _, c := range out.Configs {
		fields := buildConfigFields(descs, c.Global)
		detail := fmt.Sprintf("log=%v lan=%v wan=%v",
			c.Global["logLevel"], c.Global["lanInterface"], c.Global["wanInterface"])
		sel.Configs = append(sel.Configs, driver.ConfigItem{ID: c.ID, Name: c.Name, Selected: c.Selected,
			Detail: detail, Fields: fields})
	}
	for _, x := range out.Dnss {
		sel.Dns = append(sel.Dns, driver.ConfigItem{ID: x.ID, Name: x.Name, Selected: x.Selected,
			Body: x.Dns.String, Summary: upstreamSummary(x.Dns.Upstream)})
	}
	for _, x := range out.Routings {
		sel.Routings = append(sel.Routings, driver.ConfigItem{ID: x.ID, Name: x.Name, Selected: x.Selected,
			Body: x.Routing.String, Summary: routingSummary(x.Routing), References: x.ReferenceGroups})
	}
	return sel, nil
}

// configFieldDescs fetches the flat config field metadata. It is advisory:
// any failure degrades to no metadata, and the field list is then derived
// from the global response itself (older daed builds lack the query).
func (d *Driver) configFieldDescs(ctx context.Context) []rawFlatDesc {
	var out struct {
		ConfigFlatDesc []rawFlatDesc `json:"configFlatDesc"`
	}
	if err := d.client.Do(ctx, qConfigFlatDesc, nil, &out); err != nil {
		return nil
	}
	return out.ConfigFlatDesc
}

// globalInputKey converts a configFlatDesc mapping ("global.tproxy_port")
// into the GraphQL globalInput key ("tproxyPort"). Only the global section
// maps to globalInput; group/routing/dns entries are not editable here.
func globalInputKey(mapping string) (string, bool) {
	rest, ok := strings.CutPrefix(mapping, "global.")
	if !ok || rest == "" || strings.Contains(rest, ".") {
		return "", false
	}
	var b strings.Builder
	for _, part := range strings.Split(rest, "_") {
		if part == "" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString(part)
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	if b.Len() == 0 {
		return "", false
	}
	return b.String(), true
}

// fieldType maps a configFlatDesc Go type onto the edit type taxonomy of
// driver.ConfigField.Type.
func fieldType(f rawFlatDesc) string {
	switch {
	case f.IsArray:
		return "array"
	case f.Type == "bool":
		return "bool"
	case f.Type == "time.Duration":
		return "duration"
	case f.Type == "int", f.Type == "int64",
		f.Type == "uint16", f.Type == "uint32", f.Type == "uint64":
		return "int"
	default:
		return "string"
	}
}

// inferType guesses an edit type from a raw JSON value, for keys the flat
// description did not cover.
func inferType(v any) string {
	switch v.(type) {
	case bool:
		return "bool"
	case float64:
		return "int"
	case []any:
		return "array"
	default:
		return "string"
	}
}

// formatValue renders a global field value as editable text.
func formatValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, formatValue(e))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(t)
	}
}

// buildConfigFields joins the flat field metadata with the current values of
// one config's global object. The response map is authoritative for which
// keys exist: a metadata entry the backend did not echo back is skipped, and
// keys without metadata are still offered with an inferred type, sorted for
// a stable order.
func buildConfigFields(descs []rawFlatDesc, global map[string]any) []driver.ConfigField {
	var fields []driver.ConfigField
	seen := map[string]bool{}
	for _, f := range descs {
		key, ok := globalInputKey(f.Mapping)
		if !ok || seen[key] {
			continue
		}
		v, ok := global[key]
		if !ok {
			continue
		}
		seen[key] = true
		fields = append(fields, driver.ConfigField{
			Name: key, Value: formatValue(v), Type: fieldType(f),
			Default: f.DefaultValue, Desc: f.Desc, Required: f.Required,
		})
	}
	leftover := make([]string, 0, len(global))
	for k := range global {
		if !seen[k] {
			leftover = append(leftover, k)
		}
	}
	sort.Strings(leftover)
	for _, k := range leftover {
		fields = append(fields, driver.ConfigField{
			Name: k, Value: formatValue(global[k]), Type: inferType(global[k]),
		})
	}
	return fields
}

// routingSummary renders the parsed rules of a routing profile as neutral
// DSL-ish lines ("domain(example.com) -> proxy").
func routingSummary(r rawDaeRouting) []string {
	var out []string
	for _, rule := range r.Rules {
		conds := make([]string, 0, len(rule.Conditions.And))
		for _, f := range rule.Conditions.And {
			conds = append(conds, renderFunction(f))
		}
		if len(conds) == 0 {
			continue
		}
		out = append(out, strings.Join(conds, " && ")+" -> "+renderFunction(rule.Outbound))
	}
	if fb := renderFunctionOrPlaintext(r.Fallback); fb != "" {
		out = append(out, "fallback: "+fb)
	}
	return out
}

func upstreamSummary(ups []rawParam) []string {
	out := make([]string, 0, len(ups))
	for _, p := range ups {
		out = append(out, p.Key+": "+p.Val)
	}
	return out
}

func renderFunction(f rawFunction) string {
	prefix := ""
	if f.Not {
		prefix = "!"
	}
	// daed parses the must_direct outbound as function "direct" carrying the
	// positional param "must"; render it back in its DSL spelling.
	if f.Name == "direct" && len(f.Params) == 1 &&
		f.Params[0].Key == "" && f.Params[0].Val == "must" {
		return prefix + "must_direct"
	}
	if len(f.Params) == 0 {
		return prefix + f.Name
	}
	parts := make([]string, 0, len(f.Params))
	for _, p := range f.Params {
		switch {
		case p.Key != "" && p.Val != "":
			parts = append(parts, p.Key+":"+p.Val)
		case p.Key != "":
			parts = append(parts, p.Key)
		default:
			parts = append(parts, p.Val)
		}
	}
	return prefix + f.Name + "(" + strings.Join(parts, ", ") + ")"
}

func renderFunctionOrPlaintext(f rawFunctionOrPlaintext) string {
	if f.Val != "" {
		return f.Val
	}
	return renderFunction(rawFunction{Name: f.Name, Not: f.Not, Params: f.Params})
}

// ValidateRouting / ValidateDns parse raw DSL through the backend parser
// without storing anything. daed reports syntax problems as GraphQL errors
// carrying the line/column of the offending token.
func (d *Driver) ValidateRouting(ctx context.Context, raw string) error {
	var out struct {
		ParsedRouting map[string]any `json:"parsedRouting"`
	}
	return d.client.Do(ctx, qParsedRouting, map[string]any{"raw": raw}, &out)
}

func (d *Driver) ValidateDns(ctx context.Context, raw string) error {
	var out struct {
		ParsedDns map[string]any `json:"parsedDns"`
	}
	return d.client.Do(ctx, qParsedDns, map[string]any{"raw": raw}, &out)
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
		// A blank value is a valid empty list (e.g. lanInterface unset), not
		// an error: cloning a config round-trips every field, unset ones
		// included.
		parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' })
		if parts == nil {
			parts = []string{}
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

// Interfaces lists the NICs daed sees. A backend without the field (or one
// that refuses the query) yields an empty list: interface data is advisory,
// used for hints and the home page's network state.
func (d *Driver) Interfaces(ctx context.Context) ([]driver.NetworkInterface, error) {
	var out struct {
		General rawGeneralWithInterfaces `json:"general"`
	}
	if err := d.client.Do(ctx, qInterfaces, nil, &out); err != nil {
		if strings.Contains(err.Error(), "Cannot query field") {
			return nil, nil
		}
		return nil, err
	}
	ifaces := make([]driver.NetworkInterface, 0, len(out.General.Interfaces))
	for _, i := range out.General.Interfaces {
		iface := driver.NetworkInterface{Name: i.Name, Up: i.Flag.Up, IPs: stripPrefixLen(i.IP)}
		for _, dr := range i.Flag.Default {
			iface.Default = true
			if iface.Gateway == "" {
				iface.Gateway = dr.Gateway
			}
		}
		ifaces = append(ifaces, iface)
	}
	return ifaces, nil
}

// stripPrefixLen turns "192.168.5.43/24" into "192.168.5.43": the prefix
// length is routing trivia on a status line.
func stripPrefixLen(ips []string) []string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if i := strings.IndexByte(ip, '/'); i >= 0 {
			ip = ip[:i]
		}
		out = append(out, ip)
	}
	return out
}

// printable drops control and other non-printable runes (daed's parser
// errors embed raw bytes of the offending link).
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// UpdatePassword changes the signed-in account's password. daed answers with
// a fresh token and invalidates the old one, so the new token and the new
// password (what silent re-auth replays) must both be persisted.
func (d *Driver) UpdatePassword(ctx context.Context, currentPassword, newPassword string) error {
	var out struct {
		UpdatePassword string `json:"updatePassword"`
	}
	if err := d.client.Do(ctx, mUpdatePassword,
		map[string]any{"currentPassword": currentPassword, "newPassword": newPassword}, &out); err != nil {
		return err
	}
	d.opts.Password = newPassword
	d.client.SetToken(out.UpdatePassword)
	if d.opts.SaveToken != nil {
		d.opts.SaveToken(out.UpdatePassword)
	}
	if d.credHook != nil {
		d.credHook(d.opts.Username, newPassword)
	}
	return nil
}

// Logout forgets the local session. daed has no logout mutation — the JWT
// stays valid until it expires — so all this can do is stop replaying the
// stored credentials.
func (d *Driver) Logout(ctx context.Context) error {
	d.opts.Username, d.opts.Password, d.opts.Token = "", "", ""
	d.client.SetToken("")
	return nil
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
