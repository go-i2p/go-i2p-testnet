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
)

func newTestServer(t *testing.T, p *Poller) *Server {
	t.Helper()
	s, err := NewServer(ServerConfig{Poller: p, PollInterval: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }

func sample() *i2pcontrol.Sample {
	return &i2pcontrol.Sample{
		UptimeMs:    i64(90000),
		Version:     "2.60.0",
		Status:      "OK",
		NetStatus:   new(int),
		CollectedAt: time.Now().UTC(),
		KnownPeers:  i64(3),
		BWInbound1s: f64(1024),
	}
}

func staticPoller(statuses ...*RouterStatus) *Poller {
	p := NewPoller(time.Hour, time.Second, nil)
	for _, st := range statuses {
		p.statuses[st.Name] = st
	}
	return p
}

func TestHealthz(t *testing.T) {
	s := newTestServer(t, staticPoller())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("healthz = %d, want 200", rec.Code)
	}
}

func TestIndexRenders(t *testing.T) {
	up := &RouterStatus{Name: "router-i2pd-1", Impl: "i2pd", State: StateUp, Sample: sample(), LastSeen: time.Now()}
	down := &RouterStatus{Name: "router-goi2p-1", Impl: "goi2p", State: StateDown, LastError: "connection refused"}
	s := newTestServer(t, staticPoller(up, down))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"router-i2pd-1", "router-goi2p-1", "connection refused", "2.60.0", "Routers: <b>2</b>"} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %q", want)
		}
	}
	if !strings.Contains(body, `http-equiv="refresh" content="5"`) {
		t.Error("index must auto-refresh every 5s")
	}
	// down router must render the red state class
	if !strings.Contains(body, `class="down"`) {
		t.Error("down router not marked with down class")
	}
}

// TestEscaping is the security core: every router-supplied string must be
// HTML-escaped so a hostile router (or hostile version/status/error text)
// cannot inject script into the dashboard.
func TestEscaping(t *testing.T) {
	bad := `<script>alert("xss")</script>`
	evilSample := sample()
	evilSample.Version = bad
	evilSample.Status = bad
	st := &RouterStatus{
		Name:      "evil\"><script>x</script>",
		Impl:      bad,
		URL:       "http://example.com/" + bad,
		State:     StateDown,
		LastError: bad,
		Sample:    evilSample,
	}
	p := staticPoller(st)
	s := newTestServer(t, p)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(") {
		t.Fatal("index: raw <script> from router data made it through")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("index: expected escaped form of router string")
	}

	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/router/evil%22%3E%3Cscript%3Ex%3C%2Fscript%3E", nil))
	if rec2.Code == http.StatusOK && strings.Contains(rec2.Body.String(), "<script>x</script>") {
		t.Fatal("router page: raw <script> from router name made it through")
	}
}

func TestRouterDetailPage(t *testing.T) {
	st := sample()
	up := &RouterStatus{Name: "r1", Impl: "i2pd", State: StateUp, Sample: st, LastSeen: st.CollectedAt,
		History: []*i2pcontrol.Sample{st, st}}
	s := newTestServer(t, staticPoller(up))

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/router/r1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /router/r1 = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Last 2 samples") {
		t.Error("detail page must show recent samples")
	}

	rec404 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec404, httptest.NewRequest(http.MethodGet, "/router/nope", nil))
	if rec404.Code != http.StatusNotFound {
		t.Errorf("unknown router = %d, want 404", rec404.Code)
	}
}

func TestAPIStatusJSONShape(t *testing.T) {
	up := &RouterStatus{Name: "r1", Impl: "i2pd", URL: "https://172.28.0.2:7650/", State: StateUp,
		Sample: sample(), LastSeen: time.Now(), History: []*i2pcontrol.Sample{sample()}}
	s := newTestServer(t, staticPoller(up))

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content type = %q", ct)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{"generated_at", "refresh_seconds", "totals", "routers"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing top-level key %q", key)
		}
	}
	tot := got["totals"].(map[string]interface{})
	for _, key := range []string{"routers", "up", "down", "stale", "pending"} {
		if _, ok := tot[key]; !ok {
			t.Errorf("missing totals key %q", key)
		}
	}
	rs := got["routers"].([]interface{})
	if len(rs) != 1 {
		t.Fatalf("routers len = %d, want 1", len(rs))
	}
	r0 := rs[0].(map[string]interface{})
	for _, key := range []string{"name", "impl", "url", "state", "sample", "history"} {
		if _, ok := r0[key]; !ok {
			t.Errorf("missing router key %q", key)
		}
	}
	sm := r0["sample"].(map[string]interface{})
	for _, key := range []string{"uptime_ms", "version", "status", "net_status", "tunnels_inbound",
		"known_peers", "active_peers", "bw_inbound_1s", "bw_outbound_15s", "collected_at"} {
		if _, ok := sm[key]; !ok {
			t.Errorf("missing sample key %q", key)
		}
	}
	// Passwords/tokens must never be serialized.
	if strings.Contains(rec.Body.String(), "Password") || strings.Contains(rec.Body.String(), "Token") {
		t.Error("API payload must not mention secrets")
	}
}

func TestUnknownPaths404(t *testing.T) {
	s := newTestServer(t, staticPoller())
	for _, path := range []string{"/nope", "/router/", "/router/a/b"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("%s returned 200", path)
		}
	}
}

func TestServerServeShutdown(t *testing.T) {
	p := staticPoller()
	s, err := NewServer(ServerConfig{BindAddr: "127.0.0.1:0", Poller: p})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(ctx) }()
	// Give the listener a moment; then cancel and expect a clean exit.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("Serve returned error after shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after context cancel")
	}
}
