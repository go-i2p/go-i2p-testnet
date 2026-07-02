package i2pd

import (
	"bytes"
	"context"
	"fmt"
	"gopkg.in/ini.v1"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"go-i2p-testnet/lib/utils"
)

// I2PDDataDir is the i2pd data directory inside router containers.
// The per-router config volume is mounted here and the container runs
// i2pd with --datadir pointing at it, so netDb, router.info and keys
// all live on the volume.
const I2PDDataDir = "/var/lib/i2pd"

// I2PDConfig represents the i2pd configuration.
// Only options valid on i2pd >= 2.44 are included; i2pd exits fatally
// on unrecognized config keys.
type I2PDConfig struct {
	// Global options (before any section)
	TunnelsConf   string `ini:"tunconf"`
	TunnelsDir    string `ini:"tunnelsdir"`
	CertsDir      string `ini:"certsdir"`
	Pidfile       string `ini:"pidfile"`
	Log           string `ini:"log"`
	Logfile       string `ini:"logfile,omitempty"`
	Loglevel      string `ini:"loglevel"`
	Logclftime    bool   `ini:"logclftime"`
	Daemon        bool   `ini:"daemon"`
	Family        string `ini:"family,omitempty"`
	Ifname        string `ini:"ifname,omitempty"`
	Ifname4       string `ini:"ifname4,omitempty"`
	Ifname6       string `ini:"ifname6,omitempty"`
	Address4      string `ini:"address4,omitempty"`
	Address6      string `ini:"address6,omitempty"`
	Host          string `ini:"host,omitempty"`
	Port          int    `ini:"port"`
	IPv4          bool   `ini:"ipv4"`
	IPv6          bool   `ini:"ipv6"`
	Bandwidth     string `ini:"bandwidth"`
	Share         int    `ini:"share"`
	Notransit     bool   `ini:"notransit"`
	Floodfill     bool   `ini:"floodfill"`
	Service       bool   `ini:"service"`
	Datadir       string `ini:"datadir"`
	Netid         int    `ini:"netid"`
	Nat           bool   `ini:"nat"`
	ReservedRange bool   `ini:"reservedrange"`
	// Sections
	NTCP2   NTCP2Config   `ini:"ntcp2"`
	SSU2    SSU2Config    `ini:"ssu2"`
	HTTP    HTTPConfig    `ini:"http"`
	Reseed  ReseedConfig  `ini:"reseed"`
	Limits  LimitsConfig  `ini:"limits"`
	Persist PersistConfig `ini:"persist"`
	Nettime NettimeConfig `ini:"nettime"`
}

// NTCP2 Section
type NTCP2Config struct {
	Enabled   bool `ini:"enabled"`
	Published bool `ini:"published"`
	Port      int  `ini:"port"`
}

// SSU2 Section
type SSU2Config struct {
	Enabled   bool `ini:"enabled"`
	Published bool `ini:"published"`
	Port      int  `ini:"port"`
}

// HTTP Section
type HTTPConfig struct {
	Enabled bool   `ini:"enabled"`
	Address string `ini:"address"`
	Port    int    `ini:"port"`
	Webroot string `ini:"webroot"`
	Auth    bool   `ini:"auth"`
	User    string `ini:"user,omitempty"`
	Pass    string `ini:"pass,omitempty"`
	Lang    string `ini:"lang"`
}

// Reseed Section
type ReseedConfig struct {
	Verify    bool   `ini:"verify"`
	URLs      string `ini:"urls"`
	YggURLs   string `ini:"yggurls,omitempty"`
	File      string `ini:"file,omitempty"`
	ZipFile   string `ini:"zipfile,omitempty"`
	Proxy     string `ini:"proxy,omitempty"`
	Threshold int    `ini:"threshold"`
}

// Limits Section
type LimitsConfig struct {
	TransitTunnels int     `ini:"transittunnels"`
	OpenFiles      int     `ini:"openfiles"`
	CoreSize       int     `ini:"coresize"`
	Zombies        float64 `ini:"zombies"`
}

// Persist Section
type PersistConfig struct {
	Profiles    bool `ini:"profiles"`
	Addressbook bool `ini:"addressbook"`
}

// Nettime Section
type NettimeConfig struct {
	Enabled         bool   `ini:"enabled"`
	NtpServers      string `ini:"ntpservers"`
	NtpSyncInterval int    `ini:"ntpsyncinterval"`
}

func GenerateDefaultI2PDConfig() *I2PDConfig {
	return &I2PDConfig{
		// Global options (before any section)
		TunnelsConf: I2PDDataDir + "/tunnels.conf",
		TunnelsDir:  I2PDDataDir + "/tunnels.d/",
		CertsDir:    "/usr/share/i2pd/certificates",
		Pidfile:     "/run/i2pd.pid",
		Log:         "stdout",
		Logfile:     "",
		Loglevel:    "debug",
		Logclftime:  false,
		Daemon:      false,
		Family:      "",
		Host:        "",
		Port:        4567,
		IPv4:        true,
		IPv6:        false,
		Bandwidth:   "L",
		Share:       100,
		Notransit:   false,
		Floodfill:   true,
		Service:     false,
		Datadir:     I2PDDataDir,
		Netid:       2,
		Nat:         true,

		// NTCP2 section
		NTCP2: NTCP2Config{
			Enabled:   true,
			Published: true,
			Port:      4567,
		},

		// SSU2 section
		SSU2: SSU2Config{
			Enabled:   true,
			Published: true,
			Port:      4567,
		},

		// HTTP section (webconsole)
		HTTP: HTTPConfig{
			Enabled: true,
			Address: "127.0.0.1",
			Port:    7070,
			Webroot: "/",
			Auth:    false,
			Lang:    "english",
		},

		// Reseed section
		Reseed: ReseedConfig{
			Verify:    true,
			URLs:      "https://reseed.i2p-projekt.de/,https://i2p.mooo.com/netDb/,https://netdb.i2p2.no/",
			Threshold: 25,
		},

		// Limits section
		Limits: LimitsConfig{
			TransitTunnels: 10000,
			OpenFiles:      0,
			CoreSize:       0,
			Zombies:        0.00,
		},

		// Persist section
		Persist: PersistConfig{
			Profiles:    true,
			Addressbook: true,
		},

		// Nettime section
		Nettime: NettimeConfig{
			Enabled:         false,
			NtpServers:      "pool.ntp.org",
			NtpSyncInterval: 72,
		},
	}
}

func GenerateRouterConfig(routerID int, ip string) (string, error) {
	log.WithFields(map[string]interface{}{
		"routerID": routerID,
		"ip":       ip,
	}).Debug("Starting i2pd router config generation")

	// Initialize default configuration and adjust it for the isolated testnet
	config := GenerateDefaultI2PDConfig()
	config.Netid = 5
	config.ReservedRange = false
	config.Nat = false
	config.Floodfill = true
	// Publish the container IP so peers can reach this router
	config.Host = ip
	// The testnet network is internal-only: never attempt clearnet reseed
	config.Reseed = ReseedConfig{
		Verify:    false,
		URLs:      "",
		Threshold: 0,
	}
	config.Persist.Profiles = false

	// Create an INI file from the struct
	iniFile := ini.Empty()
	err := iniFile.ReflectFrom(config)
	if err != nil {
		log.WithError(err).Error("Failed to reflect config struct to INI file")
		return "", err
	}

	// Write INI file to a string
	var buffer bytes.Buffer
	_, err = iniFile.WriteTo(&buffer)
	if err != nil {
		log.WithError(err).Error("Failed to write INI file to buffer")
		return "", err
	}

	configData := buffer.String()

	log.WithFields(map[string]interface{}{
		"routerID": routerID,
		"config":   configData,
	}).Debug("i2pd router configuration generated successfully")

	return configData, nil
}

func CopyConfigToVolume(cli *client.Client, ctx context.Context, volumeName string, configData string) error {
	// Create a temporary container to copy data into the volume
	log.WithField("volumeName", volumeName).Debug("Starting config copy to volume")

	tempContainerConfig := &container.Config{
		Image:      "alpine",
		Tty:        false,
		WorkingDir: I2PDDataDir,
		Cmd:        []string{"sh", "-c", "mkdir -p " + I2PDDataDir + " && sleep 1d"},
	}

	hostConfig := &container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:%s", volumeName, I2PDDataDir),
		},
	}

	log.WithFields(map[string]interface{}{
		"image":      tempContainerConfig.Image,
		"volumeName": volumeName,
	}).Debug("Creating temporary container")

	resp, err := cli.ContainerCreate(ctx, tempContainerConfig, hostConfig, nil, nil, "")
	if err != nil {
		log.WithError(err).Error("Failed to create temporary container")
		return fmt.Errorf("error creating temporary container: %v", err)
	}
	defer func() {
		log.WithField("containerID", resp.ID).Debug("Removing temporary container")
		removeOptions := container.RemoveOptions{Force: true}
		err := cli.ContainerRemove(ctx, resp.ID, removeOptions)
		if err != nil {
			log.WithError(err).Error("Failed to remove temporary container")
		}
	}()

	// Start the container
	log.WithField("containerID", resp.ID).Debug("Starting temporary container")
	startOptions := container.StartOptions{}
	if err := cli.ContainerStart(ctx, resp.ID, startOptions); err != nil {
		log.WithError(err).Error("Failed to start temporary container")
		return fmt.Errorf("error starting temporary container: %v", err)
	}

	// Copy the configuration file into the container
	log.Debug("Creating tar archive of config data")
	tarReader, err := utils.CreateTarArchive("i2pd.conf", configData)
	if err != nil {
		log.WithError(err).Error("Failed to create tar archive")
		return fmt.Errorf("error creating tar archive: %v", err)
	}

	// Copy to the container's volume-mounted directory
	log.WithField("containerID", resp.ID).Debug("Copying config to container")
	err = cli.CopyToContainer(ctx, resp.ID, I2PDDataDir, tarReader, container.CopyToContainerOptions{})
	if err != nil {
		log.WithError(err).Error("Failed to copy config to container")
		return fmt.Errorf("error copying to container: %v", err)
	}

	// Stop the container
	log.WithField("containerID", resp.ID).Debug("Stopping temporary container")
	stopOptions := container.StopOptions{}
	if err := cli.ContainerStop(ctx, resp.ID, stopOptions); err != nil {
		log.WithError(err).Error("Failed to stop temporary container")
		return fmt.Errorf("error stopping temporary container: %v", err)
	}

	log.Debug("Successfully copied config to volume")
	return nil
}
