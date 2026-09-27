package daed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// richQuery reports whether the mock's most recent request was the rich
// groups query (only it asks for matchedNodes), so a handler can reject
// exactly what an old daed build would reject — and never a caller's
// fallback query.
func richQuery(m *mockGraphQL) bool {
	reqs := m.reqs()
	if len(reqs) == 0 {
		return false
	}
	return strings.Contains(reqs[len(reqs)-1].query, "matchedNodes")
}

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

// A token expiring under N concurrent requests must cost one shared refresh:
// the requests that lost the re-auth race wait for it and then replay,
// instead of each failing with a raw "access denied".
func TestReAuthSingleflight(t *testing.T) {
	var tokens int32
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		switch op {
		case "Token":
			atomic.AddInt32(&tokens, 1)
			return map[string]any{"token": "jwt-2"}, nil
		case "NumberUsers":
			if auth != "Bearer jwt-2" {
				return nil, []gqlError{{Message: "access denied"}}
			}
			return map[string]any{"numberUsers": 1}, nil
		}
		return nil, []gqlError{{Message: "unexpected op " + op}}
	})
	d.client.SetToken("jwt-expired")

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = d.NumberUsers(ctxT(t))
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
	}
	// One refresh for everyone, plus one per straggler whose denial landed
	// after a refresh had already finished (scheduler-dependent, usually 0-1
	// extra). Well below n proves the requests shared refreshes instead of
	// each triggering its own; the old loser-fails behavior is caught by
	// the err check above.
	if got := atomic.LoadInt32(&tokens); int(got) > n/2 {
		t.Fatalf("token refreshed %d times, want <= %d (shared refresh)", got, n/2)
	}
}

// When the refresh succeeds but the replay is still denied, the call must
// surface driver.ErrNeedAuth (the documented contract), not a bare GraphQL
// error the UI can only string-match on.
func TestReplayDeniedReturnsErrNeedAuth(t *testing.T) {
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op == "Token" {
			return map[string]any{"token": "jwt-2"}, nil
		}
		return nil, []gqlError{{Message: "access denied"}}
	})
	d.client.SetToken("jwt-expired")

	_, err := d.NumberUsers(ctxT(t))
	if !errors.Is(err, driver.ErrNeedAuth) {
		t.Fatalf("want ErrNeedAuth, got %v", err)
	}
}

// The re-auth hook refreshes the token by calling this same client. When the
// token query itself is denied, Do must not re-enter itself: it used to
// recurse until the goroutine stack ran out.
func TestAccessDeniedOnTokenQueryDoesNotRecurse(t *testing.T) {
	calls := 0
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op == "Token" {
			calls++
			return nil, []gqlError{{Message: "access denied"}}
		}
		return nil, []gqlError{{Message: "unexpected op " + op}}
	})

	_, err := d.Login(ctxT(t), "alice", "s3cret1")
	if err == nil || !strings.Contains(err.Error(), "need authentication") {
		t.Fatalf("want ErrNeedAuth, got %v", err)
	}
	// One attempt for the login itself, one for the re-auth it triggered.
	if calls != 2 {
		t.Fatalf("token query ran %d times, want 2 (no further recursion)", calls)
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
	var m *mockGraphQL
	m = &mockGraphQL{handler: func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "Groups" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		// Only the rich query (it alone asks for matchedNodes) is rejected,
		// like an old daed build lacking the GroupSubscription type.
		if richQuery(m) {
			return nil, []gqlError{{Message: `Cannot query field "matchedNodes" on type "GroupSubscription".`}}
		}
		return map[string]any{"groups": []any{
			map[string]any{"id": "1", "name": "proxy", "policy": "random", "nodes": []any{}},
		}}, nil
	}}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	d := New(Options{Endpoint: srv.URL + "/graphql", Username: "alice", Password: "s3cret1"})

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
}

// Stock daed v2.1.1 (pre-traffic-fix chains) rejects the modern Global
// fields; ListSelections must downgrade to the legacy field set and stick
// with it, same as the qGroupsRich → qGroups demotion.
func TestListSelectionsFallbackOnOldSchema(t *testing.T) {
	var m *mockGraphQL
	m = &mockGraphQL{handler: func(op string, vars map[string]any, auth string) (any, []gqlError) {
		switch op {
		case "Selections":
			// Only the modern query (it alone asks for disableThp) is
			// rejected, like stock daed v2.1.1 lacking the newer globals.
			if reqs := m.reqs(); len(reqs) > 0 && strings.Contains(reqs[len(reqs)-1].query, "disableThp") {
				return nil, []gqlError{{Message: `Cannot query field "disableThp" on type "Global".`}}
			}
			return map[string]any{"configs": []any{
				map[string]any{"id": "1", "name": "global", "selected": true,
					"global": map[string]any{"tproxyPort": 1234, "soMarkFromDaeSet": true}},
			}, "dnss": []any{}, "routings": []any{}}, nil
		case "ConfigFlatDesc":
			return map[string]any{"configFlatDesc": []any{}}, nil
		}
		return nil, []gqlError{{Message: "unexpected op " + op}}
	}}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	d := New(Options{Endpoint: srv.URL + "/graphql", Username: "alice", Password: "s3cret1"})

	sel, err := d.ListSelections(ctxT(t))
	if err != nil {
		t.Fatalf("first ListSelections should fall back: %v", err)
	}
	if len(sel.Configs) != 1 || sel.Configs[0].Name != "global" {
		t.Fatalf("configs = %+v", sel.Configs)
	}
	if _, err := d.ListSelections(ctxT(t)); err != nil {
		t.Fatalf("second ListSelections: %v", err)
	}
	if reqs := m.reqs(); len(reqs) == 0 || strings.Contains(reqs[len(reqs)-1].query, "disableThp") {
		t.Fatalf("fallback not sticky: modern fields still queried")
	}
}

// tea.Batch runs commands concurrently, so ListGroups calls overlap: the
// schema-fallback flag is written by whichever call discovers the old
// schema while others are still reading it. Run with -race.
func TestListGroupsConcurrentSchemaFallback(t *testing.T) {
	var rejected int32
	var m *mockGraphQL
	m = &mockGraphQL{handler: func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "Groups" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		// Reject the rich query a few times (concurrent callers all discover
		// the old schema before the flag propagates). Keying off the query
		// text — not a call counter — keeps a goroutine's fallback query from
		// being rejected too, which would make the test racy.
		if richQuery(m) && atomic.AddInt32(&rejected, 1) <= 3 {
			return nil, []gqlError{{Message: `Cannot query field "matchedNodes" on type "GroupSubscription".`}}
		}
		return map[string]any{"groups": []any{
			map[string]any{"id": "1", "name": "proxy", "policy": "random", "nodes": []any{}},
		}}, nil
	}}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	d := New(Options{Endpoint: srv.URL + "/graphql", Username: "alice", Password: "s3cret1"})
	ctx := ctxT(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				gs, err := d.ListGroups(ctx)
				if err != nil {
					t.Errorf("ListGroups: %v", err)
					return
				}
				if len(gs) != 1 || gs[0].Name != "proxy" {
					t.Errorf("groups = %+v", gs)
					return
				}
			}
		}()
	}
	wg.Wait()
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
					"global": map[string]any{
						"logLevel": "info", "lanInterface": []any{"eth0", "wlan0"},
						"wanInterface": []any{}, "tproxyPort": float64(12345),
						"allowInsecure": false, "checkInterval": "30s",
						"bandwidthMaxTx": "0",
					}}},
				"dnss":     []any{map[string]any{"id": "d1", "name": "默认DNS", "selected": true}},
				"routings": []any{map[string]any{"id": "r1", "name": "默认路由", "selected": true}},
			}, nil
		case "ConfigFlatDesc":
			return map[string]any{"configFlatDesc": []any{
				// global.* entries map to globalInput keys.
				map[string]any{"name": "Global.TproxyPort", "mapping": "global.tproxy_port",
					"type": "uint16", "defaultValue": "12345"},
				map[string]any{"name": "Global.LogLevel", "mapping": "global.log_level",
					"type": "string", "defaultValue": "info", "desc": "Log level."},
				map[string]any{"name": "Global.LanInterface", "mapping": "global.lan_interface",
					"type": "string", "isArray": true},
				map[string]any{"name": "Global.AllowInsecure", "mapping": "global.allow_insecure",
					"type": "bool", "defaultValue": "false"},
				map[string]any{"name": "Global.CheckInterval", "mapping": "global.check_interval",
					"type": "time.Duration", "defaultValue": "30s"},
				// Other sections are not globalInput fields and must be skipped.
				map[string]any{"name": "Routing", "mapping": "routing", "type": "config.Routing", "required": true},
				map[string]any{"name": "Group.Policy", "mapping": "group.policy", "type": "config.FunctionListOrString"},
				// A metadata entry the response does not carry is skipped too.
				map[string]any{"name": "Global.Mptcp", "mapping": "global.mptcp", "type": "bool"},
			}}, nil
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

	byName := map[string]driver.ConfigField{}
	for _, f := range sel.Configs[0].Fields {
		byName[f.Name] = f
	}
	// Metadata-driven fields: key conversion, type taxonomy, value formatting.
	if f := byName["tproxyPort"]; f.Type != "int" || f.Value != "12345" || f.Default != "12345" {
		t.Fatalf("tproxyPort = %+v", f)
	}
	if f := byName["logLevel"]; f.Type != "string" || f.Value != "info" || f.Desc == "" {
		t.Fatalf("logLevel = %+v", f)
	}
	if f := byName["lanInterface"]; f.Type != "array" || f.Value != "eth0, wlan0" {
		t.Fatalf("lanInterface = %+v", f)
	}
	if f := byName["wanInterface"]; f.Type != "array" || f.Value != "" {
		t.Fatalf("wanInterface (empty array) = %+v", f)
	}
	if f := byName["allowInsecure"]; f.Type != "bool" || f.Value != "false" {
		t.Fatalf("allowInsecure = %+v", f)
	}
	if f := byName["checkInterval"]; f.Type != "duration" || f.Value != "30s" {
		t.Fatalf("checkInterval = %+v", f)
	}
	// Keys without metadata are still offered, with the type inferred.
	if f := byName["bandwidthMaxTx"]; f.Type != "string" || f.Value != "0" {
		t.Fatalf("bandwidthMaxTx (inferred) = %+v", f)
	}
	// Non-global sections and keys absent from the response never surface.
	for _, bad := range []string{"routing", "group.policy", "mptcp"} {
		if _, ok := byName[bad]; ok {
			t.Fatalf("field %q should not be offered", bad)
		}
	}
	_ = m
}

// qSelections must ask for every globalInput key: the field list is built
// from what the backend echoes back, so a key missing from the document is
// silently uneditable. The list mirrors daed v2.1.1's globalInput (SDL).
func TestSelectionsQueryCoversEveryGlobalKey(t *testing.T) {
	d, m := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		switch op {
		case "Selections", "ConfigFlatDesc":
			return map[string]any{"configs": []any{}, "dnss": []any{}, "routings": []any{},
				"configFlatDesc": []any{}}, nil
		}
		return nil, []gqlError{{Message: "unexpected op " + op}}
	})
	if _, err := d.ListSelections(ctxT(t)); err != nil {
		t.Fatalf("ListSelections: %v", err)
	}
	q := m.reqs()[0].query
	for _, k := range []string{
		"tproxyPort", "tproxyPortProtect", "soMarkFromDae",
		"logLevel", "tcpCheckUrl", "tcpCheckHttpMethod", "udpCheckDns",
		"checkInterval", "checkTolerance", "lanInterface", "wanInterface",
		"allowInsecure", "dialMode", "disableWaitingNetwork", "disableThp",
		"enableLocalTcpFastRedirect", "autoConfigKernelParameter", "autoConfigFirewallRule",
		"sniffingTimeout", "autoSniffPunt",
		"tlsImplementation", "utlsImitate", "tlsFragment", "tlsFragmentLength", "tlsFragmentInterval",
		"pprofPort", "mptcp", "bootstrapResolver", "fallbackResolver",
		"bandwidthMaxTx", "bandwidthMaxRx", "udphopInterval", "bpfConnStateMapSize",
	} {
		if !strings.Contains(q, k) {
			t.Errorf("qSelections does not select %s", k)
		}
	}
}

// The structured overview is what the config page shows above the raw DSL,
// so it must survive the union (fallback) and the and-conditions.
func TestSelectionsRoutingAndDnsSummary(t *testing.T) {
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		switch op {
		case "Selections":
			return map[string]any{
				"configs": []any{},
				"dnss": []any{map[string]any{"id": "d1", "name": "DNS", "selected": true,
					"dns": map[string]any{"string": "upstream {}",
						"upstream": []any{
							map[string]any{"key": "alidns", "val": "udp://223.5.5.5:53"},
							map[string]any{"key": "gfw", "val": "tcp://8.8.4.4:53"},
						}}}},
				"routings": []any{map[string]any{"id": "r1", "name": "路由", "selected": true,
					"routing": map[string]any{"string": "fallback: direct",
						"rules": []any{
							map[string]any{
								"conditions": map[string]any{"and": []any{
									map[string]any{"name": "dip", "not": false,
										"params": []any{map[string]any{"key": "", "val": "1.2.3.4"}}},
									map[string]any{"name": "domain", "not": true,
										"params": []any{map[string]any{"key": "suffix", "val": "cn"}}},
								}},
								"outbound": map[string]any{"name": "proxy", "params": []any{}},
							},
							map[string]any{
								"conditions": map[string]any{"and": []any{
									map[string]any{"name": "domain", "params": []any{
										map[string]any{"key": "suffix", "val": "example.com"},
									}},
								}},
								"outbound": map[string]any{"name": "block", "params": []any{}},
							},
							// daed parses must_direct as function "direct"
							// with the positional param "must".
							map[string]any{
								"conditions": map[string]any{"and": []any{
									map[string]any{"name": "pname", "params": []any{
										map[string]any{"key": "", "val": "dnsmasq"},
									}},
								}},
								"outbound": map[string]any{"name": "direct", "params": []any{
									map[string]any{"key": "", "val": "must"},
								}},
							},
						},
						// Plaintext branch of the FunctionOrPlaintext union.
						"fallback": map[string]any{"val": "direct"},
					}}},
			}, nil
		case "ConfigFlatDesc":
			return map[string]any{"configFlatDesc": []any{}}, nil
		}
		return nil, []gqlError{{Message: "unexpected op " + op}}
	})
	sel, err := d.ListSelections(ctxT(t))
	if err != nil {
		t.Fatalf("ListSelections: %v", err)
	}
	wantRouting := []string{
		"dip(1.2.3.4) && !domain(suffix:cn) -> proxy",
		"domain(suffix:example.com) -> block",
		"pname(dnsmasq) -> must_direct",
		"fallback: direct",
	}
	if got := sel.Routings[0].Summary; !reflect.DeepEqual(got, wantRouting) {
		t.Fatalf("routing summary =\n%q\nwant\n%q", got, wantRouting)
	}
	wantDns := []string{"alidns: udp://223.5.5.5:53", "gfw: tcp://8.8.4.4:53"}
	if got := sel.Dns[0].Summary; !reflect.DeepEqual(got, wantDns) {
		t.Fatalf("dns summary = %q, want %q", got, wantDns)
	}
	if sel.Routings[0].Body != "fallback: direct" {
		t.Fatalf("routing body = %q", sel.Routings[0].Body)
	}
}

// A Function fallback (not Plaintext) must render through the other union
// branch.
func TestSelectionsRoutingFunctionFallback(t *testing.T) {
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		switch op {
		case "Selections":
			return map[string]any{
				"configs": []any{}, "dnss": []any{},
				"routings": []any{map[string]any{"id": "r1", "name": "路由", "selected": true,
					"routing": map[string]any{"string": "",
						"fallback": map[string]any{"name": "must_direct", "params": []any{}},
					}}},
			}, nil
		case "ConfigFlatDesc":
			return map[string]any{"configFlatDesc": []any{}}, nil
		}
		return nil, []gqlError{{Message: "unexpected op " + op}}
	})
	sel, err := d.ListSelections(ctxT(t))
	if err != nil {
		t.Fatalf("ListSelections: %v", err)
	}
	want := []string{"fallback: must_direct"}
	if got := sel.Routings[0].Summary; !reflect.DeepEqual(got, want) {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestValidateRoutingAndDns(t *testing.T) {
	d, m := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		switch op {
		case "ParsedRouting":
			if vars["raw"] != "fallback: direct" {
				t.Errorf("raw = %v", vars["raw"])
			}
			return map[string]any{"parsedRouting": map[string]any{"string": ""}}, nil
		case "ParsedDns":
			if vars["raw"] == "upstream {{{" {
				return nil, []gqlError{{Message: "line 1:24 upstream {{{" +
					"\n                              ^: mismatched input '{' expecting '}'"}}
			}
			return map[string]any{"parsedDns": map[string]any{"string": ""}}, nil
		}
		return nil, []gqlError{{Message: "unexpected op " + op}}
	})

	if err := d.ValidateRouting(ctxT(t), "fallback: direct"); err != nil {
		t.Fatalf("ValidateRouting: %v", err)
	}
	if err := d.ValidateDns(ctxT(t), "upstream {}"); err != nil {
		t.Fatalf("ValidateDns: %v", err)
	}
	err := d.ValidateDns(ctxT(t), "upstream {{{")
	if err == nil {
		t.Fatal("ValidateDns on broken DSL: want error")
	}
	if !strings.Contains(err.Error(), "mismatched input") {
		t.Fatalf("error should carry the parser detail: %v", err)
	}
	if len(m.reqs()) != 3 {
		t.Fatalf("requests = %d, want 3", len(m.reqs()))
	}
}

// globalInputKey is the bridge between configFlatDesc's flat keys and the
// GraphQL globalInput keys; the mapping must cover daed v2.1.1's Global
// section exactly.
func TestGlobalInputKeyConversion(t *testing.T) {
	cases := map[string]string{
		"global.tproxy_port":                  "tproxyPort",
		"global.so_mark_from_dae_set":         "soMarkFromDaeSet",
		"global.tcp_check_url":                "tcpCheckUrl",
		"global.udphop_interval":              "udphopInterval",
		"global.auto_config_kernel_parameter": "autoConfigKernelParameter",
		"global.mptcp":                        "mptcp",
		"global.bandwidth_max_rx":             "bandwidthMaxRx",
	}
	for mapping, want := range cases {
		got, ok := globalInputKey(mapping)
		if !ok || got != want {
			t.Errorf("globalInputKey(%q) = %q, %v; want %q", mapping, got, ok, want)
		}
	}
	for _, bad := range []string{"routing", "group.policy", "dns.upstream", "global.", "global.a.b", ""} {
		if got, ok := globalInputKey(bad); ok {
			t.Errorf("globalInputKey(%q) = %q, want not ok", bad, got)
		}
	}
}

func TestSubscriptionsMapping(t *testing.T) {
	d, m := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "Subscriptions" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		return map[string]any{"subscriptions": []any{map[string]any{
			"id": "s1", "tag": "机场A", "link": "https://example.com/sub",
			"status": "updated", "info": "3 nodes", "cronEnable": true,
			"cronExp":   "0 */6 * * *",
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
	if subs[0].CronExp != "0 */6 * * *" {
		t.Fatalf("CronExp = %q, want %q", subs[0].CronExp, "0 */6 * * *")
	}
	// The mock echoes every field it is handed, so a field the query never
	// selects still unmarshals fine. Assert on the document itself: this is
	// what catches a query that forgets to ask for cronExp.
	if q := m.reqs()[0].query; !strings.Contains(q, "cronExp") {
		t.Fatalf("qSubscriptions does not select cronExp:\n%s", q)
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

// Presets are pure rendering, so they are tested without a server: the DSL
// they emit must be exactly what daed's own templates produce, and detection
// must round-trip without mislabeling hand-written routings.
func TestRoutingPresetsBuildAndDetect(t *testing.T) {
	d := New(Options{})

	presets := d.RoutingPresets()
	wantIDs := []string{"gfw", "nonCn", "cnOnly", "global"}
	if len(presets) != len(wantIDs) {
		t.Fatalf("presets = %+v", presets)
	}
	for i, id := range wantIDs {
		if presets[i].ID != id || !presets[i].Group {
			t.Fatalf("presets[%d] = %+v, want id %q", i, presets[i], id)
		}
	}

	const prelude = "pname(NetworkManager, systemd-resolved, dnsmasq) -> must_direct\n" +
		"dip(geoip:private) -> direct"
	cases := map[string]string{
		"gfw":    prelude + "\ndomain(geosite:gfw) -> proxy\nfallback: direct",
		"nonCn":  prelude + "\ndip(geoip:cn) -> direct\ndomain(geosite:cn) -> direct\nfallback: proxy",
		"cnOnly": prelude + "\ndip(geoip:cn) -> proxy\ndomain(geosite:cn) -> proxy\nfallback: direct",
		"global": prelude + "\nfallback: proxy",
	}
	for id, want := range cases {
		got, err := d.BuildRoutingPreset(id, "proxy")
		if err != nil {
			t.Fatalf("BuildRoutingPreset(%s): %v", id, err)
		}
		if got != want {
			t.Fatalf("BuildRoutingPreset(%s) =\n%q\nwant\n%q", id, got, want)
		}
		if mode := d.DetectRoutingPreset(got); mode != id {
			t.Fatalf("DetectRoutingPreset(%s) = %q", id, mode)
		}
	}
	if _, err := d.BuildRoutingPreset("nope", "proxy"); err == nil {
		t.Fatal("unknown preset should error")
	}
}

// A group name is interpolated into the DSL verbatim, so anything that is
// not a plain identifier must be refused here rather than produce DSL the
// backend cannot parse.
func TestBuildRoutingPresetRejectsUnusableGroup(t *testing.T) {
	d := New(Options{})
	for _, bad := range []string{"", "my group", "pro\"xy", "a:b", "组"} {
		if _, err := d.BuildRoutingPreset("gfw", bad); err == nil {
			t.Errorf("BuildRoutingPreset(gfw, %q) should fail", bad)
		}
	}
	if _, err := d.BuildRoutingPreset("gfw", "my-proxy_1.2"); err != nil {
		t.Errorf("plain group name rejected: %v", err)
	}
}

func TestDetectRoutingPresetEdgeCases(t *testing.T) {
	d := New(Options{})
	cases := []struct {
		name, raw, want string
	}{
		{"daed default template", "# Default routing rules\n" +
			"pname(NetworkManager, systemd-resolved) -> must_direct\n" +
			"dip(geoip:private) -> direct\ndip(geoip:cn) -> direct\n" +
			"domain(geosite:cn) -> direct\nfallback: proxy", "nonCn"},
		{"odd spacing", "pname(NetworkManager,systemd-resolved)->must_direct\n" +
			"dip(geoip:private)->direct\nfallback:proxy", "global"},
		{"comments and blanks", "\n# hi\npname(x) -> must_direct\n\n" +
			"dip(geoip:private) -> direct\n\ndomain(geosite:gfw) -> proxy\nfallback: direct\n", "gfw"},
		{"custom rule", preludeFor() + "\ndomain(geosite:us) -> proxy\nfallback: direct", ""},
		{"mac rule (not offered)", preludeFor() + "\nmac('AA:BB:CC:DD:EE:FF') -> proxy\nfallback: direct", ""},
		{"missing prelude", "domain(geosite:gfw) -> proxy\nfallback: direct", ""},
		{"cnOnly split across groups", preludeFor() +
			"\ndip(geoip:cn) -> a\ndomain(geosite:cn) -> b\nfallback: direct", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := d.DetectRoutingPreset(c.raw); got != c.want {
			t.Errorf("%s: DetectRoutingPreset = %q, want %q", c.name, got, c.want)
		}
	}
}

func preludeFor() string {
	return "pname(NetworkManager, systemd-resolved, dnsmasq) -> must_direct\n" +
		"dip(geoip:private) -> direct"
}

// A routing that contains a preset's rules plus extras must read as custom:
// "apply preset" rebuilds the DSL from the template and would silently drop
// the extras, so detection has to require the exact rule set.
func TestDetectRoutingPresetRejectsSupersets(t *testing.T) {
	d := New(Options{})
	cases := []struct{ name, raw string }{
		{"nonCn plus extra gfw rule", preludeFor() +
			"\n domain(geosite:gfw) -> proxy\ndip(geoip:cn) -> direct\ndomain(geosite:cn) -> direct\nfallback: proxy"},
		{"gfw plus extra cn rule", preludeFor() +
			"\ndomain(geosite:gfw) -> proxy\nfallback: direct\ndip(geoip:cn) -> direct"},
		{"duplicated cn rule", preludeFor() +
			"\ndip(geoip:cn) -> direct\ndip(geoip:cn) -> direct\nfallback: proxy"},
		{"gfw rule to a builtin target", preludeFor() +
			"\ndomain(geosite:gfw) -> direct\nfallback: proxy"},
		{"cn rule to another builtin", preludeFor() +
			"\ndip(geoip:cn) -> block\nfallback: proxy"},
		{"prelude only, no fallback", preludeFor()},
		{"two fallbacks", preludeFor() + "\nfallback: proxy\nfallback: direct"},
		{"second pname rule", "pname(a) -> must_direct\npname(b) -> must_direct\n" +
			"dip(geoip:private) -> direct\nfallback: proxy"},
	}
	for _, c := range cases {
		if got := d.DetectRoutingPreset(c.raw); got != "" {
			t.Errorf("%s: DetectRoutingPreset = %q, want custom", c.name, got)
		}
	}
}

// The manual-node list walks the global nodes connection (subscription
// nodes included) and must page as deep as the per-subscription walker —
// a 20-page ceiling hid manual nodes on large instances.
func TestListManualNodesPaginatesPastTwentyPages(t *testing.T) {
	const pages = 25
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "AllNodes" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		page := 1
		if after, _ := vars["after"].(string); after != "" {
			n, _ := strconv.Atoi(strings.TrimPrefix(after, "p"))
			page = n + 1
		}
		edges := make([]any, 0, 3)
		for i := 0; i < 3; i++ {
			edges = append(edges, map[string]any{
				"id": fmt.Sprintf("p%d-%d", page, i), "name": "manual", "subscriptionID": "",
			})
		}
		return map[string]any{"nodes": map[string]any{
			"edges":    edges,
			"pageInfo": map[string]any{"endCursor": fmt.Sprintf("p%d", page), "hasNextPage": page < pages},
		}}, nil
	})

	nodes, err := d.ListManualNodes(ctxT(t))
	if err != nil {
		t.Fatalf("ListManualNodes: %v", err)
	}
	if len(nodes) != pages*3 {
		t.Fatalf("got %d manual nodes, want %d", len(nodes), pages*3)
	}
}

func TestImportNodesReportsPerLinkResults(t *testing.T) {
	d, m := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "ImportNodes" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		// Batch semantics: a bad link must not abort the rest.
		if vars["rollbackError"] != false {
			t.Errorf("rollbackError = %v, want false", vars["rollbackError"])
		}
		return map[string]any{"importNodes": []any{
			map[string]any{"link": "ss://good", "node": map[string]any{
				"id": "n1", "name": "good", "protocol": "ss", "link": "ss://good"}},
			map[string]any{"link": "ss://bad", "error": "unsupported protocol"},
		}}, nil
	})

	results, err := d.ImportNodes(ctxT(t), []string{"ss://good", "ss://bad"}, "tag1")
	if err != nil {
		t.Fatalf("ImportNodes: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Error != "" || results[0].Node == nil || results[0].Node.ID != "n1" {
		t.Fatalf("good link result = %+v", results[0])
	}
	if results[1].Error != "unsupported protocol" || results[1].Node != nil {
		t.Fatalf("bad link result = %+v", results[1])
	}
	args, ok := m.reqs()[0].vars["args"].([]any)
	if !ok || len(args) != 2 {
		t.Fatalf("args = %#v", m.reqs()[0].vars["args"])
	}
	if arg, _ := args[0].(map[string]any); arg["tag"] != "tag1" {
		t.Fatalf("tag not passed through: %#v", arg)
	}

	// Single-link import delegates to the batch mutation.
	if err := d.ImportNode(ctxT(t), "ss://good", ""); err != nil {
		t.Fatalf("ImportNode: %v", err)
	}
	last := m.reqs()[1]
	if last.vars["rollbackError"] != false {
		t.Fatalf("ImportNode should also use rollbackError=false: %+v", last.vars)
	}
}

func TestNodeAndSubscriptionEditMutations(t *testing.T) {
	d, m := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		switch op {
		case "TagNode":
			return map[string]any{"tagNode": 0}, nil
		case "UpdateNode":
			return map[string]any{"updateNode": map[string]any{"id": vars["id"]}}, nil
		case "TagSubscription":
			return map[string]any{"tagSubscription": 0}, nil
		case "UpdateSubscriptionLink":
			return map[string]any{"updateSubscriptionLink": map[string]any{"id": vars["id"]}}, nil
		}
		return nil, []gqlError{{Message: "unexpected op " + op}}
	})

	if err := d.TagNode(ctxT(t), "n1", "新标签"); err != nil {
		t.Fatalf("TagNode: %v", err)
	}
	if err := d.UpdateNode(ctxT(t), "n1", "ss://new"); err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	if err := d.TagSubscription(ctxT(t), "s1", "机场C"); err != nil {
		t.Fatalf("TagSubscription: %v", err)
	}
	if err := d.UpdateSubscriptionLink(ctxT(t), "s1", "https://c.example/sub"); err != nil {
		t.Fatalf("UpdateSubscriptionLink: %v", err)
	}
	reqs := m.reqs()
	want := []struct{ op, field, val string }{
		{"TagNode", "tag", "新标签"},
		{"UpdateNode", "newLink", "ss://new"},
		{"TagSubscription", "tag", "机场C"},
		{"UpdateSubscriptionLink", "link", "https://c.example/sub"},
	}
	for i, w := range want {
		if reqs[i].op != w.op {
			t.Fatalf("req %d op = %q, want %q", i, reqs[i].op, w.op)
		}
		if reqs[i].vars[w.field] != w.val {
			t.Fatalf("req %d %s = %v, want %q", i, w.field, reqs[i].vars[w.field], w.val)
		}
	}
}

func TestInterfacesMappingAndDegradation(t *testing.T) {
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "Interfaces" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		return map[string]any{"general": map[string]any{"interfaces": []any{
			map[string]any{"name": "eth0", "ifindex": 2,
				"ip": []any{"192.168.1.5", "fe80::1"},
				"flag": map[string]any{"up": true,
					"default": []any{map[string]any{"ipVersion": "4", "gateway": "192.168.1.1"}}}},
			map[string]any{"name": "wlan0", "ifindex": 3, "ip": []any{},
				"flag": map[string]any{"up": false}},
		}}}, nil
	})

	ifaces, err := d.Interfaces(ctxT(t))
	if err != nil {
		t.Fatalf("Interfaces: %v", err)
	}
	if len(ifaces) != 2 {
		t.Fatalf("ifaces = %+v", ifaces)
	}
	if !ifaces[0].Up || !ifaces[0].Default || ifaces[0].Gateway != "192.168.1.1" ||
		len(ifaces[0].IPs) != 2 {
		t.Fatalf("eth0 = %+v", ifaces[0])
	}
	if ifaces[1].Up || ifaces[1].Default {
		t.Fatalf("wlan0 = %+v", ifaces[1])
	}

	// An older daed without general.interfaces degrades to an empty list.
	old, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		return nil, []gqlError{{Message: `Cannot query field "interfaces" on type "General".`}}
	})
	ifaces, err = old.Interfaces(ctxT(t))
	if err != nil || len(ifaces) != 0 {
		t.Fatalf("degraded Interfaces = %+v, %v", ifaces, err)
	}
}

func TestSelectionsReferenceGroups(t *testing.T) {
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		switch op {
		case "Selections":
			return map[string]any{
				"configs": []any{map[string]any{"id": "c1", "name": "默认", "selected": true,
					"global": map[string]any{"logLevel": "info"}}},
				"dnss": []any{},
				"routings": []any{
					map[string]any{"id": "r1", "name": "默认路由", "selected": true,
						"referenceGroups": []any{"proxy", "hk"},
						"routing":         map[string]any{"string": "fallback: proxy"}},
					map[string]any{"id": "r2", "name": "自定义", "selected": false,
						"referenceGroups": []any{},
						"routing":         map[string]any{"string": "fallback: direct"}},
				}}, nil
		case "ConfigFlatDesc":
			return map[string]any{"configFlatDesc": []any{}}, nil
		}
		return nil, []gqlError{{Message: "unexpected op " + op}}
	})

	sel, err := d.ListSelections(ctxT(t))
	if err != nil {
		t.Fatalf("ListSelections: %v", err)
	}
	if got := sel.Routings[0].References; len(got) != 2 || got[0] != "proxy" || got[1] != "hk" {
		t.Fatalf("references = %+v", got)
	}
	if len(sel.Routings[1].References) != 0 {
		t.Fatalf("unexpected references = %+v", sel.Routings[1].References)
	}
}

func TestUpdatePasswordPersistsNewToken(t *testing.T) {
	var saved []string
	d, m := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		switch op {
		case "Token":
			return map[string]any{"token": "jwt-1"}, nil
		case "UpdatePassword":
			if vars["currentPassword"] != "s3cret1" || vars["newPassword"] != "n3wp4ssw0rd" {
				t.Errorf("vars = %+v", vars)
			}
			return map[string]any{"updatePassword": "jwt-3"}, nil
		}
		return nil, []gqlError{{Message: "unexpected op " + op}}
	})
	d.opts.SaveToken = func(tok string) { saved = append(saved, tok) }

	if err := d.UpdatePassword(ctxT(t), "s3cret1", "n3wp4ssw0rd"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	if len(saved) != 1 || saved[0] != "jwt-3" {
		t.Fatalf("SaveToken calls = %+v", saved)
	}
	if d.client.Token() != "jwt-3" {
		t.Fatalf("client token = %q", d.client.Token())
	}
	if d.opts.Password != "n3wp4ssw0rd" {
		t.Fatalf("stored password not rotated: %q", d.opts.Password)
	}
	_ = m
}

func TestLogoutClearsCredentials(t *testing.T) {
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		return map[string]any{"healthCheck": 1}, nil
	})
	d.client.SetToken("jwt-1")

	if err := d.Logout(ctxT(t)); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if d.client.Token() != "" || d.opts.Username != "" || d.opts.Password != "" {
		t.Fatalf("credentials survived logout: token=%q user=%q",
			d.client.Token(), d.opts.Username)
	}
	// Subsequent requests go out unauthenticated.
	if err := d.client.HealthCheck(ctxT(t)); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
}

// Array fields split on the comma only, matching the UI's field validator:
// an element containing a space survives, a blank value is an empty list
// (never nil — that would marshal as null instead of []).
func TestTypedValueArray(t *testing.T) {
	f := driver.ConfigField{Name: "lanInterface", Type: "array"}
	v, err := typedValue(f, "eth0, wlan0")
	if err != nil {
		t.Fatalf("typedValue: %v", err)
	}
	if !reflect.DeepEqual(v, []string{"eth0", "wlan0"}) {
		t.Fatalf("value = %#v, want [eth0 wlan0]", v)
	}
	v, err = typedValue(f, "a b,c")
	if err != nil {
		t.Fatalf("typedValue: %v", err)
	}
	if !reflect.DeepEqual(v, []string{"a b", "c"}) {
		t.Fatalf("value = %#v, want [a b c-kept]", v)
	}
	v, err = typedValue(f, "  ")
	if err != nil {
		t.Fatalf("typedValue: %v", err)
	}
	if !reflect.DeepEqual(v, []string{}) {
		t.Fatalf("blank value = %#v, want an empty non-nil slice", v)
	}
}

func TestCreateProfileConfigCloneUsesReturnedID(t *testing.T) {
	d, m := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		switch op {
		case "CreateConfig":
			return map[string]any{"createConfig": map[string]any{"id": "cfg-new"}}, nil
		case "UpdateConfig":
			return map[string]any{"updateConfig": map[string]any{"id": "cfg-new"}}, nil
		default:
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
	})

	fields := []driver.ConfigField{
		{Name: "tproxyPort", Value: "12345", Type: "int"},
		{Name: "logLevel", Value: "info", Type: "string"},
	}
	if err := d.CreateProfile(ctxT(t), "config", "克隆配置", &driver.ConfigItem{Fields: fields}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	// Exactly two requests: the create and one field update bound to the id
	// createConfig returned. The old implementation re-listed every
	// selection and looked the new profile up by name, which mis-bound when
	// several unselected profiles shared the name.
	reqs := m.reqs()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2 (create + update): %+v", len(reqs), reqs)
	}
	upd := reqs[1]
	if upd.op != "UpdateConfig" {
		t.Fatalf("second op = %s, want UpdateConfig", upd.op)
	}
	if upd.vars["id"] != "cfg-new" {
		t.Fatalf("update id = %v, want the returned cfg-new", upd.vars["id"])
	}
	global, ok := upd.vars["global"].(map[string]any)
	if !ok || global["tproxyPort"] != float64(12345) || global["logLevel"] != "info" {
		t.Fatalf("global = %#v", upd.vars["global"])
	}

	// Without source fields (plain create) only the create request fires.
	if err := d.CreateProfile(ctxT(t), "config", "空配置", nil); err != nil {
		t.Fatalf("CreateProfile plain: %v", err)
	}
	if n := len(m.reqs()); n != 3 {
		t.Fatalf("plain create fired %d extra requests, want 1", n-2)
	}
}

func TestUnknownFieldSentinelMatchesThroughWrapping(t *testing.T) {
	// The version-skew fallbacks must key off errors.Is(err, errUnknown-
	// Field), not message text: the sentinel has to survive roundTrip's
	// wrapping (and any future fmt.Errorf %w chain).
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		return nil, []gqlError{{Message: `Cannot query field "matchedNodes" on type "GroupSubscription".`}}
	})
	var out struct {
		Groups []rawGroup `json:"groups"`
	}
	err := d.client.Do(context.Background(), qGroupsRich, nil, &out)
	if !errors.Is(err, errUnknownField) {
		t.Fatalf("errors.Is(err, errUnknownField) = false for %v", err)
	}
	if !strings.Contains(err.Error(), "Cannot query field") ||
		!strings.Contains(err.Error(), "提示") {
		t.Fatalf("user-facing text lost: %v", err)
	}
}

func TestLatenciesChunksLargeIDLists(t *testing.T) {
	calls := 0
	seen := map[string]bool{}
	d, _ := newTestDriver(t, func(op string, vars map[string]any, auth string) (any, []gqlError) {
		if op != "NodeLatencies" {
			return nil, []gqlError{{Message: "unexpected op " + op}}
		}
		calls++
		ids, _ := vars["ids"].([]any)
		if len(ids) > 100 {
			t.Errorf("batch too large: %d", len(ids))
		}
		for _, id := range ids {
			s, _ := id.(string)
			if seen[s] {
				t.Errorf("node %s probed twice across batches", s)
			}
			seen[s] = true
		}
		var out []any
		for _, id := range ids {
			s, _ := id.(string)
			out = append(out, map[string]any{
				"id": s, "alive": true, "latencyMs": 42, "testedAt": "2026-09-27T10:00:00Z",
			})
		}
		return map[string]any{"nodeLatencies": out}, nil
	})

	ids := make([]string, 250)
	for i := range ids {
		ids[i] = fmt.Sprint("n", i)
	}
	lats, err := d.Latencies(ctxT(t), ids)
	if err != nil {
		t.Fatalf("Latencies: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (100+100+50)", calls)
	}
	if len(lats) != 250 {
		t.Fatalf("latencies = %d, want 250 (chunks concatenated, order kept)", len(lats))
	}
	if lats[0].NodeID != "n0" || lats[249].NodeID != "n249" {
		t.Fatalf("order not preserved: first=%s last=%s", lats[0].NodeID, lats[249].NodeID)
	}

	// A short list stays a single request (and an empty one keeps the
	// documented "every node" semantics rather than becoming a no-op).
	calls = 0
	seen = map[string]bool{}
	if _, err := d.Latencies(ctxT(t), []string{"n1"}); err != nil {
		t.Fatalf("Latencies(short): %v", err)
	}
	if calls != 1 {
		t.Fatalf("short list calls = %d, want 1", calls)
	}
	calls = 0
	if _, err := d.Latencies(ctxT(t), nil); err != nil {
		t.Fatalf("Latencies(nil): %v", err)
	}
	if calls != 1 {
		t.Fatalf("nil list calls = %d, want 1", calls)
	}
}
