package docker_control

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

const SHARED_VOLUME = "go-i2p-testnet-shared"

func CreateSharedVolume(cli *client.Client, ctx context.Context) (string, error) {
	//volumeName := "go-i2p-shared"

	log.WithField("volumeName", SHARED_VOLUME).Debug("Starting Docker volume creation")

	createOptions := volume.CreateOptions{
		Name: SHARED_VOLUME,
	}

	log.WithFields(map[string]interface{}{
		"volumeName": SHARED_VOLUME,
		"options":    createOptions,
	}).Debug("Creating Docker volume")

	_, err := cli.VolumeCreate(ctx, createOptions)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"volumeName": SHARED_VOLUME,
			"error":      err,
		}).Error("Failed to create Docker volume")
		return "", fmt.Errorf("error creating shared volume: %v", err)
	}
	log.WithField("volumeName", SHARED_VOLUME).Debug("Successfully created shared Docker volume")
	return SHARED_VOLUME, nil
}
