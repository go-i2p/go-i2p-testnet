package dashboard

import (
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

func dockerContainer(id, name, image, ip string, attached bool) types.Container {
	c := types.Container{ID: id, Image: image, Names: []string{name}}
	if attached {
		c.NetworkSettings = &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{}}
		if ip != "" {
			c.NetworkSettings.Networks["go-i2p-testnet"] = &network.EndpointSettings{IPAddress: ip}
		}
	}
	return c
}

// TestSelectRoutersFromContainerList covers the discovery path the audit found
// untested: standalone dashboard mode builds its whole roster from Docker with
// no router list in memory.
func TestSelectRoutersFromContainerList(t *testing.T) {
	list := []types.Container{
		dockerContainer("c1", "/router-i2pd-1", "i2pd-node", "172.28.0.2", true),
		dockerContainer("c2", "/router-i2pd-2", "i2pd-node", "172.28.0.3", true),
		dockerContainer("c3", "/router-goi2p-4", "go-i2p-node", "172.28.0.5", true),
		dockerContainer("z1", "/nginx-proxy", "nginx:latest", "172.17.0.9", true),
	}
	got := SelectRouters(list, "go-i2p-testnet")
	if len(got) != 3 {
		t.Fatalf("routers = %d, want 3 (unrelated containers must be ignored): %+v", len(got), got)
	}
	byName := map[string]Router{}
	for _, r := range got {
		byName[r.Name] = r
	}
	if r := byName["router-i2pd-1"]; r.Impl != ImplI2PD || r.IP != "172.28.0.2" || r.ContainerID != "c1" {
		t.Errorf("i2pd router misread: %+v", r)
	}
	if r := byName["router-goi2p-4"]; r.Impl != ImplGoI2P || r.ContainerID != "c3" {
		t.Errorf("go-i2p router misread: %+v", r)
	}
	if _, ok := byName["nginx-proxy"]; ok {
		t.Error("a non-router container joined the roster")
	}
}

// TestSelectRoutersSkipsUnattached: a router that is not on the testnet bridge
// has no address the dashboard can dial, so it must not be polled.
func TestSelectRoutersSkipsUnattached(t *testing.T) {
	list := []types.Container{
		dockerContainer("c1", "/router-i2pd-1", "i2pd-node", "172.28.0.2", true),
		dockerContainer("c2", "/router-i2pd-2", "i2pd-node", "", true),          // attached, no address yet
		dockerContainer("c3", "/router-i2pd-3", "i2pd-node", "10.0.0.9", false), // on some other network
	}
	got := SelectRouters(list, "go-i2p-testnet")
	if len(got) != 1 || got[0].Name != "router-i2pd-1" {
		t.Errorf("routers = %+v, want only the attached one", got)
	}
}

// TestSelectRoutersIgnoresStoppedWithoutNetwork: a container listed with All:true
// that has no network settings at all must not crash or be polled.
func TestSelectRoutersIgnoresStoppedWithoutNetwork(t *testing.T) {
	got := SelectRouters([]types.Container{{ID: "x", Names: []string{"/router-i2pd-9"}, Image: "i2pd-node"}}, "go-i2p-testnet")
	if len(got) != 0 {
		t.Errorf("want no routers, got %+v", got)
	}
}

func TestRouterImplClassification(t *testing.T) {
	cases := []struct {
		name, image, want string
	}{
		{"router-i2pd-1", "i2pd-node", ImplI2PD},
		{"router-goi2p-1", "go-i2p-node", ImplGoI2P},
		{"router-java-1", "i2pd-node", ImplI2PD}, // name tells nothing, image does
		{"router-other-1", "some-image", ImplGoI2P},
	}
	for _, c := range cases {
		if got := RouterImpl(c.name, c.image); got != c.want {
			t.Errorf("RouterImpl(%q, %q) = %q, want %q", c.name, c.image, got, c.want)
		}
	}
}

// TestTargetsUseDiscoveredIDs: the container id must reach the poller target,
// because topology collection is keyed on it. A roster that loses the id
// renders an empty graph with no error.
func TestTargetsUseDiscoveredIDs(t *testing.T) {
	src := NewSource(func() []Router {
		return []Router{{Name: "router-i2pd-1", Impl: ImplI2PD, IP: "172.28.0.2", ContainerID: "abc123"}}
	}, NewResolver(nil), "pw", false)
	targets := src.Targets(nil)
	if len(targets) != 1 {
		t.Fatalf("targets = %d", len(targets))
	}
	if targets[0].ContainerID != "abc123" {
		t.Errorf("ContainerID = %q, want it threaded through to the poller", targets[0].ContainerID)
	}
	if targets[0].IP != "172.28.0.2" {
		t.Errorf("IP = %q, want the testnet address for edge matching", targets[0].IP)
	}
}
