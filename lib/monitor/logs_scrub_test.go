package monitor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScrubRemovesSecrets(t *testing.T) {
	m := NewLogManager(nil, t.TempDir())
	m.SetSecrets("sup3rs3cr3t", "another-secret")

	in := "2026-11-10T04:59:58Z debug - I2PControl: Authenticate API=1 sup3rs3cr3t"
	got := m.scrub(in)
	if strings.Contains(got, "sup3rs3cr3t") {
		t.Errorf("secret survived scrub: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("expected redaction marker: %q", got)
	}

	got2 := m.scrub("plain line with another-secret inside")
	if strings.Contains(got2, "another-secret") {
		t.Errorf("second secret survived: %q", got2)
	}

	if m.scrub("nothing sensitive here") != "nothing sensitive here" {
		t.Error("clean line was modified")
	}
}

func TestScrubIgnoresEmptySecret(t *testing.T) {
	m := NewLogManager(nil, t.TempDir())
	m.SetSecrets("")
	if got := m.scrub("line"); got != "line" {
		t.Errorf("empty secret corrupted line: %q", got)
	}
}

// TestDiagnoseFromRouterLog is the fallback the audit said was missing: a
// router whose control endpoint never binds is otherwise indistinguishable
// from a crashed one. The captured log is an independent channel that can
// explain what the router is waiting for.
func TestDiagnoseFromRouterLog(t *testing.T) {
	dir := t.TempDir()
	m := NewLogManager(nil, dir)

	// A real line captured from a go-i2p router stuck below the peer floor.
	line := `2026-11-10T18:45:08.949777520Z time=2026-11-10 18:45:08 level=warning ` +
		`msg=initial reseed incomplete, retrying after backoff at=reseedUntilReady ` +
		`attempt=4 backoff=40s current_size=3 max_attempts=5 phase=bootstrap ` +
		`reason=initial reseed did not reach the peer floor, retrying threshold=40`
	if err := os.WriteFile(filepath.Join(dir, "router-goi2p-4.log"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := m.Diagnose("router-goi2p-4")
	if got == "" {
		t.Fatal("no diagnosis produced from a log that clearly states the blocker")
	}
	for _, want := range []string{"bootstrapping", "sync"} {
		if !strings.Contains(strings.ToLower(got), want) {
			t.Errorf("diagnosis should mention %q, got: %s", want, got)
		}
	}
}

func TestDiagnoseNoNetDB(t *testing.T) {
	dir := t.TempDir()
	m := NewLogManager(nil, dir)
	os.WriteFile(filepath.Join(dir, "r.log"),
		[]byte("error=no valid netDb directory found in search paths\n"), 0o644)
	got := m.Diagnose("r")
	if got == "" || !strings.Contains(strings.ToLower(got), "netdb") {
		t.Errorf("want a netDb diagnosis, got %q", got)
	}
}

// TestDiagnoseSaysNothingWhenItKnowsNothing: guessing would be worse than
// silence, and a healthy or unrelated log must produce no explanation.
func TestDiagnoseSaysNothingWhenItKnowsNothing(t *testing.T) {
	dir := t.TempDir()
	m := NewLogManager(nil, dir)
	os.WriteFile(filepath.Join(dir, "clean.log"),
		[]byte("level=info msg=router started normally\nlevel=info msg=netdb size 12\n"), 0o644)
	if got := m.Diagnose("clean"); got != "" {
		t.Errorf("healthy log produced a diagnosis: %q", got)
	}
	if got := m.Diagnose("does-not-exist"); got != "" {
		t.Errorf("missing log produced a diagnosis: %q", got)
	}
	// A nil manager must not panic: the dashboard may render before any
	// capture exists.
	var nilMgr *LogManager
	if got := nilMgr.Diagnose("x"); got != "" {
		t.Errorf("nil manager returned %q", got)
	}
}

// TestDiagnoseReadsOnlyTheTail keeps rendering cheap: the dashboard calls this
// while serving a page, and router logs can be hundreds of megabytes.
func TestDiagnoseReadsOnlyTheTail(t *testing.T) {
	dir := t.TempDir()
	m := NewLogManager(nil, dir)
	filler := strings.Repeat("x", 200*1024)
	content := "level=warning msg=initial reseed incomplete, did not reach the peer floor\n" + filler
	if err := os.WriteFile(filepath.Join(dir, "big.log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// The blocker is at the very start, outside the tail window: it must not
	// be reported, because only recent output describes the current state.
	if got := m.Diagnose("big"); got != "" {
		t.Errorf("diagnosis matched outside the tail window: %q", got)
	}
	// Now it is recent, so it must be found.
	recent := filler + "level=warning msg=initial reseed incomplete, did not reach the peer floor\n"
	if err := os.WriteFile(filepath.Join(dir, "big.log"), []byte(recent), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := m.Diagnose("big"); got == "" {
		t.Error("recent blocker in a large log was not diagnosed")
	}
}

// TestDiagnoseCannotBeTrickedIntoLeakingSecrets: the log files are scrubbed,
// but the diagnosis is built from rule text, never from log content. A secret
// must not be able to reach the page through it.
func TestDiagnoseCannotBeTrickedIntoLeakingSecrets(t *testing.T) {
	dir := t.TempDir()
	m := NewLogManager(nil, dir)
	const secret = "S3cr3t-I2PControl-Password"
	content := "level=warning msg=initial reseed incomplete did not reach the peer floor password=" + secret + "\n"
	if err := os.WriteFile(filepath.Join(dir, "x.log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := m.Diagnose("x")
	if got == "" {
		t.Fatal("expected a diagnosis")
	}
	if strings.Contains(got, secret) {
		t.Error("diagnosis echoed log content containing a secret")
	}
}
