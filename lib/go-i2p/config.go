package go_i2p

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"go-i2p-testnet/lib/utils"
	"go-i2p-testnet/lib/utils/logger"
)

var log = logger.GetTestnetLogger()

func CopyConfigToVolume(cli *client.Client, ctx context.Context, volumeName string, configData string) error {
	log.WithField("volumeName", volumeName).Debug("Starting config copy to volume")

	tempContainerConfig := &container.Config{
		Image:      "alpine",
		Tty:        false,
		WorkingDir: "/config",
		Cmd:        []string{"sh", "-c", "sleep 1d"},
	}

	hostConfig := &container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:/config", volumeName),
		},
	}

	log.WithFields(map[string]interface{}{
		"image":      tempContainerConfig.Image,
		"volumeName": volumeName,
	}).Debug("Creating temporary container")

	resp, err := cli.ContainerCreate(ctx, tempContainerConfig, hostConfig, nil, nil, "")
	if err != nil {
		log.WithError(err).Error("Failed to create temporary container")
		return fmt.Errorf("error creating temporary container: %v", err)
	}
	defer func() {
		log.WithField("containerID", resp.ID).Debug("Removing temporary container")
		RemoveOptions := container.RemoveOptions{Force: true}
		err := cli.ContainerRemove(ctx, resp.ID, RemoveOptions)
		if err != nil {
			log.WithError(err).Error("Failed to remove temporary container")
		}
	}()

	log.WithField("containerID", resp.ID).Debug("Starting temporary container")
	StartOptions := container.StartOptions{}
	if err := cli.ContainerStart(ctx, resp.ID, StartOptions); err != nil {
		log.WithError(err).Error("Failed to start temporary container")
		return fmt.Errorf("error starting temporary container: %v", err)
	}

	log.Debug("Creating tar archive of config data")
	// The volume is later mounted at /root in the router container, so this
	// lands at /root/.go-i2p/config.yaml where go-i2p looks by default.
	tarReader, err := utils.CreateTarArchive(".go-i2p/config.yaml", configData)
	if err != nil {
		log.WithError(err).Error("Failed to create tar archive")
		return fmt.Errorf("error creating tar archive: %v", err)
	}

	log.WithField("containerID", resp.ID).Debug("Copying config to container")
	err = cli.CopyToContainer(ctx, resp.ID, "/config", tarReader, container.CopyToContainerOptions{})
	if err != nil {
		log.WithError(err).Error("Failed to copy config to container")
		return fmt.Errorf("error copying to container: %v", err)
	}

	log.WithField("containerID", resp.ID).Debug("Stopping temporary container")
	StopOptions := container.StopOptions{}
	if err := cli.ContainerStop(ctx, resp.ID, StopOptions); err != nil {
		log.WithError(err).Error("Failed to stop temporary container")
		return fmt.Errorf("error stopping temporary container: %v", err)
	}

	log.Debug("Successfully copied config to volume")
	return nil
}

// GenerateRouterConfig produces the config.yaml for a go-i2p testnet node.
// The testnet network is internal-only, so the router must bootstrap from its
// local netDb (populated via the sync commands) instead of clearnet reseed
// servers. All paths use the binary's defaults under /root/.go-i2p, which
// live on the per-router volume mounted at /root.
func GenerateRouterConfig(routerID int) string {
	log.WithField("routerID", routerID).Debug("Generating go-i2p router config")
	return `# go-i2p testnet node: bootstrap only from the local netDb,
# never from clearnet reseed servers (the network is internal-only).
bootstrap:
  type: local
  low-peer-threshold: 1
netdb:
  # peers run netid 5; strict validation would reject their RouterInfos
  strict-routerinfo-network-validation: false
`
}
