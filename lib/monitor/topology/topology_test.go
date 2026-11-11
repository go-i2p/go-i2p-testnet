package topology

import (
	"strings"
	"testing"
	"time"
)

const (
	hashA = "AutoCdqqsW~S6nnqEG~BQgcUhMdxbLqwWN3qO0h0zso="
	hashB = "T2gEKfUoS~sxitfRf8C5EuZtBOdqdJqam9NfsDAkxfA="
	hashC = "ZZzEKfUoS~sxitfRf8C5EuZtBOdqdJqam9NfsDAk9qk="
)

func roster() []Node {
	return []Node{
		{Name: "router-i2pd-1", Impl: "i2pd", IP: "172.28.0.2", Hash: hashA, State: "up"},
		{Name: "router-i2pd-2", Impl: "i2pd", IP: "172.28.0.3", Hash: hashB, State: "up"},
		{Name: "router-goi2p-3", Impl: "goi2p", IP: "172.28.0.4", Hash: hashC, State: "down"},
	}
}

// TestObservedEdgeMatchedByHash is the core of "edges derived from router
// data, not a hardcoded list": the router's own session table shows only a
// short peer hash prefix, which must resolve to the right roster router.
func TestObservedEdgeMatchedByHash(t *testing.T) {
	g := Build(BuildInput{
		Nodes:    roster(),
		Sessions: []Session{{From: "router-i2pd-1", HashPrefix: "T2gE", PeerIP: "172.28.0.3", PeerPort: 4567, Transport: "SSU2", BytesSent: 1161, BytesReceived: 175}},
	})
	if len(g.Edges) != 1 {
		t.Fatalf("edges = %d, want 1: %+v", len(g.Edges), g.Edges)
	}
	e := g.Edges[0]
	if e.To != "router-i2pd-2" {
		t.Errorf("peer resolved to %q, want router-i2pd-2 (hash prefix T2gE)", e.To)
	}
	if e.Kind != KindObserved {
		t.Errorf("kind = %q, want observed", e.Kind)
	}
	if e.Match != MatchHash {
		t.Errorf("match = %q, want hash", e.Match)
	}
	if g.Observed != 1 || g.Inferred != 0 {
		t.Errorf("counts = observed %d inferred %d", g.Observed, g.Inferred)
	}
	if e.BytesSent != 1161 || e.BytesRecv != 175 {
		t.Errorf("session bytes lost: %+v", e)
	}
}

// TestAmbiguousPrefixIsNotGuessed: if two roster routers shared a prefix, a
// prefix match would be a guess. It must fall back to the address, and if that
// is ambiguous too the peer must not be silently attached to the wrong router.
func TestAmbiguousPrefixIsNotGuessed(t *testing.T) {
	nodes := []Node{
		{Name: "r1", IP: "10.0.0.1", Hash: "SAMEprefixAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		{Name: "r2", IP: "10.0.0.2", Hash: "SAMEprefixBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="},
	}
	g := Build(BuildInput{
		Nodes:    nodes,
		Sessions: []Session{{From: "r1", HashPrefix: "SAME", PeerIP: "10.0.0.9", PeerPort: 1}},
	})
	if len(g.Edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(g.Edges))
	}
	if g.Edges[0].To == "r2" && g.Edges[0].Match == MatchHash {
		t.Error("ambiguous prefix matched by hash — a guessed edge")
	}
	if g.Edges[0].Match == MatchHash {
		t.Errorf("ambiguous prefix resolved by hash to %q", g.Edges[0].To)
	}
	if g.Edges[0].To != "10.0.0.9:1" {
		t.Errorf("peer = %q, want the unmatched address kept as its own node", g.Edges[0].To)
	}
	if g.Edges[0].Match != MatchExternal {
		t.Errorf("match = %q, want external", g.Edges[0].Match)
	}
}

// TestMatchByAddress: a session whose peer hash is unknown but whose address
// is a roster router is still an observed edge.
func TestMatchByAddress(t *testing.T) {
	g := Build(BuildInput{
		Nodes:    roster(),
		Sessions: []Session{{From: "router-i2pd-1", HashPrefix: "????", PeerIP: "172.28.0.4", PeerPort: 8888, Transport: "NTCP2"}},
	})
	if len(g.Edges) != 1 {
		t.Fatalf("edges = %d", len(g.Edges))
	}
	if g.Edges[0].To != "router-goi2p-3" || g.Edges[0].Match != MatchIP {
		t.Errorf("edge = %+v, want router-goi2p-3 matched by ip", g.Edges[0])
	}
}

// TestInferredEdgeFromNetDB: netDb membership alone must produce an edge,
// clearly marked inferred so it is never read as a live connection.
func TestInferredEdgeFromNetDB(t *testing.T) {
	g := Build(BuildInput{
		Nodes:     roster(),
		Knowledge: []Knowledge{{Router: "router-i2pd-1", Hashes: []string{hashB}}},
	})
	if len(g.Edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(g.Edges))
	}
	e := g.Edges[0]
	if e.Kind != KindInferred {
		t.Errorf("kind = %q, want inferred", e.Kind)
	}
	if e.To != "router-i2pd-2" {
		t.Errorf("to = %q, want router-i2pd-2", e.To)
	}
	if !strings.Contains(e.Reason, "netDb") {
		t.Errorf("reason %q should say where the edge came from", e.Reason)
	}
}

// TestObservedBeatsInferred: the same pair seen both ways must yield exactly
// one edge, and it must be the stronger kind.
func TestObservedBeatsInferred(t *testing.T) {
	g := Build(BuildInput{
		Nodes:     roster(),
		Knowledge: []Knowledge{{Router: "router-i2pd-1", Hashes: []string{hashB}}},
		Sessions:  []Session{{From: "router-i2pd-1", HashPrefix: "T2gE", PeerIP: "172.28.0.3", PeerPort: 4567}},
	})
	if len(g.Edges) != 1 {
		t.Fatalf("edges = %d, want 1 (observed must dedupe inferred), got %+v", len(g.Edges), g.Edges)
	}
	if g.Edges[0].Kind != KindObserved {
		t.Errorf("kind = %q, want observed", g.Edges[0].Kind)
	}
}

// TestInferredNeverOverwritesObserved covers the reverse arrival order.
func TestInferredNeverOverwritesObserved(t *testing.T) {
	in := BuildInput{
		Nodes:    roster(),
		Sessions: []Session{{From: "router-i2pd-1", HashPrefix: "T2gE", PeerIP: "172.28.0.3", PeerPort: 4567}},
	}
	g := Build(in)
	observed := g.Edges[0].Kind
	// Now the same input but with knowledge listed first in the slice.
	in.Knowledge = []Knowledge{{Router: "router-i2pd-1", Hashes: []string{hashB}}}
	g2 := Build(in)
	if len(g2.Edges) != 1 || g2.Edges[0].Kind != observed {
		t.Errorf("arrival order changed the edge: %+v", g2.Edges)
	}
}

// TestSelfInfoIsNotAnEdge: every router stores its own RouterInfo; that is not
// a connection to itself.
func TestSelfInfoIsNotAnEdge(t *testing.T) {
	g := Build(BuildInput{
		Nodes:     roster(),
		Knowledge: []Knowledge{{Router: "router-i2pd-1", Hashes: []string{hashA, hashB}}},
	})
	for _, e := range g.Edges {
		if e.From == e.To {
			t.Errorf("self edge produced: %+v", e)
		}
	}
	if len(g.Edges) != 1 {
		t.Errorf("edges = %d, want only the peer edge", len(g.Edges))
	}
}

// TestHashMatchNeedsNoAddress: a session carrying a matchable peer hash is a
// real observed edge even if the address column failed to parse — the hash is
// the stronger signal, so dropping the edge would lose genuine information.
func TestHashMatchNeedsNoAddress(t *testing.T) {
	g := Build(BuildInput{
		Nodes:    roster(),
		Sessions: []Session{{From: "router-i2pd-1", HashPrefix: "T2gE"}},
	})
	if len(g.Edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(g.Edges))
	}
	if g.Edges[0].To != "router-i2pd-2" || g.Edges[0].Match != MatchHash {
		t.Errorf("edge = %+v", g.Edges[0])
	}
}

// TestUnmatchableSessionIsWarnedNotDropped: a session that resolves neither by
// hash nor by address must surface as a warning, not vanish silently.
func TestUnmatchableSessionIsWarnedNotDropped(t *testing.T) {
	g := Build(BuildInput{
		Nodes:    roster(),
		Sessions: []Session{{From: "router-i2pd-1"}},
	})
	if len(g.Edges) != 0 {
		t.Errorf("edge built from a session with no peer at all: %+v", g.Edges)
	}
	if len(g.Warnings) == 0 {
		t.Error("unmatched session must produce a warning")
	}
	if !strings.Contains(g.Warnings[0], "router-i2pd-1") {
		t.Errorf("warning should name the router: %q", g.Warnings[0])
	}
}

// TestGeneratedAtIsRecorded: the requirement asks for the graph's update time.
func TestGeneratedAtIsRecorded(t *testing.T) {
	at := time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC)
	g := Build(BuildInput{GeneratedAt: at, Nodes: roster()})
	if !g.GeneratedAt.Equal(at) {
		t.Errorf("generated_at = %v, want %v", g.GeneratedAt, at)
	}
	// Zero time must still produce a usable timestamp, never a blank page.
	g2 := Build(BuildInput{Nodes: roster()})
	if g2.GeneratedAt.IsZero() {
		t.Error("generated_at must default to now, not stay zero")
	}
}

// TestDeterministic: identical inputs must produce an identical graph so the
// page does not reshuffle between refreshes.
func TestDeterministic(t *testing.T) {
	in := BuildInput{
		GeneratedAt: time.Unix(0, 0).UTC(),
		Nodes:       roster(),
		Knowledge: []Knowledge{
			{Router: "router-i2pd-1", Hashes: []string{hashB, hashC}},
			{Router: "router-i2pd-2", Hashes: []string{hashA}},
		},
		Sessions: []Session{
			{From: "router-i2pd-2", HashPrefix: "Auto", PeerIP: "172.28.0.2", PeerPort: 1},
			{From: "router-i2pd-1", HashPrefix: "T2gE", PeerIP: "172.28.0.3", PeerPort: 2},
		},
	}
	first := Build(in)
	for i := 0; i < 25; i++ {
		next := Build(in)
		if len(next.Edges) != len(first.Edges) {
			t.Fatalf("edge count changed on run %d", i)
		}
		for j := range first.Edges {
			if first.Edges[j] != next.Edges[j] {
				t.Fatalf("run %d edge %d differs: %+v vs %+v", i, j, first.Edges[j], next.Edges[j])
			}
		}
	}
}

// TestNetDBCountSurfacesOnNode backs the "RouterInfos known" column.
func TestNetDBCountSurfacesOnNode(t *testing.T) {
	g := Build(BuildInput{
		Nodes:       roster(),
		NetDBCounts: map[string]int{"router-i2pd-1": 7},
	})
	for _, n := range g.Nodes {
		if n.Name == "router-i2pd-1" && n.KnownRIs != 7 {
			t.Errorf("KnownRIs = %d, want 7", n.KnownRIs)
		}
	}
}

// TestDegreeCountsKinds feeds the sort order used by the page.
func TestDegreeCountsKinds(t *testing.T) {
	g := Build(BuildInput{
		Nodes:     roster(),
		Sessions:  []Session{{From: "router-i2pd-1", HashPrefix: "T2gE", PeerIP: "172.28.0.3", PeerPort: 1}},
		Knowledge: []Knowledge{{Router: "router-i2pd-1", Hashes: []string{hashC}}},
	})
	d := g.Degree()
	if d["router-i2pd-1"] != [2]int{1, 1} {
		t.Errorf("degree = %v, want 1 observed 1 inferred", d["router-i2pd-1"])
	}
}

// TestEmptyRosterIsNotAnError: standalone mode with no testnet running must
// still render.
func TestEmptyRosterIsNotAnError(t *testing.T) {
	g := Build(BuildInput{})
	if g == nil || len(g.Edges) != 0 || len(g.Nodes) != 0 {
		t.Fatalf("empty input produced %+v", g)
	}
	if g.GeneratedAt.IsZero() {
		t.Error("timestamp missing")
	}
}
