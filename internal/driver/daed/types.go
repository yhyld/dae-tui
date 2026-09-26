package daed

import "encoding/json"

// gqlResponse is the envelope of every daed GraphQL reply. Errors come back
// inside an HTTP 200; authentication failures are GraphQL errors with message
// "access denied" rather than an HTTP 401.
type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors"`
}

type gqlError struct {
	Message string `json:"message"`
}

func (r gqlResponse) errorString() string {
	s := ""
	for i, e := range r.Errors {
		if i > 0 {
			s += "; "
		}
		s += e.Message
	}
	return s
}

// --- raw response types (field names mirror internal/driver/daed/schema.graphql) ---

type rawDae struct {
	Running  bool   `json:"running"`
	Modified bool   `json:"modified"`
	Version  string `json:"version"`
}

type rawGeneral struct {
	Dae rawDae `json:"dae"`
}

type rawParam struct {
	Key string `json:"key"`
	Val string `json:"val"`
}

type rawNode struct {
	ID             string `json:"id"`
	Link           string `json:"link"`
	Name           string `json:"name"`
	Address        string `json:"address"`
	Protocol       string `json:"protocol"`
	Tag            string `json:"tag"`
	SubscriptionID string `json:"subscriptionID"`
}

type rawGroupSubscription struct {
	NameFilterRegex string    `json:"nameFilterRegex"`
	MatchedCount    int       `json:"matchedCount"`
	Subscription    rawSubRef `json:"subscription"`
	MatchedNodes    []rawNode `json:"matchedNodes"`
}

type rawSubRef struct {
	ID  string `json:"id"`
	Tag string `json:"tag"`
}

type rawGroup struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	Policy        string                 `json:"policy"`
	PolicyParams  []rawParam             `json:"policyParams"`
	Nodes         []rawNode              `json:"nodes"`
	Subscriptions []rawGroupSubscription `json:"subscriptions"`
}

type rawNodeLatency struct {
	ID        string  `json:"id"`
	LatencyMs *int    `json:"latencyMs"`
	Alive     bool    `json:"alive"`
	TestedAt  string  `json:"testedAt"`
	Message   *string `json:"message"`
}

type rawSample struct {
	Timestamp    string  `json:"timestamp"`
	UploadRate   float64 `json:"uploadRate"`
	DownloadRate float64 `json:"downloadRate"`
}

type rawRuntimeOverview struct {
	UpdatedAt         string      `json:"updatedAt"`
	UploadRate        float64     `json:"uploadRate"`
	DownloadRate      float64     `json:"downloadRate"`
	UploadTotal       string      `json:"uploadTotal"`   // String in SDL
	DownloadTotal     string      `json:"downloadTotal"` // String in SDL
	ActiveConnections int         `json:"activeConnections"`
	UdpSessions       int         `json:"udpSessions"`
	Samples           []rawSample `json:"samples"`
}

type rawGeneralTraffic struct {
	RuntimeOverview rawRuntimeOverview `json:"runtimeOverview"`
}

type rawPageInfo struct {
	StartCursor string `json:"startCursor"`
	EndCursor   string `json:"endCursor"`
	HasNextPage bool   `json:"hasNextPage"`
}

type rawNodesConn struct {
	TotalCount int         `json:"totalCount"`
	Edges      []rawNode   `json:"edges"`
	PageInfo   rawPageInfo `json:"pageInfo"`
}

type rawNodeImportResult struct {
	Link  string   `json:"link"`
	Error *string  `json:"error"`
	Node  *rawNode `json:"node"`
}

type rawDefaultRoute struct {
	IPVersion string `json:"ipVersion"`
	Gateway   string `json:"gateway"`
	Source    string `json:"source"`
}

type rawInterfaceFlag struct {
	Up      bool              `json:"up"`
	Default []rawDefaultRoute `json:"default"`
}

type rawInterface struct {
	Name    string           `json:"name"`
	Ifindex int              `json:"ifindex"`
	IP      []string         `json:"ip"`
	Flag    rawInterfaceFlag `json:"flag"`
}

type rawGeneralWithInterfaces struct {
	Interfaces []rawInterface `json:"interfaces"`
}

type rawSubscription struct {
	ID         string       `json:"id"`
	UpdatedAt  string       `json:"updatedAt"`
	Tag        string       `json:"tag"`
	Link       string       `json:"link"`
	Status     string       `json:"status"`
	Info       string       `json:"info"`
	CronExp    string       `json:"cronExp"`
	CronEnable bool         `json:"cronEnable"`
	Nodes      rawNodesConn `json:"nodes"`
}

// rawSelections bundles the three profile lists fetched by qSelections /
// qSelectionsLegacy in a single document.
type rawSelections struct {
	Configs  []rawConfig      `json:"configs"`
	Dnss     []rawDnsItem     `json:"dnss"`
	Routings []rawRoutingItem `json:"routings"`
}

// rawConfig keeps global untyped: the editable field set is discovered from
// qConfigFlatDesc, so the driver must not hard-code which keys exist.
type rawConfig struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Selected bool           `json:"selected"`
	Global   map[string]any `json:"global"`
}

type rawFlatDesc struct {
	Name         string `json:"name"`
	Mapping      string `json:"mapping"`
	IsArray      bool   `json:"isArray"`
	DefaultValue string `json:"defaultValue"`
	Required     bool   `json:"required"`
	Type         string `json:"type"`
	Desc         string `json:"desc"`
}

type rawFunction struct {
	Name   string     `json:"name"`
	Not    bool       `json:"not"`
	Params []rawParam `json:"params"`
}

type rawAndFunctions struct {
	And []rawFunction `json:"and"`
}

type rawRoutingRule struct {
	Conditions rawAndFunctions `json:"conditions"`
	Outbound   rawFunction     `json:"outbound"`
}

// rawFunctionOrPlaintext decodes the FunctionOrPlaintext union: a Plaintext
// carries only val, a Function only name/params.
type rawFunctionOrPlaintext struct {
	Name   string     `json:"name"`
	Val    string     `json:"val"`
	Not    bool       `json:"not"`
	Params []rawParam `json:"params"`
}

type rawDaeRouting struct {
	String   string                 `json:"string"`
	Rules    []rawRoutingRule       `json:"rules"`
	Fallback rawFunctionOrPlaintext `json:"fallback"`
}

type rawDaeDns struct {
	String   string     `json:"string"`
	Upstream []rawParam `json:"upstream"`
}

type rawDnsItem struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Selected bool      `json:"selected"`
	Dns      rawDaeDns `json:"dns"`
}

type rawRoutingItem struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Selected        bool          `json:"selected"`
	ReferenceGroups []string      `json:"referenceGroups"`
	Routing         rawDaeRouting `json:"routing"`
}
