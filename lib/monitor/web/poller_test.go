package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-i2p-testnet/lib/monitor/i2pcontrol"
)

// mockRouter is a fake I2PControl endpoint used by poller tests.
type mockRouter struct {
	srv      *httptest.Server
	mu       sync.Mutex
	up       bool
	delay    time.Duration
	authOK   bool
	version  string
	pollHits int32
}

func newMockRouter(t *testing.T) *mockRouter {
	t.Helper()
	m := &mockRouter{up: true, authOK: true, version: "2.60.0"}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		atomic.AddInt32(&m.pollHits, 1)
		m.mu.Lock()
		up, delay, authOK, version := m.up, m.delay, m.authOK, m.version
		m.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		result := func(v interface{}) {
			b, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "result": v})
			w.Write(b)
		}
		err := func(code int, msg string) {
			b, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "error": map[string]interface{}{"code": code, "message": msg}})
			w.Write(b)
		}
		switch req.Method {
		case "Authenticate":
			if !authOK {
				err(-32001, "Invalid password")
				return
			}
			result(map[string]interface{}{"API": 1, "Token": "tok"})
		case "RouterInfo":
			if !up {
				// Simulate a dead router: hang up the connection.
				hj, ok := w.(http.Hijacker)
				if ok {
					conn, _, _ := hj.Hijack()
					conn.Close()
				}
				return
			}
			result(map[string]interface{}{"i2p.router.version": version, "i2p.router.uptime": 1000})
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockRouter) set(up bool) { m.mu.Lock(); m.up = up; m.mu.Unlock() }
func (m *mockRouter) setDelay(d time.Duration) {
	m.mu.Lock()
	m.delay = d
	m.mu.Unlock()
}

func TestPollerMixedRouters(t *testing.T) {
	upR := newMockRouter(t)
	downR := newMockRouter(t)
	downR.set(false)
	slowR := newMockRouter(t)
	slowR.setDelay(150 * time.Millisecond)

	targets := []Target{
		{Name: "router-i2pd-1", Impl: "i2pd", URL: upR.srv.URL, Password: "p"},
		{Name: "router-i2pd-2", Impl: "i2pd", URL: downR.srv.URL, Password: "p"},
		{Name: "router-i2pd-3", Impl: "i2pd", URL: slowR.srv.URL, Password: "p"},
	}
	src := func(context.Context) []Target { return targets }
	p := NewPoller(50*time.Millisecond, 2*time.Second, src)
	ctx := context.Background()
	p.DispatchNow(ctx)

	// Wait for async results (up/down resolve fast; slow one after delay).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st, ok := p.Get("router-i2pd-1")
		st2, _ := p.Get("router-i2pd-2")
		st3, _ := p.Get("router-i2pd-3")
		if ok && st.State == StateUp && st2.State == StateDown && st3.State == StateUp {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	st, _ := p.Get("router-i2pd-1")
	if st.State != StateUp {
		t.Errorf("up router state = %s (%s)", st.State, st.LastError)
	}
	st2, _ := p.Get("router-i2pd-2")
	if st2.State != StateDown {
		t.Errorf("down router state = %s", st2.State)
	}
	if st2.LastError == "" {
		t.Error("down router must carry a last error")
	}
	st3, _ := p.Get("router-i2pd-3")
	if st3.State != StateUp {
		t.Errorf("slow router state = %s (%s)", st3.State, st3.LastError)
	}

	total, up, down, _, _ := p.Totals()
	if total != 3 || up != 2 || down != 1 {
		t.Errorf("totals = %d/%d/%d, want 3/2/1", total, up, down)
	}
}

func TestPollerSlowRouterDoesNotBlockOthers(t *testing.T) {
	slow := newMockRouter(t)
	slow.setDelay(400 * time.Millisecond)
	fast := newMockRouter(t)
	targets := []Target{
		{Name: "slow", URL: slow.srv.URL, Password: "p"},
		{Name: "fast", URL: fast.srv.URL, Password: "p"},
	}
	p := NewPoller(10*time.Millisecond, 2*time.Second, func(context.Context) []Target { return targets })
	start := time.Now()
	p.DispatchNow(context.Background()) // must not wait for the slow router
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Errorf("DispatchNow blocked on slow router: %v", d)
	}
	// Fast router should complete many cycles while slow is still busy;
	// skip-if-in-flight means slow never blocks the loop.
	for i := 0; i < 20; i++ {
		p.DispatchNow(context.Background())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st, _ := p.Get("fast"); st.State == StateUp {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st, _ := p.Get("fast"); st.State != StateUp {
		t.Errorf("fast router state = %s", st.State)
	}
}

func TestPollerDownAfterRouterDies(t *testing.T) {
	r := newMockRouter(t)
	p := NewPoller(30*time.Millisecond, 300*time.Millisecond, func(context.Context) []Target {
		return []Target{{Name: "gated", URL: r.srv.URL, Password: "p"}}
	})
	ctx := context.Background()
	// First cycles succeed.
	for i := 0; i < 3; i++ {
		p.DispatchNow(ctx)
		time.Sleep(30 * time.Millisecond)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st, _ := p.Get("gated"); st.State == StateUp {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st, _ := p.Get("gated"); st.State != StateUp {
		t.Fatalf("expected up first, got %s", st.State)
	}
	// Kill the router: connection refused now -> down within a few cycles.
	r.srv.Close()
	for i := 0; i < 10; i++ {
		p.DispatchNow(ctx)
		time.Sleep(30 * time.Millisecond)
	}
	st, _ := p.Get("gated")
	if st.State != StateDown {
		t.Errorf("expected down after router died, got %s (%s)", st.State, st.LastError)
	}
	if st.LastError == "" {
		t.Error("down router must carry a last error")
	}
}

func TestPollerPicksUpNewAndRemovedRouters(t *testing.T) {
	a := newMockRouter(t)
	var mu sync.Mutex
	targets := []Target{{Name: "a", URL: a.srv.URL, Password: "p"}}
	src := func(context.Context) []Target {
		mu.Lock()
		defer mu.Unlock()
		out := make([]Target, len(targets))
		copy(out, targets)
		return out
	}
	p := NewPoller(20*time.Millisecond, time.Second, src)
	p.DispatchNow(context.Background())
	if _, ok := p.Get("a"); !ok {
		t.Fatal("router a missing")
	}
	b := newMockRouter(t)
	mu.Lock()
	targets = append(targets, Target{Name: "b", URL: b.srv.URL, Password: "p"})
	targets = targets[1:] // drop a
	mu.Unlock()
	p.DispatchNow(context.Background())
	if _, ok := p.Get("a"); ok {
		t.Error("removed router a still present")
	}
	if _, ok := p.Get("b"); !ok {
		t.Error("added router b missing")
	}
}

func TestPollerBadPinnedCertMarksDown(t *testing.T) {
	p := NewPoller(20*time.Millisecond, time.Second, func(context.Context) []Target {
		return []Target{{Name: "x", URL: "https://127.0.0.1:1/", Password: "p", PinnedCertPEM: []byte("not a cert")}}
	})
	p.DispatchNow(context.Background())
	st, ok := p.Get("x")
	if !ok {
		t.Fatal("router missing")
	}
	if st.State != StateDown || !strings.Contains(st.LastError, "client setup failed") {
		t.Errorf("state=%s err=%q, want down with setup failure", st.State, st.LastError)
	}
}

func TestPollerHistoryCapped(t *testing.T) {
	r := newMockRouter(t)
	p := NewPoller(5*time.Millisecond, time.Second, func(context.Context) []Target {
		return []Target{{Name: "h", URL: r.srv.URL, Password: "p"}}
	})
	ctx := context.Background()
	for i := 0; i < HistorySamples+30; i++ {
		p.DispatchNow(ctx)
	}
	deadline := time.Now().Add(5 * time.Second)
	var st *RouterStatus
	for time.Now().Before(deadline) {
		st, _ = p.Get("h")
		if st != nil && len(st.History) >= HistorySamples {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st == nil || len(st.History) > HistorySamples {
		t.Fatalf("history not capped at %d: %d", HistorySamples, len(st.History))
	}
}

func TestSweepStaleMarksOldSamplesAmber(t *testing.T) {
	old := time.Now().Add(-(StaleIntervals + 1) * time.Minute)
	p := staticPoller(&RouterStatus{
		Name: "slowpoke", Impl: "i2pd", State: StateUp,
		Sample: sample(), LastSeen: old,
	})
	p.interval = time.Minute
	p.sweepStale()
	st, _ := p.Get("slowpoke")
	if st.State != StateStale {
		t.Errorf("state = %s, want stale after missed intervals", st.State)
	}
	// A recently-seen router must remain up.
	fresh := &RouterStatus{Name: "fresh", State: StateUp, Sample: sample(), LastSeen: time.Now()}
	p2 := staticPoller(fresh)
	p2.sweepStale()
	if st2, _ := p2.Get("fresh"); st2.State != StateUp {
		t.Errorf("fresh router state = %s, want up", st2.State)
	}
}

func TestStaleRendersAmberClass(t *testing.T) {
	stale := &RouterStatus{Name: "amber", Impl: "i2pd", State: StateStale, Sample: sample(), LastSeen: time.Now().Add(-time.Minute)}
	s := newTestServer(t, staticPoller(stale))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `class="stale"`) {
		t.Error("stale router must render amber class")
	}
}

// --- state machine: the audit found "stale" unreachable in live operation
// and every failure reported identically as "down". These drive real poll
// cycles against mock routers, not the sweep in isolation.

// TestStaleIsReachableThroughRealCycles: a router that answers and then stops
// answering must go amber first (last good data still on screen), not straight
// to red.
func TestStaleIsReachableThroughRealCycles(t *testing.T) {
	r := newMockRouter(t)
	p := NewPoller(40*time.Millisecond, 200*time.Millisecond, func(context.Context) []Target {
		return []Target{{Name: "flaky", Impl: "i2pd", URL: r.srv.URL, Password: "p"}}
	})
	ctx := context.Background()

	waitFor(t, 2*time.Second, func() bool {
		p.DispatchNow(ctx)
		st, _ := p.Get("flaky")
		return st != nil && st.State == StateUp
	})

	// Kill it: the next failure must land on stale, keeping the last sample.
	r.set(false)
	waitFor(t, 3*time.Second, func() bool {
		p.DispatchNow(ctx)
		st, _ := p.Get("flaky")
		return st != nil && st.State == StateStale
	})
	st, _ := p.Get("flaky")
	if st.State != StateStale {
		t.Fatalf("state = %s, want stale; error=%s", st.State, st.LastError)
	}
	if st.Sample == nil {
		t.Error("stale router must keep showing its last good sample")
	}
	if st.LastError == "" {
		t.Error("stale router must still carry the failure")
	}
	if st.FailureReason == ReasonNone {
		t.Error("stale router must classify why it is failing")
	}
	// History must not be extended by failed polls.
	before := len(st.History)
	p.DispatchNow(ctx)
	if after, _ := p.Get("flaky"); len(after.History) > before {
		t.Error("a failed poll must not add a sample to history")
	}
}

// TestDownAfterSustainedFailure: amber is a grace period, not a hiding place.
// Once the last good data ages past the window the router must read red.
func TestDownAfterSustainedFailure(t *testing.T) {
	r := newMockRouter(t)
	p := NewPoller(20*time.Millisecond, 150*time.Millisecond, func(context.Context) []Target {
		return []Target{{Name: "dies", Impl: "i2pd", URL: r.srv.URL, Password: "p"}}
	})
	ctx := context.Background()
	waitFor(t, 2*time.Second, func() bool {
		p.DispatchNow(ctx)
		st, _ := p.Get("dies")
		return st != nil && st.State == StateUp
	})
	r.srv.Close() // nothing listening: connection refused

	waitFor(t, 5*time.Second, func() bool {
		p.DispatchNow(ctx)
		st, _ := p.Get("dies")
		return st != nil && st.State == StateDown
	})
	st, _ := p.Get("dies")
	if st.State != StateDown {
		t.Fatalf("state = %s, want down after sustained failure", st.State)
	}
	if st.FailureReason != ReasonNotServing {
		t.Errorf("reason = %q, want %q for connection refused", st.FailureReason, ReasonNotServing)
	}
	if st.Failures < 2 {
		t.Errorf("failures = %d, want the consecutive count to be tracked", st.Failures)
	}
}

// TestRecoveryClearsFailureState closes the loop: stale/down must be able to
// return to up with the failure bookkeeping wiped.
func TestRecoveryClearsFailureState(t *testing.T) {
	r := newMockRouter(t)
	p := NewPoller(20*time.Millisecond, 150*time.Millisecond, func(context.Context) []Target {
		return []Target{{Name: "back", Impl: "i2pd", URL: r.srv.URL, Password: "p"}}
	})
	ctx := context.Background()
	waitFor(t, 2*time.Second, func() bool {
		p.DispatchNow(ctx)
		st, _ := p.Get("back")
		return st != nil && st.State == StateUp
	})
	r.set(false)
	waitFor(t, 3*time.Second, func() bool {
		p.DispatchNow(ctx)
		st, _ := p.Get("back")
		return st != nil && st.State != StateUp
	})
	r.set(true)
	waitFor(t, 3*time.Second, func() bool {
		p.DispatchNow(ctx)
		st, _ := p.Get("back")
		return st != nil && st.State == StateUp
	})
	st, _ := p.Get("back")
	if st.FailureReason != ReasonNone || st.Failures != 0 || st.LastError != "" {
		t.Errorf("recovered router still carries failure state: %+v", st)
	}
}

// TestFailureReasonClassification locks the diagnosis an operator reads.
func TestFailureReasonClassification(t *testing.T) {
	mk := func(msg string) error { return errors.New(msg) }
	cases := []struct {
		err  error
		want string
	}{
		{mk(`i2pcontrol: request to router r failed: Post "http://172.28.0.5:7650/": dial tcp: connect: connection refused`), ReasonNotServing},
		{mk("i2pcontrol: request timed out: router r"), ReasonTimeout},
		{mk("context deadline exceeded"), ReasonTimeout},
		{mk("i2pcontrol: rpc error -32001: Invalid password"), ReasonAuth},
		{mk("i2pcontrol: router returned no authentication token"), ReasonAuth},
		{mk("i2pcontrol: router r: HTTP status 503"), ReasonProtocol},
		{mk("i2pcontrol: router r: malformed JSON response"), ReasonProtocol},
		{mk("client setup failed: bad pinned cert"), ReasonInternal},
		{mk("dial tcp: lookup host: no such host"), ReasonUnreachable},
		{nil, ReasonNone},
	}
	for _, c := range cases {
		if got := classifyFailure(c.err); got != c.want {
			t.Errorf("classifyFailure(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// TestNetworkTotalsAggregateAcrossRouters covers capability 2: the network
// view must sum the values routers report, and must not invent a zero for a
// value nobody reports.
func TestNetworkTotalsAggregateAcrossRouters(t *testing.T) {
	a := &RouterStatus{Name: "a", Impl: "i2pd", State: StateUp, Sample: &i2pcontrol.Sample{
		KnownPeers: i64(3), ActivePeers: i64(1), BWInbound1s: f64(1024), CollectedAt: time.Now(),
	}, LastSeen: time.Now()}
	b := &RouterStatus{Name: "b", Impl: "goi2p", State: StateDown, Sample: &i2pcontrol.Sample{
		KnownPeers: i64(7), CollectedAt: time.Now(),
	}, LastSeen: time.Now()}
	c := &RouterStatus{Name: "c", Impl: "i2pd", State: StateUp, Sample: &i2pcontrol.Sample{
		// TunnelsInbound nil: i2pd does not serve it.
		Participating: i64(2), CollectedAt: time.Now(),
	}, LastSeen: time.Now()}
	p := staticPoller(a, b, c)
	p.interval = time.Minute

	n := p.NetworkTotals()
	if n.Routers != 3 {
		t.Errorf("routers = %d, want 3", n.Routers)
	}
	if n.Up != 2 {
		t.Errorf("up = %d, want 2", n.Up)
	}
	if n.KnownPeers == nil || *n.KnownPeers != 10 {
		t.Errorf("known peers = %v, want 10 summed", n.KnownPeers)
	}
	if n.ActivePeers == nil || *n.ActivePeers != 1 {
		t.Errorf("active peers = %v, want 1", n.ActivePeers)
	}
	if n.TunnelsInbound != nil {
		t.Errorf("tunnels inbound = %v, want nil: no router reported it, so the total must not be a fake zero", *n.TunnelsInbound)
	}
	if n.TunnelsParticip == nil || *n.TunnelsParticip != 2 {
		t.Errorf("participating = %v, want 2", n.TunnelsParticip)
	}
	if n.BWInbound1s == nil || *n.BWInbound1s != 1024 {
		t.Errorf("bw in = %v, want 1024", n.BWInbound1s)
	}
}

// TestNetworkTotalsIgnoreStaleData: summing a router that died ten minutes
// ago would silently inflate the network view.
func TestNetworkTotalsIgnoreStaleData(t *testing.T) {
	fresh := &RouterStatus{Name: "fresh", State: StateUp, LastSeen: time.Now(),
		Sample: &i2pcontrol.Sample{KnownPeers: i64(5), CollectedAt: time.Now()}}
	ancient := &RouterStatus{Name: "ancient", State: StateDown, LastSeen: time.Now().Add(-time.Hour),
		Sample: &i2pcontrol.Sample{KnownPeers: i64(99), CollectedAt: time.Now().Add(-time.Hour)}}
	p := staticPoller(fresh, ancient)
	p.interval = time.Minute

	n := p.NetworkTotals()
	if n.KnownPeers == nil || *n.KnownPeers != 5 {
		t.Errorf("known peers = %v, want 5 (the hour-old sample must not count)", n.KnownPeers)
	}
}

// TestNetworkHistoryIsRecordedAndCapped backs the network charts.
func TestNetworkHistoryIsRecordedAndCapped(t *testing.T) {
	r := newMockRouter(t)
	p := NewPoller(5*time.Millisecond, time.Second, func(context.Context) []Target {
		return []Target{{Name: "h", URL: r.srv.URL, Password: "p"}}
	})
	ctx := context.Background()
	for i := 0; i < HistorySamples+25; i++ {
		p.DispatchNow(ctx)
	}
	waitFor(t, 5*time.Second, func() bool { return len(p.NetworkHistory()) >= HistorySamples })
	h := p.NetworkHistory()
	if len(h) > HistorySamples {
		t.Errorf("network history = %d, capped at %d", len(h), HistorySamples)
	}
	if !h[0].CollectedAt.After(h[len(h)-1].CollectedAt) {
		t.Error("network history must be newest first")
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
