package docker_control

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"go-i2p-testnet/lib/dockerfiles"
	"go-i2p-testnet/lib/utils/logger"
)

var log = logger.GetTestnetLogger()

// DockerMessage represents the structure of Docker build output messages
type DockerMessage struct {
	Stream string `json:"stream,omitempty"`
	Aux    struct {
		ID string `json:"ID,omitempty"`
	} `json:"aux,omitempty"`
	Error string `json:"error,omitempty"`
}

func BuildDockerImage(cli *client.Client, ctx context.Context, imageName, dockerfileName string) error {
	log.WithFields(map[string]interface{}{
		"imageName":      imageName,
		"dockerfileName": dockerfileName,
	}).Debug("Starting Docker image build")

	// Check if the image already exists
	exists, err := imageExists(cli, ctx, imageName)
	if err != nil {
		log.WithError(err).Error("Failed to check if Docker image exists")
		return fmt.Errorf("error checking for Docker image: %v", err)
	}

	// If the image exists, skip building it
	if exists {
		log.WithField("imageName", imageName).Debug("Docker image already exists, skipping build")
		return nil
	}

	// Retrieve the Dockerfile content from the embedded files
	dockerfileContent, err := dockerfiles.GetDockerfileContent(dockerfileName)
	if err != nil {
		log.WithError(err).Errorf("Failed to get embedded Dockerfile: %s", dockerfileName)
		return fmt.Errorf("error retrieving Dockerfile %s: %v", dockerfileName, err)
	}

	// Create an in-memory tar archive containing the embedded Dockerfile
	log.Debug("Creating in-memory tar archive of embedded Dockerfile")
	tarBuffer := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuffer)

	// Add the Dockerfile to the tar archive
	err = tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Size: int64(len(dockerfileContent)),
		Mode: 0o600,
	})
	if err != nil {
		tw.Close()
		log.WithError(err).Error("Failed to write tar header")
		return fmt.Errorf("error writing tar header: %v", err)
	}

	_, err = tw.Write(dockerfileContent)
	if err != nil {
		tw.Close()
		log.WithError(err).Error("Failed to write Dockerfile to tar")
		return fmt.Errorf("error writing Dockerfile to tar: %v", err)
	}

	// Close the tar writer
	err = tw.Close()
	if err != nil {
		log.WithError(err).Error("Failed to close tar writer")
		return fmt.Errorf("error closing tar writer: %v", err)
	}

	// Use the in-memory tar archive as the build context
	log.Debug("Initiating Docker image build")
	buildOptions := types.ImageBuildOptions{
		Tags:       []string{imageName},
		Dockerfile: "Dockerfile",
		Remove:     true,
	}

	resp, err := cli.ImageBuild(ctx, tarBuffer, buildOptions)
	if err != nil {
		log.WithError(err).Error("Docker image build failed")
		return fmt.Errorf("error building Docker image: %v", err)
	}
	defer resp.Body.Close()

	if err := streamDockerOutput(resp.Body); err != nil {
		log.WithError(err).Error("Failed to read build output")
		return fmt.Errorf("error reading build output: %v", err)
	}

	log.Debug("Docker build completed")
	return nil
}

// removeDockerImage removes a Docker image by name or ID
func RemoveDockerImage(cli *client.Client, ctx context.Context, imageName string) error {
	log.WithField("imageName", imageName).Debug("Attempting to remove Docker image")

	exists, err := imageExists(cli, ctx, imageName)
	if err != nil {
		log.WithError(err).Error("Failed to check if Docker image exists")
		return err
	}
	if !exists {
		log.WithField("imageName", imageName).Error("Cannot remove non-existent image")
		return fmt.Errorf("error: cant remove image '%s' that doesn't exist", imageName)
	}
	removeOptions := image.RemoveOptions{
		Force:         true,
		PruneChildren: true,
	}

	log.Debug("Removing Docker image")
	removedImages, err := cli.ImageRemove(ctx, imageName, removeOptions)
	if err != nil {
		log.WithError(err).Error("Failed to remove Docker image")
		return fmt.Errorf("error removing image %s: %v", imageName, err)
	}

	// Display removed images
	for _, image := range removedImages {
		log.WithField("imageID", image.Deleted).Debug("Successfully removed image")
	}
	return nil
}

func imageExists(cli *client.Client, ctx context.Context, imageName string) (bool, error) {
	log.WithField("imageName", imageName).Debug("Checking if Docker image exists")

	ListOptions := image.ListOptions{}
	images, err := cli.ImageList(ctx, ListOptions)
	if err != nil {
		log.WithError(err).Error("Failed to list Docker images")
		return false, err
	}

	for _, image := range images {
		for _, tag := range image.RepoTags {
			if tag == imageName || tag == imageName+":latest" {
				log.WithField("imageName", imageName).Debug("Docker image found")
				return true, nil
			}
		}
	}

	log.WithField("imageName", imageName).Debug("Docker image not found")
	return false, nil
}

// streamDockerOutput reads the Docker build output and prints it in a clean format
func streamDockerOutput(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)

	for scanner.Scan() {
		line := scanner.Text()
		var msg DockerMessage

		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			// If it's not JSON, print the line as-is
			fmt.Println(line)
			continue
		}

		// Handle different types of messages
		switch {
		case msg.Error != "":
			log.Errorf("Docker build error: %s", msg.Error)
		case msg.Aux.ID != "":
			log.Infof("Image ID: %s", msg.Aux.ID)
		case msg.Stream != "":
			// Clean up the stream output
			stream := strings.TrimSpace(msg.Stream)
			if stream != "" {
				// Remove extra newlines and carriage returns
				stream = strings.ReplaceAll(stream, "\r", "")
				stream = strings.TrimSpace(stream)
				fmt.Println(stream)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading docker output: %v", err)
	}

	return nil
}
