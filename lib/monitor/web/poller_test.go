package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
