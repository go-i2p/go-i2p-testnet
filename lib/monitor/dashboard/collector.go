package dashboard

// Topology collection: everything here reads what the routers themselves
// know — i2pd's transport session table, each router's netDb contents, and
// each router's own router.info identity — and feeds it to the topology
// builder. Nothing about the network's shape is hardcoded: no peer lists, no
// expected adjacency, no router names baked in. A router that is not in the
// roster still appears if it shows up in a session.

import (
	"context"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-i2p-testnet/lib/docker_control"
	goi2pnode "go-i2p-testnet/lib/go-i2p"
	"go-i2p-testnet/lib/i2pd"
	"go-i2p-testnet/lib/monitor/topology"
	"go-i2p-testnet/lib/monitor/web"

	"github.com/docker/docker/client"
	"github.com/go-i2p/common/base64"
	"github.com/go-i2p/common/router_info"
)

// netDBDir is where i2pd keeps peer RouterInfos inside its datadir. The
// layout is r<X>/routerInfo-<hash>.dat, and the hash in the file name is the
// peer's base64 ident hash — the same value i2pd prints (truncated) in its
// transport table, which is what lets an edge be matched by hash instead of
// guessed from an address.
var (
	netDBDir       = i2pd.I2PDDataDir + "/netDb"
	routerInfoPath = i2pd.I2PDDataDir + "/router.info"
)

// goRouterInfoPath is go-i2p's equivalent, when it publishes one. It sits
// beside GoI2PNetDbDir, whose value this package reads from the go-i2p
// launcher rather than repeating.
const goRouterInfoPath = "/root/.go-i2p/config/router.info"

// webconsolePort is i2pd's local HTTP console, reachable only from inside the
// container. It is the only source for transport sessions, which I2PControl
// does not expose.
const webconsolePort = 7070

// hashTTL and netdbTTL bound how often the rarely-changing facts are re-read.
// Identity hashes only change when a router regenerates its keys; netDb
// membership only changes on sync. Sessions are read on every refresh.
const (
	hashTTL  = 5 * time.Minute
	netdbTTL = 20 * time.Second
)

// sessionRe matches one transport-session row after HTML tags are stripped.
// i2pd prints: <peerhashprefix>: <ip>:<port> => [sent:received]
// The arrow arrives as the &#8658; entity or a literal ⇒ depending on
// whether the tags were stripped before or after entity decoding, so the
// separator is matched loosely.
var sessionRe = regexp.MustCompile(`([A-Za-z0-9+/~-]{4,}):\s*(\d+\.\d+\.\d+\.\d+):(\d+)[^\[]*\[(\d+):(\d+)\]`)

// transportRe finds the transport name heading each session block.
var transportRe = regexp.MustCompile(`(NTCP2?|SSU2?|TCP)\s*\(\s*\d+\s*\)`)

// Collector gathers topology inputs for a set of router targets. It is safe
// for concurrent use and caches the slow-changing facts.
type Collector struct {
	cli *client.Client

	mu       sync.Mutex
	hashes   map[string]cacheEntry // containerID -> ident hash
	netdbs   map[string]cacheList  // router name -> peer hashes + count
	lastWarn map[string]time.Time
}

type cacheEntry struct {
	value string
	at    time.Time
}

type cacheList struct {
	hashes []string
	count  int
	at     time.Time
}

// NewCollector builds a collector. A nil cli yields an empty graph rather
// than panicking, which keeps the dashboard usable when Docker is unreachable.
func NewCollector(cli *client.Client) *Collector {
	return &Collector{
		cli:      cli,
		hashes:   map[string]cacheEntry{},
		netdbs:   map[string]cacheList{},
		lastWarn: map[string]time.Time{},
	}
}

// warnOnce logs a collection failure at most once per minute per router, so a
// router that cannot be read does not spam the operator every 5 seconds.
func (c *Collector) warnOnce(router, msg string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if last, ok := c.lastWarn[router]; ok && time.Since(last) < time.Minute {
		return
	}
	c.lastWarn[router] = time.Now()
	log.WithField("router", router).Warnf(msg, args...)
}

// collectionConcurrency bounds how many routers are read in parallel during
// one topology refresh. Each router costs two to three Docker execs, so
// unbounded parallelism would hammer the Docker daemon; this keeps it modest
// while still finishing well within the 8 s budget for large rosters.
const collectionConcurrency = 8

// Inputs collects the topology inputs for the current targets and returns a
// built graph. Routers without a container id (tests, or discovery that
// cannot see Docker) contribute nodes but no edges, and a warning says so.
//
// Reads run in parallel across routers so one slow container cannot stall
// the whole refresh. Results are merged deterministically (sorted by router
// name) so identical inputs always produce an identical graph.
func (c *Collector) Inputs(ctx context.Context, statuses []*web.RouterStatus) topology.BuildInput {
	in := topology.BuildInput{
		GeneratedAt: time.Now(),
		Source:      "router transport sessions + netDb contents + router.info identity",
		NetDBCounts: map[string]int{},
	}
	if len(statuses) == 0 {
		return in
	}

	// Sort statuses by name so parallel results merge deterministically.
	sorted := make([]*web.RouterStatus, len(statuses))
	copy(sorted, statuses)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	type result struct {
		name     string
		hash     string
		running  bool
		hashes   []string
		count    int
		sessions []topology.Session
		skipped  bool
	}

	results := make([]result, len(sorted))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, collectionConcurrency)

	for i, st := range sorted {
		wg.Add(1)
		go func(i int, st *web.RouterStatus) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			r := result{name: st.Name}
			r.hash = c.identHash(ctx, st)
			r.running = c.running(ctx, st.ContainerID)

			if st.ContainerID == "" {
				r.skipped = true
			} else {
				h, count := c.netDBHashes(ctx, st)
				r.hashes = h
				r.count = count
				if st.Impl == ImplI2PD {
					r.sessions = c.sessions(ctx, st)
				}
			}
			mu.Lock()
			results[i] = r
			mu.Unlock()
		}(i, st)
	}
	wg.Wait()

	// Merge results in deterministic order.
	nodes := make([]topology.Node, 0, len(sorted))
	var skipped []string
	for _, r := range results {
		nodes = append(nodes, topology.Node{
			Name:    r.name,
			Impl:    implForName(sorted, r.name),
			IP:      ipForName(sorted, r.name),
			Hash:    r.hash,
			Running: r.running,
			State:   stateForName(sorted, r.name),
		})
		if r.skipped {
			skipped = append(skipped, r.name)
			continue
		}
		in.NetDBCounts[r.name] = r.count
		if len(r.hashes) > 0 {
			in.Knowledge = append(in.Knowledge, topology.Knowledge{Router: r.name, Hashes: r.hashes})
		}
		for _, s := range r.sessions {
			in.Sessions = append(in.Sessions, s)
		}
	}
	in.Nodes = nodes

	// Routers with no container id cannot be read for sessions or netDb
	// contents; the graph will show them as isolated, and Build reports the
	// reason so an operator knows the edges are unknown, not absent.
	if len(skipped) > 0 {
		log.WithField("routers", strings.Join(skipped, ", ")).
			Debug("topology: routers without a container id were skipped")
	}
	return in
}

func implForName(statuses []*web.RouterStatus, name string) string {
	for _, st := range statuses {
		if st.Name == name {
			return st.Impl
		}
	}
	return ""
}

func ipForName(statuses []*web.RouterStatus, name string) string {
	for _, st := range statuses {
		if st.Name == name {
			return st.IP
		}
	}
	return ""
}

func stateForName(statuses []*web.RouterStatus, name string) string {
	for _, st := range statuses {
		if st.Name == name {
			return st.State
		}
	}
	return ""
}

// Build collects and builds in one step, attaching collection warnings so an
// operator can tell "no edges" apart from "edges could not be read".
func (c *Collector) Build(ctx context.Context, statuses []*web.RouterStatus) *topology.Graph {
	in := c.Inputs(ctx, statuses)
	g := topology.Build(in)
	if len(in.Nodes) == 0 {
		g.Warnings = append(g.Warnings, "no routers in the roster yet")
		return g
	}
	var unreadable []string
	for _, st := range statuses {
		if st.ContainerID == "" {
			unreadable = append(unreadable, st.Name)
		}
	}
	if len(unreadable) > 0 {
		g.Warnings = append(g.Warnings, "no container id for "+strings.Join(unreadable, ", ")+"; their edges are unknown, not absent")
	}
	// A router that is not running cannot report sessions; say so instead of
	// letting its edges silently disappear from the graph. Routers already
	// reported above as having no container id are skipped, so the operator
	// gets one accurate reason rather than two contradictory ones.
	for _, n := range g.Nodes {
		if !n.InRoster || n.Running {
			continue
		}
		if contains(unreadable, n.Name) {
			continue
		}
		g.Warnings = append(g.Warnings, n.Name+" is not running, so its sessions are not in this graph")
	}
	return g
}

// identHash derives a router's own base64 ident hash from its router.info,
// cached because it only changes if the router regenerates its keys. This is
// what makes an edge matchable by hash: the same value appears in the peer
// netDb file names and (truncated) in the transport table.
func (c *Collector) identHash(ctx context.Context, st *web.RouterStatus) string {
	if st.ContainerID == "" || c.cli == nil {
		return ""
	}
	c.mu.Lock()
	if e, ok := c.hashes[st.ContainerID]; ok && time.Since(e.at) < hashTTL {
		c.mu.Unlock()
		return e.value
	}
	c.mu.Unlock()

	p := routerInfoPath
	if st.Impl != ImplI2PD {
		p = goRouterInfoPath
	}
	raw, err := docker_control.ReadFileFromContainerUnarchive(c.cli, ctx, st.ContainerID, p)
	var hash string
	if err == nil {
		hash = ParseIdentHash([]byte(raw))
	}
	if hash == "" {
		// Cache the miss briefly so a router that has not published its
		// router.info yet is not re-read every cycle.
		c.mu.Lock()
		if e, ok := c.hashes[st.ContainerID]; !ok || time.Since(e.at) > 30*time.Second {
			c.hashes[st.ContainerID] = cacheEntry{value: "", at: time.Now()}
		}
		c.mu.Unlock()
		c.warnOnce(st.Name, "router identity not available from %s", p)
		return ""
	}
	c.mu.Lock()
	c.hashes[st.ContainerID] = cacheEntry{value: hash, at: time.Now()}
	c.mu.Unlock()
	return hash
}

// ParseIdentHash turns raw router.info bytes into the base64 ident hash. It
// returns "" when the bytes are not a parseable RouterInfo, which is the
// normal state for a router that has not published yet.
func ParseIdentHash(raw []byte) string {
	raw = trimRouterInfoBytes(raw)
	if len(raw) == 0 {
		return ""
	}
	ri, _, err := router_info.ReadRouterInfo(raw)
	if err != nil {
		return ""
	}
	h, err := ri.IdentHash()
	if err != nil {
		return ""
	}
	return base64.EncodeToString(h[:])
}

// trimRouterInfoBytes accepts either raw binary RouterInfo bytes or the
// base64 text form some routers store on disk.
func trimRouterInfoBytes(raw []byte) []byte {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return nil
	}
	// Base64 text form: decode it, and fall back to the raw bytes when it is
	// actually binary.
	if isASCII(s) {
		if dec, err := base64.DecodeString(s); err == nil && len(dec) > 0 {
			return dec
		}
	}
	return raw
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}

// netDBHashes lists the peer RouterInfo hashes a router stores, cached for a
// short interval because it only changes on sync.
func (c *Collector) netDBHashes(ctx context.Context, st *web.RouterStatus) ([]string, int) {
	if st.ContainerID == "" || c.cli == nil {
		return nil, 0
	}
	c.mu.Lock()
	if e, ok := c.netdbs[st.Name]; ok && time.Since(e.at) < netdbTTL {
		c.mu.Unlock()
		return e.hashes, e.count
	}
	c.mu.Unlock()

	dir := netDBDir
	if st.Impl != ImplI2PD {
		dir = goi2pnode.GoI2PNetDbDir
	}
	// Print just the file names; the hash is in the name.
	cmd := []string{"sh", "-c", "find " + dir + " -name 'routerInfo-*.dat' 2>/dev/null | sed 's|.*/routerInfo-||; s|\\.dat$||'"}
	out, err := docker_control.ExecInContainer(c.cli, ctx, st.ContainerID, cmd)
	if err != nil {
		c.warnOnce(st.Name, "netDb listing failed: %v", err)
		return nil, 0
	}
	hashes := ParseNetDBListing(out)
	c.mu.Lock()
	c.netdbs[st.Name] = cacheList{hashes: hashes, count: len(hashes), at: time.Now()}
	c.mu.Unlock()
	return hashes, len(hashes)
}

// ParseNetDBListing turns the output of a netDb walk into peer ident hashes.
// It accepts both bare names and full paths, with or without the
// "routerInfo-"/".dat" decoration, so it does not depend on the shell
// pipeline having already trimmed them. Directory rows, blank lines and
// anything that is not a hash are ignored.
func ParseNetDBListing(out string) []string {
	var hashes []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		h := strings.TrimSpace(path.Base(strings.TrimSpace(line)))
		h = strings.TrimPrefix(h, "routerInfo-")
		h = strings.TrimSuffix(h, ".dat")
		if h == "" || h == "." || seen[h] {
			continue
		}
		// A base64 ident hash is long and made of the base64-url alphabet
		// i2pd uses; reject anything that leaked from the shell prompt or a
		// directory name.
		if len(h) < 40 || !isHashName(h) {
			continue
		}
		seen[h] = true
		hashes = append(hashes, h)
	}
	return hashes
}

func isHashName(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I', 'J', 'K', 'L', 'M',
			'N', 'O', 'P', 'Q', 'R', 'S', 'T', 'U', 'V', 'W', 'X', 'Y', 'Z',
			'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm',
			'n', 'o', 'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z',
			'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '-', '_', '~', '=', '+', '/':
		default:
			return false
		}
	}
	return true
}

// sessions reads i2pd's transport session table from inside the container.
// This is the only place live adjacency can come from: I2PControl reports
// peer *counts* but never which peers.
func (c *Collector) sessions(ctx context.Context, st *web.RouterStatus) []topology.Session {
	if st.ContainerID == "" || c.cli == nil || st.Impl != ImplI2PD {
		return nil
	}
	cmd := []string{"sh", "-c",
		`wget -qO- "http://127.0.0.1:` + strconv.Itoa(webconsolePort) + `/?page=transports" 2>/dev/null | sed 's/<[^>]*>/ /g'`}
	out, err := docker_control.ExecInContainer(c.cli, ctx, st.ContainerID, cmd)
	if err != nil {
		c.warnOnce(st.Name, "transport table unavailable: %v", err)
		return nil
	}
	return ParseSessions(st.Name, out)
}

// ParseSessions extracts transport sessions from a tag-stripped i2pd
// transports page. Each session carries the peer's short hash prefix, which
// the builder matches against roster ident hashes; the transport name comes
// from the section heading above the row.
func ParseSessions(router, page string) []topology.Session {
	var out []topology.Session
	transport := ""
	for _, line := range strings.Split(page, "\n") {
		if m := transportRe.FindStringSubmatch(line); m != nil {
			transport = normalizeTransport(m[1])
		}
		for _, m := range sessionRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 6 {
				continue
			}
			port, err1 := strconv.Atoi(m[3])
			sent, err2 := strconv.ParseInt(m[4], 10, 64)
			recv, err3 := strconv.ParseInt(m[5], 10, 64)
			if err1 != nil || err2 != nil || err3 != nil {
				continue
			}
			out = append(out, topology.Session{
				From:          router,
				HashPrefix:    m[1],
				PeerIP:        m[2],
				PeerPort:      port,
				Transport:     transport,
				BytesSent:     sent,
				BytesReceived: recv,
			})
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func normalizeTransport(s string) string {
	switch {
	case strings.HasPrefix(s, "NTCP"):
		return "NTCP2"
	case strings.HasPrefix(s, "SSU"):
		return "SSU2"
	default:
		return s
	}
}

// running reports container liveness from the Docker API, which is the
// authoritative answer to "is this router down" and does not depend on the
// control endpoint answering.
func (c *Collector) running(ctx context.Context, containerID string) bool {
	if containerID == "" || c.cli == nil {
		return false
	}
	inspect, err := c.cli.ContainerInspect(ctx, containerID)
	if err != nil || inspect.State == nil {
		return false
	}
	return inspect.State.Running
}

// WebconsoleTunnels reads i2pd's tunnels page from inside the container and
// returns the inbound and outbound tunnel counts. i2pd does not serve these
// over I2PControl, so the webconsole is the only source.
func (c *Collector) WebconsoleTunnels(ctx context.Context, st *web.RouterStatus) (inbound, outbound *int64) {
	if st.ContainerID == "" || c.cli == nil || st.Impl != ImplI2PD {
		return nil, nil
	}
	cmd := []string{"sh", "-c",
		`wget -qO- "http://127.0.0.1:` + strconv.Itoa(webconsolePort) + `/?page=tunnels" 2>/dev/null | sed 's/<[^>]*>/ /g' | grep -v "^\s*$"`}
	out, err := docker_control.ExecInContainer(c.cli, ctx, st.ContainerID, cmd)
	if err != nil {
		c.warnOnce(st.Name, "tunnels page unavailable: %v", err)
		return nil, nil
	}
	return parseTunnelCounts(out)
}

// parseTunnelCounts extracts inbound and outbound tunnel counts from a
// tag-stripped i2pd tunnels page. The page lists tunnels under "Inbound
// tunnels:" and "Outbound tunnels:" headings, one per line.
func parseTunnelCounts(page string) (inbound, outbound *int64) {
	section := ""
	for _, line := range strings.Split(page, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "Inbound tunnels:"):
			section = "inbound"
			continue
		case strings.HasPrefix(line, "Outbound tunnels:"):
			section = "outbound"
			continue
		case strings.HasPrefix(line, "Queue size:") || strings.HasPrefix(line, "TBM Queue size:"):
			continue
		}
		// A tunnel line looks like: "<hash>:me  established , 0.00 KiB"
		// or: "<hash>:me &#8658;  established , 2.83 KiB"
		if strings.Contains(line, "established") || strings.Contains(line, "building") || strings.Contains(line, "expiring") || strings.Contains(line, "failed") {
			switch section {
			case "inbound":
				n := (inbound != nil) && *inbound != 0
				if !n {
					inbound = new(int64)
				}
				*inbound++
			case "outbound":
				n := (outbound != nil) && *outbound != 0
				if !n {
					outbound = new(int64)
				}
				*outbound++
			}
		}
	}
	return inbound, outbound
}

// webconsolePeers reads i2pd's netDb via I2PControl-knownpeers/activepeers
// are not served, so we count peer RouterInfos from the netDb directory as
// a proxy for known peers. Fast/high-capacity peers are not exposed by
// i2pd 2.56 at all.
func (c *Collector) webconsolePeers(ctx context.Context, st *web.RouterStatus) (knownPeers *int64) {
	if st.ContainerID == "" || c.cli == nil || st.Impl != ImplI2PD {
		return nil
	}
	// known_peers comes from I2PControl (netdb.knownpeers) when served;
	// for i2pd it is served, so we don't need webconsole for it.
	// fast_peers and high_capacity_peers are not served by i2pd at all.
	return nil
}
