// Package web implements the self-contained testnet dashboard: an
// in-memory poller over the I2PControl client plus one embedded net/http
// server rendering html/template pages (go:embed) and a JSON API.
package web

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go-i2p-testnet/lib/monitor/i2pcontrol"
)

// Router states. Down renders red, Stale renders amber.
//
// The two are distinguished by how much good data is still on hand, which is
// what an operator actually cares about:
//
//	pending  never sampled successfully since the router appeared
//	up       last sample is fresh
//	stale    a poll failed (or was skipped) but the last good sample is still
//	         within StaleIntervals — the numbers shown are real, just ageing
//	down     a poll failed and the last good sample is older than
//	         StaleIntervals, or the router has never answered at all
const (
	StatePending = "pending" // never sampled yet
	StateUp      = "up"
	StateDown    = "down"
	StateStale   = "stale"
)

// Failure reasons classify the last poll error so an operator can tell "the
// control endpoint is not serving" apart from "the router is wedged" apart
// from "we are not allowed in". Without this every problem looks like one
// identical red cell.
const (
	ReasonNone        = ""
	ReasonNotServing  = "not-serving" // connection refused: I2PControl not listening yet
	ReasonUnreachable = "unreachable" // no route / host down
	ReasonTimeout     = "timeout"     // accepted the connection, never answered
	ReasonAuth        = "auth"        // credentials rejected
	ReasonProtocol    = "protocol"    // answered, but not with usable JSON-RPC
	ReasonInternal    = "internal"    // client setup or unexpected failure
)

// HistorySamples is how many samples the detail page keeps and shows.
const HistorySamples = 60

// StaleIntervals is how many missed poll intervals make a previously good
// router's data "stale" (amber) instead of healthy.
const StaleIntervals = 3

// Target describes one router to poll. Provided by the testnet CLI from its
// in-memory router list — nothing here is hardcoded.
type Target struct {
	Name          string
	Impl          string // "i2pd" | "goi2p"
	URL           string // I2PControl endpoint, e.g. https://172.28.0.2:7650/
	Password      string // never rendered, never serialized
	PinnedCertPEM []byte // trust only this cert for https endpoints
	InsecureTLS   bool   // explicit operator opt-in, warned about by the caller
	ContainerID   string // Docker id, empty in tests; used for topology only
	IP            string // container address on the testnet bridge
}

// key fingerprints everything that requires rebuilding the client when it
// changes (endpoint, password, cert). The name is excluded because it is
// the map key.
func (t Target) key() string {
	return fmt.Sprintf("%s|%s|%x|%v", t.URL, t.Password, t.PinnedCertPEM, t.InsecureTLS)
}

// SourceFunc returns the current router targets. The poller calls it at the
// start of every interval, so routers added/removed via the CLI appear and
// disappear without restarting the dashboard.
type SourceFunc func(ctx context.Context) []Target

// RouterStatus is the observable state of one router.
type RouterStatus struct {
	Name          string
	Impl          string
	URL           string
	ContainerID   string // Docker id for topology collection; empty if unknown
	IP            string // container address on the testnet bridge
	State         string
	LastError     string
	FailureReason string    // one of the Reason* constants, empty when up
	Failures      int       // consecutive failed polls, for the UI's "since" hint
	LastSeen      time.Time // last successful sample
	Sample        *i2pcontrol.Sample
	History       []*i2pcontrol.Sample // newest first, capped at HistorySamples
	InFlight      bool                 // a poll goroutine is running right now
	StaleSince    time.Time            // when this router stopped being fresh

	checkedKF string // fingerprint of the last synced client config
}

// NetworkTotals is one aggregate sample of the whole testnet, summed across
// routers that reported the field. Fields are nil when no router supplied
// them in that interval, so the UI can say "not reported" instead of showing
// a misleading zero.
type NetworkTotals struct {
	CollectedAt     time.Time
	Routers         int
	Up              int
	KnownPeers      *int64
	ActivePeers     *int64
	TunnelsInbound  *int64
	TunnelsOutbound *int64
	TunnelsParticip *int64
	BWInbound1s     *float64
	BWOutbound1s    *float64
}

// Poller polls every router concurrently on a fixed interval and keeps the
// last HistorySamples samples per router in memory.
type Poller struct {
	interval time.Duration
	timeout  time.Duration
	source   SourceFunc

	mu       sync.RWMutex
	statuses map[string]*RouterStatus
	clients  map[string]*i2pcontrol.Client
	inFlight map[string]bool  // router name -> poll goroutine running
	network  []*NetworkTotals // newest first, capped at HistorySamples

	running bool
	stop    chan struct{}
	done    chan struct{}
}

// NewPoller creates a poller. interval is how often all routers are
// sampled; timeout bounds a single router's poll. Every router is polled in
// its own goroutine and cycles never wait for stragglers, so one slow or
// down router cannot delay the others.
func NewPoller(interval, timeout time.Duration, source SourceFunc) *Poller {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if timeout <= 0 {
		timeout = i2pcontrol.DefaultTimeout
	}
	return &Poller{
		interval: interval,
		timeout:  timeout,
		source:   source,
		statuses: map[string]*RouterStatus{},
		clients:  map[string]*i2pcontrol.Client{},
		inFlight: map[string]bool{},
		network:  []*NetworkTotals{},
	}
}

// Interval exposes the configured poll interval (advertised to the UI).
func (p *Poller) Interval() time.Duration { return p.interval }

// Start launches the polling loop. It is a no-op if already running.
func (p *Poller) Start(ctx context.Context) {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.stop = make(chan struct{})
	p.done = make(chan struct{})
	p.mu.Unlock()

	go p.loop(ctx, p.stop, p.done)
}

// Stop halts the polling loop and waits for it to exit.
func (p *Poller) Stop() {
	p.mu.Lock()
	running, stopCh, doneCh := p.running, p.stop, p.done
	p.mu.Unlock()
	if !running {
		return
	}
	close(stopCh)
	<-doneCh
	p.mu.Lock()
	p.running = false
	for _, c := range p.clients {
		c.Close()
	}
	p.clients = map[string]*i2pcontrol.Client{}
	p.mu.Unlock()
}

func (p *Poller) loop(ctx context.Context, stop, done chan struct{}) {
	defer close(done)
	p.pollCycle(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollCycle(ctx)
		}
	}
}

// pollCycle refreshes the target list, kicks a non-blocking poll for every
// router, and sweeps stale entries. It returns as soon as the polls are
// dispatched (plus a bounded grace so fast results land in this cycle).
func (p *Poller) pollCycle(ctx context.Context) {
	p.syncTargets(ctx)
	p.dispatch(ctx)
	p.sweepStale()
}

// DispatchNow runs one immediate cycle (used by tests and the CLI).
func (p *Poller) DispatchNow(ctx context.Context) { p.pollCycle(ctx) }

// classifyFailure maps a poll error onto a Reason* constant. The error text
// is produced by the i2pcontrol client and never contains the password or a
// token, so it is safe to inspect. Ordering matters: an authentication
// failure can also surface as an HTTP error, so auth is checked first.
func classifyFailure(err error) string {
	if err == nil {
		return ReasonNone
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "client setup failed"):
		return ReasonInternal
	case strings.Contains(msg, "internal error while polling"):
		return ReasonInternal
	case strings.Contains(msg, "i2pcontrol: router returned no authentication token"),
		strings.Contains(msg, "invalid password"),
		strings.Contains(msg, "authentication"),
		strings.Contains(msg, "rpc error -32000"),
		strings.Contains(msg, "rpc error -32001"),
		strings.Contains(msg, "rpc error -32003"),
		strings.Contains(msg, "rpc error -32004"):
		return ReasonAuth
	case errors.Is(err, i2pcontrol.ErrTimeout),
		strings.Contains(msg, "timeout"),
		strings.Contains(msg, "timed out"),
		strings.Contains(msg, "deadline exceeded"):
		return ReasonTimeout
	case strings.Contains(msg, "connection refused"):
		return ReasonNotServing
	case strings.Contains(msg, "no such host"),
		strings.Contains(msg, "network is unreachable"),
		strings.Contains(msg, "connect: network"),
		strings.Contains(msg, "no route to host"):
		return ReasonUnreachable
	case strings.Contains(msg, "malformed json"),
		strings.Contains(msg, "http status"),
		strings.Contains(msg, "rpc error"):
		return ReasonProtocol
	default:
		return ReasonProtocol
	}
}

func (p *Poller) dispatch(ctx context.Context) {
	p.mu.RLock()
	type job struct {
		name string
		cl   *i2pcontrol.Client
		st   *RouterStatus
	}
	var jobs []job
	for name, c := range p.clients {
		if st, ok := p.statuses[name]; ok {
			jobs = append(jobs, job{name: name, cl: c, st: st})
		}
	}
	p.mu.RUnlock()

	for _, j := range jobs {
		p.mu.Lock()
		if p.inFlight[j.name] {
			// Previous poll for this router is still running: skip this
			// cycle. Its data will age and be reported stale, but it never
			// blocks anyone else.
			p.mu.Unlock()
			continue
		}
		p.inFlight[j.name] = true
		if st, ok := p.statuses[j.name]; ok {
			st.InFlight = true
		}
		p.mu.Unlock()

		go func(name string, cl *i2pcontrol.Client, st *RouterStatus) {
			defer func() {
				p.mu.Lock()
				p.inFlight[name] = false
				if cur, ok := p.statuses[name]; ok {
					cur.InFlight = false
				}
				p.mu.Unlock()
			}()
			sample, err := cl.Poll(ctx)
			p.mu.Lock()
			defer p.mu.Unlock()
			cur, ok := p.statuses[name]
			if !ok {
				return // router removed while polling
			}
			if err != nil {
				// A failed poll keeps the last good sample on screen. The
				// router is amber (stale) while that sample is still recent
				// and red (down) once it ages past StaleIntervals — so real
				// data is never thrown away by one bad cycle, and a genuinely
				// dead router still turns red within two intervals.
				cur.LastError = err.Error()
				cur.FailureReason = classifyFailure(err)
				cur.Failures++
				p.applyUnhealthy(cur, time.Now())
				return
			}
			cur.Sample = sample
			cur.LastSeen = sample.CollectedAt
			cur.LastError = ""
			cur.FailureReason = ReasonNone
			cur.Failures = 0
			cur.StaleSince = time.Time{}
			cur.State = StateUp
			cur.History = append([]*i2pcontrol.Sample{sample}, cur.History...)
			if len(cur.History) > HistorySamples {
				cur.History = cur.History[:HistorySamples]
			}
		}(j.name, j.cl, j.st)
	}
}

// applyUnhealthy decides between stale and down for a router that is not
// answering. Callers must hold p.mu.
func (p *Poller) applyUnhealthy(st *RouterStatus, now time.Time) {
	cutoff := now.Add(-time.Duration(StaleIntervals) * p.interval)
	freshEnough := !st.LastSeen.IsZero() && st.LastSeen.After(cutoff)
	if st.Sample != nil && freshEnough {
		if st.State != StateStale {
			st.State = StateStale
			st.StaleSince = now
		}
		return
	}
	st.State = StateDown
}

// sweepStale ages routers whose last good sample is older than
// StaleIntervals intervals. A router that never answered at all is reported
// down; a router whose poll is still in flight keeps its last data and goes
// amber, because the numbers on screen are real but ageing.
func (p *Poller) sweepStale() {
	now := time.Now()
	cutoff := now.Add(-time.Duration(StaleIntervals) * p.interval)
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, st := range p.statuses {
		switch st.State {
		case StateUp:
			// A poll that is still in flight (a slow router) also ages the
			// data, so the amber state is reachable without any failure.
			if !st.LastSeen.IsZero() && st.LastSeen.Before(cutoff) {
				st.State = StateStale
				st.StaleSince = now
			}
		case StateStale:
			// Amber data that ages past the window is reported down.
			if st.LastSeen.Before(cutoff) {
				st.State = StateDown
			}
		}
	}
	p.recordNetworkHistory(now)
}

// syncTargets diffs the source list against live clients: creates clients
// for new/changed targets and drops clients for removed routers.
func (p *Poller) syncTargets(ctx context.Context) {
	if p.source == nil {
		return
	}
	targets := p.source(ctx)
	want := make(map[string]Target, len(targets))
	for _, t := range targets {
		want[t.Name] = t
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Drop vanished routers. The router list is the testnet's state, not
	// ours to remember.
	for name := range p.clients {
		if _, ok := want[name]; !ok {
			p.clients[name].Close()
			delete(p.clients, name)
			delete(p.statuses, name)
		}
	}
	for name := range p.statuses {
		if _, ok := want[name]; !ok {
			delete(p.statuses, name)
		}
	}

	for name, t := range want {
		st, ok := p.statuses[name]
		if !ok {
			st = &RouterStatus{Name: t.Name, Impl: t.Impl, URL: t.URL, State: StatePending}
			p.statuses[name] = st
		} else {
			st.Impl = t.Impl
			st.URL = t.URL
		}
		st.ContainerID = t.ContainerID
		st.IP = t.IP
		if st.checkedKF == t.key() {
			continue
		}
		if c, ok := p.clients[name]; ok {
			c.Close()
			delete(p.clients, name)
		}
		opts := []i2pcontrol.Option{i2pcontrol.WithTimeout(p.timeout)}
		switch {
		case t.InsecureTLS:
			opts = append(opts, i2pcontrol.WithInsecureTLS())
		case strings.HasPrefix(t.URL, "https://") && len(t.PinnedCertPEM) > 0:
			opts = append(opts, i2pcontrol.WithPinnedCertPEM(t.PinnedCertPEM))
		}
		cl, err := i2pcontrol.New(t.Name, t.URL, t.Password, opts...)
		if err != nil {
			// Bad pinned cert etc.: record it and show the router as down.
			st.State = StateDown
			st.LastError = fmt.Sprintf("client setup failed: %v", err)
			st.checkedKF = ""
			continue
		}
		p.clients[name] = cl
		st.checkedKF = t.key()
	}
}

// Snapshot returns a consistent view of all routers, sorted by name.
func (p *Poller) Snapshot() []*RouterStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*RouterStatus, 0, len(p.statuses))
	for _, st := range p.statuses {
		out = append(out, st.copy())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns one router's status, if known.
func (p *Poller) Get(name string) (*RouterStatus, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	st, ok := p.statuses[name]
	if !ok {
		return nil, false
	}
	return st.copy(), true
}

// copy snapshots a status for readers outside the poller's lock.
func (st *RouterStatus) copy() *RouterStatus {
	cp := *st
	cp.History = append([]*i2pcontrol.Sample(nil), st.History...)
	return &cp
}

// Totals returns router counts by state for the summary header.
func (p *Poller) Totals() (routers, up, down, stale, pending int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, st := range p.statuses {
		routers++
		switch st.State {
		case StateUp:
			up++
		case StateDown:
			down++
		case StateStale:
			stale++
		default:
			pending++
		}
	}
	return
}

// NetworkTotals sums the current sample of every router that reported each
// field, so the operator can read network-wide peers/tunnels/bandwidth
// instead of adding up rows by hand. A field is nil when no router supplied
// it — i2pd does not serve tunnel in/out, so that stays honest rather than
// becoming a fake zero.
func (p *Poller) NetworkTotals() *NetworkTotals {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.aggregateLocked(time.Now())
}

// NetworkHistory returns the recorded aggregate samples, newest first, capped
// at HistorySamples — the data the network charts are drawn from.
func (p *Poller) NetworkHistory() []*NetworkTotals {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*NetworkTotals, len(p.network))
	copy(out, p.network)
	return out
}

// recordNetworkHistory appends this interval's aggregate. Callers hold p.mu.
func (p *Poller) recordNetworkHistory(now time.Time) {
	totals := p.aggregateLocked(now)
	p.network = append([]*NetworkTotals{totals}, p.network...)
	if len(p.network) > HistorySamples {
		p.network = p.network[:HistorySamples]
	}
}

// aggregateLocked computes the network-wide sums. Callers hold at least the
// read lock.
func (p *Poller) aggregateLocked(now time.Time) *NetworkTotals {
	t := &NetworkTotals{CollectedAt: now}
	var known, active, tin, tout, part int64
	var bwIn, bwOut float64
	var hasKnown, hasActive, hasTin, hasTout, hasPart, hasBW bool
	for _, st := range p.statuses {
		t.Routers++
		if st.State == StateUp {
			t.Up++
		}
		s := st.Sample
		if s == nil {
			continue
		}
		// Only count data that is not older than the stale window: summing a
		// router that died ten minutes ago would silently skew the total.
		if st.LastSeen.IsZero() || now.Sub(st.LastSeen) > time.Duration(StaleIntervals)*p.interval {
			continue
		}
		if v := s.KnownPeers; v != nil {
			known += *v
			hasKnown = true
		}
		if v := s.ActivePeers; v != nil {
			active += *v
			hasActive = true
		}
		if v := s.TunnelsInbound; v != nil {
			tin += *v
			hasTin = true
		}
		if v := s.TunnelsOutbound; v != nil {
			tout += *v
			hasTout = true
		}
		if v := s.Participating; v != nil {
			part += *v
			hasPart = true
		}
		if v := s.BWInbound1s; v != nil {
			bwIn += *v
			hasBW = true
		}
		if v := s.BWOutbound1s; v != nil {
			bwOut += *v
		}
	}
	if hasKnown {
		t.KnownPeers = &known
	}
	if hasActive {
		t.ActivePeers = &active
	}
	if hasTin {
		t.TunnelsInbound = &tin
	}
	if hasTout {
		t.TunnelsOutbound = &tout
	}
	if hasPart {
		t.TunnelsParticip = &part
	}
	if hasBW {
		t.BWInbound1s = &bwIn
		t.BWOutbound1s = &bwOut
	}
	return t
}
