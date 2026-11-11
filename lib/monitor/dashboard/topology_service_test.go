package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"go-i2p-testnet/lib/monitor/topology"
	"go-i2p-testnet/lib/monitor/web"
)

// These tests use a nil Docker client on purpose: every collection path in
// Collector checks for that and gives up quietly, so the caching and lifecycle
// logic is exercised without needing a daemon. Real collection against live
// containers is covered in docs/monitoring-verification.md.

func newTestService() *TopologyService {
	p := web.NewPoller(time.Minute, time.Second, func(ctx context.Context) []web.Target {
		return nil
	})
	return NewTopologyService(nil, p)
}

// Before the first refresh the page must still render, and it must say why it
// is empty. A nil graph here would crash the template.
func TestGraphBeforeFirstRefreshExplainsItself(t *testing.T) {
	svc := newTestService()

	g := svc.Graph(context.Background())
	if g == nil {
		t.Fatal("Graph returned nil before the first refresh")
	}
	if len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Errorf("expected an empty graph, got %d nodes / %d edges", len(g.Nodes), len(g.Edges))
	}
	if len(g.Warnings) == 0 {
		t.Fatal("a cold graph must warn, otherwise an empty topology looks like an empty network")
	}
	if !strings.Contains(strings.Join(g.Warnings, " "), "not been collected") {
		t.Errorf("warning should explain the cold start, got %q", g.Warnings)
	}
}

// Start builds the first graph synchronously, so the very first page load has
// a real timestamp rather than the cold placeholder.
func TestStartPopulatesGraphSynchronously(t *testing.T) {
	svc := newTestService()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc.Start(ctx)
	defer svc.Stop()

	g := svc.Graph(ctx)
	if strings.Contains(strings.Join(g.Warnings, " "), "not been collected") {
		t.Errorf("first refresh should have run before Start returned; warnings = %q", g.Warnings)
	}
	if g.GeneratedAt.IsZero() {
		t.Error("a graph from the first refresh should carry a timestamp")
	}
}

// Stop must not leak the refresher goroutine, and starting twice must not
// launch two loops.
func TestStartIsIdempotentAndStopIsClean(t *testing.T) {
	svc := newTestService()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc.Start(ctx)
	svc.Start(ctx) // second call must be a no-op
	svc.Stop()

	select {
	case <-svc.done:
	case <-time.After(3 * time.Second):
		t.Fatal("refresher goroutine did not exit after Stop")
	}
	svc.Stop() // stopping twice must not panic
}

// Cancelling the parent context must end the loop without an explicit Stop.
func TestParentContextCancellationStopsLoop(t *testing.T) {
	svc := newTestService()
	ctx, cancel := context.WithCancel(context.Background())

	svc.Start(ctx)
	cancel()

	select {
	case <-svc.done:
	case <-time.After(3 * time.Second):
		t.Fatal("refresher did not stop when its context was cancelled")
	}
}

// The roster the collector sees must be the poller's current one, otherwise a
// router that left the testnet would keep its nodes in the graph forever.
func TestRefreshUsesCurrentRoster(t *testing.T) {
	svc := newTestService()
	svc.Start(context.Background())
	defer svc.Stop()

	g := svc.Graph(context.Background())
	if !strings.Contains(strings.Join(g.Warnings, " "), "no routers in the roster") {
		t.Errorf("an empty poller roster should reach the collector, got warnings %q", g.Warnings)
	}
}

// Graph hands out a copy: a caller rendering a page must not see the slices
// change underneath it while a background refresh runs.
func TestGraphReturnsACopy(t *testing.T) {
	svc := newTestService()
	svc.Start(context.Background())
	defer svc.Stop()

	first := svc.Graph(context.Background())
	before := len(first.Nodes)

	first.Nodes = append(first.Nodes, topology.Node{Name: "injected"})
	first.Edges = append(first.Edges, topology.Edge{From: "a", To: "b"})

	after := svc.Graph(context.Background())
	if len(after.Nodes) != before || len(after.Edges) != 0 {
		t.Errorf("mutating a returned graph changed the cached one: %d nodes / %d edges", len(after.Nodes), len(after.Edges))
	}
}
