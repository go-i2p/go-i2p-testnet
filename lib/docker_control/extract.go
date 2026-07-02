package docker_control

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"github.com/docker/docker/client"
	"io"
	"path/filepath"
)

// ReadFileFromContainer reads a file from inside a container and returns its contents
func ReadFileFromContainer(cli *client.Client, ctx context.Context, containerID string, filePath string) (string, error) {
	// Create a reader for the file content
	reader, stat, err := cli.CopyFromContainer(ctx, containerID, filePath)
	if err != nil {
		return "", fmt.Errorf("error copying from container: %v", err)
	}
	defer reader.Close()

	// If it's not a regular file, return error
	if !stat.Mode.IsRegular() {
		return "", fmt.Errorf("requested path is not a regular file")
	}

	// Read all content
	content, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("error reading file content: %v", err)
	}

	contentStr := string(content)
	contentStrLen := len(contentStr)

	log.WithField("content_length", contentStrLen).Info("File content length")

	return contentStr, nil // Returns a tar archive?
}

func ReadFileFromContainerUnarchive(cli *client.Client, ctx context.Context, containerID string, filePath string) (string, error) {
	// Create a reader for the file content
	reader, _, err := cli.CopyFromContainer(ctx, containerID, filePath)
	if err != nil {
		return "", fmt.Errorf("error copying from container: %v", err)
	}
	defer reader.Close()

	// Create a tar reader to extract the file content
	tarReader := tar.NewReader(reader)

	// Read the tar archive and extract the file
	var fileContent bytes.Buffer
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break // End of archive
		}
		if err != nil {
			return "", fmt.Errorf("error reading tar archive: %v", err)
		}

		// Log the file name
		log.Printf("Found file in tar: %s\n", header.Name)

		// Check if the current file matches the requested file
		if header.Typeflag == tar.TypeReg && header.Name == filepath.Base(filePath) { // Use the relative name
			if _, err := io.Copy(&fileContent, tarReader); err != nil {
				return "", fmt.Errorf("error extracting file content: %v", err)
			}
			return fileContent.String(), nil
		}
	}

	return "", fmt.Errorf("file %s not found in the tar archive", filePath)
}
