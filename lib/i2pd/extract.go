package i2pd

import (
	"context"
	"time"

	"github.com/docker/docker/client"
	"github.com/go-i2p/go-i2p/lib/common/base64"
	"github.com/go-i2p/go-i2p/lib/common/router_info"
	"go-i2p-testnet/lib/docker_control"
)

// GetRouterInfoWithFilename extracts RouterInfo and returns it with the routerInfoString and filename
func GetRouterInfoWithFilename(cli *client.Client, ctx context.Context, containerID string) (*router_info.RouterInfo, string, string, error) {
	routerInfoString, err := docker_control.ReadFileFromContainer(cli, ctx, containerID, I2PDDataDir+"/router.info")
	if err != nil {
		return nil, "", "", err
	}
	ri, _, err := router_info.ReadRouterInfo([]byte(routerInfoString))
	if err != nil {
		return nil, "", "", err
	}
	identHash := ri.IdentHash()
	encodedHash := base64.EncodeToString(identHash[:])
	filename := "routerInfo-" + encodedHash + ".dat"
	return &ri, routerInfoString, filename, nil
}

// GetRouterInfoWithFilenameRaw extracts RouterInfo and returns the raw routerInfoString,
// the netDb filename and the netDb subdirectory. i2pd only writes router.info a few
// seconds after startup, so extraction is retried for a while before giving up.
func GetRouterInfoWithFilenameRaw(cli *client.Client, ctx context.Context, containerID string) (string, string, string, error) {
	var routerInfoString string
	var err error
	for attempt := 0; attempt < 15; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Second)
		}
		routerInfoString, err = docker_control.ReadFileFromContainerUnarchive(cli, ctx, containerID, I2PDDataDir+"/router.info")
		if err == nil {
			break
		}
		log.WithFields(map[string]interface{}{
			"containerID": containerID,
			"attempt":     attempt + 1,
		}).Debug("router.info not available yet, retrying")
	}
	if err != nil {
		return "", "", "", err
	}
	ri, _, err := router_info.ReadRouterInfo([]byte(routerInfoString))
	if err != nil {
		return "", "", "", err
	}
	identHash := ri.IdentHash()
	encodedHash := base64.EncodeToString(identHash[:])
	filename := "routerInfo-" + encodedHash + ".dat"
	directory := "r" + string(encodedHash[:1])
	return routerInfoString, filename, directory, err
}
