package i2pcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// rpcReq mirrors jsonrpcRequest for server-side test parsing.
type rpcReq struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      string                 `json:"id"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params"`
}

func writeResult(w http.ResponseWriter, result interface{}) {
	b, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"result":  result,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	b, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"error":   map[string]interface{}{"code": code, "message": msg},
	})
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// newMockRouter returns a test server that behaves like a spec-correct
// I2PControl endpoint: Authenticate with the right password yields a token;
// RouterInfo requires the token.
func newMockRouter(t *testing.T, password string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, ErrCodeParseError, "bad json")
			return
		}
		switch req.Method {
		case "Authenticate":
			if req.Params["Password"] != password {
				writeError(w, ErrCodeAuthFailed, "Invalid password")
				return
			}
			writeResult(w, map[string]interface{}{"API": 1, "Token": "tok-123"})
		case "RouterInfo":
			if req.Params["Token"] != "tok-123" {
				writeError(w, ErrCodeTokenNotExist, "Authentication token does not exist or has expired")
				return
			}
			writeResult(w, map[string]interface{}{
				"i2p.router.uptime":                    123456,
				"i2p.router.version":                   "2.60.0",
				"i2p.router.status":                    "0",
				"i2p.router.net.status":                float64(2),
				"i2p.router.net.tunnels.inbound":       float64(3),
				"i2p.router.net.tunnels.outbound":      float64(4),
				"i2p.router.net.bw.inbound.1s":         1.5,
				"i2p.router.net.bw.outbound.1s":        2.5,
				"i2p.router.netdb.knownpeers":          float64(7),
				"i2p.router.netdb.activepeers":         float64(5),
				"i2p.router.net.tunnels.participating": float64(1),
			})
		default:
			writeError(w, ErrCodeMethodNotFound, "method not found")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAuthenticateAndRouterInfo(t *testing.T) {
	srv := newMockRouter(t, "sesame")
	c, err := New("router-1", srv.URL, "sesame")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	s, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if s.Version != "2.60.0" {
		t.Errorf("Version = %q, want 2.60.0", s.Version)
	}
	if s.UptimeMs == nil || *s.UptimeMs != 123456 {
		t.Errorf("UptimeMs = %v, want 123456", s.UptimeMs)
	}
	// i2pd sends status as the string "0"; it must decode to the netstatus name.
	if s.Status != "OK" {
		t.Errorf("Status = %q, want OK", s.Status)
	}
	if s.NetStatus == nil || *s.NetStatus != 2 || s.NetStatusName != "FIREWALLED" {
		t.Errorf("NetStatus = %v/%q, want 2/FIREWALLED", s.NetStatus, s.NetStatusName)
	}
	if s.KnownPeers == nil || *s.KnownPeers != 7 {
		t.Errorf("KnownPeers = %v, want 7", s.KnownPeers)
	}
	if s.BWInbound1s == nil || *s.BWInbound1s != 1.5 {
		t.Errorf("BWInbound1s = %v, want 1.5", s.BWInbound1s)
	}
	if c.currentToken() != "tok-123" {
		t.Errorf("token not cached: %q", c.currentToken())
	}
}

func TestWrongPasswordFails(t *testing.T) {
	srv := newMockRouter(t, "sesame")
	c, _ := New("router-1", srv.URL, "wrong")
	if _, err := c.Poll(context.Background()); err == nil {
		t.Fatal("expected auth failure")
	} else if !strings.Contains(err.Error(), "Auth") && !strings.Contains(err.Error(), "auth") && !isAuthError(err) {
		// error message must not leak the password
		if strings.Contains(err.Error(), "wrong") {
			t.Errorf("error leaks password: %v", err)
		}
	}
}

// i2pd 2.60 quirk: wrong password returns {"result":{}} (no Token, no error).
func TestI2PDEmptyResultIsAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResult(w, map[string]interface{}{})
	}))
	defer srv.Close()
	c, _ := New("r", srv.URL, "p")
	_, err := c.Poll(context.Background())
	if err == nil {
		t.Fatal("expected error for empty Authenticate result")
	}
	if !strings.Contains(err.Error(), "no authentication token") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReauthOnExpiredToken(t *testing.T) {
	var authCalls, riCalls int32
	var current atomic.Value
	current.Store("tok-A")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcReq
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "Authenticate":
			n := atomic.AddInt32(&authCalls, 1)
			tok := fmt.Sprintf("tok-%c", 'A'+n-1)
			current.Store(tok)
			writeResult(w, map[string]interface{}{"API": 1, "Token": tok})
		case "RouterInfo":
			atomic.AddInt32(&riCalls, 1)
			if req.Params["Token"] != current.Load().(string) {
				writeError(w, ErrCodeTokenNotExist, "expired")
				return
			}
			writeResult(w, map[string]interface{}{"i2p.router.version": "x"})
		}
	}))
	defer srv.Close()

	c, _ := New("r", srv.URL, "p")
	// Pre-load a stale token to simulate expiry.
	c.token = "tok-STALE"
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll after token expiry: %v", err)
	}
	if got := atomic.LoadInt32(&authCalls); got < 1 {
		t.Errorf("expected re-authentication, authCalls=%d", got)
	}
	// Second poll should reuse the now-valid token (no extra auth).
	before := atomic.LoadInt32(&authCalls)
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if after := atomic.LoadInt32(&authCalls); after != before {
		t.Errorf("token not cached after re-auth: authCalls %d -> %d", before, after)
	}
}

func TestMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()
	c, _ := New("r", srv.URL, "p")
	_, err := c.Poll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "malformed JSON") {
		t.Fatalf("expected malformed JSON error, got %v", err)
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcReq
		json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "Authenticate" {
			time.Sleep(300 * time.Millisecond)
		}
		writeResult(w, map[string]interface{}{})
	}))
	defer srv.Close()
	c, _ := New("slow-router", srv.URL, "p", WithTimeout(80*time.Millisecond))
	start := time.Now()
	_, err := c.Poll(context.Background())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("expected ErrTimeout, got %v (%T)", err, err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("timeout not honored: took %v", elapsed)
	}
}

func TestHTTPStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c, _ := New("r", srv.URL, "p")
	if _, err := c.Poll(context.Background()); err == nil {
		t.Fatal("expected error on 503")
	}
}

func TestDecodeSampleStringNumbers(t *testing.T) {
	// Older i2pd/Java routers return uptime and bw as strings.
	fields := map[string]json.RawMessage{
		PropUptime:       json.RawMessage(`"99000"`),
		PropVersion:      json.RawMessage(`"0.9.70"`),
		PropStatus:       json.RawMessage(`"RUNNING"`),
		PropBWInbound15s: json.RawMessage(`"12.5"`),
		PropKnownPeers:   json.RawMessage(`"12"`),
		PropNetStatus:    json.RawMessage(`"1"`),
	}
	s := decodeSample(fields)
	if s.UptimeMs == nil || *s.UptimeMs != 99000 {
		t.Errorf("string uptime not decoded: %v", s.UptimeMs)
	}
	if s.Status != "RUNNING" {
		t.Errorf("free-text status mangled: %q", s.Status)
	}
	if s.BWInbound15s == nil || *s.BWInbound15s != 12.5 {
		t.Errorf("string bw not decoded: %v", s.BWInbound15s)
	}
	if s.NetStatusName != "TESTING" {
		t.Errorf("net status name = %q", s.NetStatusName)
	}
	if s.KnownPeers == nil || *s.KnownPeers != 12 {
		t.Errorf("string knownpeers not decoded: %v", s.KnownPeers)
	}
}

func TestMissingPropertiesAreNil(t *testing.T) {
	s := decodeSample(map[string]json.RawMessage{
		PropVersion: json.RawMessage(`"2.60.0"`),
	})
	if s.UptimeMs != nil || s.KnownPeers != nil || s.BWInbound1s != nil {
		t.Error("absent properties must decode to nil")
	}
}

func TestErrorMessagesRedactSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, ErrCodeInternalError, "failed for token tok-123 with password sesame")
	}))
	defer srv.Close()
	c, _ := New("r", srv.URL, "sesame")
	c.token = "tok-123"
	_, err := c.Poll(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "tok-123") || strings.Contains(err.Error(), "sesame") {
		t.Errorf("error leaked secrets: %v", err)
	}
}

func TestClosedClient(t *testing.T) {
	srv := newMockRouter(t, "p")
	c, _ := New("r", srv.URL, "p")
	c.Close()
	if _, err := c.Poll(context.Background()); err == nil {
		t.Fatal("expected error after Close")
	}
}
