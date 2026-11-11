package dashboard

// Topology service: rebuilds the graph on its own cadence in the background
// and serves the cached result to the HTTP handlers.
//
// Building a graph means executing inside every router container (transport
// table, netDb listing) and reading router.info out of each one. Doing that
// inside a request handler would make the page as slow as the slowest router
// — exactly the failure mode the poller was built to avoid — so the page
// renders whatever the last completed refresh produced, and the graph's own
// timestamp tells the operator how current it is.

import (
	"context"
	"sync"
	"time"

	"go-i2p-testnet/lib/monitor/topology"
	"go-i2p-testnet/lib/monitor/web"

	"github.com/docker/docker/client"
)

// TopologyRefresh is how often the graph is rebuilt. Slower than the metrics
// poll on purpose: adjacency changes far less often than counters do, and each
// rebuild costs several container execs.
const TopologyRefresh = 10 * time.Second

// collectionTimeout bounds one rebuild so a wedged container cannot stall the
// refresher and freeze the graph at an ever-older timestamp.
const collectionTimeout = 8 * time.Second

// TopologyService owns the cached graph and the goroutine that refreshes it.
type TopologyService struct {
	poller    *web.Poller
	collector *Collector

	mu      sync.RWMutex
	graph   *topology.Graph
	errs    []string
	runs    int
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

// NewTopologyService wires a service to a poller for the router roster.
func NewTopologyService(cli *client.Client, poller *web.Poller) *TopologyService {
	return &TopologyService{
		poller:    poller,
		collector: NewCollector(cli),
	}
}

// Start launches the refresh loop. The first graph is built synchronously so
// the very first page load has something to show.
func (t *TopologyService) Start(ctx context.Context) {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return
	}
	t.running = true
	t.done = make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	t.mu.Unlock()

	t.refresh(runCtx)
	go t.loop(runCtx, t.done)
}

func (t *TopologyService) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(TopologyRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.refresh(ctx)
		}
	}
}

// Stop halts the refresher and waits for it to exit.
func (t *TopologyService) Stop() {
	t.mu.Lock()
	running, cancel, doneCh := t.running, t.cancel, t.done
	t.running = false
	t.mu.Unlock()
	if !running {
		return
	}
	cancel()
	<-doneCh
}

// refresh rebuilds the graph under a bounded context.
func (t *TopologyService) refresh(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, collectionTimeout)
	defer cancel()

	statuses := t.poller.Snapshot()
	g := t.collector.Build(cctx, statuses)

	t.mu.Lock()
	t.graph = g
	t.runs++
	if cctx.Err() != nil {
		t.errs = []string{"topology refresh did not finish before its deadline; the graph shown is the last complete one"}
	} else {
		t.errs = nil
	}
	t.mu.Unlock()
}

// Graph returns the cached graph. It never blocks on collection, and returns
// an empty-but-valid graph before the first refresh completes so the page can
// explain itself instead of erroring.
func (t *TopologyService) Graph(ctx context.Context) *topology.Graph {
	t.mu.RLock()
	g := t.graph
	runs := t.runs
	errs := t.errs
	t.mu.RUnlock()

	if g != nil {
		// Copy the slice headers so a concurrent refresh cannot resize what a
		// caller is rendering.
		out := *g
		out.Nodes = append([]topology.Node(nil), g.Nodes...)
		out.Edges = append([]topology.Edge(nil), g.Edges...)
		out.Warnings = append(out.Warnings, errs...)
		return &out
	}
	age := "not collected yet"
	if runs == 0 {
		age = "waiting for the first refresh"
	}
	return &topology.Graph{
		Source:   "router transport sessions + netDb contents + router.info identity",
		Warnings: []string{"topology has not been collected yet (" + age + ")"},
	}
}
