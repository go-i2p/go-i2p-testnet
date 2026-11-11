package web

import (
	"encoding/xml"
	"fmt"
	"html/template"
	"strings"
	"testing"
	"time"

	"go-i2p-testnet/lib/monitor/i2pcontrol"
)

func sampleAt(t time.Time, peers, active, part *int64, bw *float64) *i2pcontrol.Sample {
	return &i2pcontrol.Sample{
		CollectedAt: t, KnownPeers: peers, ActivePeers: active,
		Participating: part, BWInbound1s: bw, BWOutbound1s: bw,
	}
}

// TestChartRendersRealPoints is the core of the graphs requirement: the SVG
// must be drawn from the samples, not emitted empty or hardcoded.
func TestChartRendersRealPoints(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	hist := []*i2pcontrol.Sample{
		sampleAt(base.Add(20*time.Second), i64(3), i64(1), i64(2), f64(2048)),
		sampleAt(base.Add(15*time.Second), i64(2), i64(1), i64(2), f64(1024)),
		sampleAt(base.Add(10*time.Second), i64(2), i64(0), i64(1), f64(512)),
		sampleAt(base.Add(5*time.Second), i64(1), i64(0), i64(1), f64(256)),
	}
	st := &RouterStatus{Name: "r", Impl: "goi2p", State: StateUp, Sample: hist[0], History: hist, LastSeen: base}
	charts := routerCharts(st)
	if len(charts) != 3 {
		t.Fatalf("charts = %d, want peers/tunnels/bandwidth", len(charts))
	}
	for _, c := range charts {
		svg := string(c.SVG())
		if !strings.Contains(svg, "<polyline") {
			t.Errorf("%s: no polyline rendered — chart is empty\n%s", c.Title, svg)
		}
		if strings.Contains(svg, "chart-empty") {
			t.Errorf("%s: rendered the empty placeholder despite having data", c.Title)
		}
		// Points must be ordered oldest-first so time runs left to right.
		first := svg[strings.Index(svg, "points=\"")+8:]
		if first == "" {
			t.Errorf("%s: no points attribute", c.Title)
		}
	}
	peers := string(charts[0].SVG())
	if strings.Count(peers, "<polyline") < 2 {
		t.Errorf("peers chart should draw known and active, got %d polylines", strings.Count(peers, "<polyline"))
	}
}

// TestChartValuesComeFromSamples: the plotted y coordinates must move with the
// data. Two different histories must not render the same geometry.
func TestChartValuesComeFromSamples(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	mk := func(vals ...int64) *RouterStatus {
		// History is newest first, so reverse the chronological input.
		out := make([]*i2pcontrol.Sample, 0, len(vals))
		for i := len(vals) - 1; i >= 0; i-- {
			n := vals[i]
			out = append(out, sampleAt(base.Add(time.Duration(i)*5*time.Second), &n, nil, nil, nil))
		}
		return &RouterStatus{Name: "r", Impl: "goi2p", State: StateUp, Sample: out[0], History: out, LastSeen: base}
	}
	flat := string(routerCharts(mk(5, 5, 5, 5))[0].SVG())
	rising := string(routerCharts(mk(1, 2, 3, 9))[0].SVG())
	if flat == "" || rising == "" {
		t.Fatal("charts did not render")
	}
	if flat == rising {
		t.Error("identical SVG for different data — the chart is not drawn from samples")
	}
	// A rising series must place its last point above its first (smaller y).
	yFirst := yOf(t, rising, 0)
	yLast := yOf(t, rising, 3)
	if !(yLast < yFirst) {
		t.Errorf("rising series: first y=%.1f last y=%.1f, expected last above first", yFirst, yLast)
	}
}

func yOf(t *testing.T, svg string, idx int) float64 {
	t.Helper()
	start := strings.Index(svg, "points=\"")
	if start < 0 {
		t.Fatal("no polyline points found")
	}
	seg := svg[start+8:]
	seg = seg[:strings.Index(seg, "\"")]
	pts := strings.Fields(seg)
	if idx >= len(pts) {
		t.Fatalf("point %d missing from %d points", idx, len(pts))
	}
	parts := strings.Split(pts[idx], ",")
	if len(parts) != 2 {
		t.Fatalf("bad point %q", pts[idx])
	}
	var v float64
	if _, err := fmt.Sscanf(parts[1], "%f", &v); err != nil {
		t.Fatalf("bad y in %q: %v", pts[idx], err)
	}
	return v
}

// TestEmptyValueIsGapNotZero: i2pd does not report tunnel in/out. Those must
// be gaps in the line, never plotted as a zero that reads like a real
// measurement.
func TestEmptyValueIsGapNotZero(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	hist := []*i2pcontrol.Sample{
		sampleAt(base, i64(4), nil, nil, f64(10)),
		sampleAt(base.Add(-5*time.Second), i64(3), nil, nil, f64(9)),
		sampleAt(base.Add(-10*time.Second), i64(2), nil, nil, f64(8)),
	}
	st := &RouterStatus{Name: "r", Impl: "i2pd", State: StateUp, Sample: hist[0], History: hist, LastSeen: base}
	charts := routerCharts(st)
	tun := string(charts[1].SVG())
	if strings.Contains(tun, "<polyline") {
		t.Error("tunnel chart drew a line from values no router reported")
	}
	if !strings.Contains(tun, "chart-empty") {
		t.Errorf("tunnel chart must explain why it is empty, got:\n%s", tun)
	}
	peers := string(charts[0].SVG())
	if !strings.Contains(peers, "<polyline") {
		t.Error("peers chart should still render from the values that do exist")
	}
}

// TestSingleSampleExplainsItself: one point is not a trend; say so.
func TestSingleSampleExplainsItself(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	hist := []*i2pcontrol.Sample{sampleAt(base, i64(1), nil, nil, nil)}
	st := &RouterStatus{Name: "r", Impl: "i2pd", State: StateUp, Sample: hist[0], History: hist, LastSeen: base}
	svg := string(routerCharts(st)[0].SVG())
	if strings.Contains(svg, "<polyline") {
		t.Error("a single sample must not be drawn as a line")
	}
	if !strings.Contains(svg, "one sample") {
		t.Errorf("expected an explanation, got %s", svg)
	}
}

// TestChartScalesToData checks the axis labels track the range, so a chart of
// 3 peers is not drawn on a 0..1000 scale.
func TestChartScalesToData(t *testing.T) {
	c := Chart{Title: "x", Labels: []string{"a", "b", "c"}, Series: []Series{{
		Name: "s", Color: "#fff", Values: []*float64{ptrf(1), ptrf(2), ptrf(3)},
	}}}
	svg := string(c.SVG())
	if strings.Contains(svg, ">1000<") || strings.Contains(svg, ">100<") {
		t.Errorf("axis scale not fitted to data:\n%s", svg)
	}
	if !strings.Contains(svg, ">3<") && !strings.Contains(svg, ">4<") && !strings.Contains(svg, ">5<") {
		t.Errorf("expected a top label near the data max, got:\n%s", svg)
	}
}

// TestChartEscapesText: titles and labels come from router-controlled data
// (names, versions), so they must not be able to inject markup into the SVG.
func TestChartEscapesText(t *testing.T) {
	c := Chart{
		Title:  `<script>alert(1)</script>`,
		Labels: []string{`<img src=x>`, "b", `"><b>`},
		Series: []Series{{Name: `<svg onload=x>`, Color: "#fff", Values: []*float64{ptrf(1), ptrf(2), ptrf(3)}}},
	}
	svg := string(c.SVG())
	for _, bad := range []string{"<script>", "<img src=x>", "<svg onload=x>"} {
		if strings.Contains(svg, bad) {
			t.Errorf("raw %q survived into the SVG", bad)
		}
	}
	if !strings.Contains(svg, "&lt;") {
		t.Error("expected escaped forms in the SVG")
	}
}

// TestNetworkChartsFromAggregates backs the index-page graphs.
func TestNetworkChartsFromAggregates(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	mk := func(peers int64) *NetworkTotals {
		p := peers
		return &NetworkTotals{CollectedAt: base, Routers: 2, Up: 2, KnownPeers: &p}
	}
	hist := []*NetworkTotals{mk(4), mk(3), mk(2), mk(1)} // newest first
	charts := networkCharts(hist)
	if len(charts) != 3 {
		t.Fatalf("network charts = %d, want 3", len(charts))
	}
	svg := string(charts[0].SVG())
	if !strings.Contains(svg, "<polyline") {
		t.Errorf("network peer chart is empty:\n%s", svg)
	}
	if networkCharts(nil) != nil {
		t.Error("no history must produce no charts, not an empty chart shell")
	}
}

// TestChartSVGIsWellFormed checks the markup the browser will actually parse.
func TestChartSVGIsWellFormed(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	hist := []*i2pcontrol.Sample{
		sampleAt(base, i64(2), i64(1), i64(1), f64(20)),
		sampleAt(base.Add(-5*time.Second), i64(1), i64(0), i64(0), f64(10)),
	}
	st := &RouterStatus{Name: "r", Impl: "goi2p", State: StateUp, Sample: hist[0], History: hist, LastSeen: base}
	svg := routerCharts(st)[0].SVG()
	s := string(svg)
	if !strings.HasPrefix(s, "<svg") || !strings.HasSuffix(s, "</svg>") {
		t.Errorf("not a complete svg element: %s", s[:min(80, len(s))])
	}
	if !strings.Contains(s, "viewBox") {
		t.Error("missing viewBox — the chart will not scale")
	}
	if strings.Contains(s, "NaN") || strings.Contains(s, "+Inf") {
		t.Errorf("non-finite coordinates leaked into the chart: %s", s)
	}
	if template.HTML(s) != svg {
		t.Error("SVG should be returned as trusted markup")
	}
}

// TestIsolatedPointBetweenGapsDrawsADot: a value reported only at one sample
// in the middle of a gappy series must still be visible. Without this the
// point would silently disappear, which is exactly how a rarely-reported
// metric would look like "never measured".
func TestIsolatedPointBetweenGapsDrawsADot(t *testing.T) {
	mid := 7.0
	c := Chart{Title: "Sparse", Labels: []string{"a", "b", "c"}, Series: []Series{
		{Name: "rare", Color: "#58a6ff", Values: []*float64{nil, &mid, nil}},
	}}
	svg := string(c.SVG())
	if !strings.Contains(svg, "<circle") {
		t.Errorf("isolated point must render as a dot:\n%s", svg)
	}
	if strings.Contains(svg, "<polyline") {
		t.Error("a single point must not be drawn as a line")
	}
	var v struct{}
	if err := xml.Unmarshal([]byte(svg), &v); err != nil {
		t.Errorf("not well-formed: %v", err)
	}
}

// TestChartSVGParsesAsXML: a browser will not render malformed SVG at all, so
// the output must be well-formed XML — not merely start with <svg and end with
// </svg>. Gaps (nil values) and the single-point path are exercised here
// because those are the branches most likely to emit an unbalanced tag.
func TestChartSVGParsesAsXML(t *testing.T) {
	cases := map[string]Chart{
		"full": {Title: "Peers", Labels: []string{"a", "b", "c"}, Series: []Series{
			{Name: "known", Color: "#58a6ff", Values: []*float64{ptrf(1), ptrf(2), ptrf(3)}}}},
		"with gaps": {Title: "Tunnels", Labels: []string{"a", "b", "c"}, Series: []Series{
			{Name: "in", Color: "#58a6ff", Values: []*float64{ptrf(1), nil, ptrf(3)}}}},
		"all zero": {Title: "Zero", Labels: []string{"a", "b"}, Series: []Series{
			{Name: "z", Color: "#58a6ff", Values: []*float64{ptrf(0), ptrf(0)}}}},
		"negative": {Title: "Neg", Labels: []string{"a", "b"}, Series: []Series{
			{Name: "n", Color: "#58a6ff", Values: []*float64{ptrf(-4), ptrf(2)}}}},
		"escaped title": {Title: `<b onmouseover=x>`, Labels: []string{`<i>`, "b"}, Series: []Series{
			{Name: `<q>`, Color: "#58a6ff", Values: []*float64{ptrf(1), ptrf(2)}}}},
	}
	for name, c := range cases {
		svg := string(c.SVG())
		if strings.Contains(svg, "chart-empty") {
			t.Errorf("%s: expected a chart, got the placeholder", name)
			continue
		}
		var v struct{}
		if err := xml.Unmarshal([]byte(svg), &v); err != nil {
			t.Errorf("%s: SVG is not well-formed XML: %v\n%s", name, err, svg)
		}
	}
	// A single point is a dot, not a line, and must still be well-formed.
	one := Chart{Title: "One", Labels: []string{"a"}, Series: []Series{
		{Name: "x", Color: "#58a6ff", Values: []*float64{ptrf(5)}}}}
	if got := string(one.SVG()); !strings.Contains(got, "chart-empty") {
		t.Errorf("a single sample must explain itself rather than draw a line: %s", got)
	}
}

// TestAllZeroSeriesStillRenders: a real measurement of zero must draw, not be
// mistaken for "no data".
func TestAllZeroSeriesStillRenders(t *testing.T) {
	c := Chart{Title: "T", Labels: []string{"a", "b"}, Series: []Series{{
		Name: "z", Color: "#fff", Values: []*float64{ptrf(0), ptrf(0)},
	}}}
	svg := string(c.SVG())
	if !strings.Contains(svg, "<polyline") {
		t.Errorf("all-zero data must still render a line: %s", svg)
	}
}
