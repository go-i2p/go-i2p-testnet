package web

// Chart construction from real samples, and the honest labelling of values an
// implementation does not expose. Both exist because of the same audit
// finding: a bare em-dash cannot tell "zero", "not collected yet" and "this
// router never reports it" apart, and a table of numbers cannot show a trend.

import (
	"sort"
	"strconv"
	"time"
)

// capability describes whether one implementation serves one I2PControl
// property at all. Verified against live routers, not inferred from docs:
// i2pd 2.56 answers these four properties with an empty result object, so no
// amount of polling will ever produce a value, while go-i2p serves them.
//
// Marking them explicitly lets the UI say "not served by i2pd" instead of
// showing a dash that reads like a missing measurement.
var notServed = map[string]map[string]bool{
	"i2pd": {
		"tunnels_inbound":     true,
		"tunnels_outbound":    true,
		"fast_peers":          true,
		"high_capacity_peers": true,
	},
}

// notServedFields lists the properties an implementation never exposes, sorted
// so the JSON is stable. It lets an API consumer distinguish a permanent
// absence from a missing measurement.
func notServedFields(impl string) []string {
	set := notServed[impl]
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// valueCell renders a numeric field with the honesty the audit asked for: a
// real number, or the reason there is none.
func valueCellC(impl, field string, v *int64, compact bool) string {
	if v != nil {
		return numI64(v)
	}
	return notServedText(impl, field, compact)
}

func valueCellF(impl, field string, v *float64, compact bool) string {
	if v != nil {
		return formatBytesPerSec(v)
	}
	return notServedText(impl, field, compact)
}

// notServedText is the single wording used wherever a value is absent, so the
// summary table and the detail page never describe the same gap differently.
func notServedText(impl, field string, compact bool) string {
	if notServed[impl][field] {
		if compact {
			return "n/a"
		}
		return "not served by " + impl
	}
	return "—"
}

// routerCharts builds the three charts the requirement names — peers,
// tunnels, bandwidth — from one router's own history. History is newest
// first; charts are oldest first so time runs left to right.
func routerCharts(st *RouterStatus) []Chart {
	n := len(st.History)
	if n == 0 {
		return nil
	}
	labels := make([]string, n)
	peers := make([]*float64, n)
	active := make([]*float64, n)
	tunIn := make([]*float64, n)
	tunOut := make([]*float64, n)
	tunPart := make([]*float64, n)
	bwIn := make([]*float64, n)
	bwOut := make([]*float64, n)

	for i := range st.History {
		oldest := st.History[n-1-i] // index into newest-first history
		labels[i] = oldest.CollectedAt.Local().Format("15:04:05")
		peers[i] = f64p(oldest.KnownPeers)
		active[i] = f64p(oldest.ActivePeers)
		tunIn[i] = f64p(oldest.TunnelsInbound)
		tunOut[i] = f64p(oldest.TunnelsOutbound)
		tunPart[i] = f64p(oldest.Participating)
		bwIn[i] = oldest.BWInbound1s
		bwOut[i] = oldest.BWOutbound1s
	}

	return []Chart{
		{
			Title:  "Peers",
			Labels: labels,
			Series: []Series{
				{Name: "known", Color: colorFor(0), Values: peers},
				{Name: "active", Color: colorFor(1), Values: active},
			},
			FormatValue: formatCount,
		},
		{
			Title:  "Tunnels",
			Labels: labels,
			Series: []Series{
				{Name: "inbound", Color: colorFor(0), Values: tunIn},
				{Name: "outbound", Color: colorFor(1), Values: tunOut},
				{Name: "participating", Color: colorFor(2), Values: tunPart},
			},
			FormatValue: formatCount,
		},
		{
			Title:  "Bandwidth",
			Labels: labels,
			Series: []Series{
				{Name: "in 1s", Color: colorFor(0), Values: bwIn},
				{Name: "out 1s", Color: colorFor(1), Values: bwOut},
			},
			FormatValue: formatBytesPerSecValue,
		},
	}
}

// networkCharts builds the same three charts summed over every router, so an
// operator can see the whole network's trend, not just one router's.
func networkCharts(history []*NetworkTotals) []Chart {
	n := len(history)
	if n == 0 {
		return nil
	}
	labels := make([]string, n)
	peers := make([]*float64, n)
	active := make([]*float64, n)
	tunIn := make([]*float64, n)
	tunOut := make([]*float64, n)
	tunPart := make([]*float64, n)
	bwIn := make([]*float64, n)
	bwOut := make([]*float64, n)

	for i := range history {
		oldest := history[n-1-i]
		labels[i] = oldest.CollectedAt.Local().Format("15:04:05")
		peers[i] = i64p(oldest.KnownPeers)
		active[i] = i64p(oldest.ActivePeers)
		tunIn[i] = i64p(oldest.TunnelsInbound)
		tunOut[i] = i64p(oldest.TunnelsOutbound)
		tunPart[i] = i64p(oldest.TunnelsParticip)
		bwIn[i] = oldest.BWInbound1s
		bwOut[i] = oldest.BWOutbound1s
	}

	return []Chart{
		{
			Title:  "Peers (all routers)",
			Labels: labels,
			Series: []Series{
				{Name: "known", Color: colorFor(0), Values: peers},
				{Name: "active", Color: colorFor(1), Values: active},
			},
			FormatValue: formatCount,
		},
		{
			Title:  "Tunnels (all routers)",
			Labels: labels,
			Series: []Series{
				{Name: "inbound", Color: colorFor(0), Values: tunIn},
				{Name: "outbound", Color: colorFor(1), Values: tunOut},
				{Name: "participating", Color: colorFor(2), Values: tunPart},
			},
			FormatValue: formatCount,
		},
		{
			Title:  "Bandwidth (all routers)",
			Labels: labels,
			Series: []Series{
				{Name: "in 1s", Color: colorFor(0), Values: bwIn},
				{Name: "out 1s", Color: colorFor(1), Values: bwOut},
			},
			FormatValue: formatBytesPerSecValue,
		},
	}
}

// window describes how much history the charts cover, so the x axis is not
// the only clue. At the default interval this is 60 samples.
func window(interval time.Duration, samples int) string {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	d := time.Duration(samples) * interval
	if d < time.Minute {
		return strconv.Itoa(int(d.Seconds())) + "s window"
	}
	return strconv.Itoa(int(d.Minutes())) + " minute window"
}

func formatCount(v float64) string {
	return strconv.FormatFloat(v, 'f', 0, 64)
}

func formatBytesPerSecValue(v float64) string { return formatBytesPerSec(&v) }

func f64p(v *int64) *float64 {
	if v == nil {
		return nil
	}
	f := float64(*v)
	return &f
}

func i64p(v *int64) *float64 { return f64p(v) }
