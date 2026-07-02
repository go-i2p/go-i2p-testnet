package monitor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"go-i2p-testnet/lib/utils/logger"
)

var log = logger.GetTestnetLogger()

// ANSI colors assigned round-robin to routers for side-by-side streaming
var palette = []string{
	"\x1b[32m", // green
	"\x1b[33m", // yellow
	"\x1b[34m", // blue
	"\x1b[35m", // magenta
	"\x1b[36m", // cyan
	"\x1b[31m", // red
}

// capture follows one container's log stream, writing every line to a
// per-router file and fanning it out to live subscribers.
type capture struct {
	containerID string
	name        string
	color       string
	cancel      context.CancelFunc
	file        *os.File
	done        chan struct{}
}

// subscriber receives colored, name-prefixed log lines. filter is a router
// name, or empty to receive lines from every router.
type subscriber struct {
	ch     chan string
	filter string
}

// LogManager captures docker logs of router containers to files under logDir
// and multiplexes them to live subscribers for side-by-side viewing.
type LogManager struct {
	cli       *client.Client
	logDir    string
	mu        sync.Mutex
	caps      map[string]*capture // keyed by containerID
	subs      map[*subscriber]struct{}
	nextColor int
}

func NewLogManager(cli *client.Client, logDir string) *LogManager {
	return &LogManager{
		cli:    cli,
		logDir: logDir,
		caps:   make(map[string]*capture),
		subs:   make(map[*subscriber]struct{}),
	}
}

// StartCapture begins following the container's logs. Each session truncates
// the router's log file so files always describe the current run.
func (m *LogManager) StartCapture(ctx context.Context, containerID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.caps[containerID]; exists {
		return nil
	}

	if err := os.MkdirAll(m.logDir, 0o755); err != nil {
		return fmt.Errorf("error creating log directory %s: %v", m.logDir, err)
	}
	logPath := filepath.Join(m.logDir, name+".log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("error opening log file %s: %v", logPath, err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	c := &capture{
		containerID: containerID,
		name:        name,
		color:       palette[m.nextColor%len(palette)],
		cancel:      cancel,
		file:        file,
		done:        make(chan struct{}),
	}
	m.nextColor++
	m.caps[containerID] = c

	go m.run(streamCtx, c)

	log.WithFields(map[string]interface{}{
		"name": name,
		"file": logPath,
	}).Debug("Started log capture")
	return nil
}

// run follows the container's log stream, re-attaching when it ends: the
// docker API closes the stream when the container restarts, but the
// container (and its log file) live on. Ends for good when the capture is
// cancelled or the container no longer exists.
func (m *LogManager) run(ctx context.Context, c *capture) {
	defer func() {
		c.file.Close()
		m.mu.Lock()
		delete(m.caps, c.containerID)
		m.mu.Unlock()
		close(c.done)
		log.WithField("name", c.name).Debug("Log capture ended")
	}()

	since := ""
	for {
		lastTS, err := m.follow(ctx, c, since)
		if err != nil {
			log.WithError(err).WithField("name", c.name).Debug("Log stream ended")
		}
		if ctx.Err() != nil {
			return
		}
		if _, err := m.cli.ContainerInspect(ctx, c.containerID); err != nil {
			return // container is gone
		}
		if lastTS != "" {
			since = lastTS
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// follow streams one incarnation of the container's logs, returning the
// timestamp of the last line seen so a re-attach can resume from there.
func (m *LogManager) follow(ctx context.Context, c *capture, since string) (string, error) {
	reader, err := m.cli.ContainerLogs(ctx, c.containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true,
		Since:      since,
	})
	if err != nil {
		return "", err
	}

	// Containers run without TTY, so the log stream is multiplexed:
	// demux with stdcopy into a pipe, then scan it line by line.
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, pw, reader)
		pw.CloseWithError(err)
		reader.Close()
	}()

	lastTS := ""
	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if ts, _, found := strings.Cut(line, " "); found {
			lastTS = ts
		}
		if _, err := c.file.WriteString(line + "\n"); err != nil {
			log.WithError(err).WithField("name", c.name).Error("Failed to write log line")
		}
		m.publish(c, line)
	}
	return lastTS, scanner.Err()
}

func (m *LogManager) publish(c *capture, line string) {
	pretty := fmt.Sprintf("%s[%-16s]\x1b[0m %s", c.color, c.name, line)
	m.mu.Lock()
	for s := range m.subs {
		if s.filter == "" || s.filter == c.name {
			select {
			case s.ch <- pretty:
			default: // drop lines rather than block on a slow consumer
			}
		}
	}
	m.mu.Unlock()
}

// Subscribe streams one router's log lines. The returned unsubscribe func
// must be called when done.
func (m *LogManager) Subscribe(name string) (<-chan string, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	found := false
	for _, c := range m.caps {
		if c.name == name {
			found = true
			break
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("no log capture for router %q (see Names)", name)
	}
	return m.subscribe(name), func() { m.unsubscribeByFilter(name) }, nil
}

// SubscribeAll streams every router's log lines interleaved.
func (m *LogManager) SubscribeAll() (<-chan string, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.subscribe(""), func() { m.unsubscribeByFilter("") }
}

// subscribe registers a subscriber; callers must hold m.mu.
func (m *LogManager) subscribe(filter string) chan string {
	s := &subscriber{
		ch:     make(chan string, 1000),
		filter: filter,
	}
	m.subs[s] = struct{}{}
	return s.ch
}

func (m *LogManager) unsubscribeByFilter(filter string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for s := range m.subs {
		if s.filter == filter {
			delete(m.subs, s)
		}
	}
}

// StopCapture stops following one container and waits for its file to close.
func (m *LogManager) StopCapture(containerID string) {
	m.mu.Lock()
	c, ok := m.caps[containerID]
	m.mu.Unlock()
	if !ok {
		return
	}
	c.cancel()
	<-c.done
}

// StopAll stops every capture and waits for all log files to be flushed.
func (m *LogManager) StopAll() {
	m.mu.Lock()
	caps := make([]*capture, 0, len(m.caps))
	for _, c := range m.caps {
		caps = append(caps, c)
	}
	m.mu.Unlock()

	for _, c := range caps {
		c.cancel()
	}
	for _, c := range caps {
		<-c.done
	}
}

// Names lists the routers currently being captured (for autocomplete).
func (m *LogManager) Names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.caps))
	for _, c := range m.caps {
		names = append(names, c.name)
	}
	sort.Strings(names)
	return names
}
