package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func row(name, state, uptime, peers string) *RouterRow {
	return &RouterRow{Name: name, State: state, Uptime: uptime, KnownPeers: peers}
}

func names(rows []*RouterRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

// TestParseDurationDisplay: "2m 10s" is 130 seconds and must sort above
// "45s". The generic number scan would read them as 2 and 45 and invert the
// order, which is the bug this guards.
func TestParseDurationDisplay(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"45s", 45},
		{"2m 10s", 130},
		{"1h 02m 03s", 3723},
		{"10m 00s", 600},
		{"—", -1},
		{"never", -1},
		{"not served by i2pd", -1},
		{"", -1},
	}
	for _, c := range cases {
		if got := parseDurationDisplay(c.in); got != c.want {
			t.Errorf("parseDurationDisplay(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if !(parseDurationDisplay("2m 10s") > parseDurationDisplay("45s")) {
		t.Error("130s must sort above 45s")
	}
}

// TestSortByStatePutsTroubleFirst is what an operator scanning for a problem
// needs: broken routers on top, not alphabetically buried.
func TestSortByStatePutsTroubleFirst(t *testing.T) {
	rows := []*RouterRow{
		row("router-a", StateUp, "1h", "5"),
		row("router-b", StateUp, "1h", "5"),
		row("router-c", StateDown, "0s", "—"),
		row("router-d", StateStale, "5s", "2"),
	}
	sortRows(rows, "state", "asc")
	got := names(rows)
	if got[0] != "router-c" || got[1] != "router-d" {
		t.Errorf("order = %v, want down then stale first", got)
	}
	// desc reverses it.
	sortRows(rows, "state", "desc")
	if names(rows)[0] != "router-a" {
		t.Errorf("desc order = %v, want healthy first", names(rows))
	}
}

func TestSortByUptimeAndPeers(t *testing.T) {
	rows := []*RouterRow{
		row("short", StateUp, "45s", "2"),
		row("long", StateUp, "2m 10s", "10"),
		row("hour", StateUp, "1h 00m 00s", "100"),
	}
	sortRows(rows, "uptime", "asc")
	if got := names(rows); got[0] != "short" || got[2] != "hour" {
		t.Errorf("uptime order = %v", got)
	}
	sortRows(rows, "peers", "desc")
	if got := names(rows); got[0] != "hour" {
		t.Errorf("peers desc order = %v, want the largest first", got)
	}
}

// TestSortUnknownColumnFallsBackToName: a mistyped query parameter must not
// blank the page or panic.
func TestSortUnknownColumnFallsBackToName(t *testing.T) {
	key, dir := sortArgs(httptest.NewRequest(http.MethodGet, "/?sort=nonsense&dir=sideeways", nil))
	if key != "name" || dir != "asc" {
		t.Errorf("got %q/%q, want name/asc", key, dir)
	}
	s := newTestServer(t, staticPoller(
		&RouterStatus{Name: "b", State: StateUp, LastSeen: time.Now()},
		&RouterStatus{Name: "a", State: StateUp, LastSeen: time.Now()},
	))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?sort=state&dir=desc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("sorted request = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "▲") && !strings.Contains(body, "▼") {
		t.Error("the active sort column must be marked")
	}
	// html/template escapes & inside attribute values, so check for the
	// escaped form.
	if !strings.Contains(body, `/?sort=name&amp;dir=asc`) {
		t.Errorf("headers must link to sortable views:\n%s", body[strings.Index(body, "<thead>"):strings.Index(body, "</thead>")+8])
	}
}

// TestFirstNumberScalesUnits: 1.5 KiB must sort above 900 B, or bandwidth
// sorting would be meaningless.
func TestFirstNumberScalesUnits(t *testing.T) {
	if !(firstNumber("1.5 KiB/s") > firstNumber("900 B/s")) {
		t.Errorf("1.5 KiB/s = %v should exceed 900 B/s = %v",
			firstNumber("1.5 KiB/s"), firstNumber("900 B/s"))
	}
	if firstNumber("—") != 0 {
		t.Error("missing values must not sort as real numbers")
	}
}
