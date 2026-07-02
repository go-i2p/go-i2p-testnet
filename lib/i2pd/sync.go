package i2pd

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/sirupsen/logrus"
	"io"
	"os"
)

func SyncNetDbToShared(cli *client.Client, ctx context.Context, containerID string, volumeName string) error {
	routerInfoString, filename, directory, err := GetRouterInfoWithFilenameRaw(cli, ctx, containerID)
	if err != nil {
		log.WithError(err).Error("GetRouterInfoWithFilenameRaw failed")
		return err
	}
	log.WithFields(logrus.Fields{
		"routerInfoString len": len(routerInfoString),
		"filename":             filename,
		"directory":            directory,
	}).Debug("GetRouterInfoWithFilename Raw")

	// Create the temporary helper container
	helperContainerName := fmt.Sprintf("helper-container-%s", containerID[:12])
	helperConfig := &container.Config{
		Image: "alpine",
		Cmd:   []string{"sleep", "60"},
	}

	hostConfig := container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:/shared", volumeName),
		},
	}

	resp, err := cli.ContainerCreate(ctx, helperConfig, &hostConfig, nil, nil, helperContainerName)
	if err != nil {
		return fmt.Errorf("error creating helper container: %v", err)
	}

	helperContainerID := resp.ID
	defer func() {
		removeOptions := container.RemoveOptions{Force: true}
		cli.ContainerRemove(ctx, helperContainerID, removeOptions)
	}()

	if err := cli.ContainerStart(ctx, helperContainerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("error starting helper container: %v", err)
	}

	// Create the target directory inside the helper container**
	mkdirCmd := []string{"mkdir", "-p", fmt.Sprintf("/shared/netDb/%s", directory)}
	execConfig := types.ExecConfig{
		Cmd:          mkdirCmd,
		AttachStdout: true,
		AttachStderr: true,
	}
	execIDResp, err := cli.ContainerExecCreate(ctx, helperContainerID, execConfig)
	if err != nil {
		return fmt.Errorf("error creating exec config for mkdir: %v", err)
	}

	err = cli.ContainerExecStart(ctx, execIDResp.ID, types.ExecStartCheck{})
	if err != nil {
		return fmt.Errorf("error executing mkdir in helper container: %v", err)
	}

	// Create a tar archive containing the router info file
	tarBuffer := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuffer)

	header := &tar.Header{
		Name: filename,
		Mode: 0600,
		Size: int64(len(routerInfoString)),
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("error writing tar header: %v", err)
	}

	if _, err := tw.Write([]byte(routerInfoString)); err != nil {
		return fmt.Errorf("error writing router info to tar: %v", err)
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("error closing tar writer: %v", err)
	}

	// Copy the tar archive to the helper container's shared volume
	err = cli.CopyToContainer(ctx, helperContainerID, "/shared/netDb/"+directory, bytes.NewReader(tarBuffer.Bytes()), container.CopyToContainerOptions{})
	if err != nil {
		return fmt.Errorf("error copying router info to helper container: %v", err)
	}

	log.WithFields(logrus.Fields{
		"directory": directory,
		"filename":  filename,
		"path":      "/shared/netDb/" + directory,
	}).Debug("Successfully synced router info to shared volume")

	return nil
}

// SyncSharedToNetDb syncs netDb from the shared volume to the router container
func SyncSharedToNetDb(cli *client.Client, ctx context.Context, containerID string, volumeName string) error {
	// Define the destination path inside the target container
	destinationPath := I2PDDataDir + "/netDb"

	// Create a temporary helper container with the shared volume mounted
	helperContainerName := "helper-container"
	helperConfig := &container.Config{
		Image: "alpine",
		Cmd:   []string{"sleep", "60"},
	}
	hostConfig := &container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:/shared", volumeName),
		},
	}

	resp, err := cli.ContainerCreate(ctx, helperConfig, hostConfig, nil, nil, helperContainerName)
	if err != nil {
		return fmt.Errorf("error creating helper container: %v", err)
	}
	helperContainerID := resp.ID
	defer func() {
		removeOptions := container.RemoveOptions{Force: true}
		cli.ContainerRemove(ctx, helperContainerID, removeOptions)
	}()

	// Start the helper container
	startOptions := container.StartOptions{}
	if err := cli.ContainerStart(ctx, helperContainerID, startOptions); err != nil {
		return fmt.Errorf("error starting helper container: %v", err)
	}

	// **Ensure that /shared/netDb exists in the helper container**
	mkdirCmd := []string{"mkdir", "-p", "/shared/netDb"}
	execConfig := types.ExecConfig{
		Cmd:          mkdirCmd,
		AttachStdout: true,
		AttachStderr: true,
	}
	execIDResp, err := cli.ContainerExecCreate(ctx, helperContainerID, execConfig)
	if err != nil {
		return fmt.Errorf("error creating exec config for mkdir: %v", err)
	}

	err = cli.ContainerExecStart(ctx, execIDResp.ID, types.ExecStartCheck{})
	if err != nil {
		return fmt.Errorf("error executing mkdir in helper container: %v", err)
	}

	// **Copy the netDb directory from the helper container (shared volume) to the target container**
	reader, _, err := cli.CopyFromContainer(ctx, helperContainerID, "/shared/netDb")
	if err != nil {
		return fmt.Errorf("error copying from helper container: %v", err)
	}
	defer reader.Close()

	// **Ensure the destination directory exists in the router container**
	execOptions := types.ExecConfig{
		Cmd:          []string{"mkdir", "-p", destinationPath},
		AttachStdout: true,
		AttachStderr: true,
	}
	execIDResp, err = cli.ContainerExecCreate(ctx, containerID, execOptions)
	if err != nil {
		return fmt.Errorf("error creating exec config for mkdir in target container: %v", err)
	}
	err = cli.ContainerExecStart(ctx, execIDResp.ID, types.ExecStartCheck{})
	if err != nil {
		return fmt.Errorf("error starting exec for mkdir in target container: %v", err)
	}

	// **Copy the netDb directory to the target container**
	// The archive from CopyFromContainer is rooted at "netDb/", so extract
	// into the data dir (the parent), not into netDb itself — extracting at
	// destinationPath would nest a second netDb directory inside it.
	copyToContainerOptions := types.CopyToContainerOptions{
		AllowOverwriteDirWithFile: true,
	}
	err = cli.CopyToContainer(ctx, containerID, I2PDDataDir, reader, copyToContainerOptions)
	if err != nil {
		return fmt.Errorf("error copying to target container: %v", err)
	}

	fmt.Println("Successfully synchronized netDb from shared volume to container")
	return nil
}

// SyncRouterInfoToNetDb sorts the RouterInfo into the proper location in netDb
func SyncRouterInfoToNetDb(cli *client.Client, ctx context.Context, containerID string, volumeName string) error {
	// Get RouterInfo, routerInfoString, and the generated filename
	routerInfoString, filename, directory, err := GetRouterInfoWithFilenameRaw(cli, ctx, containerID)
	if err != nil {
		return fmt.Errorf("error getting router info: %v", err)
	}
	log.Debugf("got filename: %s\n", filename)

	// Create a temporary helper container with the shared volume mounted
	helperContainerName := fmt.Sprintf("helper-container-%s", containerID[:12])
	helperConfig := &container.Config{
		Image: "alpine",
		Cmd:   []string{"sleep", "60"},
	}
	hostConfig := &container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:/shared", volumeName),
		},
	}

	resp, err := cli.ContainerCreate(ctx, helperConfig, hostConfig, nil, nil, helperContainerName)
	if err != nil {
		return fmt.Errorf("error creating helper container: %v", err)
	}
	helperContainerID := resp.ID
	defer func() {
		// Clean up the helper container
		removeOptions := container.RemoveOptions{Force: true}
		cli.ContainerRemove(ctx, helperContainerID, removeOptions)
	}()

	// Start the helper container
	startOptions := container.StartOptions{}
	if err := cli.ContainerStart(ctx, helperContainerID, startOptions); err != nil {
		return fmt.Errorf("error starting helper container: %v", err)
	}

	// Define the target directory inside the helper container
	targetDir := "/shared/netDb"

	// Create a tar archive with the directory and file
	tarBuffer := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuffer)
	// Include the directory in the tar header
	header := &tar.Header{
		Name: fmt.Sprintf("%s/%s", directory, filename), // Include directory
		Mode: 0600,
		Size: int64(len(routerInfoString)),
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("error writing tar header: %v", err)
	}
	if _, err := tw.Write([]byte(routerInfoString)); err != nil {
		return fmt.Errorf("error writing router info to tar: %v", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("error closing tar writer: %v", err)
	}

	// Copy the tar archive to the helper container
	err = cli.CopyToContainer(ctx, helperContainerID, targetDir, bytes.NewReader(tarBuffer.Bytes()), types.CopyToContainerOptions{})
	if err != nil {
		return fmt.Errorf("error copying router info to helper container: %v", err)
	}

	// Optionally, list the contents of the target directory to verify
	execConfig := types.ExecConfig{
		Cmd:          []string{"ls", "-lR", targetDir},
		AttachStdout: true,
		AttachStderr: true,
	}
	execIDResp, err := cli.ContainerExecCreate(ctx, helperContainerID, execConfig)
	if err != nil {
		return fmt.Errorf("error creating exec config for listing: %v", err)
	}
	respAttach, err := cli.ContainerExecAttach(ctx, execIDResp.ID, types.ExecStartCheck{})
	if err != nil {
		return fmt.Errorf("error attaching to exec: %v", err)
	}
	defer respAttach.Close()
	io.Copy(os.Stdout, respAttach.Reader)

	fmt.Printf("Successfully synced RouterInfo to %s\n", targetDir)
	// Now rsync
	fmt.Printf("Beginning rsync\n")

	rsyncCmd := []string{"rsync", "-avzP", "/shared/netDb/", I2PDDataDir + "/netDb/"}
	execOptions := container.ExecOptions{
		Cmd:          rsyncCmd,
		AttachStdout: true,
		AttachStderr: true,
	}
	execIDResp, err = cli.ContainerExecCreate(ctx, containerID, execOptions)
	if err != nil {
		return fmt.Errorf("error creating exec config for rsync: %v", err)
	}
	execResp, err := cli.ContainerExecAttach(ctx, execIDResp.ID, container.ExecStartOptions{})
	if err != nil {
		return fmt.Errorf("error attaching to exec for rsync: %v", err)
	}
	defer execResp.Close()
	io.Copy(os.Stdout, execResp.Reader)
	return nil
}
