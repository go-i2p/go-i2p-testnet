package monitor

import (
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
