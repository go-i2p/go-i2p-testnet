package docker_control

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// ExecInContainer runs cmd inside the container, waits for it to finish and
// returns its combined stdout/stderr. A non-zero exit code is returned as an error.
func ExecInContainer(cli *client.Client, ctx context.Context, containerID string, cmd []string) (string, error) {
	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("error creating exec: %v", err)
	}

	attach, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecStartOptions{})
	if err != nil {
		return "", fmt.Errorf("error attaching to exec: %v", err)
	}
	defer attach.Close()

	var output bytes.Buffer
	if _, err := stdcopy.StdCopy(&output, &output, attach.Reader); err != nil {
		return "", fmt.Errorf("error reading exec output: %v", err)
	}

	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return output.String(), fmt.Errorf("error inspecting exec: %v", err)
	}
	if inspect.ExitCode != 0 {
		return output.String(), fmt.Errorf("command %v exited with code %d: %s",
			cmd, inspect.ExitCode, strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}
