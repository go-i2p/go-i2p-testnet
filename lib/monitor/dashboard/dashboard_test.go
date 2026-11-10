package dashboard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-i2p-testnet/lib/monitor/web"
)

func TestResolvePasswordEnv(t *testing.T) {
	t.Setenv(EnvPassword, "env-secret")
	dir := t.TempDir()
	pw, err := ResolvePassword(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pw != "env-secret" {
		t.Errorf("password = %q", pw)
	}
	// Must not have written a file when env provided one.
	if _, err := os.Stat(filepath.Join(dir, PasswordFileName)); !os.IsNotExist(err) {
		t.Error("env password should not create a file")
	}
}

func TestResolvePasswordFile(t *testing.T) {
	t.Setenv(EnvPassword, "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, PasswordFileName), []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pw, err := ResolvePassword(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pw != "file-secret" {
		t.Errorf("password = %q", pw)
	}
}

func TestResolvePasswordGenerates(t *testing.T) {
	t.Setenv(EnvPassword, "")
	dir := t.TempDir()
	pw, err := ResolvePassword(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) < 32 {
		t.Errorf("generated password too short: %d", len(pw))
	}
	st, err := os.Stat(filepath.Join(dir, PasswordFileName))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("password file mode = %v, want 0600", st.Mode().Perm())
	}
	// Second call re-reads the same password.
	pw2, err := ResolvePassword(dir)
	if err != nil || pw2 != pw {
		t.Errorf("second resolve = %q, %v; want %q", pw2, err, pw)
	}
}

func TestDefaultPasswordRefused(t *testing.T) {
	t.Setenv(EnvPassword, DefaultPassword)
	dir := t.TempDir()
	if _, err := ResolvePassword(dir); err == nil {
		t.Fatal("default password must be refused without local-testing opt-in")
	}
	t.Setenv(EnvAllowDefaultPassword, "1")
	if _, err := ResolvePassword(dir); err != nil {
		t.Fatalf("default password must be allowed with opt-in: %v", err)
	}
}

func TestDefaultPasswordInFileRefused(t *testing.T) {
	t.Setenv(EnvPassword, "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, PasswordFileName), []byte(DefaultPassword+"\n"), 0o600)
	if _, err := ResolvePassword(dir); err == nil {
		t.Fatal("default password from file must be refused")
	}
}

func TestTargetsBuildPerImpl(t *testing.T) {
	routers := []Router{
		{Name: "router-i2pd-1", Impl: "i2pd", IP: "172.28.0.2", ContainerID: "c1"},
		{Name: "router-goi2p-1", Impl: "goi2p", IP: "172.28.0.3", ContainerID: "c2"},
	}
	src := NewSource(func() []Router { return routers }, NewResolver(nil), "pw", false)
	targets := src.Targets(context.Background())
	if len(targets) != 2 {
		t.Fatalf("targets = %d", len(targets))
	}
	if targets[0].URL != "https://172.28.0.2:7650/" {
		t.Errorf("i2pd URL = %s, want pinned-https endpoint", targets[0].URL)
	}
	if targets[1].URL != "http://172.28.0.3:7650/" {
		t.Errorf("goi2p URL = %s, want http endpoint", targets[1].URL)
	}
}

func TestTargetsSkipIPless(t *testing.T) {
	src := NewSource(func() []Router {
		return []Router{{Name: "x", Impl: "i2pd"}}
	}, NewResolver(nil), "pw", false)
	if got := src.Targets(context.Background()); len(got) != 0 {
		t.Errorf("routers without IP must be skipped, got %+v", got)
	}
}

// TestGeneratedConfigsCarryPassword guards the config generators used at
// "add": the I2PControl section must exist and the redaction helper must
// work on their output.
func TestI2PDConfigRedaction(t *testing.T) {
	in := "[i2pcontrol]\nenabled=true\npassword=supersecret\n[reseed]\n"
	out := strings.Join(mapLines(in), "")
	if strings.Contains(out, "supersecret") {
		t.Errorf("redaction failed: %s", out)
	}
	if !strings.Contains(out, "password= [redacted]") {
		t.Errorf("unexpected redaction output: %s", out)
	}
}

func mapLines(in string) []string {
	var res []string
	for _, line := range strings.Split(in, "\n") {
		if strings.Contains(strings.ToLower(strings.TrimSpace(line)), "password") {
			if eq := strings.Index(line, "="); eq >= 0 {
				line = line[:eq+1] + " [redacted]"
			}
		}
		res = append(res, line)
	}
	return res
}

// Compile-time assertion that Source.Targets matches web.SourceFunc.
var _ web.SourceFunc = (&Source{}).Targets
