package i2pcontrol

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout bounds a single RPC round-trip (dial+TLS+request+response).
const DefaultTimeout = 5 * time.Second

// JSON-RPC error codes defined by the I2PControl spec (as implemented by
// go-i2p master and the Java router). A JSON-RPC error with one of the auth
// codes means our cached token is no longer usable and we must
// re-authenticate.
const (
	ErrCodeParseError     = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternalError  = -32603

	ErrCodeAuthRequired  = -32000 // no authentication token presented
	ErrCodeAuthFailed    = -32001 // authentication failed (invalid password)
	ErrCodeNotImpl       = -32002
	ErrCodeTokenNotExist = -32003 // token does not exist (expired/invalid)
	ErrCodeTokenExpired  = -32004 // token expired and removed
)

// RouterInfo property keys requested by the dashboard. The set is the union
// of what i2pd and go-i2p serve; each implementation answers only the keys
// it knows (verified against live routers — see docs/monitoring-discovery.md).
var (
	PropUptime          = "i2p.router.uptime"
	PropVersion         = "i2p.router.version"
	PropStatus          = "i2p.router.status"
	PropNetStatus       = "i2p.router.net.status"
	PropTunnelsInbound  = "i2p.router.net.tunnels.inbound"
	PropTunnelsOutbound = "i2p.router.net.tunnels.outbound"
	PropTunnelsParticip = "i2p.router.net.tunnels.participating"
	PropBWInbound1s     = "i2p.router.net.bw.inbound.1s"
	PropBWInbound15s    = "i2p.router.net.bw.inbound.15s"
	PropBWOutbound1s    = "i2p.router.net.bw.outbound.1s"
	PropBWOutbound15s   = "i2p.router.net.bw.outbound.15s"
	PropKnownPeers      = "i2p.router.netdb.knownpeers"
	PropActivePeers     = "i2p.router.netdb.activepeers"
	PropFastPeers       = "i2p.router.netdb.fastpeers"
	PropHighCapPeers    = "i2p.router.netdb.highcapacitypeers"

	// RequestedProperties is the property set sent in every RouterInfo call.
	RequestedProperties = []string{
		PropUptime, PropVersion, PropStatus, PropNetStatus,
		PropTunnelsInbound, PropTunnelsOutbound, PropTunnelsParticip,
		PropBWInbound1s, PropBWInbound15s, PropBWOutbound1s, PropBWOutbound15s,
		PropKnownPeers, PropActivePeers, PropFastPeers, PropHighCapPeers,
	}
)

// netstatusNames maps the I2PControl network status codes (spec enum) to
// human-readable names.
var netstatusNames = map[int]string{
	0:  "OK",
	1:  "TESTING",
	2:  "FIREWALLED",
	3:  "HIDDEN",
	4:  "WARN_FIREWALLED_AND_FAST",
	5:  "WARN_FIREWALLED_AND_FLOODFILL",
	6:  "WARN_FIREWALLED_WITH_INBOUND_TCP",
	7:  "WARN_FIREWALLED_WITH_UDP_DISABLED",
	8:  "ERROR_I2CP",
	9:  "ERROR_CLOCK_SKEW",
	10: "ERROR_PRIVATE_TCP_ADDRESS",
	11: "ERROR_SYMMETRIC_NAT",
	12: "ERROR_UDP_PORT_IN_USE",
	13: "ERROR_NO_ACTIVE_PEERS_CHECK_CONNECTION_AND_FIREWALL",
	14: "ERROR_UDP_DISABLED_AND_TCP_UNSET",
}

// NetStatusName maps an I2PControl network status code to its name.
func NetStatusName(code int) string {
	if n, ok := netstatusNames[code]; ok {
		return n
	}
	return "UNKNOWN(" + strconv.Itoa(code) + ")"
}

// Sample is one decoded RouterInfo poll result. Properties the router did
// not return are nil (numeric) or empty (strings) and rendered as "—" by
// the UI.
type Sample struct {
	UptimeMs          *int64
	Version           string
	Status            string
	NetStatus         *int
	NetStatusName     string
	TunnelsInbound    *int64
	TunnelsOutbound   *int64
	Participating     *int64
	BWInbound1s       *float64
	BWInbound15s      *float64
	BWOutbound1s      *float64
	BWOutbound15s     *float64
	KnownPeers        *int64
	ActivePeers       *int64
	FastPeers         *int64
	HighCapacityPeers *int64
	CollectedAt       time.Time
}

// Client is a single-router I2PControl JSON-RPC client with token caching.
// It is safe for concurrent use. The password and tokens are never included
// in returned errors.
type Client struct {
	name     string // router display name, for error messages only
	url      string // e.g. https://172.28.0.2:7650/
	password string
	timeout  time.Duration
	httpc    *http.Client

	mu     sync.Mutex
	token  string
	nextID int
	closed bool
}

// Option configures a Client.
type Option func(*clientConfig)

type clientConfig struct {
	timeout     time.Duration
	pinnedCert  []byte // DER
	insecureTLS bool
}

// WithTimeout sets the per-request timeout (default DefaultTimeout).
func WithTimeout(d time.Duration) Option { return func(c *clientConfig) { c.timeout = d } }

// WithPinnedCertPEM trusts only the given PEM certificate — the router's own
// generated i2pcontrol.crt.pem — by comparing the presented leaf certificate
// byte-for-byte. This is how i2pd's unavoidable self-signed cert is handled
// without disabling verification.
func WithPinnedCertPEM(pem []byte) Option {
	return func(c *clientConfig) { c.pinnedCert = pem }
}

// WithInsecureTLS skips TLS verification entirely. Operator opt-in only; the
// dashboard prints a loud warning whenever this is active.
func WithInsecureTLS() Option { return func(c *clientConfig) { c.insecureTLS = true } }

// New creates a Client for the router display name at url with password.
func New(name, url, password string, opts ...Option) (*Client, error) {
	cfg := &clientConfig{timeout: DefaultTimeout}
	for _, o := range opts {
		o(cfg)
	}
	if cfg.timeout <= 0 {
		cfg.timeout = DefaultTimeout
	}
	tlsCfg, err := buildTLSConfig(name, cfg)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: cfg.timeout}).DialContext,
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   cfg.timeout,
		DisableKeepAlives:     true, // avoid pooled sockets to dead routers
		ResponseHeaderTimeout: cfg.timeout,
	}
	return &Client{
		name:     name,
		url:      url,
		password: password,
		timeout:  cfg.timeout,
		httpc:    &http.Client{Transport: transport, Timeout: cfg.timeout},
	}, nil
}

// decodePEMBlock extracts the first PEM CERTIFICATE block as DER.
func decodePEMBlock(pem []byte) ([]byte, error) {
	const begin = "-----BEGIN CERTIFICATE-----"
	const end = "-----END CERTIFICATE-----"
	s := string(pem)
	start := strings.Index(s, begin)
	if start < 0 {
		return nil, errors.New("no PEM certificate block found")
	}
	body := s[start+len(begin):]
	stop := strings.Index(body, end)
	if stop < 0 {
		return nil, errors.New("unterminated PEM certificate block")
	}
	var b64 strings.Builder
	for _, r := range body[:stop] {
		if r != '\n' && r != '\r' && r != ' ' && r != '\t' {
			b64.WriteRune(r)
		}
	}
	return base64.StdEncoding.DecodeString(b64.String())
}

func buildTLSConfig(name string, cfg *clientConfig) (*tls.Config, error) {
	switch {
	case cfg.insecureTLS:
		//nolint:gosec // explicit operator opt-in; caller warns loudly
		return &tls.Config{InsecureSkipVerify: true}, nil
	case len(cfg.pinnedCert) > 0:
		der, err := decodePEMBlock(cfg.pinnedCert)
		if err != nil {
			return nil, fmt.Errorf("router %s: bad pinned cert: %w", name, err)
		}
		if _, err := x509.ParseCertificate(der); err != nil {
			return nil, fmt.Errorf("router %s: bad pinned cert: %w", name, err)
		}
		return &tls.Config{
			// The i2pd/go-i2p I2PControl cert is self-signed with no
			// SANs, so normal chain+hostname verification can never
			// succeed. We replace it with strict byte-for-byte pinning
			// of the router's own certificate.
			//nolint:gosec // InsecureSkipVerify is required to bypass chain building; VerifyPeerCertificate below does real, stricter checking.
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				for _, raw := range rawCerts {
					if bytes.Equal(raw, der) {
						return nil
					}
				}
				return fmt.Errorf("tls: router %s presented a certificate that does not match the pinned one", name)
			},
		}, nil
	default:
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	}
}

// Name returns the router display name.
func (c *Client) Name() string { return c.name }

// Close marks the client unusable and drops the cached token.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.token = ""
	c.httpc.CloseIdleConnections()
	return nil
}

// rpcError is a JSON-RPC level error returned by the router.
type rpcError struct {
	Code    int
	Message string
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("i2pcontrol: rpc error %d: %s", e.Code, e.Message)
}

// isAuthError reports whether err is an auth failure a fresh token could fix.
func isAuthError(err error) bool {
	var re *rpcError
	if !errors.As(err, &re) {
		return false
	}
	switch re.Code {
	case ErrCodeAuthRequired, ErrCodeAuthFailed, ErrCodeTokenNotExist, ErrCodeTokenExpired:
		return true
	}
	return false
}

type jsonrpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      string      `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call performs one JSON-RPC call. Errors never contain the password or a
// token.
func (c *Client) call(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("i2pcontrol: client closed")
	}
	c.nextID++
	id := strconv.Itoa(c.nextID)
	token := c.token
	c.mu.Unlock()

	body, err := json.Marshal(jsonrpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		// Transport errors contain only the URL (which we build ourselves
		// without userinfo) — never the password or token.
		return nil, fmt.Errorf("i2pcontrol: request to router %s failed: %s", c.name, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("i2pcontrol: router %s: reading response: %v", c.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("i2pcontrol: router %s: HTTP status %d", c.name, resp.StatusCode)
	}
	var jr jsonrpcResponse
	if err := json.Unmarshal(raw, &jr); err != nil {
		return nil, fmt.Errorf("i2pcontrol: router %s: malformed JSON response", c.name)
	}
	if jr.Error != nil {
		return nil, &rpcError{Code: jr.Error.Code, Message: redact(jr.Error.Message, token, c.password)}
	}
	return jr.Result, nil
}

// Authenticate obtains and caches a fresh token.
func (c *Client) Authenticate(ctx context.Context) error {
	res, err := c.call(ctx, "Authenticate", map[string]interface{}{
		"API":      1,
		"Password": c.password,
	})
	if err != nil {
		return err
	}
	var r struct {
		Token string `json:"Token"`
		API   int    `json:"API"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return errors.New("i2pcontrol: malformed Authenticate result")
	}
	if r.Token == "" {
		// i2pd 2.60 answers {"result":{}} for a wrong password instead of a
		// JSON-RPC error — treat a missing token as an auth failure.
		return &rpcError{Code: ErrCodeAuthFailed, Message: "router returned no authentication token"}
	}
	c.mu.Lock()
	c.token = r.Token
	c.mu.Unlock()
	return nil
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// RouterInfo fetches the requested properties, authenticating first if
// necessary and retrying exactly once after a re-authentication on auth
// errors.
func (c *Client) RouterInfo(ctx context.Context) (*Sample, error) {
	if c.currentToken() == "" {
		if err := c.Authenticate(ctx); err != nil {
			return nil, err
		}
	}
	s, err := c.routerInfoWithToken(ctx, c.currentToken())
	if err != nil && isAuthError(err) {
		if aerr := c.Authenticate(ctx); aerr != nil {
			return nil, aerr
		}
		return c.routerInfoWithToken(ctx, c.currentToken())
	}
	return s, err
}

func (c *Client) routerInfoWithToken(ctx context.Context, token string) (*Sample, error) {
	params := map[string]interface{}{"Token": token}
	for _, p := range RequestedProperties {
		params[p] = ""
	}
	res, err := c.call(ctx, "RouterInfo", params)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(res, &fields); err != nil {
		return nil, errors.New("i2pcontrol: malformed RouterInfo result")
	}
	return decodeSample(fields), nil
}

// ErrTimeout reports a per-router poll timeout.
var ErrTimeout = errors.New("i2pcontrol: request timed out")

// Poll performs a RouterInfo query bounded by the client timeout, converts
// transport timeouts into ErrTimeout, and recovers from unexpected panics
// so one bad router cannot take down the dashboard.
func (c *Client) Poll(ctx context.Context) (s *Sample, err error) {
	defer func() {
		if r := recover(); r != nil {
			_ = debug.Stack() // keep the trace out of user-visible errors
			err = fmt.Errorf("i2pcontrol: router %s: internal error while polling", c.name)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	s, err = c.RouterInfo(ctx)
	if err != nil {
		if isTimeoutErr(err) {
			return nil, fmt.Errorf("%w: router %s", ErrTimeout, c.name)
		}
		return nil, err
	}
	return s, nil
}

func isTimeoutErr(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var nerr interface{ Timeout() bool }
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "context deadline exceeded")
}

// decodeSample maps the property map onto Sample, tolerating both JSON
// number and JSON string encodings for the same field (i2pd sends uptime as
// a number and status as the string "0"; older builds and other
// implementations vary).
func decodeSample(fields map[string]json.RawMessage) *Sample {
	s := &Sample{CollectedAt: time.Now().UTC()}

	getInt := func(key string) *int64 {
		raw, ok := fields[key]
		if !ok {
			return nil
		}
		var v int64
		if err := json.Unmarshal(raw, &v); err == nil {
			return &v
		}
		var f float64
		if err := json.Unmarshal(raw, &f); err == nil {
			i := int64(f)
			return &i
		}
		var str string
		if err := json.Unmarshal(raw, &str); err == nil {
			if i, err := strconv.ParseInt(strings.TrimSpace(str), 10, 64); err == nil {
				return &i
			}
			if f, err := strconv.ParseFloat(strings.TrimSpace(str), 64); err == nil {
				i := int64(f)
				return &i
			}
		}
		return nil
	}
	getFloat := func(key string) *float64 {
		raw, ok := fields[key]
		if !ok {
			return nil
		}
		var v float64
		if err := json.Unmarshal(raw, &v); err == nil {
			return &v
		}
		var str string
		if err := json.Unmarshal(raw, &str); err == nil {
			if f, err := strconv.ParseFloat(strings.TrimSpace(str), 64); err == nil {
				return &f
			}
		}
		return nil
	}
	getStr := func(key string) string {
		raw, ok := fields[key]
		if !ok {
			return ""
		}
		var str string
		if err := json.Unmarshal(raw, &str); err == nil {
			return str
		}
		return string(raw) // numbers etc. keep their JSON text form
	}

	s.UptimeMs = getInt(PropUptime)
	s.Version = getStr(PropVersion)
	s.TunnelsInbound = getInt(PropTunnelsInbound)
	s.TunnelsOutbound = getInt(PropTunnelsOutbound)
	s.Participating = getInt(PropTunnelsParticip)
	s.BWInbound1s = getFloat(PropBWInbound1s)
	s.BWInbound15s = getFloat(PropBWInbound15s)
	s.BWOutbound1s = getFloat(PropBWOutbound1s)
	s.BWOutbound15s = getFloat(PropBWOutbound15s)
	s.KnownPeers = getInt(PropKnownPeers)
	s.ActivePeers = getInt(PropActivePeers)
	s.FastPeers = getInt(PropFastPeers)
	s.HighCapacityPeers = getInt(PropHighCapPeers)

	if ns := getInt(PropNetStatus); ns != nil {
		code := int(*ns)
		s.NetStatus = &code
		s.NetStatusName = NetStatusName(code)
	}
	statusStr := getStr(PropStatus)
	if statusStr != "" {
		// i2pd sends a numeric code as a string ("0"); translate it.
		if code, ok := parseStatusAsCode(statusStr); ok {
			s.Status = NetStatusName(code)
			if s.NetStatus == nil {
				c := code
				s.NetStatus = &c
				s.NetStatusName = NetStatusName(code)
			}
		} else {
			s.Status = statusStr
		}
	}
	return s
}

func parseStatusAsCode(str string) (int, bool) {
	str = strings.TrimSpace(str)
	v, err := strconv.Atoi(str)
	if err != nil || v < 0 || v > 14 {
		return 0, false
	}
	if _, isKnown := netstatusNames[v]; !isKnown {
		return 0, false
	}
	return v, true
}

// redact removes secret substrings (token, password) from server-provided
// error messages.
func redact(msg, token, password string) string {
	if token != "" && strings.Contains(msg, token) {
		msg = strings.ReplaceAll(msg, token, "[redacted]")
	}
	if password != "" && strings.Contains(msg, password) {
		msg = strings.ReplaceAll(msg, password, "[redacted]")
	}
	return msg
}
