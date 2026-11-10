package go_i2p

import (
	"context"
	"fmt"

	"github.com/docker/docker/client"
	"go-i2p-testnet/lib/docker_control"
)

func BuildImage(cli *client.Client, ctx context.Context) error {
	log.WithFields(map[string]interface{}{
		"imageName":  docker_control.GoI2PNode.ImageName,
		"dockerfile": docker_control.GoI2PNode.DockerfileName,
	}).Debug("Starting Docker image build")
	err := docker_control.BuildDockerImage(cli, ctx, docker_control.GoI2PNode.ImageName, docker_control.GoI2PNode.DockerfileName)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"imageName":  docker_control.GoI2PNode.ImageName,
			"dockerfile": docker_control.GoI2PNode.DockerfileName,
			"error":      err,
		}).Error("Failed to build Docker image")
		return fmt.Errorf("error building Docker image: %v", err)
	}

	log.WithField("imageName", docker_control.GoI2PNode.ImageName).Debug("Successfully built Docker image")
	return nil
}

func RemoveImage(cli *client.Client, ctx context.Context) error {
	log.WithField("imageName", docker_control.GoI2PNode.ImageName).Debug("Starting Docker image removal")
	err := docker_control.RemoveDockerImage(cli, ctx, "go-i2p-node")
	if err != nil {
		log.WithFields(map[string]interface{}{
			"imageName": docker_control.GoI2PNode.ImageName,
			"error":     err,
		}).Error("Failed to remove Docker image")
		return fmt.Errorf("error removing Docker image: %v", err)
	}

	log.WithField("imageName", docker_control.GoI2PNode.ImageName).Debug("Successfully removed Docker image")
	return nil
}
