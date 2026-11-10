package docker_control

import (
	"context"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

func CreateDockerNetwork(cli *client.Client, ctx context.Context, networkName string) (string, error) {
	log.WithField("networkName", networkName).Debug("Starting Docker network creation")
	// Check if the network already exists
	log.Debug("Checking for existing networks")
	networks, err := cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		log.WithError(err).Error("Failed to list Docker networks")
		return "", err
	}
	for _, net := range networks {
		if net.Name == networkName {
			log.WithFields(map[string]interface{}{
				"networkName": networkName,
				"networkID":   net.ID,
			}).Debug("Network already exists, using existing network")
			return net.ID, nil
		}
	}

	// Create the network
	createOptions := network.CreateOptions{
		Driver:   "bridge",
		Internal: true, // Isolates from clearnet
		IPAM: &network.IPAM{
			Config: []network.IPAMConfig{
				{
					Subnet: "172.28.0.0/16",
				},
			},
		},
	}

	log.WithFields(map[string]interface{}{
		"networkName": networkName,
		"driver":      createOptions.Driver,
		"subnet":      createOptions.IPAM.Config[0].Subnet,
		"internal":    createOptions.Internal,
	}).Debug("Creating new Docker network")

	resp, err := cli.NetworkCreate(ctx, networkName, createOptions)
	if err != nil {
		log.WithError(err).Error("Failed to create Docker network")
		return "", err
	}
	log.WithFields(map[string]interface{}{
		"networkName": networkName,
		"networkID":   resp.ID,
	}).Debug("Successfully created Docker network")
	return resp.ID, nil
}
