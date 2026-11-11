package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go-i2p-testnet/lib/monitor/i2pcontrol"
	"go-i2p-testnet/lib/monitor/topology"
)

//go:embed templates/*.html
var templateFS embed.FS

// TopologyFunc returns the current topology graph. The server takes it as a
// function rather than importing the Docker-side collector, so the web layer
// stays free of Docker and the graph it renders always comes from live router
// data.
type TopologyFunc func(ctx context.Context) *topology.Graph

// ServerConfig configures the dashboard HTTP server.
type ServerConfig struct {
	// BindAddr defaults to "127.0.0.1:8765" — the dashboard binds to
	// loopback unless the operator explicitly asks for more.
	BindAddr string
	Poller   *Poller
	// PollInterval is advertised to the browser for auto-refresh.
	PollInterval time.Duration
	// Topology, when set, enables /topology and /api/topology.
	Topology TopologyFunc
	// Diagnose, when set, returns a plain-language explanation for a router
	// the poller cannot observe, derived from what the router itself reports
	// outside I2PControl (its log output and process state). The audit's
	// finding was that an unobservable router showed only "connection
	// refused", which is true but not actionable. It must be cheap and must
	// not block: it is called while a page renders.
	Diagnose func(name string) string
}

// Server is the embedded dashboard web server.
type Server struct {
	cfg     ServerConfig
	mux     *http.ServeMux
	httpc   *http.Server
	pages   *template.Template
	started time.Time
}

// NewServer builds the dashboard server. All router-supplied strings are
// rendered through html/template, so every router-supplied string is
// escaped at render time; TestEscaping and TestChartsEscapedInHTML hold that to
// the served bytes.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.BindAddr == "" {
		cfg.BindAddr = "127.0.0.1:8765"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	// No FuncMap: every display string is precomputed in Go (see rowFrom and
	// the view structs), so templates only select fields. That keeps the
	// formatting rules in one testable place and leaves no template-callable
	// helpers unused.
	pages, err := template.New("").ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:     cfg,
		pages:   pages,
		started: time.Now(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/router/", s.handleRouter)
	mux.HandleFunc("/api/status", s.handleAPIStatus)
	mux.HandleFunc("/healthz", s.handleHealthz)
	if cfg.Topology != nil {
		mux.HandleFunc("/topology", s.handleTopology)
		mux.HandleFunc("/api/topology", s.handleAPITopology)
	} else {
		// Registered explicitly so the route exists and says why it is empty,
		// rather than 404ing with no explanation.
		mux.HandleFunc("/topology", s.handleTopologyUnavailable)
		mux.HandleFunc("/api/topology", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "topology collection is not configured"})
		})
	}
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

// topologyView is the template-facing graph: the model plus precomputed
// display strings, so the template never formats or dereferences anything.
type topologyView struct {
	Title        string
	NavNote      string
	Graph        *topology.Graph
	Summary      edgeSummary
	GeneratedTxt string
	Age          string
	Rows         []*topologyRow
	Routers      []*routerRow
	Rings        []topologyRing // SVG node positions
	Edges        []topologySVGEdge
	HasEdges     bool
	RefreshSecs  int
}

type topologyRow struct {
	From      string
	To        string
	Kind      string
	KindClass string
	Match     string
	Transport string
	Address   string
	Bytes     string
	Reason    string
	HashShort string
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/topology" {
		http.NotFound(w, r)
		return
	}
	if s.cfg.Topology == nil {
		s.handleTopologyUnavailable(w, r)
		return
	}
	g := s.cfg.Topology(r.Context())
	view := s.topologyView(g)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.pages.ExecuteTemplate(w, "topology.html", view)
}

func (s *Server) handleTopologyUnavailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	s.pages.ExecuteTemplate(w, "topology.html", topologyView{
		Graph:        &topology.Graph{},
		GeneratedTxt: "unavailable",
		Age:          "never",
		RefreshSecs:  int(s.cfg.PollInterval / time.Second),
	})
}

func (s *Server) handleAPITopology(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Topology == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "topology collection is not configured"})
		return
	}
	g := s.cfg.Topology(r.Context())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(g)
}

// routerRow is one roster router as the topology page shows it: identity,
// liveness and how much it knows, so an isolated router can be told apart
// from a dead one.
type routerRow struct {
	Name    string
	Impl    string
	IP      string
	Hash    string
	Running bool
	State   string
	Class   string
	RIs     int
	Links   int
}

// topologyView converts a graph into display-ready rows and SVG geometry.
// Node positions come from the roster's own connectivity — the most connected
// router first, laid out on a circle — never from a hardcoded arrangement.
func (s *Server) topologyView(g *topology.Graph) topologyView {
	v := topologyView{
		Title:       "topology",
		NavNote:     "topology from live router data",
		Graph:       g,
		RefreshSecs: int(s.cfg.PollInterval / time.Second),
	}
	if g == nil {
		g = &topology.Graph{}
		v.Graph = g
		v.GeneratedTxt = "never"
		v.Age = "never"
		return v
	}
	v.GeneratedTxt = formatClockFull(g.GeneratedAt)
	v.Age = formatAgo(g.GeneratedAt)
	v.Summary = summarize(g)

	// Sort routers by observed degree, then name, so the layout is stable and
	// the busiest routers sit at predictable positions.
	deg := g.Degree()
	names := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		names = append(names, n.Name)
	}
	sort.SliceStable(names, func(i, j int) bool {
		di, dj := deg[names[i]], deg[names[j]]
		if di[0] != dj[0] {
			return di[0] > dj[0]
		}
		if di[1] != dj[1] {
			return di[1] > dj[1]
		}
		return names[i] < names[j]
	})
	pos := map[string][2]float64{}
	v.Rings = layoutRing(names)
	decorateRings(&v, g)
	for i, name := range names {
		if i < len(v.Rings) {
			pos[name] = [2]float64{v.Rings[i].X, v.Rings[i].Y}
		}
	}

	for _, n := range g.Nodes {
		if !n.InRoster {
			continue
		}
		d := deg[n.Name]
		v.Routers = append(v.Routers, &routerRow{
			Name: n.Name, Impl: n.Impl, IP: n.IP, Hash: n.Hash, Running: n.Running,
			State: n.State, Class: statusClass(n.State), RIs: n.KnownRIs, Links: d[0] + d[1],
		})
	}

	for _, e := range g.Edges {
		v.HasEdges = true
		v.Edges = append(v.Edges, topologySVGEdge{Edge: e, X1: pos[e.From][0], Y1: pos[e.From][1], X2: pos[e.To][0], Y2: pos[e.To][1]})
		bytes := ""
		if e.BytesSent > 0 || e.BytesRecv > 0 {
			bytes = fmt.Sprintf("%s sent / %s recv", humanBytes(e.BytesSent), humanBytes(e.BytesRecv))
		}
		v.Rows = append(v.Rows, &topologyRow{
			From: e.From, To: e.To, Kind: e.Kind, KindClass: kindClass(e.Kind),
			Match: e.Match, Transport: e.Transport, Address: e.PeerAddress,
			Bytes: bytes, Reason: e.Reason, HashShort: shortHash(e.PeerHash),
		})
	}
	return v
}

func kindClass(kind string) string {
	if kind == topology.KindObserved {
		return "observed"
	}
	return "inferred"
}

func shortHash(h string) string {
	if len(h) <= 8 {
		return h
	}
	return h[:8] + "…"
}

// humanBytes formats a plain byte count (not a rate) with the same units the
// rate columns use, so per-session totals and bandwidth read consistently.
func humanBytes(v int64) string {
	return formatBytesValue(float64(v))
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
	Failure       string // classified reason for the last failure, or ""
	Failures      int
	Diagnosis     string // what the router's own log says it is waiting on
}

// missing returns the text shown when a router did not report a value. The
// audit's finding was that a bare em-dash cannot be distinguished from zero,
// so a value the implementation is known never to serve is labelled instead.
//
// compact picks the wording for the context: the summary table needs short
// cells it can scan across many rows, while the detail page has room to name
// the implementation outright.
func (st *RouterStatus) missing(field string, compact bool) string {
	if notServed[st.Impl][field] {
		if compact {
			return "n/a"
		}
		return "not served by " + st.Impl
	}
	return "—"
}

func (s *Server) rowFrom(st *RouterStatus, compact bool) *RouterRow {
	row := &RouterRow{
		Name:          st.Name,
		Impl:          st.Impl,
		URL:           st.URL,
		State:         st.State,
		StateClass:    statusClass(st.State),
		LastError:     st.LastError,
		LastSeen:      formatAgo(st.LastSeen),
		Failure:       st.FailureReason,
		Failures:      st.Failures,
		Version:       st.missing("version", compact),
		NetStatusName: st.missing("net_status", compact),
		Uptime:        st.missing("uptime", compact),
		TunIn:         st.missing("tunnels_inbound", compact),
		TunOut:        st.missing("tunnels_outbound", compact),
		TunPart:       st.missing("tunnels_participating", compact),
		KnownPeers:    st.missing("known_peers", compact),
		ActivePeers:   st.missing("active_peers", compact),
		FastPeers:     st.missing("fast_peers", compact),
		HighCapPeers:  st.missing("high_capacity_peers", compact),
		BWIn1s:        st.missing("bw_inbound_1s", compact),
		BWIn15s:       st.missing("bw_inbound_15s", compact),
		BWOut1s:       st.missing("bw_outbound_1s", compact),
		BWOut15s:      st.missing("bw_outbound_15s", compact),
		Status:        st.missing("status", compact),
	}
	if st.Sample != nil {
		s := st.Sample
		setIf(&row.Version, s.Version)
		setIf(&row.NetStatusName, s.NetStatusName)
		row.Uptime = orStr(formatDuration(s.UptimeMs))
		row.TunIn = orStr(valueCellC(st.Impl, "tunnels_inbound", s.TunnelsInbound, compact))
		row.TunOut = orStr(valueCellC(st.Impl, "tunnels_outbound", s.TunnelsOutbound, compact))
		row.TunPart = orStr(valueCellC(st.Impl, "tunnels_participating", s.Participating, compact))
		row.KnownPeers = orStr(valueCellC(st.Impl, "known_peers", s.KnownPeers, compact))
		row.ActivePeers = orStr(valueCellC(st.Impl, "active_peers", s.ActivePeers, compact))
		row.FastPeers = orStr(valueCellC(st.Impl, "fast_peers", s.FastPeers, compact))
		row.HighCapPeers = orStr(valueCellC(st.Impl, "high_capacity_peers", s.HighCapacityPeers, compact))
		row.BWIn1s = orStr(valueCellF(st.Impl, "bw_inbound_1s", s.BWInbound1s, compact))
		row.BWIn15s = orStr(valueCellF(st.Impl, "bw_inbound_15s", s.BWInbound15s, compact))
		row.BWOut1s = orStr(valueCellF(st.Impl, "bw_outbound_1s", s.BWOutbound1s, compact))
		row.BWOut15s = orStr(valueCellF(st.Impl, "bw_outbound_15s", s.BWOutbound15s, compact))
		setIf(&row.Status, s.Status)
	}
	if st.LastSeen.IsZero() {
		row.LastSeen = "never"
	}
	// A router we cannot observe may still be explainable from its own log
	// output, which is a second channel independent of I2PControl.
	if st.State != StateUp && s != nil && s.cfg.Diagnose != nil {
		row.Diagnosis = s.cfg.Diagnose(st.Name)
	}
	return row
}

// setIf overwrites target when value is non-empty.
func setIf(target *string, value string) {
	if value != "" {
		*target = value
	}
}

// orStr keeps a precomputed explanation when a formatter returned empty.
func orStr(s string) string {
	if s == "" {
		return "—"
	}
	return s
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
	Title   string
	NavNote string
	*RouterRow
	History     []SampleRow
	Charts      []Chart
	RefreshSecs int
	NowText     string
	ChartWindow string
	Samples     int
}

type indexView struct {
	Title                           string
	NavNote                         string
	Routers                         []*RouterRow
	Total, Up, Down, Stale, Pending int
	RefreshSecs                     int
	Now                             time.Time
	NowText                         string
	Uptime                          string
	Network                         *NetworkTotals
	NetworkText                     string
	Sort                            string
	SortDir                         string
	Columns                         []sortColumn
	Charts                          []Chart
	ChartWindow                     string
	HasTopology                     bool
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
		rows = append(rows, s.rowFrom(rs, true))
	}
	now := time.Now()
	net := st.NetworkTotals()
	sortKey, sortDir := sortArgs(r)
	sortRows(rows, sortKey, sortDir)

	view := indexView{
		Title:       "status",
		NavNote:     "snapshot " + formatClock(time.Now()),
		Routers:     rows,
		Sort:        sortKey,
		SortDir:     sortDir,
		Columns:     sortColumns(sortKey, sortDir),
		Total:       total,
		Up:          up,
		Down:        down,
		Stale:       stale,
		Pending:     pending,
		RefreshSecs: int(s.cfg.PollInterval / time.Second),
		Now:         now,
		NowText:     formatClockFull(now),
		Uptime:      formatDuration(ptr(int64(time.Since(s.started) / time.Millisecond))),
		Network:     net,
		NetworkText: networkSummary(net),
		Charts:      networkCharts(st.NetworkHistory()),
		ChartWindow: window(s.cfg.PollInterval, HistorySamples),
		HasTopology: s.cfg.Topology != nil,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.pages.ExecuteTemplate(w, "index.html", view)
}

// networkSummary phrases the aggregate totals for the header, calling out a
// value no router reported instead of printing a bare zero.
func networkSummary(n *NetworkTotals) string {
	if n == nil {
		return "no aggregate sample yet"
	}
	parts := []string{
		fmt.Sprintf("%d routers", n.Routers),
		fmt.Sprintf("%d up", n.Up),
	}
	if n.KnownPeers != nil {
		parts = append(parts, "known peers "+numI64(n.KnownPeers))
	} else {
		parts = append(parts, "known peers —")
	}
	if n.ActivePeers != nil {
		parts = append(parts, "active peers "+numI64(n.ActivePeers))
	}
	if n.TunnelsParticip != nil {
		parts = append(parts, "participating tunnels "+numI64(n.TunnelsParticip))
	}
	if n.BWInbound1s != nil {
		parts = append(parts, "in "+formatBytesPerSec(n.BWInbound1s))
	}
	if n.BWOutbound1s != nil {
		parts = append(parts, "out "+formatBytesPerSec(n.BWOutbound1s))
	}
	return strings.Join(parts, " · ")
}

// sortArgs reads the sort column and direction from the query string. Sorting
// is done server-side so the page stays JavaScript-free; an unknown column
// falls back to the default name order rather than erroring.
func sortArgs(r *http.Request) (string, string) {
	q := r.URL.Query()
	key := strings.ToLower(q.Get("sort"))
	if !validSortKeys[key] {
		key = "name"
	}
	dir := strings.ToLower(q.Get("dir"))
	if dir != "asc" && dir != "desc" {
		dir = "asc"
	}
	return key, dir
}

var validSortKeys = map[string]bool{
	"name": true, "state": true, "impl": true, "uptime": true,
	"peers": true, "tunnels": true, "bw": true, "seen": true, "version": true,
}

// sortRows orders the display rows. Health comes first when sorting by state
// so a broken router is never buried under a long list of healthy ones, which
// is what an operator scanning for trouble needs.
func sortRows(rows []*RouterRow, key, dir string) {
	less := func(a, b *RouterRow) bool {
		switch key {
		case "state":
			// down < stale < pending < up for display priority
			return stateRank(a.State) < stateRank(b.State)
		case "impl":
			return a.Impl < b.Impl
		case "uptime":
			return parseDurationDisplay(a.Uptime) < parseDurationDisplay(b.Uptime)
		case "peers":
			return firstNumber(a.KnownPeers) < firstNumber(b.KnownPeers)
		case "tunnels":
			return firstNumber(a.TunPart) < firstNumber(b.TunPart)
		case "bw":
			return firstNumber(a.BWIn1s) < firstNumber(b.BWIn1s)
		case "seen":
			return durationAgo(a.LastSeen) < durationAgo(b.LastSeen)
		case "version":
			return a.Version < b.Version
		default:
			return a.Name < b.Name
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if dir == "desc" {
			return less(rows[j], rows[i])
		}
		return less(rows[i], rows[j])
	})
}

// sortColumn is one header cell: its label, the key it sorts by, and the
// arrow showing the active column.
type sortColumn struct {
	Key   string
	Label string
	// Next is the direction the link requests. It is interpolated as a
	// separate template action so html/template escapes it as a query value;
	// carrying "&dir=x" as one string would be escaped into "%26dir%3dx" and
	// produce a broken link.
	Next string
	Mark string
}

// sortColumns builds the header row so the table's sortability is visible
// rather than hidden behind an undocumented query parameter.
func sortColumns(active, dir string) []sortColumn {
	specs := [][]string{
		{"name", "Router"}, {"impl", "Impl"}, {"state", "State"}, {"version", "Version"},
		{"", "Net status"}, {"uptime", "Uptime"}, {"tunnels", "Tunnels in/out/part"},
		{"peers", "Peers known/active"}, {"bw", "BW in/out (1s)"}, {"seen", "Last seen"},
	}
	out := make([]sortColumn, 0, len(specs))
	for _, sp := range specs {
		col := sortColumn{Key: sp[0], Label: sp[1], Next: "asc"}
		// A column with no key is not sortable; it still needs its header
		// cell, or the header row would not line up with the body.
		if col.Key != "" {
			if active == col.Key && dir == "asc" {
				col.Next = "desc"
			}
			if active == col.Key {
				col.Mark = " " + dirMark(dir)
			}
		}
		out = append(out, col)
	}
	return out
}

func dirMark(dir string) string {
	if dir == "desc" {
		return "▼"
	}
	return "▲"
}

// durationShape accepts only the output of formatDuration.
var durationShape = regexp.MustCompile(`^([0-9]+s|[0-9]+m [0-9]{2}s|[0-9]+h [0-9]{2}m [0-9]{2}s)$`)

func stateRank(state string) int {
	switch state {
	case StateDown:
		return 0
	case StateStale:
		return 1
	case StatePending:
		return 2
	default:
		return 3
	}
}

// firstNumber pulls the leading number out of an already-formatted display
// string ("1.5 KiB/s" -> 1536, "3m 12s" -> 192) so sorting works on the values
// the operator actually reads without keeping a parallel numeric field.
func firstNumber(s string) float64 {
	v, _ := parseDisplayNumber(s)
	return v
}

// parseDurationDisplay converts the human duration format back to seconds for
// sorting. The generic number scan cannot do this: "2m 10s" would read as 2
// and "45s" as 45, which would order a 45-second uptime above a 130-second
// one. An unparseable value ("—", "not served by i2pd") sorts lowest so real
// measurements always outrank missing ones.
func parseDurationDisplay(s string) float64 {
	t := strings.TrimSpace(s)
	if t == "" || t == "never" {
		return -1
	}
	// Every duration format ends in "s" ("45s", "2m 10s", "1h 02m 03s"). The
	// shape check matters: "not served by i2pd" contains a digit, so scanning
	// for numbers alone would turn an explanation into a fake 0-second uptime.
	if !strings.HasSuffix(t, "s") || !durationShape.MatchString(t) {
		return -1
	}
	var total float64
	var num strings.Builder
	gotAny := false
	for _, r := range t {
		if r >= '0' && r <= '9' {
			num.WriteRune(r)
			continue
		}
		if num.Len() == 0 {
			continue
		}
		v, err := strconv.ParseFloat(num.String(), 64)
		num.Reset()
		if err != nil {
			continue
		}
		gotAny = true
		switch r {
		case 'h':
			total += v * 3600
		case 'm':
			total += v * 60
		case 's':
			total += v
		}
	}
	if !gotAny {
		return -1
	}
	return total
}

func durationAgo(s string) float64 {
	switch s {
	case "never":
		return math.MaxFloat64
	case "just now":
		return 0
	}
	// "5s ago", "2m ago", or an absolute timestamp; only the relative forms
	// need to order meaningfully.
	if strings.HasSuffix(s, "ago") {
		v, ok := parseDisplayNumber(s)
		if ok {
			if strings.Contains(s, "m ago") {
				return v * 60
			}
			return v
		}
	}
	return -1
}

func parseDisplayNumber(s string) (float64, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, false
	}
	i := 0
	for i < len(t) && (t[i] >= '0' && t[i] <= '9' || t[i] == '.' || t[i] == '-') {
		i++
	}
	if i == 0 {
		return 0, false
	}
	f, err := strconv.ParseFloat(t[:i], 64)
	if err != nil {
		return 0, false
	}
	// Scale by the unit that follows so 1.5 KiB sorts above 900 B.
	rest := strings.ToLower(t[i:])
	switch {
	case strings.Contains(rest, "mib"):
		f *= 1024 * 1024
	case strings.Contains(rest, "kib"), strings.Contains(rest, "kb"):
		f *= 1024
	case strings.Contains(rest, "gib"):
		f *= 1024 * 1024 * 1024
	case strings.Contains(rest, "tib"):
		f *= 1024 * 1024 * 1024 * 1024
	case strings.HasPrefix(rest, "h"):
		f *= 3600
	}
	return f, true
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
	view := routerView{
		Title:       st.Name,
		NavNote:     st.Name,
		RouterRow:   s.rowFrom(st, false),
		Charts:      routerCharts(st),
		RefreshSecs: int(s.cfg.PollInterval / time.Second),
		NowText:     formatClockFull(time.Now()),
		ChartWindow: window(s.cfg.PollInterval, HistorySamples),
		Samples:     len(st.History),
	}
	for _, samp := range st.History {
		view.History = append(view.History, sampleRow(samp))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.pages.ExecuteTemplate(w, "router.html", view)
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
	Name          string      `json:"name"`
	Impl          string      `json:"impl"`
	URL           string      `json:"url"`
	State         string      `json:"state"`
	FailureReason string      `json:"failure_reason,omitempty"`
	Failures      int         `json:"consecutive_failures,omitempty"`
	LastError     string      `json:"last_error,omitempty"`
	LastSeen      string      `json:"last_seen,omitempty"`
	Sample        *apiSample  `json:"sample,omitempty"`
	History       []apiSample `json:"history,omitempty"`
	// NotServed lists the properties this implementation never exposes over
	// I2PControl, so a consumer can tell "absent forever" from "absent now".
	NotServed []string `json:"not_served_by_implementation,omitempty"`
}

// apiNetworkTotals is the summed state of the whole testnet, with nil meaning
// "no router reported this field" rather than zero.
type apiNetworkTotals struct {
	Routers         int      `json:"routers"`
	Up              int      `json:"up"`
	KnownPeers      *int64   `json:"known_peers,omitempty"`
	ActivePeers     *int64   `json:"active_peers,omitempty"`
	TunnelsInbound  *int64   `json:"tunnels_inbound,omitempty"`
	TunnelsOutbound *int64   `json:"tunnels_outbound,omitempty"`
	TunnelsParticip *int64   `json:"tunnels_participating,omitempty"`
	BWInbound1s     *float64 `json:"bw_inbound_1s,omitempty"`
	BWOutbound1s    *float64 `json:"bw_outbound_1s,omitempty"`
}

type apiStatus struct {
	GeneratedAt string `json:"generated_at"`
	RefreshSecs int    `json:"refresh_seconds"`
	HistoryMax  int    `json:"history_max_samples"`
	TopologyURL string `json:"topology_url,omitempty"`
	Totals      struct {
		Routers int `json:"routers"`
		Up      int `json:"up"`
		Down    int `json:"down"`
		Stale   int `json:"stale"`
		Pending int `json:"pending"`
	} `json:"totals"`
	Network *apiNetworkTotals `json:"network,omitempty"`
	Routers []apiRouter       `json:"routers"`
}

func toAPINetwork(n *NetworkTotals) *apiNetworkTotals {
	if n == nil {
		return nil
	}
	return &apiNetworkTotals{
		Routers: n.Routers, Up: n.Up,
		KnownPeers: n.KnownPeers, ActivePeers: n.ActivePeers,
		TunnelsInbound: n.TunnelsInbound, TunnelsOutbound: n.TunnelsOutbound,
		TunnelsParticip: n.TunnelsParticip,
		BWInbound1s:     n.BWInbound1s, BWOutbound1s: n.BWOutbound1s,
	}
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
	resp.HistoryMax = HistorySamples
	if s.cfg.Topology != nil {
		resp.TopologyURL = "/topology"
	}
	resp.Totals.Routers, resp.Totals.Up, resp.Totals.Down, resp.Totals.Stale, resp.Totals.Pending = s.cfg.Poller.Totals()
	resp.Network = toAPINetwork(s.cfg.Poller.NetworkTotals())
	all := s.cfg.Poller.Snapshot()
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	for _, st := range all {
		ar := apiRouter{
			Name:          st.Name,
			Impl:          st.Impl,
			URL:           st.URL,
			State:         st.State,
			FailureReason: st.FailureReason,
			Failures:      st.Failures,
			LastError:     st.LastError,
			Sample:        toAPISample(st.Sample),
			NotServed:     notServedFields(st.Impl),
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
