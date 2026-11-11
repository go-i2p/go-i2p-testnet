// Package topology turns what the routers themselves report — transport
// sessions, netDb contents and router identity hashes — into a graph of
// which router is connected to which.
//
// It holds no Docker or HTTP code and nothing is hardcoded: the graph is built
// from inputs the caller gathers from live routers, and every edge carries
// whether it was directly observed (a transport session exists right now) or
// merely inferred (the router stores the peer's RouterInfo but no session was
// seen). The snapshot records the time it was built so the UI can show how old
// the graph is.
package topology

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Edge kinds.
const (
	// KindObserved: the source router currently has a transport session to
	// the peer. This is the strongest signal available.
	KindObserved = "observed"
	// KindInferred: the source stores the peer's RouterInfo in its netDb, so
	// it *can* build tunnels through it, but no session was observed.
	KindInferred = "inferred"
)

// Match strategies, reported so an operator can judge an edge's confidence.
const (
	// MatchHash: the peer hash prefix from the router's own session table
	// matched exactly one roster router's ident hash.
	MatchHash = "hash"
	// MatchIP: the peer address matched a roster router's container IP.
	MatchIP = "ip"
	// MatchNetDB: the edge came from netDb membership, keyed by hash.
	MatchNetDB = "netdb"
	// MatchExternal: the peer matched no roster router — a session pointing
	// somewhere outside the testnet, surfaced rather than dropped.
	MatchExternal = "external"
)

// Node is one vertex: a testnet router, or a peer seen in a session that is
// not part of the roster.
type Node struct {
	Name     string `json:"name"`
	Impl     string `json:"impl,omitempty"`
	IP       string `json:"ip,omitempty"`
	Hash     string `json:"hash,omitempty"` // base64 ident hash, empty until derived
	InRoster bool   `json:"in_roster"`
	Running  bool   `json:"running"`
	State    string `json:"state,omitempty"` // poller state for routers
	KnownRIs int    `json:"known_router_infos"`
}

func (n Node) label() string {
	switch {
	case n.Name != "":
		return n.Name
	case n.IP != "":
		return n.IP
	default:
		return "unknown"
	}
}

// Session is one transport session a router reported: "I am talking to this
// peer right now".
type Session struct {
	From          string // router name that reported it
	PeerIP        string
	PeerPort      int
	HashPrefix    string // short peer ident hash from the router's own table
	Transport     string // NTCP2 / SSU2 / TCP
	BytesSent     int64
	BytesReceived int64
}

// Knowledge is one router's netDb membership: the peer ident hashes stored.
type Knowledge struct {
	Router string
	Hashes []string
}

// Edge is a directed connection between two nodes.
type Edge struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Kind        string `json:"kind"`
	Match       string `json:"match"`
	Transport   string `json:"transport,omitempty"`
	BytesSent   int64  `json:"bytes_sent,omitempty"`
	BytesRecv   int64  `json:"bytes_received,omitempty"`
	PeerAddress string `json:"peer_address,omitempty"`
	PeerHash    string `json:"peer_hash,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// Key identifies an edge by its endpoints, used for de-duplication.
func (e Edge) Key() string { return e.From + "\x00" + e.To }

// Graph is a complete topology snapshot.
type Graph struct {
	GeneratedAt time.Time `json:"generated_at"`
	Source      string    `json:"source"`
	Nodes       []Node    `json:"nodes"`
	Edges       []Edge    `json:"edges"`
	Warnings    []string  `json:"warnings,omitempty"`
	Observed    int       `json:"observed"`
	Inferred    int       `json:"inferred"`
}

// BuildInput is everything Build needs, gathered by the caller from live
// routers. A plain struct keeps the builder unit-testable without Docker.
type BuildInput struct {
	GeneratedAt time.Time
	Source      string
	Nodes       []Node
	Sessions    []Session
	Knowledge   []Knowledge
	NetDBCounts map[string]int
}

type nodeIndex struct {
	byHash map[string]Node
	byIP   map[string]Node
}

func indexNodes(nodes []Node) nodeIndex {
	idx := nodeIndex{byHash: map[string]Node{}, byIP: map[string]Node{}}
	for _, n := range nodes {
		if n.Hash != "" {
			idx.byHash[n.Hash] = n
		}
		if n.IP != "" {
			idx.byIP[n.IP] = n
		}
	}
	return idx
}

// matchByHash finds the roster router whose ident hash starts with prefix.
// Routers' session tables show only the first few characters of a peer hash,
// so a prefix match is the strongest signal available; it is accepted only
// when exactly one router matches, so an ambiguous prefix never produces a
// wrong edge.
func (idx nodeIndex) matchByHash(prefix string) (Node, bool) {
	if len(prefix) < 4 {
		return Node{}, false
	}
	var found Node
	var hits int
	for h, n := range idx.byHash {
		if strings.HasPrefix(h, prefix) {
			found = n
			hits++
		}
	}
	if hits == 1 {
		return found, true
	}
	return Node{}, false
}

// Build computes the graph. Edges are de-duplicated per (from,to) pair with
// observed always winning over inferred, and the result is sorted so identical
// inputs always produce an identical graph.
func Build(in BuildInput) *Graph {
	if in.GeneratedAt.IsZero() {
		in.GeneratedAt = time.Now()
	}

	nodes := make([]Node, 0, len(in.Nodes))
	for _, n := range in.Nodes {
		n.InRoster = true
		if c, ok := in.NetDBCounts[n.Name]; ok {
			n.KnownRIs = c
		}
		nodes = append(nodes, n)
	}
	idx := indexNodes(nodes)

	// Sessions pointing outside the roster become vertices too, so an operator
	// sees them instead of them silently vanishing.
	peers := map[string]Node{}
	addPeer := func(addr, hash string) string {
		if existing, ok := peers[addr]; ok {
			return existing.label()
		}
		name := addr
		if name == "" {
			name = "unknown"
		}
		peers[addr] = Node{Name: name, IP: addr, Hash: hash}
		return name
	}

	edges := map[string]Edge{}
	var warnings []string

	// --- observed edges from transport sessions ---
	for _, s := range in.Sessions {
		if s.From == "" {
			continue
		}
		var to, match, peerHash string
		if owner, ok := idx.matchByHash(s.HashPrefix); ok {
			to, match, peerHash = owner.Name, MatchHash, owner.Hash
		} else if owner, ok := idx.byIP[s.PeerIP]; ok {
			to, match, peerHash = owner.Name, MatchIP, owner.Hash
		} else {
			addr := s.PeerIP
			if s.PeerPort != 0 {
				addr = fmt.Sprintf("%s:%d", s.PeerIP, s.PeerPort)
			}
			if addr == "" {
				warnings = append(warnings, fmt.Sprintf(
					"session from %s has no peer address (prefix %q)", s.From, s.HashPrefix))
				continue
			}
			to = addPeer(addr, s.HashPrefix)
			match, peerHash = MatchExternal, s.HashPrefix
		}
		if to == s.From {
			continue // a router never edges to itself
		}
		e := Edge{
			From: s.From, To: to, Kind: KindObserved, Match: match,
			Transport: s.Transport, BytesSent: s.BytesSent, BytesRecv: s.BytesReceived,
			PeerAddress: s.PeerIP, PeerHash: peerHash,
			Reason: fmt.Sprintf("%s session reported by %s", orUnknown(s.Transport), s.From),
		}
		if prev, ok := edges[e.Key()]; !ok || prev.Kind != KindObserved {
			edges[e.Key()] = e
		}
	}

	// --- inferred edges from netDb membership ---
	ownerOf := map[string]string{}
	for _, n := range nodes {
		if n.Hash != "" {
			ownerOf[n.Hash] = n.Name
		}
	}
	for _, k := range in.Knowledge {
		if k.Router == "" {
			continue
		}
		for _, h := range k.Hashes {
			owner, ok := ownerOf[h]
			if !ok || owner == k.Router {
				continue // not a roster router, or the router's own info
			}
			key := k.Router + "\x00" + owner
			if existing, ok := edges[key]; ok && existing.Kind == KindObserved {
				continue // already proven connected
			}
			edges[key] = Edge{
				From: k.Router, To: owner, Kind: KindInferred, Match: MatchNetDB,
				PeerHash: h,
				Reason:   fmt.Sprintf("%s stores %s's RouterInfo in its netDb", k.Router, owner),
			}
		}
	}

	// --- assemble ---
	out := &Graph{
		GeneratedAt: in.GeneratedAt,
		Source:      in.Source,
		Warnings:    dedupeSorted(warnings),
	}

	present := map[string]bool{}
	for _, n := range nodes {
		present[n.Name] = true
	}
	for _, e := range edges {
		for _, name := range []string{e.From, e.To} {
			if present[name] {
				continue
			}
			present[name] = true
			if n, ok := peers[name]; ok {
				out.Nodes = append(out.Nodes, n)
			} else {
				out.Nodes = append(out.Nodes, Node{Name: name})
			}
		}
	}
	out.Nodes = append(out.Nodes, nodes...)
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].Name < out.Nodes[j].Name })

	for _, e := range edges {
		if e.Kind == KindObserved {
			out.Observed++
		} else {
			out.Inferred++
		}
		out.Edges = append(out.Edges, e)
	}
	sort.Slice(out.Edges, func(i, j int) bool {
		if out.Edges[i].From != out.Edges[j].From {
			return out.Edges[i].From < out.Edges[j].From
		}
		if out.Edges[i].To != out.Edges[j].To {
			return out.Edges[i].To < out.Edges[j].To
		}
		return out.Edges[i].Kind < out.Edges[j].Kind
	})
	return out
}

// Degree returns per node the count of observed and inferred out-neighbours,
// used to sort the view by connectivity.
func (g *Graph) Degree() map[string][2]int {
	out := map[string][2]int{}
	for _, n := range g.Nodes {
		out[n.Name] = [2]int{}
	}
	seen := map[string]bool{}
	for _, e := range g.Edges {
		if seen[e.Key()] {
			continue
		}
		seen[e.Key()] = true
		d := out[e.From]
		if e.Kind == KindObserved {
			d[0]++
		} else {
			d[1]++
		}
		out[e.From] = d
	}
	return out
}

func orUnknown(s string) string {
	if s == "" {
		return "transport"
	}
	return s
}

func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
