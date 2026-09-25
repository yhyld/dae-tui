package daed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"dae-tui/internal/driver"
)

// mockGraphQL dispatches on the operation name embedded in the query text.
type mockGraphQL struct {
	mu       sync.Mutex
	handler  func(op string, vars map[string]any, auth string) (any, []gqlError)
	requests []mockReq
}

type mockReq struct {
	op    string
	query string
	vars  map[string]any
	auth  string
}

func (m *mockGraphQL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req gqlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	op := opName(req.Query)
	m.mu.Lock()
	m.requests = append(m.requests, mockReq{op: op, query: req.Query, vars: req.Variables, auth: r.Header.Get("Authorization")})
	handler := m.handler
	m.mu.Unlock()

	data, errs := handler(op, req.Variables, r.Header.Get("Authorization"))
	resp := map[string]any{}
	if errs != nil {
		resp["errors"] = errs
	} else if data != nil {
		resp["data"] = data
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (m *mockGraphQL) reqs() []mockReq {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]mockReq, len(m.requests))
	copy(out, m.requests)
	return out
}

func opName(q string) string {
	fields := strings.Fields(q)
	for i, f := range fields {
		if (f == "query" || f == "mutation") && i+1 < len(fields) {
			name := fields[i+1]
			if cut := strings.IndexAny(name, "("); cut >= 0 {
				name = name[:cut]
			}
			if name != "" && name != "{" {
				return name
			}
		}
	}
	return ""
}

// newTestDriver wires a Driver against a mock server, with credentials so
// re-auth can be exercised.
func newTestDriver(t *testing.T, handler func(op string, vars map[string]any, auth string) (any, []gqlError)) (*Driver, *mockGraphQL) {
	t.Helper()
	m := &mockGraphQL{handler: handler}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	opts := Options{Endpoint: srv.URL + "/graphql", Username: "alice", Password: "s3cret1"}
	var saved []string
	opts.SaveToken = func(tok string) { saved = append(saved, tok); _ = saved }
	return New(opts), m
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

func TestNumberUsersTokenAndBearer(t *testing.T) {
	d, m := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		switch op {
		case "HealthCheck":
			return map[string]any{"healthCheck": 1}, nil
		case "NumberUsers":
			return map[string]any{"numberUsers": 1}, nil
		case "Token":
			if vars["username"] != "alice" || vars["password"] != "s3cret1" {
				return nil, []gqlError{{Message: "bad credentials"}}
			}
			return map[string]any{"token": "jwt-1"}, nil
		case "General":
			if auth != "Bearer jwt-1" {
				return nil, []gqlError{{Message: "access denied"}}
			}
			return map[string]any{"general": map[string]any{"dae": map[string]any{
				"running": true, "modified": false, "version": "v2.1.1",
			}}}, nil
		}
		return nil, []gqlError{{Message: "unexpected op " + op}}
	})

	if n, err := d.NumberUsers(ctxT(t)); err != nil || n != 1 {
		t.Fatalf("NumberUsers = %d, %v", n, err)
	}
	st, err := d.Login(ctxT(t), "alice", "s3cret1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if st.Version != "v2.1.1" || !st.Running {
		t.Fatalf("status = %+v", st)
	}
	// The General query must have carried the bearer token.
	last := m.reqs()[len(m.reqs())-1]
	if last.auth != "Bearer jwt-1" {
		t.Fatalf("auth header = %q", last.auth)
	}
}

func TestAccessDeniedTriggersReAuthAndRetry(t *testing.T) {
	var tokens int
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		switch op {
		case "Token":
			tokens++
			return map[string]any{"token": "jwt-2"}, nil
		case "Groups":
			if auth != "Bearer jwt-2" {
				return nil, []gqlError{{Message: "access denied"}}
			}
			return map[string]any{"groups": []any{}}, nil
		}
		return nil, []gqlError{{Message: "unexpected op " + op}}
	})
	// Simulate a stale token installed at construction time.
	d.client.SetToken("jwt-expired")

	if _, err := d.ListGroups(ctxT(t)); err != nil {
		t.Fatalf("ListGroups should retry after re-auth: %v", err)
	}
	if tokens != 1 {
		t.Fatalf("re-auth ran %d times, want 1", tokens)
	}
}

func TestAccessDeniedWithoutCredentials(t *testing.T) {
	m := &mockGraphQL{handler: func(op string, vars map[string]any, auth string) (any, []gqlError) {
		return nil, []gqlError{{Message: "access denied"}}
	}}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	d := New(Options{Endpoint: srv.URL, Token: "jwt-expired"})

	_, err := d.ListGroups(ctxT(t))
	if err == nil || !strings.Contains(err.Error(), "need authentication") {
		t.Fatalf("want ErrNeedAuth, got %v", err)
	}
}

func TestListGroupsAndFixedIndex(t *testing.T) {
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "Groups" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		return map[string]any{"groups": []any{
			map[string]any{
				"id": "1", "name": "proxy", "policy": "fixed",
				"policyParams": []any{map[string]any{"key": "", "val": "2"}},
				"nodes": []any{
					map[string]any{"id": "n1", "name": "东京", "protocol": "vmess", "tag": "subA", "subscriptionID": "s1"},
					map[string]any{"id": "n2", "name": "HK-01", "protocol": "ss", "tag": "", "subscriptionID": ""},
					map[string]any{"id": "n3", "name": "SG-02", "protocol": "trojan", "tag": "subA", "subscriptionID": "s1"},
				},
				"subscriptions": []any{map[string]any{
					"nameFilterRegex": "香港",
					"matchedCount":    1,
					"subscription":    map[string]any{"id": "s1", "tag": "subA"},
					"matchedNodes":    []any{map[string]any{"id": "n3", "name": "SG-02", "protocol": "trojan", "tag": "subA", "subscriptionID": "s1"}},
				}},
			},
		}}, nil
	})

	groups, err := d.ListGroups(ctxT(t))
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d", len(groups))
	}
	g := groups[0]
	if len(g.Subscriptions) != 1 || g.Subscriptions[0].Tag != "subA" ||
		g.Subscriptions[0].NameFilterRegex != "香港" || len(g.Subscriptions[0].Nodes) != 1 {
		t.Fatalf("group subscription mapping incomplete: %+v", g.Subscriptions)
	}
	if g.FixedIndex() != 2 {
		t.Fatalf("FixedIndex = %d, want 2", g.FixedIndex())
	}
	if sel := g.SelectedNode(); sel == nil || sel.Name != "SG-02" {
		t.Fatalf("SelectedNode = %+v", sel)
	}
	if len(g.Nodes) != 3 {
		t.Fatalf("mapping incomplete: %+v", g)
	}
}

func TestStaleDaemonHint(t *testing.T) {
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		return nil, []gqlError{{Message: `Cannot query field "runtimeOverview" on type "General".`}}
	})
	_, err := d.Traffic(ctxT(t), 10, 60)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "systemctl restart daed") {
		t.Fatalf("missing stale-daemon hint: %v", err)
	}
}

func TestSubscriptionNodesPagination(t *testing.T) {
	calls := 0
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "NodesBySubscription" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		if vars["subscriptionId"] != "s1" {
			t.Errorf("subscriptionId = %v", vars["subscriptionId"])
		}
		calls++
		if calls == 1 {
			return map[string]any{"nodes": map[string]any{
				"totalCount": 3,
				"edges": []any{
					map[string]any{"id": "n1", "name": "a"},
					map[string]any{"id": "n2", "name": "b"},
				},
				"pageInfo": map[string]any{"endCursor": "n2", "hasNextPage": true},
			}}, nil
		}
		if after, _ := vars["after"].(string); after != "n2" {
			t.Errorf("second page after = %v, want n2", vars["after"])
		}
		return map[string]any{"nodes": map[string]any{
			"totalCount": 3,
			"edges":      []any{map[string]any{"id": "n3", "name": "c"}},
			"pageInfo":   map[string]any{"endCursor": "n3", "hasNextPage": false},
		}}, nil
	})

	nodes, err := d.SubscriptionNodes(ctxT(t), "s1")
	if err != nil {
		t.Fatalf("SubscriptionNodes: %v", err)
	}
	if len(nodes) != 3 || nodes[0].ID != "n1" || nodes[2].ID != "n3" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestListGroupsFallbackOnOldSchema(t *testing.T) {
	richCalls := 0
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "Groups" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		// The first Groups call is the rich query (a fresh driver tries it
		// first); reject it like an old daed build would.
		if richCalls == 0 {
			richCalls++
			return nil, []gqlError{{Message: `Cannot query field "matchedNodes" on type "GroupSubscription".`}}
		}
		return map[string]any{"groups": []any{
			map[string]any{"id": "1", "name": "proxy", "policy": "random", "nodes": []any{}},
		}}, nil
	})

	gs, err := d.ListGroups(ctxT(t))
	if err != nil {
		t.Fatalf("first ListGroups should fall back: %v", err)
	}
	if len(gs) != 1 || gs[0].Name != "proxy" {
		t.Fatalf("groups = %+v", gs)
	}
	if _, err := d.ListGroups(ctxT(t)); err != nil {
		t.Fatalf("second ListGroups: %v", err)
	}
	if richCalls != 1 {
		t.Fatalf("rich query retried after fallback: %d calls", richCalls)
	}
}

func TestListManualNodesFiltersSubscriptionNodes(t *testing.T) {
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "AllNodes" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		return map[string]any{"nodes": map[string]any{
			"edges": []any{
				map[string]any{"id": "n1", "name": "sub-node", "subscriptionID": "s1"},
				map[string]any{"id": "n2", "name": "manual-1", "subscriptionID": ""},
				map[string]any{"id": "n3", "name": "manual-2", "subscriptionID": ""},
			},
			"pageInfo": map[string]any{"endCursor": "n3", "hasNextPage": false},
		}}, nil
	})

	nodes, err := d.ListManualNodes(ctxT(t))
	if err != nil {
		t.Fatalf("ListManualNodes: %v", err)
	}
	if len(nodes) != 2 || nodes[0].ID != "n2" || nodes[1].ID != "n3" {
		t.Fatalf("manual nodes = %+v", nodes)
	}
}

func TestTestLatencyChunksLargeIDLists(t *testing.T) {
	calls := 0
	d, m := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "TestNodeLatencies" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		calls++
		ids, _ := vars["ids"].([]any)
		if len(ids) > 100 {
			t.Errorf("batch too large: %d", len(ids))
		}
		if calls == 1 {
			if len(ids) != 100 {
				t.Errorf("first batch = %d, want 100", len(ids))
			}
		} else if len(ids) != 50 {
			t.Errorf("second batch = %d, want 50", len(ids))
		}
		return map[string]any{"testNodeLatencies": []any{}}, nil
	})

	ids := make([]string, 150)
	for i := range ids {
		ids[i] = fmt.Sprint("n", i)
	}
	if err := d.TestLatency(ctxT(t), ids); err != nil {
		t.Fatalf("TestLatency: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	_ = m
}

func TestSetGroupPolicyFixedParams(t *testing.T) {
	d, m := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "GroupSetPolicy" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		return map[string]any{"groupSetPolicy": 0}, nil
	})

	if err := d.SetGroupPolicy(ctxT(t), "g1", policyFixed(3)); err != nil {
		t.Fatalf("SetGroupPolicy: %v", err)
	}
	r := m.reqs()[0]
	if r.vars["id"] != "g1" || r.vars["policy"] != "fixed" {
		t.Fatalf("vars = %+v", r.vars)
	}
	pps, ok := r.vars["policyParams"].([]any)
	if !ok || len(pps) != 1 {
		t.Fatalf("policyParams = %#v", r.vars["policyParams"])
	}
	pp, ok := pps[0].(map[string]any)
	if !ok || pp["val"] != "3" {
		t.Fatalf("val = %v, want \"3\"", pp["val"])
	}
	if _, has := pp["key"]; has {
		t.Fatalf("key should be omitted for positional param, got %#v", pp)
	}

	// Auto policy must omit policyParams entirely.
	if err := d.SetGroupPolicy(ctxT(t), "g1", policyAuto()); err != nil {
		t.Fatalf("SetGroupPolicy auto: %v", err)
	}
	if _, has := m.reqs()[1].vars["policyParams"]; has {
		t.Fatalf("auto policy should not send policyParams")
	}
}

func TestTrafficParsesStringTotals(t *testing.T) {
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "RuntimeOverview" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		if vars["windowSec"] != float64(10) || vars["maxPoints"] != float64(60) {
			t.Errorf("vars = %+v", vars)
		}
		return map[string]any{"general": map[string]any{"runtimeOverview": map[string]any{
			"updatedAt":  "2026-09-25T12:00:00Z",
			"uploadRate": 2048.5, "downloadRate": 999999.25,
			"uploadTotal": "123456789", "downloadTotal": "9876543210",
			"activeConnections": 42, "udpSessions": 7,
			"samples": []any{
				map[string]any{"timestamp": "2026-09-25T11:59:59Z", "uploadRate": 1.0, "downloadRate": 2.0},
				map[string]any{"timestamp": "2026-09-25T12:00:00Z", "uploadRate": 3.0, "downloadRate": 4.0},
			},
		}}}, nil
	})

	snap, err := d.Traffic(ctxT(t), 10, 60)
	if err != nil {
		t.Fatalf("Traffic: %v", err)
	}
	if snap.UpTotal != 123456789 || snap.DownTotal != 9876543210 {
		t.Fatalf("totals = %d/%d", snap.UpTotal, snap.DownTotal)
	}
	if snap.Conns != 42 || snap.UDPSessions != 7 {
		t.Fatalf("counters = %+v", snap)
	}
	if len(snap.UpSeries) != 2 || snap.UpSeries[1] != 3.0 || snap.DownSeries[0] != 2.0 {
		t.Fatalf("series = %+v", snap)
	}
}

func TestRunDryAndSelections(t *testing.T) {
	d, m := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		switch op {
		case "Run":
			if vars["dry"] != true {
				t.Errorf("dry = %v", vars["dry"])
			}
			return map[string]any{"run": 0}, nil
		case "Selections":
			return map[string]any{
				"configs": []any{map[string]any{"id": "c1", "name": "默认", "selected": true,
					"global": map[string]any{"logLevel": "info", "lanInterface": []any{"eth0"}, "wanInterface": []any{"wlan0"}}}},
				"dnss":     []any{map[string]any{"id": "d1", "name": "默认DNS", "selected": true}},
				"routings": []any{map[string]any{"id": "r1", "name": "默认路由", "selected": true}},
			}, nil
		}
		return nil, []gqlError{{Message: "unexpected op " + op}}
	})

	if err := d.Run(ctxT(t), true); err != nil {
		t.Fatalf("Run dry: %v", err)
	}
	sel, err := d.ListSelections(ctxT(t))
	if err != nil {
		t.Fatalf("ListSelections: %v", err)
	}
	if len(sel.Configs) != 1 || !sel.Configs[0].Selected || sel.Configs[0].Detail == "" {
		t.Fatalf("configs = %+v", sel.Configs)
	}
	if len(sel.Dns) != 1 || len(sel.Routings) != 1 {
		t.Fatalf("selections = %+v", sel)
	}
	_ = m
}

func TestSubscriptionsMapping(t *testing.T) {
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "Subscriptions" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		return map[string]any{"subscriptions": []any{map[string]any{
			"id": "s1", "tag": "机场A", "link": "https://example.com/sub",
			"status": "updated", "info": "3 nodes", "cronEnable": true,
			"updatedAt": "2026-09-25T10:00:00Z",
			"nodes":     map[string]any{"totalCount": 3},
		}}}, nil
	})

	subs, err := d.ListSubscriptions(ctxT(t))
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	if len(subs) != 1 || subs[0].NodeCount != 3 || subs[0].Tag != "机场A" || !subs[0].CronEnable {
		t.Fatalf("subs = %+v", subs)
	}
}

func TestLatencyNullFields(t *testing.T) {
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "NodeLatencies" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		return map[string]any{"nodeLatencies": []any{
			map[string]any{"id": "n1", "latencyMs": 120, "alive": true, "testedAt": "2026-09-25T12:00:00Z", "message": nil},
			map[string]any{"id": "n2", "latencyMs": nil, "alive": false, "testedAt": "0001-01-01T00:00:00Z", "message": "dial timeout"},
		}}, nil
	})

	lats, err := d.Latencies(ctxT(t), nil)
	if err != nil {
		t.Fatalf("Latencies: %v", err)
	}
	if lats[0].Ms != 120 || !lats[0].Alive || lats[0].TestedAt.IsZero() {
		t.Fatalf("lat[0] = %+v", lats[0])
	}
	if lats[1].Alive || lats[1].Ms != 0 || lats[1].Message != "dial timeout" {
		t.Fatalf("lat[1] = %+v", lats[1])
	}
}

// helpers to build driver.Policy values concisely.
func policyFixed(i int) driver.Policy {
	return driver.Policy{Name: "fixed", FixedIndex: i}
}

func policyAuto() driver.Policy {
	return driver.Policy{Name: "min_moving_avg"}
}
