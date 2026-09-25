// Package driver defines the backend abstraction for dae-tui. A Driver talks
// to some proxy control plane (daed GraphQL API in Phase 1; a bare dae
// config-file driver is a future Phase 2) and exposes a normalized set of
// operations and domain types to the UI layer.
package driver

import (
	"context"
	"errors"
	"time"
)

// ErrNeedAuth indicates the backend rejected our credentials (or none were
// provided); the UI should show the login/setup form.
var ErrNeedAuth = errors.New("need authentication")

// Caps reports which operations a driver supports so the UI can degrade
// gracefully on less capable backends.
type Caps struct {
	SwitchNode    bool
	TestLatency   bool
	TrafficStats  bool
	Subscriptions bool
	ConfigMgmt    bool
}

// Status is a snapshot of backend health and proxy runtime state.
type Status struct {
	Version   string
	Running   bool
	Modified  bool // running config differs from stored selection
	UpdatedAt time.Time
}

// Node is a proxy node (server) known to the backend.
type Node struct {
	ID             string
	Name           string
	Tag            string
	Protocol       string
	Address        string
	SubscriptionID string // empty for manually imported nodes
}

// Group is a routing group: a set of candidate nodes plus the selection
// policy that decides which node carries traffic.
type Group struct {
	ID            string
	Name          string
	Policy        string // random | fixed | min_avg10 | min_moving_avg | min
	PolicyParams  []Param
	Nodes         []Node
	Subscriptions []GroupSubscription
}

// Param is a policy parameter as a raw key/value pair (daed passes these
// through verbatim; for policy "fixed" the single param's Val is the node
// index inside Group.Nodes as a decimal string).
type Param struct {
	Key string
	Val string
}

// GroupSubscription describes a subscription attached to a group, with the
// nodes it contributes to the group (after nameFilterRegex filtering).
type GroupSubscription struct {
	SubscriptionID  string
	Tag             string
	MatchedCount    int
	NameFilterRegex string
	Nodes           []Node
}

// FixedIndex returns the node index selected by a "fixed" policy, or -1.
func (g Group) FixedIndex() int {
	if g.Policy != "fixed" {
		return -1
	}
	for _, p := range g.PolicyParams {
		n := 0
		for _, c := range p.Val {
			if c < '0' || c > '9' {
				return -1
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	return -1
}

// Members returns the full member list of the group: nodes contributed by
// attached subscriptions (after name filtering) plus directly-attached
// nodes (Group.Nodes alone holds only the latter in daed v2).
func (g Group) Members() []Node {
	seen := map[string]bool{}
	var out []Node
	for _, sub := range g.Subscriptions {
		for _, n := range sub.Nodes {
			if !seen[n.ID] {
				seen[n.ID] = true
				out = append(out, n)
			}
		}
	}
	for _, n := range g.Nodes {
		if !seen[n.ID] {
			seen[n.ID] = true
			out = append(out, n)
		}
	}
	return out
}

// SelectedNode returns the node currently pinned by a fixed policy.
func (g Group) SelectedNode() *Node {
	i := g.FixedIndex()
	if i < 0 || i >= len(g.Nodes) {
		return nil
	}
	return &g.Nodes[i]
}

// Latency is the latest latency probe result for one node.
type Latency struct {
	NodeID   string
	Ms       int
	Alive    bool
	Message  string
	TestedAt time.Time
}

// TrafficSnapshot carries runtime traffic counters and a rate time series.
type TrafficSnapshot struct {
	UpRate      float64 // bytes/sec
	DownRate    float64 // bytes/sec
	UpTotal     int64
	DownTotal   int64
	Conns       int
	UDPSessions int
	UpSeries    []float64
	DownSeries  []float64
	UpdatedAt   time.Time
}

// Subscription is a subscription source fetched periodically by the backend.
type Subscription struct {
	ID         string
	Tag        string
	Link       string
	Status     string
	Info       string
	NodeCount  int
	CronExp    string
	CronEnable bool
	UpdatedAt  time.Time
}

// ConfigItem is one stored config/dns/routing profile.
type ConfigItem struct {
	ID       string
	Name     string
	Selected bool
	Detail   string        // one-line summary
	Body     string        // full DSL text for detail panes (dns/routing)
	Fields   []ConfigField // editable global fields (config section only)
	Summary  []string      // neutral structured overview lines (routing rules / dns upstreams)
}

// ConfigField describes one editable global config field.
type ConfigField struct {
	Name     string // globalInput key
	Label    string // display label; backends may leave it empty for the UI to resolve
	Value    string // current value as text
	Type     string // string | int | bool | duration | array
	Default  string // backend default as text (empty when unknown)
	Desc     string // backend documentation (empty when unknown)
	Required bool
}

// Selections lists stored config profiles per section with the selected one.
type Selections struct {
	Configs  []ConfigItem
	Dns      []ConfigItem
	Routings []ConfigItem
}

// Policy describes a group selection policy to apply.
type Policy struct {
	Name       string // random | fixed | min_avg10 | min_moving_avg | min
	FixedIndex int    // used when Name == "fixed"
	// FixedIndex is retained for API completeness: daed supports fixed
	// groups, but the TUI no longer creates them (a v2 fixed group may hold
	// exactly one member, so pinning silently rebuilt the whole group).
	// Existing fixed groups are still displayed read-only.
}

// RoutingPreset is a ready-made routing template a backend can render as
// DSL. IDs are backend-stable; the UI owns the labels and descriptions.
type RoutingPreset struct {
	ID    string // e.g. "gfw"
	Group bool   // the template sends traffic through a proxy group
}

// Driver is the backend abstraction consumed by the UI.
type Driver interface {
	Name() string
	Capabilities() Caps

	// Connect verifies connectivity and returns status. It may return
	// ErrNeedAuth if no valid credentials are available.
	Connect(ctx context.Context) (Status, error)
	// NumberUsers reports how many accounts exist on the backend (0 means
	// first-run setup is required).
	NumberUsers(ctx context.Context) (int, error)
	// Login authenticates with username/password and stores credentials.
	Login(ctx context.Context, username, password string) (Status, error)
	// CreateUser creates the first account (only valid when NumberUsers==0).
	CreateUser(ctx context.Context, username, password string) (Status, error)

	ListGroups(ctx context.Context) ([]Group, error)
	SetGroupPolicy(ctx context.Context, groupID string, p Policy) error
	// SubscriptionNodes lists all nodes of a subscription (the driver
	// handles server-side pagination).
	SubscriptionNodes(ctx context.Context, subscriptionID string) ([]Node, error)

	// Group lifecycle management.
	CreateGroup(ctx context.Context, name string, policy string) error
	RemoveGroup(ctx context.Context, groupID string) error
	RenameGroup(ctx context.Context, groupID, name string) error

	// Group membership management (mirrors the daed web UI's group editor).
	AddGroupSubscriptions(ctx context.Context, groupID string, subIDs []string, nameFilterRegex string) error
	RemoveGroupSubscriptions(ctx context.Context, groupID string, subIDs []string) error
	AddGroupNodes(ctx context.Context, groupID string, nodeIDs []string) error
	RemoveGroupNodes(ctx context.Context, groupID string, nodeIDs []string) error
	// ListManualNodes returns subscription-less (manually imported) nodes,
	// which are the candidates for AddGroupNodes.
	ListManualNodes(ctx context.Context) ([]Node, error)

	// Manual node management.
	ImportNode(ctx context.Context, link, tag string) error
	RemoveNodes(ctx context.Context, nodeIDs []string) error

	TestLatency(ctx context.Context, nodeIDs []string) error
	Latencies(ctx context.Context, nodeIDs []string) ([]Latency, error)

	Traffic(ctx context.Context, windowSec, maxPoints int) (TrafficSnapshot, error)

	ListSubscriptions(ctx context.Context) ([]Subscription, error)
	AddSubscription(ctx context.Context, link, tag string) error
	UpdateSubscription(ctx context.Context, id string) error
	RemoveSubscriptions(ctx context.Context, ids []string) error
	// UpdateSubscriptionCron changes the auto-update schedule.
	UpdateSubscriptionCron(ctx context.Context, id, cronExp string, enable bool) error

	ListSelections(ctx context.Context) (Selections, error)
	SelectConfig(ctx context.Context, id string) error
	SelectDns(ctx context.Context, id string) error
	SelectRouting(ctx context.Context, id string) error

	// Profile management (section: "config" | "dns" | "routing").
	// CreateProfile creates a new profile; when src is non-nil the new
	// profile is a clone of src (fields / DSL content) instead of the
	// built-in template.
	CreateProfile(ctx context.Context, section, name string, src *ConfigItem) error
	RenameProfile(ctx context.Context, section, id, name string) error
	RemoveProfile(ctx context.Context, section, id string) error
	// UpdateDnsText / UpdateRoutingText replace the raw DSL content.
	UpdateDnsText(ctx context.Context, id, text string) error
	UpdateRoutingText(ctx context.Context, id, text string) error
	// RoutingPresets lists the ready-made routing templates; BuildRouting-
	// Preset renders one. DetectRoutingPreset reports which preset a stored
	// routing DSL corresponds to ("" when it is custom), so the UI can show
	// the active mode.
	RoutingPresets() []RoutingPreset
	BuildRoutingPreset(id, proxyGroup string) (string, error)
	DetectRoutingPreset(raw string) string
	// ValidateDns / ValidateRouting parse raw DSL text through the backend's
	// parser without storing it, so an edited profile can be rejected before
	// it replaces a working one.
	ValidateDns(ctx context.Context, raw string) error
	ValidateRouting(ctx context.Context, raw string) error
	// UpdateConfigField applies a partial globalInput update for one field.
	UpdateConfigField(ctx context.Context, id string, field ConfigField, value string) error
	// UpdateConfigFields applies a partial globalInput update for several
	// fields at once (used to clone configs).
	UpdateConfigFields(ctx context.Context, id string, fields []ConfigField) error
	// Run applies the selected config+dns+routing; dry validates only.
	Run(ctx context.Context, dry bool) error
}
