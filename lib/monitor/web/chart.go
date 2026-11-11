package web

// Server-rendered line charts. The dashboard has no JavaScript, no CDN and no
// charting library (see the "stdlib only" requirement), so the graphs are
// drawn as SVG here, from the poller's real samples. A chart with no data
// says so rather than rendering an empty box, and a value no router reported
// is drawn as a gap, never as a fabricated zero.

import (
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
)

// Chart geometry. Kept modest so the SVG stays readable in a table cell and
// scales with the CSS width.
const (
	chartWidth   = 640
	chartHeight  = 180
	chartPadLeft = 62
	chartPadTop  = 18
	chartPadBot  = 30
)

// Series is one plotted line. Values align index-for-index with the chart's
// Labels; a nil entry is a gap (the router did not report that value).
type Series struct {
	Name   string
	Color  string
	Values []*float64
}

// Chart is one renderable graph.
type Chart struct {
	Title       string
	Labels      []string // oldest first; rendered sparsely on the x axis
	Series      []Series
	FormatValue func(float64) string
}

// seriesColors is the palette used for plotted lines, chosen for contrast on
// the dark background.
var seriesColors = []string{"#58a6ff", "#3fb950", "#d29922", "#f85149", "#bc8cff"}

// colorFor returns the palette colour for a series position.
func colorFor(i int) string { return seriesColors[i%len(seriesColors)] }

// ptrf returns a pointer to v, for building value slices.
func ptrf(v float64) *float64 { return &v }

// valueCount returns how many plotted points exist in total and how many
// series carry at least one. A nil entry means the router did not report that
// value at that time, so it is not a point.
func (c Chart) valueCount() (points int, seriesWithData int) {
	for _, s := range c.Series {
		var n int
		for _, v := range s.Values {
			if v != nil {
				n++
			}
		}
		points += n
		if n > 0 {
			seriesWithData++
		}
	}
	return points, seriesWithData
}

// pointCount is the number of label slots.
func (c Chart) pointCount() int { return len(c.Labels) }

// SVG renders the chart. When there is nothing to plot it returns a short
// explanation instead of an empty frame, so an operator is never left
// guessing whether a blank chart means "zero" or "no data yet".
func (c Chart) SVG() template.HTML {
	if len(c.Series) == 0 || c.pointCount() == 0 {
		return template.HTML(c.placeholder("no samples yet — the chart fills as the poller collects them"))
	}
	points, _ := c.valueCount()
	if points == 0 {
		// Nothing to plot. Say which kind of nothing it is: for i2pd tunnel
		// counts the answer is "this implementation never reports it", and an
		// empty box would look like a collection bug.
		reason := "no router reported this value"
		if c.pointCount() < 2 {
			reason = "only one sample collected so far"
		}
		return template.HTML(c.placeholder(c.Title + ": " + reason))
	}
	if c.pointCount() < 2 {
		return template.HTML(c.placeholder(c.Title + ": only one sample collected so far — a trend needs two"))
	}
	// One point with a real window is still drawn (as a dot) rather than
	// hidden behind a placeholder: it proves the value exists, which is
	// information the operator would otherwise lose.
	var note string
	if points == 1 {
		note = `<text x="` + fmt.Sprintf("%d", chartPadLeft) + `" y="` + fmt.Sprintf("%d", chartHeight-2) + `" class="xlab">1 point so far — not yet a trend</text>`
	}

	plotW := float64(chartWidth - chartPadLeft - 12)
	plotH := float64(chartHeight - chartPadTop - chartPadBot)

	maxV := 0.0
	haveNeg := false
	minV := 0.0
	for _, s := range c.Series {
		for _, v := range s.Values {
			if v == nil {
				continue
			}
			if *v > maxV {
				maxV = *v
			}
			if *v < 0 {
				haveNeg = true
				if *v < minV {
					minV = *v
				}
			}
		}
	}
	if maxV == 0 && minV == 0 {
		maxV = 1 // avoid a divide-by-zero on an all-zero series
	}
	// Choose a gridline step first, then the top of the scale, so axis labels
	// land on whole readable numbers. Deriving the top from the data max
	// directly produced labels like "3.8" on a peer-count chart, which reads
	// as a measurement that was never taken.
	integral := true
	for _, s := range c.Series {
		for _, v := range s.Values {
			if v != nil && math.Trunc(*v) != *v {
				integral = false
			}
		}
	}
	top, step := niceScale(maxV, integral)
	bottom := 0.0
	if haveNeg {
		bottom = -niceCeil(-minV)
	}
	span := top - bottom

	x := func(i int) float64 {
		if c.pointCount() == 1 {
			return float64(chartPadLeft) + plotW/2
		}
		return float64(chartPadLeft) + plotW*float64(i)/float64(c.pointCount()-1)
	}
	y := func(v float64) float64 {
		frac := (v - bottom) / span
		return float64(chartPadTop) + plotH*(1-frac)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="%s">`,
		chartWidth, chartHeight, chartWidth, chartHeight, escapeAttr(c.Title))

	// frame + horizontal gridlines with value labels
	for val := 0.0; val <= top+step/2; val += step {
		gy := y(val)
		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" class="grid"/>`,
			chartPadLeft, gy, chartWidth-12, gy)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" class="ylab">%s</text>`,
			chartPadLeft-6, gy+3.5, escapeText(c.fmtValue(val)))
	}
	if bottom < 0 {
		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" class="zero"/>`,
			chartPadLeft, y(0), chartWidth-12, y(0))
	}

	// x labels: first, middle, last are always enough to read the window
	for _, i := range labelIndices(c.pointCount()) {
		if i >= len(c.Labels) {
			continue
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" class="xlab">%s</text>`,
			x(i), chartHeight-10, escapeText(c.Labels[i]))
	}

	// series polylines, broken at gaps. A single isolated point is drawn as a
	// dot so it is still visible.
	for _, s := range c.Series {
		var pts []string
		var loneX, loneY float64
		flush := func() {
			switch len(pts) {
			case 0:
			case 1:
				fmt.Fprintf(&b, `<circle r="2.5" fill="%s" cx="%.1f" cy="%.1f"/>`, s.Color, loneX, loneY)
			default:
				fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" points="%s"/>`,
					s.Color, strings.Join(pts, " "))
			}
			pts = pts[:0]
		}
		for i, v := range s.Values {
			if i >= c.pointCount() {
				break
			}
			if v == nil {
				flush()
				continue
			}
			loneX, loneY = x(i), y(*v)
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", loneX, loneY))
		}
		flush()
	}

	// legend
	lx := chartPadLeft
	for _, s := range c.Series {
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="9" height="9" fill="%s"/>`, lx, 4, s.Color)
		fmt.Fprintf(&b, `<text x="%d" y="%d" class="legend">%s</text>`, lx+13, 12, escapeText(s.Name))
		lx += 13 + 7*len(s.Name) + 18
	}

	b.WriteString(note)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func (c Chart) fmtValue(v float64) string {
	if c.FormatValue != nil {
		return c.FormatValue(v)
	}
	// A whole value is printed without a decimal point: "3", not "3.0".
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	if math.Abs(v) >= 100 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

func (c Chart) placeholder(msg string) string {
	return fmt.Sprintf(`<div class="chart-empty">%s</div>`, escapeText(msg))
}

// niceCeil rounds up to 1/2/5 * 10^n so axis labels are readable numbers.
func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	exp := math.Floor(math.Log10(v))
	base := math.Pow(10, exp)
	for _, m := range []float64{1, 2, 5, 10} {
		if cand := m * base; cand >= v {
			return cand
		}
	}
	return 10 * base
}

// niceScale returns an axis maximum and a gridline step for the given data
// maximum. When the plotted values are whole numbers (peer and tunnel counts)
// the step is forced to be whole too, so a chart of 3 peers is labelled
// 0/1/2/3 rather than 0/0.8/1.6 — fractional labels on an integer metric read
// as measurements that were never taken.
func niceScale(maxV float64, integral bool) (top, step float64) {
	if maxV <= 0 {
		return 1, 1
	}
	raw := maxV / 4
	exp := math.Floor(math.Log10(raw))
	base := math.Pow(10, exp)
	for _, m := range []float64{1, 2, 5, 10} {
		if m*base >= raw {
			step = m * base
			break
		}
	}
	if integral {
		if step < 1 {
			step = 1
		} else {
			step = math.Ceil(step)
		}
	}
	top = math.Ceil(maxV/step) * step
	if top <= 0 {
		top = step
	}
	return top, step
}

// labelIndices picks up to three x-axis label positions.
func labelIndices(n int) []int {
	switch {
	case n <= 0:
		return nil
	case n == 1:
		return []int{0}
	case n == 2:
		return []int{0, 1}
	default:
		return []int{0, n / 2, n - 1}
	}
}

func escapeText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func escapeAttr(s string) string { return escapeText(s) }
