package dashboard

import (
	"os"
	"strings"
	"testing"

	"go-i2p-testnet/lib/monitor/topology"
	"go-i2p-testnet/lib/monitor/web"

	"github.com/go-i2p/common/base64"
)

// The transports page, after the container-side `sed 's/<[^>]*>/ /g'`, looks
// like this (captured from a live i2pd 2.56 router and re-indented). The
// arrow arrives as the HTML entity because only tags were stripped.
const liveTransportsPage = `
 Transports: 

 NTCP2 ( 0 ) 
 
 SSU2 ( 1 ) 
T2gE: 172.28.0.3:4567 &#8658;  [163195:176973] 
 Total sessions: 1 
`

func TestParseSessionsFromLivePage(t *testing.T) {
	got := ParseSessions("router-i2pd-1", liveTransportsPage)
	if len(got) != 1 {
		t.Fatalf("sessions = %d, want 1: %+v", len(got), got)
	}
	s := got[0]
	if s.From != "router-i2pd-1" {
		t.Errorf("From = %q", s.From)
	}
	if s.HashPrefix != "T2gE" {
		t.Errorf("HashPrefix = %q, want T2gE — this is what matches the peer by hash", s.HashPrefix)
	}
	if s.PeerIP != "172.28.0.3" || s.PeerPort != 4567 {
		t.Errorf("peer = %s:%d, want 172.28.0.3:4567", s.PeerIP, s.PeerPort)
	}
	if s.Transport != "SSU2" {
		t.Errorf("Transport = %q, want SSU2 from the section heading", s.Transport)
	}
	if s.BytesSent != 163195 || s.BytesReceived != 176973 {
		t.Errorf("bytes = %d/%d, want 163195/176973", s.BytesSent, s.BytesReceived)
	}
}

func TestParseSessionsMultipleTransports(t *testing.T) {
	page := "NTCP2 ( 1 )\nAuto: 172.28.0.2:8888 x [10:20]\nSSU2 ( 1 )\nT2gE: 172.28.0.3:4567 y [30:40]\n"
	got := ParseSessions("r1", page)
	if len(got) != 2 {
		t.Fatalf("sessions = %d, want 2: %+v", len(got), got)
	}
	if got[0].Transport != "NTCP2" {
		t.Errorf("first transport = %q, want NTCP2", got[0].Transport)
	}
	if got[1].Transport != "SSU2" {
		t.Errorf("second transport = %q, want SSU2", got[1].Transport)
	}
	if got[0].HashPrefix != "Auto" {
		t.Errorf("first prefix = %q", got[0].HashPrefix)
	}
}

// TestParseSessionsIgnoresGarbage: the page is HTML scraped through a shell
// pipeline, so anything that is not a session row must be skipped rather than
// turned into a confident-looking edge.
func TestParseSessionsIgnoresGarbage(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"nav only":          "Main page Router commands Tunnels Transports",
		"css":               ".tunnel.established { color: #56B734; }",
		"no brackets":       "T2gE: 172.28.0.3:4567 =>",
		"non-numeric bytes": "T2gE: 172.28.0.3:4567 => [abc:def]",
		"bad port":          "T2gE: 172.28.0.3:notaport => [1:2]",
		"short prefix":      "ab: 172.28.0.3:4567 => [1:2]",
		"external address":  "AbCd: 203.0.113.9:4567 => [1:2]",
	}
	for name, page := range cases {
		got := ParseSessions("r1", page)
		switch name {
		case "external address":
			// A real session to a non-testnet peer must still parse: the
			// builder reports it as external rather than dropping it.
			if len(got) != 1 {
				t.Errorf("%s: sessions = %d, want 1", name, len(got))
			}
		default:
			if len(got) != 0 {
				t.Errorf("%s: parsed %+v from non-session text", name, got)
			}
		}
	}
}

func TestParseNetDBListing(t *testing.T) {
	out := "/var/lib/i2pd/netDb/rA/routerInfo-AutoCdqqsW~S6nnqEG~BQgcUhMdxbLqwWN3qO0h0zso=.dat\n" +
		"/var/lib/i2pd/netDb/rT/routerInfo-T2gEKfUoS~sxitfRf8C5EuZtBOdqdJqam9NfsDAkxfA=.dat\n" +
		"\n" + "r-\n" + "r0\n" + "somefile.txt\n"
	got := ParseNetDBListing(out)
	if len(got) != 2 {
		t.Fatalf("hashes = %v, want the two routerInfo names only", got)
	}
	if got[0] != "AutoCdqqsW~S6nnqEG~BQgcUhMdxbLqwWN3qO0h0zso=" {
		t.Errorf("first = %q", got[0])
	}
}

// TestParseNetDBListingRejectsDirectoryNoise: the find|sed pipeline can leak
// directory names or shell errors; those must never become phantom peers.
func TestParseNetDBListingRejectsDirectoryNoise(t *testing.T) {
	for _, in := range []string{"", "r0\n", "routerInfo-.dat", "notahash!", "command not found: find"} {
		if got := ParseNetDBListing(in); len(got) != 0 {
			t.Errorf("ParseNetDBListing(%q) = %v, want nothing", in, got)
		}
	}
}

func TestParseNetDBListingDedupes(t *testing.T) {
	h := "AutoCdqqsW~S6nnqEG~BQgcUhMdxbLqwWN3qO0h0zso="
	got := ParseNetDBListing(h + "\n" + h + "\n")
	if len(got) != 1 {
		t.Errorf("dupes kept: %v", got)
	}
}

// TestParseIdentHashRejectsJunk: a router that has not published its
// router.info yet returns an empty file, a directory listing, or partial
// writes. All must yield "" so the node is shown as unknown rather than
// matched on a garbage hash.
func TestParseIdentHashRejectsJunk(t *testing.T) {
	for _, in := range []string{"", "   ", "not a router info at all", "\x00\x01\x02", "Error response from daemon"} {
		if got := ParseIdentHash([]byte(in)); got != "" {
			t.Errorf("ParseIdentHash(%q) = %q, want empty", in, got)
		}
	}
}

// TestParseIdentHashFixture checks the parser against a router.info captured
// from a live i2pd router, whose ident hash is recorded alongside it. This is
// the value edges are matched by, so a parser regression would silently turn
// every observed edge into an inferred one.
func TestParseIdentHashFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/router.info")
	if err != nil {
		t.Skip("no router.info fixture captured")
	}
	want, err := os.ReadFile("testdata/router.info.hash")
	if err != nil {
		t.Skip("no expected hash recorded for the fixture")
	}
	got := ParseIdentHash(raw)
	if got == "" {
		t.Fatal("live router.info did not parse")
	}
	if got != strings.TrimSpace(string(want)) {
		t.Errorf("ident hash = %q, want %q", got, strings.TrimSpace(string(want)))
	}
	// The same bytes must parse whether stored raw or base64-encoded.
	if alt := ParseIdentHash([]byte(base64.EncodeToString(raw))); alt != got {
		t.Errorf("base64 form parsed to %q, want %q", alt, got)
	}
}

// TestParseIdentHashOfGarbageIsNotMistakenForAKnownHash guards the round-trip
// assumption above: junk must never parse.
func TestParseIdentHashOfGarbageIsNotMistakenForAKnownHash(t *testing.T) {
	if got := ParseIdentHash([]byte(base64.EncodeToString([]byte("junkjunkjunk")))); got != "" {
		t.Errorf("junk base64 parsed to %q", got)
	}
}

// TestCollectionWarnings checks that every roster router whose edges the graph
// cannot account for gets exactly one accurate explanation. This is the path
// that used to stay silent when a router was running but unobservable, which
// left the topology page showing running=yes next to a down state with no word
// about why.
func TestCollectionWarnings(t *testing.T) {
	g := &topology.Graph{Nodes: []topology.Node{
		{Name: "router-i2pd-1", InRoster: true, Running: true, State: web.StateUp},
		{Name: "router-i2pd-2", InRoster: true, Running: false, State: web.StateDown},
		{Name: "router-i2pd-3", InRoster: true, Running: true, State: web.StateDown},
		{Name: "router-i2pd-4", InRoster: true, Running: true, State: web.StateUp},
		{Name: "external-peer", InRoster: false, Running: false},
	}}

	got := strings.Join(collectionWarnings(g, []string{"router-i2pd-4"}), "\n")

	if !strings.Contains(got, "no container id for router-i2pd-4") {
		t.Errorf("missing no-container-id warning:\n%s", got)
	}
	if !strings.Contains(got, "router-i2pd-2 is not running") {
		t.Errorf("missing not-running warning:\n%s", got)
	}
	if !strings.Contains(got, "router-i2pd-3 is running but its control endpoint is not answering") {
		t.Errorf("missing running-but-unobservable warning:\n%s", got)
	}
	// A healthy router, a peer outside the roster, and a router already
	// explained by the missing-container-id reason must not be warned about.
	for _, name := range []string{"router-i2pd-1", "external-peer"} {
		if strings.Contains(got, name+" is") {
			t.Errorf("unexpected warning for %s:\n%s", name, got)
		}
	}
	if strings.Contains(got, "router-i2pd-4 is") {
		t.Errorf("router-i2pd-4 should get one reason, not two:\n%s", got)
	}
	// All-healthy roster produces no warnings at all.
	allUp := &topology.Graph{Nodes: []topology.Node{
		{Name: "a", InRoster: true, Running: true, State: web.StateUp},
	}}
	if w := collectionWarnings(allUp, nil); len(w) != 0 {
		t.Errorf("healthy roster produced warnings: %q", w)
	}
}
