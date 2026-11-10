package i2pd

import (
	"context"
	"fmt"

	"go-i2p-testnet/lib/docker_control"

	"github.com/docker/docker/client"
)

func BuildImage(cli *client.Client, ctx context.Context) error {
	log.WithFields(map[string]interface{}{
		"imageName":  docker_control.I2PDNode.ImageName,
		"dockerfile": docker_control.I2PDNode.DockerfileName,
	}).Debug("Starting i2pd Docker image build")
	err := docker_control.BuildDockerImage(cli, ctx, docker_control.I2PDNode.ImageName, docker_control.I2PDNode.DockerfileName)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"imageName":  docker_control.I2PDNode.ImageName,
			"dockerfile": docker_control.I2PDNode.DockerfileName,
			"error":      err,
		}).Error("Failed to build i2pd Docker image")
		return fmt.Errorf("error building i2pd Docker image: %v", err)
	}

	log.WithField("imageName", docker_control.I2PDNode.ImageName).Debug("Successfully built i2pd Docker image")
	return nil
}

func RemoveImage(cli *client.Client, ctx context.Context) error {
	log.WithField("imageName", docker_control.I2PDNode.ImageName).Debug("Starting i2pd Docker image removal")
	err := docker_control.RemoveDockerImage(cli, ctx, docker_control.I2PDNode.ImageName)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"imageName": docker_control.I2PDNode.ImageName,
			"error":     err,
		}).Error("Failed to remove i2pd Docker image")
		return fmt.Errorf("error removing i2pd Docker image: %v", err)
	}

	log.WithField("imageName", docker_control.I2PDNode.ImageName).Debug("Successfully removed i2pd Docker image")
	return nil
}
