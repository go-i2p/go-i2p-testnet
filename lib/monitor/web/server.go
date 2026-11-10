package web

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"go-i2p-testnet/lib/monitor/i2pcontrol"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// ServerConfig configures the dashboard HTTP server.
type ServerConfig struct {
	// BindAddr defaults to "127.0.0.1:8765" — the dashboard binds to
	// loopback unless the operator explicitly asks for more.
	BindAddr string
	Poller   *Poller
	// PollInterval is advertised to the browser for auto-refresh.
	PollInterval time.Duration
}

// Server is the embedded dashboard web server.
type Server struct {
	cfg     ServerConfig
	mux     *http.ServeMux
	httpc   *http.Server
	tmpl    *template.Template
	started time.Time
}

// NewServer builds the dashboard server. All router-supplied strings are
// rendered through html/template (auto-escaped) — see escaping_test.go.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.BindAddr == "" {
		cfg.BindAddr = "127.0.0.1:8765"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	funcs := template.FuncMap{
		"bytesPerSec": formatBytesPerSec,
		"duration":    formatDuration,
		"ago":         formatAgo,
		"statusClass": statusClass,
		"orDash":      orDash,
		"numI":        numI64,
		"numF":        numF64,
		"numPct":      numPtr,
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.tmpl")
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:     cfg,
		tmpl:    tmpl,
		started: time.Now(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/router/", s.handleRouter)
	mux.HandleFunc("/api/status", s.handleAPIStatus)
	mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux = mux
	s.httpc = &http.Server{
		Addr:              cfg.BindAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s, nil
}

// Handler exposes the mux for tests.
func (s *Server) Handler() http.Handler { return s.mux }

// Serve runs the dashboard until ctx is cancelled or the listener fails.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.BindAddr)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() { errc <- s.httpc.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.httpc.Shutdown(shutCtx)
		return nil
	case err := <-errc:
		return err
	}
}

// Addr returns the actual bound address (useful when port 0 was requested).
func (s *Server) Addr() string { return s.httpc.Addr }

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/healthz" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// RouterRow is a display-ready view of one router: every value is
// precomputed in Go so templates never dereference a nil Sample. Field
// strings are still escaped by html/template at render time.
type RouterRow struct {
	Name          string
	Impl          string
	URL           string
	State         string
	StateClass    string
	Version       string
	NetStatusName string
	Uptime        string
	TunIn         string
	TunOut        string
	TunPart       string
	KnownPeers    string
	ActivePeers   string
	FastPeers     string
	HighCapPeers  string
	BWIn1s        string
	BWIn15s       string
	BWOut1s       string
	BWOut15s      string
	Status        string
	LastError     string
	LastSeen      string
}

func rowFrom(st *RouterStatus) *RouterRow {
	row := &RouterRow{
		Name:       st.Name,
		Impl:       st.Impl,
		URL:        st.URL,
		State:      st.State,
		StateClass: statusClass(st.State),
		LastError:  st.LastError,
		LastSeen:   formatAgo(st.LastSeen),
	}
	if st.Sample != nil {
		s := st.Sample
		row.Version = orDash(s.Version)
		row.NetStatusName = orDash(s.NetStatusName)
		row.Uptime = formatDuration(s.UptimeMs)
		row.TunIn = numI64(s.TunnelsInbound)
		row.TunOut = numI64(s.TunnelsOutbound)
		row.TunPart = numI64(s.Participating)
		row.KnownPeers = numI64(s.KnownPeers)
		row.ActivePeers = numI64(s.ActivePeers)
		row.FastPeers = numI64(s.FastPeers)
		row.HighCapPeers = numI64(s.HighCapacityPeers)
		row.BWIn1s = formatBytesPerSec(s.BWInbound1s)
		row.BWIn15s = formatBytesPerSec(s.BWInbound15s)
		row.BWOut1s = formatBytesPerSec(s.BWOutbound1s)
		row.BWOut15s = formatBytesPerSec(s.BWOutbound15s)
		row.Status = orDash(s.Status)
	} else {
		row.LastSeen = "never"
	}
	return row
}

// SampleRow is one history entry, display-ready.
type SampleRow struct {
	Time        string
	Uptime      string
	TunIn       string
	TunOut      string
	TunPart     string
	KnownPeers  string
	ActivePeers string
	BWIn1s      string
	BWOut1s     string
}

func sampleRow(s *i2pcontrol.Sample) SampleRow {
	return SampleRow{
		Time:        s.CollectedAt.Local().Format("15:04:05"),
		Uptime:      formatDuration(s.UptimeMs),
		TunIn:       numI64(s.TunnelsInbound),
		TunOut:      numI64(s.TunnelsOutbound),
		TunPart:     numI64(s.Participating),
		KnownPeers:  numI64(s.KnownPeers),
		ActivePeers: numI64(s.ActivePeers),
		BWIn1s:      formatBytesPerSec(s.BWInbound1s),
		BWOut1s:     formatBytesPerSec(s.BWOutbound1s),
	}
}

type routerView struct {
	*RouterRow
	History []SampleRow
}

type indexView struct {
	Routers                         []*RouterRow
	Total, Up, Down, Stale, Pending int
	RefreshSecs                     int
	Now                             time.Time
	Uptime                          string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	st := s.cfg.Poller
	total, up, down, stale, pending := st.Totals()
	var rows []*RouterRow
	for _, rs := range st.Snapshot() {
		rows = append(rows, rowFrom(rs))
	}
	view := indexView{
		Routers:     rows,
		Total:       total,
		Up:          up,
		Down:        down,
		Stale:       stale,
		Pending:     pending,
		RefreshSecs: int(s.cfg.PollInterval / time.Second),
		Now:         time.Now(),
		Uptime:      formatDuration(ptr(int64(time.Since(s.started) / time.Millisecond))),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.tmpl.ExecuteTemplate(w, "index.tmpl", view)
}

func (s *Server) handleRouter(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/router/")
	if name == "" || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	st, ok := s.cfg.Poller.Get(name)
	if !ok {
		http.Error(w, "unknown router", http.StatusNotFound)
		return
	}
	view := routerView{RouterRow: rowFrom(st)}
	for _, samp := range st.History {
		view.History = append(view.History, sampleRow(samp))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.tmpl.ExecuteTemplate(w, "router.tmpl", view)
}

// apiSample is the JSON shape of one sample (stable field names; see
// api_status_test.go).
type apiSample struct {
	UptimeMs          *int64   `json:"uptime_ms"`
	Version           string   `json:"version"`
	Status            string   `json:"status"`
	NetStatus         *int     `json:"net_status"`
	NetStatusName     string   `json:"net_status_name"`
	TunnelsInbound    *int64   `json:"tunnels_inbound"`
	TunnelsOutbound   *int64   `json:"tunnels_outbound"`
	Participating     *int64   `json:"tunnels_participating"`
	BWInbound1s       *float64 `json:"bw_inbound_1s"`
	BWInbound15s      *float64 `json:"bw_inbound_15s"`
	BWOutbound1s      *float64 `json:"bw_outbound_1s"`
	BWOutbound15s     *float64 `json:"bw_outbound_15s"`
	KnownPeers        *int64   `json:"known_peers"`
	ActivePeers       *int64   `json:"active_peers"`
	FastPeers         *int64   `json:"fast_peers"`
	HighCapacityPeers *int64   `json:"high_capacity_peers"`
	CollectedAt       string   `json:"collected_at"`
}

type apiRouter struct {
	Name      string      `json:"name"`
	Impl      string      `json:"impl"`
	URL       string      `json:"url"`
	State     string      `json:"state"`
	LastError string      `json:"last_error,omitempty"`
	LastSeen  string      `json:"last_seen,omitempty"`
	Sample    *apiSample  `json:"sample,omitempty"`
	History   []apiSample `json:"history,omitempty"`
}

type apiStatus struct {
	GeneratedAt string `json:"generated_at"`
	RefreshSecs int    `json:"refresh_seconds"`
	Totals      struct {
		Routers int `json:"routers"`
		Up      int `json:"up"`
		Down    int `json:"down"`
		Stale   int `json:"stale"`
		Pending int `json:"pending"`
	} `json:"totals"`
	Routers []apiRouter `json:"routers"`
}

func toAPISample(s *i2pcontrol.Sample) *apiSample {
	if s == nil {
		return nil
	}
	return &apiSample{
		UptimeMs: s.UptimeMs, Version: s.Version, Status: s.Status,
		NetStatus: s.NetStatus, NetStatusName: s.NetStatusName,
		TunnelsInbound: s.TunnelsInbound, TunnelsOutbound: s.TunnelsOutbound,
		Participating: s.Participating,
		BWInbound1s:   s.BWInbound1s, BWInbound15s: s.BWInbound15s,
		BWOutbound1s: s.BWOutbound1s, BWOutbound15s: s.BWOutbound15s,
		KnownPeers: s.KnownPeers, ActivePeers: s.ActivePeers,
		FastPeers: s.FastPeers, HighCapacityPeers: s.HighCapacityPeers,
		CollectedAt: s.CollectedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	var resp apiStatus
	resp.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	resp.RefreshSecs = int(s.cfg.PollInterval / time.Second)
	resp.Totals.Routers, resp.Totals.Up, resp.Totals.Down, resp.Totals.Stale, resp.Totals.Pending = s.cfg.Poller.Totals()
	all := s.cfg.Poller.Snapshot()
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	for _, st := range all {
		ar := apiRouter{
			Name:      st.Name,
			Impl:      st.Impl,
			URL:       st.URL,
			State:     st.State,
			LastError: st.LastError,
			Sample:    toAPISample(st.Sample),
		}
		if !st.LastSeen.IsZero() {
			ar.LastSeen = st.LastSeen.UTC().Format(time.RFC3339)
		}
		for _, samp := range st.History {
			if v := toAPISample(samp); v != nil {
				ar.History = append(ar.History, *v)
			}
		}
		resp.Routers = append(resp.Routers, ar)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(resp)
}
