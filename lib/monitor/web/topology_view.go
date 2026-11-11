package web

// Topology graph rendering. The picture is drawn from the graph the collector
// built out of live router data — transport sessions and netDb membership —
// so the layout is derived from connectivity, never from a hardcoded set of
// nodes or edges. Solid lines are observed sessions, dashed lines are
// inferred knowledge, and the page states the graph's update time.

import (
	"fmt"
	"html/template"
	"math"
	"strings"

	"go-i2p-testnet/lib/monitor/topology"
)

// Ring geometry for the circular layout. The canvas scales with the number
// of nodes so routers stay legible even at large rosters: each node gets at
// least 60 px of arc, and the SVG viewBox grows accordingly.
const (
	ringMinWidth  = 520
	ringMinHeight = 420
	ringMinRadius = 165
	nodeArcPx     = 60 // minimum arc length per node
)

// topologyRing is one node's placed position.
type topologyRing struct {
	Name  string
	X     float64
	Y     float64
	State string
	Impl  string
	RIs   int
}

type topologySVGEdge struct {
	Edge   topology.Edge
	X1, Y1 float64
	X2, Y2 float64
}

// ringGeometry returns the canvas size and radius for n nodes, scaled so
// each node has at least nodeArcPx of arc.
func ringGeometry(n int) (width, height, radius float64) {
	if n <= 0 {
		return ringMinWidth, ringMinHeight, ringMinRadius
	}
	// Circumference needed so each node gets nodeArcPx of arc.
	circ := float64(n) * nodeArcPx
	r := circ / (2 * math.Pi)
	if r < ringMinRadius {
		r = ringMinRadius
	}
	// Square canvas with padding so labels fit.
	pad := 60.0
	size := 2*r + pad
	if size < ringMinWidth {
		size = ringMinWidth
	}
	return size, size, r
}

// layoutRing places the named nodes evenly on a circle, starting at the top.
// Order comes from the caller (sorted by observed connectivity), so the
// picture reflects the graph rather than a fixed arrangement.
func layoutRing(names []string) ([]topologyRing, float64, float64, float64) {
	n := len(names)
	if n == 0 {
		return nil, ringMinWidth, ringMinHeight, ringMinRadius
	}
	width, height, radius := ringGeometry(n)
	cx, cy := width/2, height/2
	out := make([]topologyRing, 0, n)
	for i, name := range names {
		ang := -math.Pi/2 + 2*math.Pi*float64(i)/float64(n)
		out = append(out, topologyRing{
			Name: name,
			X:    cx + radius*math.Cos(ang),
			Y:    cy + radius*math.Sin(ang),
		})
	}
	return out, width, height, radius
}

// decorateRings fills in the per-node display fields from the graph.
func decorateRings(v *topologyView, g *topology.Graph) {
	byName := map[string]topology.Node{}
	for _, n := range g.Nodes {
		byName[n.Name] = n
	}
	for i := range v.Rings {
		n := byName[v.Rings[i].Name]
		v.Rings[i].State = n.State
		v.Rings[i].Impl = n.Impl
		v.Rings[i].RIs = n.KnownRIs
	}
}

// TopologySVG renders the graph. Nodes are placed on a circle ordered by
// observed connectivity, so a router with live sessions lands at the top and
// the picture is stable between refreshes.
func (v topologyView) TopologySVG() template.HTML {
	if len(v.Rings) == 0 {
		return template.HTML(`<div class="chart-empty">no routers in the roster yet — the graph fills in once the testnet is running</div>`)
	}
	pos := map[string][2]float64{}
	for _, r := range v.Rings {
		pos[r.Name] = [2]float64{r.X, r.Y}
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart topo" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" role="img" aria-label="router topology">`,
		v.Width, v.Height, v.Width, v.Height)

	// edges first so nodes sit on top
	for _, e := range v.Edges {
		if _, ok := pos[e.Edge.From]; !ok {
			continue
		}
		if _, ok := pos[e.Edge.To]; !ok {
			continue
		}
		dash := `stroke-dasharray="5 4"`
		color := "#d29922" // inferred
		if e.Edge.Kind == topology.KindObserved {
			dash = ""
			color = "#3fb950" // observed
		}
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="2" %s opacity="0.75"><title>%s: %s → %s (%s)</title></line>`,
			e.X1, e.Y1, e.X2, e.Y2, color, dash,
			e.Edge.Kind, escapeText(e.Edge.From), escapeText(e.Edge.To), escapeText(e.Edge.Reason))
	}

	for _, r := range v.Rings {
		fill := nodeFill(r.State)
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="26" fill="%s" stroke="#30363d" stroke-width="1.5"/>`, r.X, r.Y, fill)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class="node" text-anchor="middle">%s</text>`,
			r.X, r.Y+3, escapeText(shortLabel(r.Name)))
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class="nodesub" text-anchor="middle">%s</text>`,
			r.X, r.Y+40, escapeText(r.State))
	}

	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// LegendSVG is a static key for the two edge kinds — it describes the drawing
// convention, not data, so it is the only hardcoded SVG markup.
func (v topologyView) LegendSVG() template.HTML {
	return template.HTML(`<svg class="legend-svg" width="300" height="24" viewBox="0 0 300 24">` +
		`<line x1="4" y1="12" x2="40" y2="12" stroke="#3fb950" stroke-width="2"/>` +
		`<text x="46" y="16" class="legend">observed session</text>` +
		`<line x1="160" y1="12" x2="196" y2="12" stroke="#d29922" stroke-width="2" stroke-dasharray="5 4"/>` +
		`<text x="202" y="16" class="legend">inferred</text></svg>`)
}

func nodeFill(state string) string {
	switch state {
	case StateUp:
		return "#1b3a26"
	case StateStale:
		return "#3a3116"
	case StateDown:
		return "#3a1b1e"
	default:
		return "#1b2129"
	}
}

func shortLabel(name string) string {
	// "router-i2pd-2" reads better on the graph as "i2pd-2"; the full name
	// stays in the table and the tooltip.
	if s := strings.TrimPrefix(name, "router-"); s != "" {
		return s
	}
	return name
}

// EdgeSummary counts edges by kind and match strategy for the header, so an
// operator can see at a glance how much of the graph is directly observed.
type edgeSummary struct {
	Observed, Inferred, ByHash, ByIP, External int
}

func (s edgeSummary) Text() string {
	parts := []string{
		fmt.Sprintf("%d observed", s.Observed),
		fmt.Sprintf("%d inferred", s.Inferred),
	}
	if s.ByHash > 0 {
		parts = append(parts, fmt.Sprintf("%d matched by router hash", s.ByHash))
	}
	if s.ByIP > 0 {
		parts = append(parts, fmt.Sprintf("%d matched by address", s.ByIP))
	}
	if s.External > 0 {
		parts = append(parts, fmt.Sprintf("%d outside the roster", s.External))
	}
	return strings.Join(parts, " · ")
}

func summarize(g *topology.Graph) edgeSummary {
	var s edgeSummary
	for _, e := range g.Edges {
		if e.Kind == topology.KindObserved {
			s.Observed++
		} else {
			s.Inferred++
		}
		switch e.Match {
		case topology.MatchHash:
			s.ByHash++
		case topology.MatchIP:
			s.ByIP++
		case topology.MatchExternal:
			s.External++
		}
	}
	return s
}
