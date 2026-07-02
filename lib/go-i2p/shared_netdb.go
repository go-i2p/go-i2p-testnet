package go_i2p

import (
	"context"
	"fmt"

	"github.com/docker/docker/client"
	"go-i2p-testnet/lib/docker_control"
)

// GoI2PNetDbDir is where the go-i2p binary keeps its netDb inside the
// router container ($HOME/.go-i2p/config/netDb with HOME=/root).
const GoI2PNetDbDir = "/root/.go-i2p/config/netDb"

// SyncSharedToNetDb copies all RouterInfos from the shared volume (mounted
// at /shared in the router container) into the go-i2p router's netDb.
// go-i2p and i2pd use the same on-disk layout (r<X>/routerInfo-<hash>.dat),
// so this is a straight copy.
func SyncSharedToNetDb(cli *client.Client, ctx context.Context, containerID string) error {
	cmd := []string{"sh", "-c", fmt.Sprintf(
		"mkdir -p %s && if [ -d /shared/netDb ]; then cp -r /shared/netDb/. %s/; fi",
		GoI2PNetDbDir, GoI2PNetDbDir)}
	out, err := docker_control.ExecInContainer(cli, ctx, containerID, cmd)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"containerID": containerID,
			"output":      out,
			"error":       err,
		}).Error("Failed to sync shared netDb into go-i2p container")
		return fmt.Errorf("error syncing shared netDb into go-i2p container: %v", err)
	}
	log.WithField("containerID", containerID).Debug("Synced shared netDb into go-i2p container")
	return nil
}

// SyncNetDbToShared copies the go-i2p router's netDb entries into the shared
// volume. Best effort: the go-i2p router does not currently publish its own
// RouterInfo, so this only re-exports entries it has learned or imported.
func SyncNetDbToShared(cli *client.Client, ctx context.Context, containerID string) error {
	cmd := []string{"sh", "-c", fmt.Sprintf(
		"mkdir -p /shared/netDb && if [ -d %s ]; then cp -r %s/. /shared/netDb/; fi",
		GoI2PNetDbDir, GoI2PNetDbDir)}
	out, err := docker_control.ExecInContainer(cli, ctx, containerID, cmd)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"containerID": containerID,
			"output":      out,
			"error":       err,
		}).Error("Failed to sync go-i2p netDb to shared volume")
		return fmt.Errorf("error syncing go-i2p netDb to shared volume: %v", err)
	}
	log.WithField("containerID", containerID).Debug("Synced go-i2p netDb to shared volume")
	return nil
}
