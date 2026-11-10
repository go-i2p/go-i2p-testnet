// Package web implements the self-contained testnet dashboard: an
// in-memory poller over the I2PControl client plus one embedded net/http
// server rendering html/template pages (go:embed) and a JSON API.
package web

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go-i2p-testnet/lib/monitor/i2pcontrol"
)

// Router states. Down renders red, Stale renders amber.
const (
	StatePending = "pending" // never sampled yet
	StateUp      = "up"
	StateDown    = "down"
	StateStale   = "stale"
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
	Name      string
	Impl      string
	URL       string
	State     string
	LastError string
	LastSeen  time.Time // last successful sample
	Sample    *i2pcontrol.Sample
	History   []*i2pcontrol.Sample // newest first, capped at HistorySamples

	checkedKF string // fingerprint of the last synced client config
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
	inFlight map[string]bool // router name -> poll goroutine running

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
		p.mu.Unlock()

		go func(name string, cl *i2pcontrol.Client, st *RouterStatus) {
			defer func() {
				p.mu.Lock()
				p.inFlight[name] = false
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
				cur.State = StateDown
				cur.LastError = err.Error()
				return
			}
			cur.Sample = sample
			cur.LastSeen = sample.CollectedAt
			cur.LastError = ""
			cur.State = StateUp
			cur.History = append([]*i2pcontrol.Sample{sample}, cur.History...)
			if len(cur.History) > HistorySamples {
				cur.History = cur.History[:HistorySamples]
			}
		}(j.name, j.cl, j.st)
	}
}

// sweepStale marks routers whose last good sample is older than
// StaleIntervals intervals as stale (amber) — unless their current poll
// failed, which is reported as down (red) with the last error.
func (p *Poller) sweepStale() {
	cutoff := time.Now().Add(-time.Duration(StaleIntervals) * p.interval)
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, st := range p.statuses {
		switch st.State {
		case StateUp:
			if !st.LastSeen.IsZero() && st.LastSeen.Before(cutoff) {
				st.State = StateStale
			}
		case StateStale:
			// Recovered to up when a fresh sample landed after the sweep.
		}
	}
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
