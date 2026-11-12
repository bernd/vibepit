package container

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/tui"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/network"
	dockerclient "github.com/moby/moby/client"
	"golang.org/x/term"
)

const (
	LabelVibepit         = "vibepit"
	LabelRole            = "vibepit.role"
	LabelUID             = "vibepit.uid"
	LabelUser            = "vibepit.user"
	LabelVolumeHome      = "vibepit.volume.home"
	LabelVolumeLinuxbrew = "vibepit.volume.linuxbrew"
	LabelProjectDir      = "vibepit.project.dir"
	LabelSessionID       = "vibepit.session-id"

	RoleProxy   = "proxy"
	RoleSandbox = "sandbox"

	ProxyBinaryPath    = "/vibepit"
	SandboxBinaryPath  = "/vibed"
	ProxyConfigPath    = "/config.json"
	HomeMountPath      = "/home/code"
	LinuxbrewMountPath = "/home/linuxbrew"
	ContainerHostname  = "vibes"

	ProxyImage       = "gcr.io/distroless/base-debian13:latest"
	LabelControlPort = "vibepit.control-port"

	SSHContainerPort = "2222/tcp"
	SSHHostKeyPath   = "/etc/vibepit/sshd/host-key"
	SSHHostPubPath   = "/etc/vibepit/sshd/host-key.pub"
	SSHPubKeyEnv     = "VIBEPIT_SSH_PUBKEY"
	SessionStatePath = "/tmp/vibed-sessions.json"
)

const (
	// pingTimeout bounds each daemon probe, so an unreachable daemon does
	// not hang every command until the OS gives up on the connection.
	pingTimeout = 10 * time.Second
	// removeTimeout bounds cleanup that must outlive a cancelled context. It
	// is short because Ctrl-C cannot interrupt it.
	removeTimeout = 5 * time.Second
)

// Variables so tests can shorten them.
var (
	// execExitTimeout bounds the wait for the daemon to record an exec's
	// exit after its stream ended.
	execExitTimeout = 5 * time.Second
	execExitPoll    = 50 * time.Millisecond
	// createSettleTimeout bounds the wait for a container whose create call
	// was interrupted to appear, since the daemon may still be creating it.
	createSettleTimeout = 2 * time.Second
	createSettlePoll    = 100 * time.Millisecond
)

var (
	// sshContainerPort is SSHContainerPort as a network.Port.
	sshContainerPort, _ = network.PortFrom(2222, network.TCP)
	loopback            = netip.AddrFrom4([4]byte{127, 0, 0, 1})
)

// Client wraps the Docker/Podman API, trying Docker first then falling back
// to the Podman-compatible socket.
type Client struct {
	docker *dockerclient.Client
	debug  bool
}

type ClientOpt func(*Client) error

func WithDebug(debug bool) ClientOpt {
	return func(c *Client) error {
		c.debug = debug
		return nil
	}
}

func NewClient(opts ...ClientOpt) (*Client, error) {
	client := &Client{}

	for _, opt := range opts {
		if err := opt(client); err != nil {
			return nil, fmt.Errorf("applying client option: %w", err)
		}
	}

	// First try the regular Docker environment chain. If DOCKER_HOST names a
	// daemon, or the DOCKER_* settings are invalid, any failure is an error:
	// falling back would silently use a different runtime or different
	// settings than the ones the user configured.
	cli, defaultErr := pingedClient(client.debug, dockerclient.FromEnv)
	if defaultErr == nil {
		client.docker = cli
		return client, nil
	}
	if host := os.Getenv(dockerclient.EnvOverrideHost); host != "" {
		return nil, fmt.Errorf("%s=%s (unset it to use a local socket): %w", dockerclient.EnvOverrideHost, host, defaultErr)
	}
	if errors.Is(defaultErr, errClientConfig) {
		return nil, defaultErr
	}
	if client.debug {
		tui.Debug("Could not use default Docker host: %v", defaultErr)
	}

	// Then fall back to manual socket detection.
	u, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("cannot determine current user: %w", err)
	}

	detectedCli, err := findSocket(
		client.debug,
		// Used on macOS with Docker Desktop
		fmt.Sprintf("unix://%s/.docker/run/docker.sock", u.HomeDir),
		// Rootless Podman
		fmt.Sprintf("unix:///run/user/%s/podman/podman.sock", u.Uid),
	)
	if err != nil {
		// Report the default host too: its error (e.g. permission denied on
		// /var/run/docker.sock) is often the one that explains the failure.
		return nil, fmt.Errorf("couldn't find a Docker socket: %w", errors.Join(defaultErr, err))
	}

	client.docker = detectedCli
	return client, nil
}

func displayDockerHost(debug bool, cli *dockerclient.Client) {
	if !debug {
		return
	}
	host := cli.DaemonHost()
	if path, ok := strings.CutPrefix(host, "unix://"); ok {
		if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
			host = fmt.Sprintf("unix://%s -> %s", path, resolved)
		}
	}
	tui.Debug("Using container daemon at %s", host)
}

// findSocket returns a client for the first socket whose daemon is usable.
// Every socket is tried, so one stale or unsupported daemon does not hide a
// working one. The error lists why each socket failed.
func findSocket(debug bool, paths ...string) (*dockerclient.Client, error) {
	var errs []error
	for _, path := range paths {
		if debug {
			tui.Debug("Trying socket: %s", path)
		}
		cli, err := pingedClient(debug, dockerclient.WithHost(path))
		if err == nil {
			return cli, nil
		}
		if debug {
			tui.Debug("Could not use socket: %v", err)
		}
		errs = append(errs, err)
	}

	return nil, errors.Join(errs...)
}

// errClientConfig marks a client that could not be created from its
// options, as opposed to a daemon that could not be reached.
var errClientConfig = errors.New("invalid Docker client configuration")

// pingedClient returns a client for opts whose daemon answered a ping. The
// ping negotiates the API version, so an unsupported daemon fails here
// rather than on the first real call.
func pingedClient(debug bool, opts ...dockerclient.Opt) (*dockerclient.Client, error) {
	cli, err := dockerclient.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errClientConfig, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	if _, err := cli.Ping(ctx, dockerclient.PingOptions{NegotiateAPIVersion: true}); err != nil {
		host := cli.DaemonHost()
		_ = cli.Close()
		return nil, fmt.Errorf("%s: %w", host, err)
	}
	displayDockerHost(debug, cli)
	return cli, nil
}

func (c *Client) Close() error { return c.docker.Close() }

// EnsureImage pulls the image if it is not available locally. Returns true
// if the image was pulled, false if it was already present.
func (c *Client) EnsureImage(ctx context.Context, ref string) (bool, error) {
	images, err := c.docker.ImageList(ctx, dockerclient.ImageListOptions{
		Filters: make(dockerclient.Filters).Add("reference", ref),
	})
	if err != nil {
		return false, fmt.Errorf("list images: %w", err)
	}
	if len(images.Items) > 0 {
		return false, nil
	}

	return true, c.PullImage(ctx, ref)
}

// PullImage pulls the latest version of the image.
func (c *Client) PullImage(ctx context.Context, ref string) error {
	tui.Status("Pulling", "image %s", ref)
	reader, err := c.docker.ImagePull(ctx, ref, dockerclient.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	defer reader.Close()

	if err := displayPullProgress(reader.JSONMessages(ctx)); err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	return nil
}

// displayPullProgress consumes Docker pull JSON messages and shows a single
// updating status line with aggregated download progress.
func displayPullProgress(msgs iter.Seq2[jsonstream.Message, error]) error {
	isTerminal := term.IsTerminal(int(os.Stdout.Fd()))

	type layerProgress struct {
		current int64
		total   int64
	}
	layers := map[string]*layerProgress{}
	wroteProgress := false
	// fail clears the progress line so the caller's error starts on a clean line.
	fail := func(err error) error {
		if wroteProgress {
			fmt.Fprint(os.Stdout, "\r\033[K")
		}
		return err
	}

	for msg, err := range msgs {
		if err != nil {
			return fail(err)
		}
		if msg.Error != nil {
			return fail(msg.Error)
		}

		if msg.ID == "" || msg.Progress == nil {
			continue
		}

		lp, ok := layers[msg.ID]
		if !ok {
			lp = &layerProgress{}
			layers[msg.ID] = lp
		}
		lp.current = msg.Progress.Current
		lp.total = msg.Progress.Total

		if !isTerminal {
			continue
		}

		var totalBytes, currentBytes int64
		for _, l := range layers {
			currentBytes += l.current
			totalBytes += l.total
		}

		if currentBytes == 0 && totalBytes == 0 {
			continue
		}

		var line string
		if totalBytes > 0 {
			pct := min(int(float64(currentBytes)/float64(totalBytes)*100), 100)
			line = fmt.Sprintf("             %s / %s, %d%%",
				humanBytes(currentBytes), humanBytes(totalBytes), pct)
		} else {
			line = fmt.Sprintf("             %s",
				humanBytes(currentBytes))
		}
		fmt.Fprintf(os.Stdout, "\r\033[K%s", line)
		wroteProgress = true
	}
	if isTerminal && wroteProgress {
		fmt.Fprintf(os.Stdout, "\r\033[K             done\n")
	}
	return nil
}

func humanBytes(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// RunningSession describes a running sandbox container found by FindRunningSession.
type RunningSession struct {
	ContainerID string
	SessionID   string
	ProjectDir  string
}

// FindRunningSession returns a running sandbox container for the given project
// directory, or nil if none is found.
func (c *Client) FindRunningSession(ctx context.Context, projectDir string) (*RunningSession, error) {
	list, err := c.docker.ContainerList(ctx, dockerclient.ContainerListOptions{
		Filters: make(dockerclient.Filters).Add("label",
			fmt.Sprintf("%s=%s", LabelProjectDir, projectDir),
			LabelRole+"="+RoleSandbox,
		),
	})
	if err != nil {
		return nil, err
	}
	if containers := list.Items; len(containers) > 0 {
		return &RunningSession{
			ContainerID: containers[0].ID,
			SessionID:   containers[0].Labels[LabelSessionID],
			ProjectDir:  projectDir,
		}, nil
	}
	return nil, nil
}

// FindAnySessionContainer returns the session ID for any container (sandbox or
// proxy) matching the given project directory. This is useful for cleanup when
// the sandbox may have crashed but the proxy is still running.
func (c *Client) FindAnySessionContainer(ctx context.Context, projectDir string) (string, error) {
	list, err := c.docker.ContainerList(ctx, dockerclient.ContainerListOptions{
		All: true,
		Filters: make(dockerclient.Filters).Add("label",
			fmt.Sprintf("%s=%s", LabelProjectDir, projectDir),
			LabelSessionID,
		),
	})
	if err != nil {
		return "", err
	}
	if containers := list.Items; len(containers) > 0 {
		return containers[0].Labels[LabelSessionID], nil
	}
	return "", nil
}

// SessionContainer describes a container belonging to a session.
type SessionContainer struct {
	ID   string
	Name string
	Role string // "proxy" or "sandbox"
}

// SessionContainers returns all containers for a session with their roles.
func (c *Client) SessionContainers(ctx context.Context, sessionID string) ([]SessionContainer, error) {
	list, err := c.docker.ContainerList(ctx, dockerclient.ContainerListOptions{
		All:     true,
		Filters: make(dockerclient.Filters).Add("label", fmt.Sprintf("%s=%s", LabelSessionID, sessionID)),
	})
	if err != nil {
		return nil, err
	}
	var result []SessionContainer
	for _, ctr := range list.Items {
		name := ""
		if len(ctr.Names) > 0 {
			name = strings.TrimPrefix(ctr.Names[0], "/")
		}
		result = append(result, SessionContainer{
			ID:   ctr.ID,
			Name: name,
			Role: ctr.Labels[LabelRole],
		})
	}
	return result, nil
}

// FindProxyContainerID returns the container ID of the proxy for a session.
func (c *Client) FindProxyContainerID(ctx context.Context, sessionID string) (string, error) {
	list, err := c.docker.ContainerList(ctx, dockerclient.ContainerListOptions{
		All: true,
		Filters: make(dockerclient.Filters).Add("label",
			fmt.Sprintf("%s=%s", LabelSessionID, sessionID),
			fmt.Sprintf("%s=%s", LabelRole, RoleProxy),
		),
	})
	if err != nil {
		return "", err
	}
	if len(list.Items) == 0 {
		return "", fmt.Errorf("no proxy container found for session %s", sessionID)
	}
	return list.Items[0].ID, nil
}

// AttachAndStartSession connects to a container's main process stdio, then starts it.
// This ensures startup output from the entrypoint is visible to the attached
// client. When the user exits the shell, the container's entrypoint exits and
// the container stops on its own. Returns an *ExitError if the container exits
// with a non-zero status code.
func (c *Client) AttachAndStartSession(ctx context.Context, containerID string, hooks TerminalHooks) error {
	resp, err := c.docker.ContainerAttach(ctx, containerID, dockerclient.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	defer resp.Close()

	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	// Register wait before start to avoid missing fast exits between start and wait.
	// Use next-exit so created containers do not return immediately.
	wait := c.docker.ContainerWait(waitCtx, containerID, dockerclient.ContainerWaitOptions{
		Condition: container.WaitConditionNextExit,
	})

	if _, err := c.docker.ContainerStart(ctx, containerID, dockerclient.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start attached container: %w", err)
	}

	resizeFn := func(height, width uint) {
		c.docker.ContainerResize(ctx, containerID, dockerclient.ContainerResizeOptions{
			Height: height, Width: width,
		})
	}

	if err := runTTYSession(ctx, resp.HijackedResponse, resizeFn, hooks); err != nil {
		return err
	}

	// Retrieve the container's exit code.
	select {
	case result := <-wait.Result:
		if result.StatusCode != 0 {
			return &ExitError{Code: int(result.StatusCode)}
		}
		return nil
	case err := <-wait.Error:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ExecSession starts a new interactive shell inside a running container.
// Used when reattaching to an existing session. Returns an *ExitError if
// the shell exits with a non-zero status code.
func (c *Client) ExecSession(ctx context.Context, containerID string, hooks TerminalHooks) error {
	size := terminalSize()

	execResp, err := c.docker.ExecCreate(ctx, containerID, dockerclient.ExecCreateOptions{
		TTY:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"/bin/bash", "--login"},
		ConsoleSize:  size,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}

	hijack, err := c.docker.ExecAttach(ctx, execResp.ID, dockerclient.ExecAttachOptions{
		TTY:         true,
		ConsoleSize: size,
	})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	defer hijack.Close()

	resizeFn := func(height, width uint) {
		c.docker.ExecResize(ctx, execResp.ID, dockerclient.ExecResizeOptions{
			Height: height, Width: width,
		})
	}

	if err := runTTYSession(ctx, hijack.HijackedResponse, resizeFn, hooks); err != nil {
		return err
	}

	code, err := c.waitExecExit(ctx, execResp.ID)
	if err != nil {
		return err
	}
	if code != 0 {
		return &ExitError{Code: code}
	}
	return nil
}

var (
	// errShellStillRunning is returned when the shell had not exited by the
	// end of the wait after its session stream ended, e.g. because the
	// connection to the daemon dropped or the daemon is slow to record it.
	errShellStillRunning = errors.New("the shell had not exited when its session ended; it may still be running in the sandbox")
	// errExitStatusUnavailable is returned when the exec is gone before its
	// exit was read, e.g. because the sandbox was removed meanwhile or the
	// runtime drops finished execs. Reporting success would be a guess.
	errExitStatusUnavailable = errors.New("the shell's exit status is unavailable")
)

// waitExecExit returns the exit code of an exec whose stream has ended. The
// stream can end before the daemon records the exit; until then the exec
// still reads as running with exit code 0, so poll until it has stopped.
func (c *Client) waitExecExit(ctx context.Context, execID string) (int, error) {
	waitCtx, cancel := context.WithTimeout(ctx, execExitTimeout)
	defer cancel()
	var code int
	err := poll(waitCtx, execExitPoll, func(ctx context.Context) (bool, error) {
		inspect, err := c.docker.ExecInspect(ctx, execID, dockerclient.ExecInspectOptions{})
		switch {
		case err == nil && !inspect.Running:
			code = inspect.ExitCode
			return true, nil
		case cerrdefs.IsNotFound(err):
			return true, errExitStatusUnavailable
		}
		return false, err
	})
	switch {
	case err == nil:
		return code, nil
	case errors.Is(err, errExitStatusUnavailable):
		return 0, err
	case ctx.Err() != nil:
		return 0, ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		return 0, errShellStillRunning
	default:
		return 0, fmt.Errorf("exec inspect: %w", err)
	}
}

// poll calls check every interval until it reports done, then returns the
// error check returned with it. An error check returns while not done is
// treated as transient and retried. When ctx ends first, poll returns the
// error of the last check, or ctx.Err() if that check had none.
func poll(ctx context.Context, interval time.Duration, check func(context.Context) (done bool, err error)) error {
	var lastErr error
	for {
		done, err := check(ctx)
		if done {
			return err
		}
		if ctx.Err() == nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return lastErr
			}
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// NetworkInfo is returned by CreateNetwork with the Docker-assigned addresses.
type NetworkInfo struct {
	ID        string
	ProxyIP   string
	SandboxIP string
}

// CreateNetwork creates an internal Docker network with a random /24 subnet
// and derives a static IP for the proxy (gateway + 1). The subnet is
// explicitly specified so that Docker allows static IP assignment.
func (c *Client) CreateNetwork(ctx context.Context, name string) (NetworkInfo, error) {
	subnet, gateway, err := randomSubnet()
	if err != nil {
		return NetworkInfo{}, fmt.Errorf("generate subnet: %w", err)
	}

	resp, err := c.docker.NetworkCreate(ctx, name, dockerclient.NetworkCreateOptions{
		Internal: true,
		Labels:   map[string]string{LabelVibepit: "true"},
		IPAM: &network.IPAM{
			Config: []network.IPAMConfig{
				{
					Subnet:  subnet,
					Gateway: gateway,
				},
			},
		},
	})
	if err != nil {
		return NetworkInfo{}, fmt.Errorf("create network: %w", err)
	}

	proxyIP := gateway.Next()
	sandboxIP := proxyIP.Next()
	return NetworkInfo{
		ID:        resp.ID,
		ProxyIP:   proxyIP.String(),
		SandboxIP: sandboxIP.String(),
	}, nil
}

// randomSubnet generates a random /24 subnet in the 10.x.x.0/8 range,
// returning the prefix and gateway IP (x.x.x.1). There is a small
// chance (~1/65k) of colliding with an existing Docker network, in which
// case network creation will fail with a "pool overlaps" error.
func randomSubnet() (netip.Prefix, netip.Addr, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return netip.Prefix{}, netip.Addr{}, err
	}
	subnet := netip.PrefixFrom(netip.AddrFrom4([4]byte{10, b[0], b[1], 0}), 24)
	gateway := netip.AddrFrom4([4]byte{10, b[0], b[1], 1})
	return subnet, gateway, nil
}

func (c *Client) RemoveNetwork(ctx context.Context, networkID string) error {
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	_, err := c.docker.NetworkRemove(ctx, networkID, dockerclient.NetworkRemoveOptions{})
	return err
}

// removeAfterFailedCreate removes the container named name if a create
// call failed after the daemon had already created it, as when the request
// is interrupted in flight. The caller never got an ID, so nothing else
// would remove it. A container is only removed if it carries this session's
// ID, so a name conflict with another container never removes that one.
// It returns createErr, joined with any removal error or a note that the
// lookup failed.
func (c *Client) removeAfterFailedCreate(ctx context.Context, name, sessionID string, createErr error) error {
	if name == "" || sessionID == "" || daemonAnswered(createErr) {
		return createErr
	}
	// The daemon may still be creating the container, so keep looking for a
	// moment. The lookup must outlive a cancelled ctx, as that is the usual
	// cause of the failure.
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), createSettleTimeout)
	defer cancel()
	var found container.InspectResponse
	seenNotFound := false
	err := poll(lookupCtx, createSettlePoll, func(ctx context.Context) (bool, error) {
		info, err := c.docker.ContainerInspect(ctx, name, dockerclient.ContainerInspectOptions{})
		switch {
		case err == nil:
			found = info.Container
			return true, nil
		case cerrdefs.IsNotFound(err):
			seenNotFound = true
			return false, nil
		}
		return false, err
	})
	switch {
	case err == nil:
	case seenNotFound && errors.Is(err, context.DeadlineExceeded):
		// The last lookups found nothing: it was not created.
		return createErr
	default:
		return errors.Join(createErr, fmt.Errorf("could not check whether container %s was left behind: %w", name, err))
	}
	if containerLabels(found)[LabelSessionID] != sessionID {
		return createErr
	}
	if err := c.removeContainer(ctx, found.ID); err != nil {
		return errors.Join(createErr, fmt.Errorf("remove container %s: %w", name, err))
	}
	return createErr
}

// daemonAnswered reports whether err carries the daemon's HTTP response to a
// create request. The daemon then finished handling it without creating the
// container. Any other error, such as a cancellation, a timeout or a dropped
// connection, leaves open whether the container was created.
//
// The client classifies HTTP error responses into the categories below. 503
// and the statuses it leaves unclassified (e.g. 502 and 504) are not counted:
// a proxy in front of the daemon may return them after the daemon created
// the container.
func daemonAnswered(err error) bool {
	for _, is := range []func(error) bool{
		cerrdefs.IsInvalidArgument, cerrdefs.IsNotFound, cerrdefs.IsAlreadyExists,
		cerrdefs.IsPermissionDenied, cerrdefs.IsResourceExhausted, cerrdefs.IsFailedPrecondition,
		cerrdefs.IsConflict, cerrdefs.IsNotModified, cerrdefs.IsAborted, cerrdefs.IsOutOfRange,
		cerrdefs.IsNotImplemented, cerrdefs.IsInternal, cerrdefs.IsDataLoss,
		cerrdefs.IsUnauthorized,
	} {
		if is(err) {
			return true
		}
	}
	return false
}

// removeContainer force-removes a container on a context that survives
// cancellation of ctx.
func (c *Client) removeContainer(ctx context.Context, containerID string) error {
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	_, err := c.docker.ContainerRemove(ctx, containerID, dockerclient.ContainerRemoveOptions{Force: true})
	return err
}

// cleanupContext returns a context for removing resources that still works
// after ctx is cancelled (e.g. by Ctrl-C), bounded by removeTimeout.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
}

// ProxyContainerConfig holds the parameters for starting the in-network proxy.
type ProxyContainerConfig struct {
	BinaryPath     string
	ConfigPath     string
	NetworkID      string
	ProxyIP        string
	ControlAPIPort int
	Name           string
	SessionID      string
	TLSKeyPEM      string
	TLSCertPEM     string
	CACertPEM      string
	ProjectDir     string
	NoRestart      bool // when true, omits the restart policy so the proxy stops with the session
	SSHPort        int  // when > 0, publish this port for SSH forwarding to sandbox
	ExtraHosts     []string
}

// StartProxyContainer creates and starts a minimal container that runs the
// vibepit proxy binary, then connects it to the bridge network so it can
// reach the internet. The control API port is published to 127.0.0.1 on the
// same host port. Returns the container ID and that port.
func (c *Client) StartProxyContainer(ctx context.Context, cfg ProxyContainerConfig) (string, string, error) {
	if cfg.ControlAPIPort < 1 || cfg.ControlAPIPort > 65535 {
		return "", "", fmt.Errorf("invalid control API port %d", cfg.ControlAPIPort)
	}
	containerPort, _ := network.PortFrom(uint16(cfg.ControlAPIPort), network.TCP)
	proxyIP, err := parseIPv4(cfg.ProxyIP)
	if err != nil {
		return "", "", fmt.Errorf("parse proxy IP: %w", err)
	}

	var env []string
	if cfg.TLSKeyPEM != "" {
		env = append(env,
			"VIBEPIT_PROXY_TLS_KEY="+cfg.TLSKeyPEM,
			"VIBEPIT_PROXY_TLS_CERT="+cfg.TLSCertPEM,
			"VIBEPIT_PROXY_CA_CERT="+cfg.CACertPEM,
		)
	}

	portStr := strconv.Itoa(cfg.ControlAPIPort)

	labels := map[string]string{
		LabelVibepit:     "true",
		LabelRole:        RoleProxy,
		LabelProjectDir:  cfg.ProjectDir,
		LabelControlPort: portStr,
	}
	if cfg.SessionID != "" {
		labels[LabelSessionID] = cfg.SessionID
	}

	exposedPorts := network.PortSet{
		containerPort: struct{}{},
	}
	portBindings := network.PortMap{
		containerPort: []network.PortBinding{
			{HostIP: loopback, HostPort: portStr},
		},
	}
	if cfg.SSHPort > 0 {
		exposedPorts[sshContainerPort] = struct{}{}
		portBindings[sshContainerPort] = []network.PortBinding{{HostIP: loopback}}
	}

	hostConfig := &container.HostConfig{
		// Use bridge as the primary network so HostIP/HostPort publishing is
		// actually activated by the runtime.
		NetworkMode: "bridge",
		Binds: []string{
			cfg.BinaryPath + ":" + ProxyBinaryPath + ":ro",
			cfg.ConfigPath + ":" + ProxyConfigPath + ":ro",
		},
		ExtraHosts:   []string{"host-gateway:host-gateway"},
		PortBindings: portBindings,
	}
	if len(cfg.ExtraHosts) > 0 {
		hostConfig.ExtraHosts = append(hostConfig.ExtraHosts, cfg.ExtraHosts...)
	}
	if !cfg.NoRestart {
		hostConfig.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}
	}

	resp, err := c.docker.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config: &container.Config{
			Image:        ProxyImage,
			Cmd:          []string{ProxyBinaryPath, "proxy", "--config", ProxyConfigPath},
			Labels:       labels,
			Env:          env,
			WorkingDir:   "/",
			ExposedPorts: exposedPorts,
		},
		HostConfig: hostConfig,
		Name:       cfg.Name,
	})
	if err != nil {
		return "", "", c.removeAfterFailedCreate(ctx, cfg.Name, cfg.SessionID, fmt.Errorf("create proxy container: %w", err))
	}
	// The caller only cleans up a container it got an ID for, so remove it
	// here if it cannot be started.
	fail := func(err error) (string, string, error) {
		if rmErr := c.removeContainer(ctx, resp.ID); rmErr != nil {
			err = errors.Join(err, fmt.Errorf("remove proxy container: %w", rmErr))
		}
		return "", "", err
	}
	// Attach to the isolated vibepit network with the fixed proxy IP so
	// sandbox containers can use it as DNS/HTTP proxy endpoint.
	if _, err := c.docker.NetworkConnect(ctx, cfg.NetworkID, dockerclient.NetworkConnectOptions{
		Container: resp.ID,
		EndpointConfig: &network.EndpointSettings{
			IPAMConfig: &network.EndpointIPAMConfig{
				IPv4Address: proxyIP,
			},
		},
	}); err != nil {
		return fail(fmt.Errorf("connect proxy to session network: %w", err))
	}
	if _, err := c.docker.ContainerStart(ctx, resp.ID, dockerclient.ContainerStartOptions{}); err != nil {
		return fail(fmt.Errorf("start proxy container: %w", err))
	}

	return resp.ID, portStr, nil
}

// parseIPv4 parses s as an IPv4 address. The session network is IPv4-only
// and the addresses end up in IPv4Address fields.
func parseIPv4(s string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, err
	}
	if !addr.Is4() {
		return netip.Addr{}, fmt.Errorf("%s is not an IPv4 address", s)
	}
	return addr, nil
}

// ProxySession describes a running proxy for session discovery.
type ProxySession struct {
	ContainerID string
	SessionID   string
	ControlPort string
	ProjectDir  string
	StartedAt   time.Time
}

// ListProxySessions returns all running vibepit proxy containers with their
// session metadata. ControlPort is the host port published for the control
// port label, or the label value itself if no binding is found.
func (c *Client) ListProxySessions(ctx context.Context) ([]ProxySession, error) {
	list, err := c.docker.ContainerList(ctx, dockerclient.ContainerListOptions{
		Filters: make(dockerclient.Filters).Add("label",
			LabelVibepit+"=true",
			LabelRole+"="+RoleProxy,
		),
	})
	if err != nil {
		return nil, err
	}

	var sessions []ProxySession
	for _, ctr := range list.Items {
		sessions = append(sessions, ProxySession{
			ContainerID: ctr.ID,
			SessionID:   ctr.Labels[LabelSessionID],
			ControlPort: controlPortOf(ctr),
			ProjectDir:  ctr.Labels[LabelProjectDir],
			StartedAt:   time.Unix(ctr.Created, 0),
		})
	}
	return sessions, nil
}

// controlPortOf returns the host port published for a proxy's control port
// label, read from the list result so no extra inspect call is needed. It
// falls back to the label value if the port is not published.
func controlPortOf(ctr container.Summary) string {
	label := ctr.Labels[LabelControlPort]
	for _, p := range ctr.Ports {
		if p.Type == "tcp" && p.PublicPort != 0 && strconv.Itoa(int(p.PrivatePort)) == label {
			return strconv.Itoa(int(p.PublicPort))
		}
	}
	return label
}

// SandboxContainerConfig holds the parameters for the sandboxed sandbox container.
type SandboxContainerConfig struct {
	Image               string
	ProjectDir          string
	WorkDir             string
	RuntimeDir          string
	HomeVolumeName      string
	LinuxbrewVolumeName string
	NetworkID           string
	ProxyIP             string
	SandboxIP           string // static IP on the session network (for SSH forwarding)
	ProxyPort           int
	Name                string
	Term                string
	ColorTerm           string
	UID                 int
	User                string
	SessionID           string   // session identifier for labeling
	Daemon              bool     // when true, creates daemon-mode container
	DaemonBinaryPath    string   // host path to vibepit binary (bind-mounted at /vibepit)
	DaemonHostKeyPath   string   // host path to SSH host key (bind-mounted at /etc/vibepit/sshd/host-key)
	DaemonHostPubPath   string   // host path to SSH host pub key
	DaemonAuthorizedKey string   // SSH public key for client auth (set as VIBEPIT_SSH_PUBKEY env)
	DaemonEntrypoint    []string // entrypoint override for daemon mode
	Memory              int64    // memory limit in bytes; 0 means unlimited
}

// CreateSandboxContainer creates the sandboxed development container
// with proxy environment variables and a read-only root filesystem.
func (c *Client) CreateSandboxContainer(ctx context.Context, cfg SandboxContainerConfig) (string, error) {
	// Parse addresses before any side effects so bad input fails cleanly.
	dnsIP, err := parseIPv4(cfg.ProxyIP)
	if err != nil {
		return "", fmt.Errorf("parse proxy IP: %w", err)
	}
	var sandboxIP netip.Addr
	if cfg.SandboxIP != "" {
		if sandboxIP, err = parseIPv4(cfg.SandboxIP); err != nil {
			return "", fmt.Errorf("parse sandbox IP: %w", err)
		}
	}

	proxyURL := fmt.Sprintf("http://%s", net.JoinHostPort(cfg.ProxyIP, strconv.Itoa(cfg.ProxyPort)))
	env := []string{
		fmt.Sprintf("TERM=%s", cfg.Term),
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		fmt.Sprintf("VIBEPIT_PROJECT_DIR=%s", cfg.ProjectDir),
		"HTTP_PROXY=" + proxyURL,
		"HTTPS_PROXY=" + proxyURL,
		"http_proxy=" + proxyURL,
		"https_proxy=" + proxyURL,
		"grpc_proxy=" + proxyURL,
		"NODE_USE_ENV_PROXY=1", // Required for Node.js built-in fetch/http clients to honor proxy env vars
		"NO_PROXY=localhost,127.0.0.1",
		"no_proxy=localhost,127.0.0.1",
		fmt.Sprintf("JAVA_TOOL_OPTIONS=-Dhttps.proxyHost=%[1]s -Dhttps.proxyPort=%[2]d -Dhttp.proxyHost=%[1]s -Dhttp.proxyPort=%[2]d", cfg.ProxyIP, cfg.ProxyPort),
	}
	if cfg.ColorTerm != "" {
		env = append(env, fmt.Sprintf("COLORTERM=%s", cfg.ColorTerm))
	}

	binds := []string{
		cfg.HomeVolumeName + ":" + HomeMountPath,
		cfg.LinuxbrewVolumeName + ":" + LinuxbrewMountPath,
		cfg.ProjectDir + ":" + cfg.ProjectDir,
	}
	// Hide the project's .vibepit directory in the sandbox.
	{
		vibepitConfigDir := filepath.Join(cfg.ProjectDir, config.ProjectConfigDirName)
		fakeConfigDir := filepath.Join(cfg.RuntimeDir, filepath.Base(vibepitConfigDir))
		if err := os.MkdirAll(vibepitConfigDir, 0700); err != nil {
			return "", fmt.Errorf("create fake config dir: %w", err)
		}
		binds = append(binds, fakeConfigDir+":"+vibepitConfigDir+":ro")
	}
	// Maven doesn't honor any HTTP_PROXY variables, so we have to create a temporary Maven settings.xml and
	// configure it globally via MAVEN_ARGS.
	{
		mavenConfigDir := filepath.Join(cfg.RuntimeDir, "maven")
		if err := os.MkdirAll(mavenConfigDir, 0700); err != nil {
			return "", fmt.Errorf("create maven config dir: %w", err)
		}
		mavenSettings := filepath.Join(mavenConfigDir, "settings.xml")
		if err := os.WriteFile(mavenSettings, config.MavenSettings(cfg.ProxyIP, cfg.ProxyPort), 0o600); err != nil {
			return "", fmt.Errorf("write maven settings: %w", err)
		}
		binds = append(binds, mavenSettings+":/etc/vibepit/maven-settings.xml:ro")
		env = append(env, `MAVEN_ARGS=--global-settings=/etc/vibepit/maven-settings.xml`)
	}
	if _, err := os.Stat("/etc/localtime"); err == nil {
		binds = append(binds, "/etc/localtime:/etc/localtime:ro")
	}

	labels := map[string]string{
		LabelVibepit:         "true",
		LabelRole:            RoleSandbox,
		LabelUID:             fmt.Sprintf("%d", cfg.UID),
		LabelUser:            cfg.User,
		LabelVolumeHome:      cfg.HomeVolumeName,
		LabelVolumeLinuxbrew: cfg.LinuxbrewVolumeName,
		LabelProjectDir:      cfg.ProjectDir,
		LabelSessionID:       cfg.SessionID,
	}

	containerConfig := &container.Config{
		Image:      cfg.Image,
		Env:        env,
		Hostname:   ContainerHostname,
		Labels:     labels,
		Tty:        true,
		OpenStdin:  true,
		WorkingDir: cfg.WorkDir,
	}

	hostConfig := &container.HostConfig{
		Binds:          binds,
		DNS:            []netip.Addr{dnsIP},
		Init:           new(true),
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		Tmpfs:          map[string]string{"/tmp": "exec"},
		Resources:      container.Resources{Memory: cfg.Memory},
	}

	var networkingConfig *network.NetworkingConfig

	if cfg.Daemon {
		containerConfig.Tty = false
		containerConfig.OpenStdin = false
		containerConfig.Entrypoint = cfg.DaemonEntrypoint
		containerConfig.Cmd = nil
		containerConfig.Env = append(containerConfig.Env,
			fmt.Sprintf("%s=%s", SSHPubKeyEnv, cfg.DaemonAuthorizedKey),
		)
		hostConfig.Binds = append(hostConfig.Binds,
			cfg.DaemonBinaryPath+":"+SandboxBinaryPath+":ro",
			cfg.DaemonHostKeyPath+":"+SSHHostKeyPath+":ro",
			cfg.DaemonHostPubPath+":"+SSHHostPubPath+":ro",
		)
		// Daemon sandbox stays on the isolated network only — SSH access
		// is forwarded through the proxy container to preserve isolation.
	}

	endpointSettings := &network.EndpointSettings{}
	if sandboxIP.IsValid() {
		endpointSettings.IPAMConfig = &network.EndpointIPAMConfig{
			IPv4Address: sandboxIP,
		}
	}
	networkingConfig = &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			cfg.NetworkID: endpointSettings,
		},
	}

	resp, err := c.docker.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config:           containerConfig,
		HostConfig:       hostConfig,
		NetworkingConfig: networkingConfig,
		Name:             cfg.Name,
	})
	if err != nil {
		return "", c.removeAfterFailedCreate(ctx, cfg.Name, cfg.SessionID, fmt.Errorf("create sandbox container: %w", err))
	}
	for _, w := range resp.Warnings {
		tui.Warn("container runtime: %s", w)
	}

	return resp.ID, nil
}

// StartContainer starts a previously created container without attaching.
func (c *Client) StartContainer(ctx context.Context, containerID string) error {
	_, err := c.docker.ContainerStart(ctx, containerID, dockerclient.ContainerStartOptions{})
	return err
}

// FindPublishedPort inspects a container and returns the host port bound to
// the given container port (e.g. "2222/tcp"). Returns an error if the port
// is not published.
func (c *Client) FindPublishedPort(ctx context.Context, containerID string, containerPort string) (int, error) {
	info, err := c.docker.ContainerInspect(ctx, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return 0, fmt.Errorf("inspect container: %w", err)
	}
	return publishedPort(info.Container, containerPort)
}

// FindControlPort returns the host-published control API port for a proxy
// container. It reads the container port from the control port label and
// looks up the published binding.
func (c *Client) FindControlPort(ctx context.Context, containerID string) (int, error) {
	info, err := c.docker.ContainerInspect(ctx, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return 0, fmt.Errorf("inspect container: %w", err)
	}
	portStr := containerLabels(info.Container)[LabelControlPort]
	if portStr == "" {
		return 0, fmt.Errorf("control port not found")
	}
	return publishedPort(info.Container, portStr+"/tcp")
}

// containerLabels returns an inspected container's labels. Some runtimes
// omit Config from inspect data.
func containerLabels(info container.InspectResponse) map[string]string {
	if info.Config == nil {
		return nil
	}
	return info.Config.Labels
}

// publishedPort extracts the host port for a container port from an inspect result.
func publishedPort(info container.InspectResponse, containerPort string) (int, error) {
	port, err := network.ParsePort(containerPort)
	if err != nil {
		return 0, fmt.Errorf("parse container port: %w", err)
	}
	if info.NetworkSettings == nil {
		return 0, fmt.Errorf("port %s not published", containerPort)
	}
	bindings, ok := info.NetworkSettings.Ports[port]
	if !ok || len(bindings) == 0 {
		return 0, fmt.Errorf("port %s not published", containerPort)
	}
	hostPort, err := strconv.Atoi(bindings[0].HostPort)
	if err != nil {
		return 0, fmt.Errorf("parse host port: %w", err)
	}
	return hostPort, nil
}

// StopAndRemove stops a container (best-effort) then forcibly removes it.
// Uses a short stop timeout since callers invoke this after the workload
// has already exited.
func (c *Client) StopAndRemove(ctx context.Context, containerID string) error {
	// Separate budgets, so a slow stop cannot starve the remove.
	stopCtx, cancelStop := cleanupContext(ctx)
	defer cancelStop()
	timeout := 2
	c.docker.ContainerStop(stopCtx, containerID, dockerclient.ContainerStopOptions{Timeout: &timeout})

	return c.removeContainer(ctx, containerID)
}

// EnsureVolume creates a named volume if it does not already exist, labelling
// it with the owner UID and username for later identification.
func (c *Client) EnsureVolume(ctx context.Context, name string, uid int, user string) error {
	list, err := c.docker.VolumeList(ctx, dockerclient.VolumeListOptions{
		Filters: make(dockerclient.Filters).Add("name", name),
	})
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	for _, v := range list.Items {
		if v.Name == name {
			return nil
		}
	}

	_, err = c.docker.VolumeCreate(ctx, dockerclient.VolumeCreateOptions{
		Name: name,
		Labels: map[string]string{
			LabelVibepit: "true",
			LabelUID:     fmt.Sprintf("%d", uid),
			LabelUser:    user,
		},
	})
	if err != nil {
		return fmt.Errorf("create volume: %w", err)
	}
	return nil
}

// ContainerLogs returns the last n lines of a container's log output.
func (c *Client) ContainerLogs(ctx context.Context, containerID string, tail int) (string, error) {
	reader, err := c.docker.ContainerLogs(ctx, containerID, dockerclient.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(tail),
	})
	if err != nil {
		return "", err
	}
	defer reader.Close()
	raw, err := io.ReadAll(reader)
	if err != nil && len(raw) == 0 {
		return "", err
	}
	logs := demuxLogs(raw)
	if err != nil {
		// The logs are diagnostics, so partial output beats none, but say so.
		if logs != "" && !strings.HasSuffix(logs, "\n") {
			logs += "\n"
		}
		logs += fmt.Sprintf("[logs incomplete: %v]\n", err)
	}
	return logs, nil
}

// demuxLogs strips the 8-byte frame headers the daemon adds to the logs of
// containers without a TTY, interleaving all streams in arrival order. Logs
// that do not start with a frame header, such as raw TTY logs, are returned
// as is. A truncated last frame keeps the payload that arrived, and bytes
// after an unrecognised header are kept raw, so no output is lost.
// stdcopy.StdCopy is not used because it silently drops such input.
func demuxLogs(raw []byte) string {
	if !isFrameHeader(raw) {
		return string(raw)
	}
	var out strings.Builder
	for rest := raw; len(rest) > 0; {
		if !isFrameHeader(rest) {
			out.Write(rest)
			break
		}
		size := int(binary.BigEndian.Uint32(rest[4:8]))
		payload := rest[8:]
		if size > len(payload) {
			out.Write(payload)
			break
		}
		out.Write(payload[:size])
		rest = payload[size:]
	}
	return out.String()
}

// isFrameHeader reports whether b starts with a stdcopy frame header: a
// stream type (stdin, stdout, stderr or daemon error) and three zero bytes.
func isFrameHeader(b []byte) bool {
	return len(b) >= 8 && b[0] <= 3 && b[1] == 0 && b[2] == 0 && b[3] == 0
}

// ContainerStatus returns a short status string for a container (e.g. "running", "exited (1)").
func (c *Client) ContainerStatus(ctx context.Context, containerID string) string {
	return c.ContainerSummary(ctx, containerID).Status
}

// ContainerSummary holds the runtime state `vibepit status` shows for a
// container, collected from a single inspect call.
type ContainerSummary struct {
	// Status is a short status string (e.g. "running", "exited (exit 1)"),
	// or "unknown" if the container cannot be inspected.
	Status string
	// StartedAt is the zero time if unknown or not started.
	StartedAt time.Time
	// MemoryLimit is the applied memory limit in bytes; 0 means no limit.
	// Only meaningful when MemoryLimitKnown is set: some runtimes omit
	// HostConfig from inspect data, and that must not read as "unlimited".
	MemoryLimit      int64
	MemoryLimitKnown bool
}

// ContainerSummary inspects a container once and returns its status, start
// time and memory limit.
func (c *Client) ContainerSummary(ctx context.Context, containerID string) ContainerSummary {
	info, err := c.docker.ContainerInspect(ctx, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return ContainerSummary{Status: "unknown"}
	}
	return summarizeContainer(info.Container)
}

func summarizeContainer(info container.InspectResponse) ContainerSummary {
	s := ContainerSummary{Status: "unknown"}
	if state := info.State; state != nil {
		s.Status = string(state.Status)
		if state.ExitCode != 0 {
			s.Status = fmt.Sprintf("%s (exit %d)", state.Status, state.ExitCode)
		}
		if t, err := time.Parse(time.RFC3339Nano, state.StartedAt); err == nil {
			s.StartedAt = t
		}
	}
	if info.HostConfig != nil {
		s.MemoryLimit = info.HostConfig.Memory
		s.MemoryLimitKnown = true
	}
	return s
}

// ImageRepoDigest returns the repo digest for a locally available image
// (e.g., "ghcr.io/bernd/vibepit@sha256:abc123"). This pins the exact
// image content independently of mutable tags.
func (c *Client) ImageRepoDigest(ctx context.Context, ref string) (string, error) {
	inspect, err := c.docker.ImageInspect(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("inspect image %s: %w", ref, err)
	}
	if len(inspect.RepoDigests) == 0 {
		return "", fmt.Errorf("image %s has no repo digests (local-only image?)", ref)
	}

	// Strip tag to match against RepoDigests.
	refRepo := ref
	if idx := strings.LastIndex(ref, ":"); idx > 0 {
		if !strings.Contains(ref[idx:], "/") {
			refRepo = ref[:idx]
		}
	}
	for _, rd := range inspect.RepoDigests {
		if strings.HasPrefix(rd, refRepo+"@") {
			return rd, nil
		}
	}
	return inspect.RepoDigests[0], nil
}
