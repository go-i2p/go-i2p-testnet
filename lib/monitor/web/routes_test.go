package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-i2p-testnet/lib/monitor/i2pcontrol"
	"go-i2p-testnet/lib/monitor/topology"
)

func graphFixture() *topology.Graph {
	return topology.Build(topology.BuildInput{
		GeneratedAt: time.Unix(1700000000, 0).UTC(),
		Source:      "fixture",
		Nodes: []topology.Node{
			{Name: "router-i2pd-1", Impl: "i2pd", IP: "172.28.0.2", Hash: hashA, State: "up", InRoster: true},
			{Name: "router-i2pd-2", Impl: "i2pd", IP: "172.28.0.3", Hash: hashB, State: "up", InRoster: true},
		},
		Sessions:  []topology.Session{{From: "router-i2pd-1", HashPrefix: hashB[:4], PeerIP: "172.28.0.3", PeerPort: 4567, Transport: "SSU2", BytesSent: 1161, BytesReceived: 175}},
		Knowledge: []topology.Knowledge{{Router: "router-i2pd-2", Hashes: []string{hashA}}},
	})
}

const (
	hashA = "AutoCdqqsW~S6nnqEG~BQgcUhMdxbLqwWN3qO0h0zso="
	hashB = "T2gEKfUoS~sxitfRf8C5EuZtBOdqdJqam9NfsDAkxfA="
)

func newServerWithTopo(t *testing.T, p *Poller, g *topology.Graph) *Server {
	t.Helper()
	s, err := NewServer(ServerConfig{
		Poller: p, PollInterval: 5 * time.Second,
		Topology: func(context.Context) *topology.Graph { return g },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestTopologyRouteRendersEdges: the requirement asks for a topology view with
// an update time and observed-vs-inferred distinction, rendered as HTML.
func TestTopologyRouteRendersEdges(t *testing.T) {
	s := newServerWithTopo(t, staticPoller(), graphFixture())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/topology", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/topology = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"router-i2pd-1", "router-i2pd-2",
		"observed", "inferred", // kinds are labelled
		"2023-11-14",          // graph update time is shown
		"SSU2",                // transport from the router's own table
		"172.28.0.3",          // peer address
		"<svg",                // an actual picture, not just text
		"hardcoded peer list", // the page says the data is not hardcoded
		"transport session",   // and names where it came from
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/topology missing %q", want)
		}
	}
	if strings.Contains(body, "No edges yet") {
		t.Error("fixture has edges but the page says there are none")
	}
}

// TestTopologyEdgeDistinguishesKinds checks the two kinds are visually
// separable, which is the whole point of the requirement.
func TestTopologyEdgeDistinguishesKinds(t *testing.T) {
	s := newServerWithTopo(t, staticPoller(), graphFixture())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/topology", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `badge observed`) || !strings.Contains(body, `badge inferred`) {
		t.Error("both edge kinds must be rendered with their own class")
	}
	// observed is a solid line, inferred dashed.
	if !strings.Contains(body, `stroke-dasharray`) {
		t.Error("inferred edges must be dashed in the SVG")
	}
}

func TestAPITopologyJSON(t *testing.T) {
	s := newServerWithTopo(t, staticPoller(), graphFixture())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/topology", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/topology = %d", rec.Code)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{"generated_at", "source", "nodes", "edges", "observed", "inferred"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing key %q", key)
		}
	}
	edges := got["edges"].([]interface{})
	if len(edges) != 2 {
		t.Fatalf("edges = %d, want 2 (one observed, one inferred)", len(edges))
	}
	for _, e := range edges {
		em := e.(map[string]interface{})
		if em["kind"] != "observed" && em["kind"] != "inferred" {
			t.Errorf("edge kind = %v", em["kind"])
		}
		if em["match"] == nil || em["match"] == "" {
			t.Error("every edge must say how the peer was matched")
		}
	}
}

// TestTopologyPasswordsNeverSerialized: the graph carries router identity
// data; it must not carry credentials.
func TestTopologyPasswordsNeverSerialized(t *testing.T) {
	p := NewPoller(time.Hour, time.Second, nil)
	p.statuses["r"] = &RouterStatus{Name: "r", Impl: "i2pd", State: StateUp,
		URL: "https://172.28.0.2:7650/", ContainerID: "abc", IP: "172.28.0.2",
		Sample: sample(), LastSeen: time.Now()}
	s := newServerWithTopo(t, p, graphFixture())
	for _, path := range []string{"/api/topology", "/api/status"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := strings.ToLower(rec.Body.String())
		for _, bad := range []string{"password", "token", "secret"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s leaks %q", path, bad)
			}
		}
	}
}

// TestTopologyAbsentSaysWhy: with no collector configured the page must
// explain itself instead of 404ing blankly.
func TestTopologyAbsentSaysWhy(t *testing.T) {
	s := newTestServer(t, staticPoller())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/topology", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 with an explanation", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not") {
		t.Error("page should say why topology is unavailable")
	}
}

// TestIndexHasChartsAndNetworkTotals covers capabilities 2 and 3 on the page
// an operator actually reads.
func TestIndexHasChartsAndNetworkTotals(t *testing.T) {
	now := time.Now()
	hist := []*i2pcontrol.Sample{
		{CollectedAt: now, KnownPeers: i64(4), ActivePeers: i64(2), BWInbound1s: f64(4096)},
		{CollectedAt: now.Add(-5 * time.Second), KnownPeers: i64(3), ActivePeers: i64(1), BWInbound1s: f64(2048)},
		{CollectedAt: now.Add(-10 * time.Second), KnownPeers: i64(2), ActivePeers: i64(1), BWInbound1s: f64(1024)},
	}
	up := &RouterStatus{Name: "router-i2pd-1", Impl: "i2pd", State: StateUp,
		Sample: hist[0], History: hist, LastSeen: now}
	p := staticPoller(up)
	p.interval = 5 * time.Second
	p.recordNetworkHistory(now)
	p.recordNetworkHistory(now.Add(time.Second))

	s := newTestServer(t, p)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("/ = %d", rec.Code)
	}
	for _, want := range []string{"<svg", "polyline", "Network totals", "Network over time", "topology"} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %q", want)
		}
	}
	// The aggregate must be summed, not left to the operator.
	if !strings.Contains(body, "known peers 4") {
		t.Errorf("expected summed peers in:\n%s", body)
	}
}

// TestRouterPageHasChartsAndExplainsMissingValues covers capability 1 and 5.
func TestRouterPageHasChartsAndExplainsMissingValues(t *testing.T) {
	now := time.Now()
	hist := []*i2pcontrol.Sample{
		{CollectedAt: now, KnownPeers: i64(5), Participating: i64(2), BWInbound1s: f64(100)},
		{CollectedAt: now.Add(-5 * time.Second), KnownPeers: i64(4), Participating: i64(1), BWInbound1s: f64(90)},
		{CollectedAt: now.Add(-10 * time.Second), KnownPeers: i64(3), Participating: i64(1), BWInbound1s: f64(80)},
	}
	st := &RouterStatus{Name: "r1", Impl: "i2pd", State: StateUp, Sample: hist[0], History: hist, LastSeen: now}
	srv, err := NewServer(ServerConfig{Poller: staticPoller(st), PollInterval: 7 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	s := srv
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/router/r1", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "<svg") || !strings.Contains(body, "polyline") {
		t.Error("router page must render charts from its samples")
	}
	// i2pd does not serve tunnel in/out: the page must say so, not show a dash.
	if !strings.Contains(body, "not served by i2pd") {
		t.Error("router page must explain values the implementation never serves")
	}
	// Refresh must follow the configured interval. The old template hardcoded
	// content="5", so this is checked with a non-5 interval that would have
	// failed before.
	if !strings.Contains(body, `content="7"`) {
		t.Errorf("detail page must honour the configured refresh interval:\n%s", body[:400])
	}
}

// TestDownRouterRendersNoBrokenCells is the readability finding: a router with
// no sample produced cells like "//" and " / ".
func TestDownRouterRendersNoBrokenCells(t *testing.T) {
	down := &RouterStatus{Name: "dead", Impl: "goi2p", State: StateDown,
		LastError: "connection refused", FailureReason: ReasonNotServing, Failures: 3}
	s := newTestServer(t, staticPoller(down))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	for _, bad := range []string{"<td>//</td>", "<td>/</td>", "<td> / </td>", "<dd></dd>"} {
		if strings.Contains(body, bad) {
			t.Errorf("index contains broken empty cell %q", bad)
		}
	}
	if !strings.Contains(body, "—") {
		t.Error("missing values should render as an em-dash")
	}
	// The diagnosis must be on the page.
	if !strings.Contains(body, ReasonNotServing) || !strings.Contains(body, "3 in a row") {
		t.Errorf("index must show the failure reason and streak:\n%s", body)
	}
}

// TestStaleRouterRendersAmber keeps the colour contract for the new state.
func TestStaleRouterRendersAmber(t *testing.T) {
	stale := &RouterStatus{Name: "amber", Impl: "i2pd", State: StateStale,
		Sample: sample(), LastSeen: time.Now().Add(-time.Minute), FailureReason: ReasonTimeout}
	s := newTestServer(t, staticPoller(stale))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `class="stale"`) {
		t.Error("stale router must render amber")
	}
	if !strings.Contains(body, ReasonTimeout) {
		t.Error("stale router must show why it is not fresh")
	}
}

// TestAPIStatusCarriesNewFields keeps the JSON contract for automation.
func TestAPIStatusCarriesNewFields(t *testing.T) {
	now := time.Now()
	up := &RouterStatus{Name: "r1", Impl: "i2pd", State: StateUp, Sample: sample(), LastSeen: now,
		FailureReason: ReasonTimeout, Failures: 2}
	p := staticPoller(up)
	p.interval = 5 * time.Second
	p.recordNetworkHistory(now)
	s := newServerWithTopo(t, p, graphFixture())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"history_max_samples", "network", "topology_url"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing top-level key %q", key)
		}
	}
	r0 := got["routers"].([]interface{})[0].(map[string]interface{})
	if r0["failure_reason"] != ReasonTimeout {
		t.Errorf("failure_reason = %v", r0["failure_reason"])
	}
	ns, ok := r0["not_served_by_implementation"].([]interface{})
	if !ok || len(ns) == 0 {
		t.Error("i2pd routers must list the properties they never serve")
	}
	joined := ""
	for _, v := range ns {
		joined += v.(string) + " "
	}
	for _, want := range []string{"tunnels_inbound", "fast_peers"} {
		if !strings.Contains(joined, want) {
			t.Errorf("not_served missing %q, got %q", want, joined)
		}
	}
}

// TestChartsEscapedInHTML: chart titles/labels derive from router data, so the
// page must not become an injection point through the SVG.
func TestChartsEscapedInHTML(t *testing.T) {
	now := time.Now()
	bad := `<script>alert(1)</script>`
	st := &RouterStatus{Name: bad, Impl: bad, State: StateDown,
		LastError: bad, Sample: &i2pcontrol.Sample{CollectedAt: now, KnownPeers: i64(1)},
		History: []*i2pcontrol.Sample{
			{CollectedAt: now, KnownPeers: i64(2)},
			{CollectedAt: now.Add(-5 * time.Second), KnownPeers: i64(1)},
		},
		LastSeen: now}
	s := newTestServer(t, staticPoller(st))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(rec.Body.String(), "<script>alert(1)</script>") {
		t.Error("router-supplied text reached the page unescaped")
	}
}
