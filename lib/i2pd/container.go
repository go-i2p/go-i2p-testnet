package i2pd

import (
	"context"
	"fmt"

	"go-i2p-testnet/lib/docker_control"
	"go-i2p-testnet/lib/utils/logger"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

var log = logger.GetTestnetLogger()

// CreateRouterContainer sets up an i2pd router container.
func CreateRouterContainer(cli *client.Client, ctx context.Context, routerID int, ip, networkName, volumeName string) (string, error) {
	containerName := fmt.Sprintf("router-i2pd-%d", routerID)

	log.WithFields(map[string]interface{}{
		"routerID":      routerID,
		"containerName": containerName,
		"ip":            ip,
		"networkName":   networkName,
	}).Debug("Starting i2pd router container creation")

	// Prepare container configuration
	containerConfig := &container.Config{
		Image: "i2pd-node",
	}

	// Host configuration
	hostConfig := &container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:/var/lib/i2pd", volumeName),
			// fmt.Sprintf("%s:/root/.i2pd", volumeName),
			fmt.Sprintf("%s:/shared", docker_control.SHARED_VOLUME), // move SHARED_VOLUME to docker_control
		},
	}

	// Network settings
	endpointSettings := &network.EndpointSettings{
		IPAMConfig: &network.EndpointIPAMConfig{
			IPv4Address: ip,
		},
	}

	networkingConfig := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: endpointSettings,
		},
	}

	log.WithFields(map[string]interface{}{
		"containerName": containerName,
		"image":         containerConfig.Image,
		"ip":            ip,
		"networkName":   networkName,
		"volumeName":    volumeName,
	}).Debug("Creating i2pd container with config")

	// Create the container
	resp, err := cli.ContainerCreate(ctx, containerConfig, hostConfig, networkingConfig, nil, containerName)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"containerName": containerName,
			"error":         err,
		}).Error("Failed to create i2pd container")
		return "", fmt.Errorf("error creating i2pd container: %v", err)
	}

	// Start the container
	log.WithFields(map[string]interface{}{
		"containerID":   resp.ID,
		"containerName": containerName,
	}).Debug("Starting i2pd container")
	startOptions := container.StartOptions{}
	if err := cli.ContainerStart(ctx, resp.ID, startOptions); err != nil {
		log.WithFields(map[string]interface{}{
			"containerID":   resp.ID,
			"containerName": containerName,
			"error":         err,
		}).Error("Failed to start i2pd container")
		return "", fmt.Errorf("error starting i2pd container: %v", err)
	}

	log.WithFields(map[string]interface{}{
		"containerID":   resp.ID,
		"containerName": containerName,
	}).Debug("Successfully created and started i2pd router container")

	return resp.ID, nil
}
