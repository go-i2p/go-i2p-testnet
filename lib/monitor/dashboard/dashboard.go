// Package dashboard wires the testnet's router inventory to the web
// dashboard: it turns routers (either the CLI's in-memory list or the live
// Docker topology) into poller targets, resolves the I2PControl password
// from the environment or a gitignored file, and fetches each i2pd router's
// generated TLS certificate so the client can pin it.
package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go-i2p-testnet/lib/docker_control"
	"go-i2p-testnet/lib/monitor/web"
	"go-i2p-testnet/lib/utils/logger"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

var log = logger.GetTestnetLogger()

// EnvPassword is the environment variable holding the I2PControl password
// shared by every testnet router and the dashboard.
const EnvPassword = "GO_I2P_TESTNET_I2PCONTROL_PASSWORD"

// EnvAllowDefaultPassword opts into using the well-known default password
// for local testing only. Any other environment refuses it.
const EnvAllowDefaultPassword = "TESTNET_ALLOW_DEFAULT_I2PCONTROL_PASSWORD"

// DefaultPassword is the I2PControl factory default. It must never be used
// outside explicitly opted-in local testing.
const DefaultPassword = "itoopie"

// PasswordFileName is the gitignored file where a generated session password
// is stored so the dashboard can read it.
const PasswordFileName = ".i2pcontrol-password"

// I2PControlPort is where testnet routers serve I2PControl.
const I2PControlPort = 7650

// Implementation names used across the dashboard. These are the only router
// kinds the testnet knows how to launch, and the values come from the CLI's
// own router records, never from a per-router list.
const (
	ImplI2PD  = "i2pd"
	ImplGoI2P = "goi2p"
)

// i2pControlCertPath is where i2pd writes its self-signed I2PControl cert
// inside the per-router datadir volume.
const i2pControlCertPath = "/var/lib/i2pd/i2pcontrol.crt.pem"

// Router is the minimal router description the dashboard needs. The CLI
// passes its own router entries; standalone mode builds them from Docker.
type Router struct {
	Name        string // container name, e.g. "router-i2pd-1"
	Impl        string // "i2pd" | "goi2p"
	IP          string
	ContainerID string
}

// Resolver fetches the pinned certificate for one i2pd router. It is
// memoized per container; an empty result means "not generated yet" and is
// retried on the next poll cycle.
type Resolver struct {
	cli *client.Client

	mu    sync.Mutex
	certs map[string]certEntry
}

type certEntry struct {
	pem     []byte
	attempt time.Time
}

func NewResolver(cli *client.Client) *Resolver {
	return &Resolver{cli: cli, certs: map[string]certEntry{}}
}

// Cert returns the router's I2PControl certificate PEM, or nil if it is not
// available yet. Retries are rate-limited so a router without a cert (go-i2p
// uses plaintext HTTP anyway, or i2pd mid-startup) doesn't hammer the Docker
// API every cycle.
func (r *Resolver) Cert(ctx context.Context, rt Router) []byte {
	if rt.Impl != "i2pd" || rt.ContainerID == "" || r.cli == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.certs[rt.ContainerID]
	if ok && len(e.pem) > 0 {
		return e.pem
	}
	if ok && time.Since(e.attempt) < 5*time.Second {
		return nil
	}
	r.certs[rt.ContainerID] = certEntry{attempt: time.Now()}

	pem, err := docker_control.ReadFileFromContainerUnarchive(r.cli, ctx, rt.ContainerID, i2pControlCertPath)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"router": rt.Name,
			"error":  fmt.Sprintf("%v", err),
		}).Debug("I2PControl cert not available yet")
		return nil
	}
	if !strings.Contains(pem, "BEGIN CERTIFICATE") {
		return nil
	}
	r.certs[rt.ContainerID] = certEntry{pem: []byte(pem), attempt: time.Now()}
	return []byte(pem)
}

// Source adapts a router-list function to the poller's web.SourceFunc.
type Source struct {
	routers  func() []Router
	resolver *Resolver
	password string
	// InsecureTLS, when true, skips TLS verification (explicit operator
	// opt-in; the caller must warn).
	InsecureTLS bool
}

// NewSource builds the poller target source.
func NewSource(routers func() []Router, resolver *Resolver, password string, insecureTLS bool) *Source {
	return &Source{routers: routers, resolver: resolver, password: password, InsecureTLS: insecureTLS}
}

// Targets converts the current routers into dashboard targets. i2pd serves
// I2PControl over HTTPS (cert pinned); go-i2p serves plaintext HTTP on the
// internal-only bridge.
func (s *Source) Targets(ctx context.Context) []web.Target {
	routers := s.routers()
	out := make([]web.Target, 0, len(routers))
	for _, rt := range routers {
		if rt.IP == "" {
			continue
		}
		t := web.Target{
			Name:        rt.Name,
			Impl:        rt.Impl,
			Password:    s.password,
			InsecureTLS: s.InsecureTLS,
			ContainerID: rt.ContainerID,
			IP:          rt.IP,
		}
		switch rt.Impl {
		case "i2pd":
			t.URL = fmt.Sprintf("https://%s:%d/", rt.IP, I2PControlPort)
			t.PinnedCertPEM = s.resolver.Cert(ctx, rt)
		default: // goi2p and anything else: plaintext JSON-RPC
			t.URL = fmt.Sprintf("http://%s:%d/", rt.IP, I2PControlPort)
		}
		out = append(out, t)
	}
	return out
}

// ResolvePassword determines the I2PControl password to use:
//  1. EnvPassword, if set;
//  2. the gitignored password file (logs-dir/.i2pcontrol-password), if present;
//  3. a freshly generated random password, written to the file (0600).
//
// The factory default "itoopie" is accepted only when
// EnvAllowDefaultPassword is set to a truthy value (local testing);
// otherwise it is rejected with an error.
func ResolvePassword(logsDir string) (string, error) {
	allowDefault := isTruthy(os.Getenv(EnvAllowDefaultPassword))

	if pw := os.Getenv(EnvPassword); pw != "" {
		return validatePassword(pw, allowDefault)
	}

	path := filepath.Join(logsDir, PasswordFileName)
	if data, err := os.ReadFile(path); err == nil {
		pw := strings.TrimSpace(string(data))
		if pw != "" {
			return validatePassword(pw, allowDefault)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("reading I2PControl password file: %w", err)
	}

	pw, err := generatePassword()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return "", fmt.Errorf("creating logs dir for I2PControl password: %w", err)
	}
	if err := os.WriteFile(path, []byte(pw+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("writing I2PControl password file: %w", err)
	}
	log.WithField("path", path).Info("Generated I2PControl password file (password itself is never logged)")
	return pw, nil
}

func validatePassword(pw string, allowDefault bool) (string, error) {
	if pw == DefaultPassword && !allowDefault {
		return "", fmt.Errorf("refusing to use the default I2PControl password %q outside local testing: set %s or unset %s",
			DefaultPassword, EnvPassword, EnvAllowDefaultPassword)
	}
	return pw, nil
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	}
	return false
}

func generatePassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating I2PControl password: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// RoutersFromDocker discovers testnet routers from the live Docker state:
// containers attached to the testnet network whose names start with
// "router-". Used by the standalone `dashboard` mode so nothing needs to be
// hardcoded.
func RoutersFromDocker(cli *client.Client, networkName string) func() []Router {
	return func() []Router {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		list, err := cli.ContainerList(ctx, container.ListOptions{All: true})
		if err != nil {
			log.WithError(err).Warn("dashboard: docker container list failed")
			return nil
		}
		return SelectRouters(list, networkName)
	}
}

// SelectRouters is the discovery decision, separated from the Docker call so
// it can be tested against container listings directly. A container joins the
// roster when its name is a testnet router name and it holds an address on the
// testnet network; everything else is ignored, including unrelated containers
// that happen to be running on the host.
func SelectRouters(list []types.Container, networkName string) []Router {
	var out []Router
	for _, c := range list {
		name := ""
		for _, n := range c.Names {
			if strings.HasPrefix(strings.TrimPrefix(n, "/"), "router-") {
				name = strings.TrimPrefix(n, "/")
				break
			}
		}
		if name == "" {
			continue
		}
		ip := ""
		if nets := c.NetworkSettings; nets != nil {
			if ep, ok := nets.Networks[networkName]; ok && ep.IPAddress != "" {
				ip = ep.IPAddress
			}
		}
		if ip == "" {
			continue // not attached to the testnet network
		}
		out = append(out, Router{Name: name, Impl: RouterImpl(name, c.Image), IP: ip, ContainerID: c.ID})
	}
	return out
}

// RouterImpl classifies a router by name, falling back to its image. go-i2p is
// the default because it is the implementation with the fewest tell-tale
// strings, not because it is expected more often.
func RouterImpl(name, image string) string {
	switch {
	case strings.HasPrefix(name, "router-i2pd-"):
		return ImplI2PD
	case strings.HasPrefix(name, "router-goi2p-"):
		return ImplGoI2P
	case strings.Contains(image, "i2pd"):
		return ImplI2PD
	default:
		return ImplGoI2P
	}
}
