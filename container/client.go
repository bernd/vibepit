package container

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bernd/vibepit/config"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/go-connections/nat"
	"golang.org/x/term"
)

const (
	LabelVibepit    = "vibepit"
	LabelRole       = "vibepit.role"
	LabelUID        = "vibepit.uid"
	LabelUser       = "vibepit.user"
	LabelVolume     = "vibepit.volume"
	LabelProjectDir = "vibepit.project.dir"
	LabelSessionID  = "vibepit.session-id"

	RoleProxy = "proxy"
	RoleDev   = "dev"

	ProxyBinaryPath   = "/vibepit"
	ProxyConfigPath   = "/config.json"
	HomeMountPath     = "/home/code"
	ContainerHostname = "vibes"

	ProxyImage       = "gcr.io/distroless/base-debian13:latest"
	LabelControlPort = "vibepit.control-port"
)

// Client wraps the Docker/Podman API, trying Docker first then falling back
// to the Podman-compatible socket.
type Client struct {
	docker *dockerclient.Client
}

func NewClient() (*Client, error) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err == nil {
		if _, err := cli.Ping(context.Background()); err == nil {
			return &Client{docker: cli}, nil
		}
		cli.Close()
	}

	// Fall back to the rootless Podman socket which exposes a Docker-compatible API.
	podmanSock := fmt.Sprintf("unix:///run/user/%d/podman/podman.sock", os.Getuid())
	cli, err = dockerclient.NewClientWithOpts(
		dockerclient.WithHost(podmanSock),
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("no Docker or Podman socket found: %w", err)
	}
	if _, err := cli.Ping(context.Background()); err != nil {
		cli.Close()
		return nil, fmt.Errorf("no Docker or Podman socket found: %w", err)
	}
	return &Client{docker: cli}, nil
}

func (c *Client) Close() error { return c.docker.Close() }

// EnsureImage pulls the image if it is not available locally.
func (c *Client) EnsureImage(ctx context.Context, ref string, quiet bool) error {
	images, err := c.docker.ImageList(ctx, image.ListOptions{
		Filters: filters.NewArgs(filters.Arg("reference", ref)),
	})
	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}
	if len(images) > 0 {
		return nil
	}

	return c.PullImage(ctx, ref, quiet)
}

// PullImage pulls the latest version of the image.
func (c *Client) PullImage(ctx context.Context, ref string, quiet bool) error {
	fmt.Printf("+ Pulling image: %s\n", ref)
	reader, err := c.docker.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	defer reader.Close()

	if quiet {
		// Drain the pull output to complete the operation.
		_, err = io.Copy(io.Discard, reader)
	} else {
		isTerminal := term.IsTerminal(int(os.Stdout.Fd()))
		err = jsonmessage.DisplayJSONMessagesStream(reader, os.Stdout, os.Stdout.Fd(), isTerminal, nil)
	}
	if err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	return nil
}

// FindRunningSession returns the ID of an already-running dev container for
// the given project directory, or empty string if none is found.
func (c *Client) FindRunningSession(ctx context.Context, projectDir string) (string, error) {
	containers, err := c.docker.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", fmt.Sprintf("%s=%s", LabelProjectDir, projectDir)),
			filters.Arg("label", LabelRole+"="+RoleDev),
		),
	})
	if err != nil {
		return "", err
	}
	if len(containers) > 0 {
		return containers[0].ID, nil
	}
	return "", nil
}

// FindProxyIP returns the IP address of the running vibepit proxy container
// by inspecting its network settings. Returns an error if no proxy is running.
func (c *Client) FindProxyIP(ctx context.Context) (string, error) {
	containers, err := c.docker.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", LabelVibepit+"=true"),
			filters.Arg("label", LabelRole+"="+RoleProxy),
		),
	})
	if err != nil {
		return "", err
	}
	if len(containers) == 0 {
		return "", fmt.Errorf("no running vibepit proxy container found")
	}

	info, err := c.docker.ContainerInspect(ctx, containers[0].ID)
	if err != nil {
		return "", err
	}
	for _, ep := range info.NetworkSettings.Networks {
		if ep.IPAddress != "" {
			return ep.IPAddress, nil
		}
	}
	return "", fmt.Errorf("proxy container has no IP address")
}

// AttachSession connects to a container's main process stdio. When the
// user exits the shell, the container's entrypoint exits and the container
// stops on its own. Returns an *ExitError if the container exits with a
// non-zero status code.
func (c *Client) AttachSession(ctx context.Context, containerID string) error {
	// Start waiting for the container exit before attaching to avoid a
	// race where the container exits between attach and wait.
	waitCh, waitErrCh := c.docker.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

	resp, err := c.docker.ContainerAttach(ctx, containerID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	defer resp.Close()

	resizeFn := func(height, width uint) {
		c.docker.ContainerResize(ctx, containerID, container.ResizeOptions{
			Height: height, Width: width,
		})
	}

	if err := runTTYSession(ctx, resp, resizeFn); err != nil {
		return err
	}

	// Retrieve the container's exit code.
	select {
	case result := <-waitCh:
		if result.StatusCode != 0 {
			return &ExitError{Code: int(result.StatusCode)}
		}
		return nil
	case err := <-waitErrCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ExecSession starts a new interactive shell inside a running container.
// Used when reattaching to an existing session. Returns an *ExitError if
// the shell exits with a non-zero status code.
func (c *Client) ExecSession(ctx context.Context, containerID string) error {
	size := terminalSize()

	execResp, err := c.docker.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"/bin/bash", "--login"},
		ConsoleSize:  size,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}

	hijack, err := c.docker.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{
		Tty:         true,
		ConsoleSize: size,
	})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	defer hijack.Close()

	resizeFn := func(height, width uint) {
		c.docker.ContainerExecResize(ctx, execResp.ID, container.ResizeOptions{
			Height: height, Width: width,
		})
	}

	if err := runTTYSession(ctx, hijack, resizeFn); err != nil {
		return err
	}

	// Retrieve the exec process exit code.
	inspect, err := c.docker.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return err
	}
	if inspect.ExitCode != 0 {
		return &ExitError{Code: inspect.ExitCode}
	}
	return nil
}

// NetworkInfo is returned by CreateNetwork with the Docker-assigned addresses.
type NetworkInfo struct {
	ID      string
	ProxyIP string
}

// CreateNetwork creates an internal Docker network with a random /24 subnet
// and derives a static IP for the proxy (gateway + 1). The subnet is
// explicitly specified so that Docker allows static IP assignment.
func (c *Client) CreateNetwork(ctx context.Context, name string) (NetworkInfo, error) {
	subnet, gateway, err := randomSubnet()
	if err != nil {
		return NetworkInfo{}, fmt.Errorf("generate subnet: %w", err)
	}

	resp, err := c.docker.NetworkCreate(ctx, name, network.CreateOptions{
		Internal: true,
		Labels:   map[string]string{LabelVibepit: "true"},
		IPAM: &network.IPAM{
			Config: []network.IPAMConfig{
				{
					Subnet:  subnet,
					Gateway: gateway.String(),
				},
			},
		},
	})
	if err != nil {
		return NetworkInfo{}, fmt.Errorf("create network: %w", err)
	}

	proxyIP := nextIP(gateway)
	return NetworkInfo{
		ID:      resp.ID,
		ProxyIP: proxyIP.String(),
	}, nil
}

// randomSubnet generates a random /24 subnet in the 10.x.x.0/8 range,
// returning the CIDR string and gateway IP (x.x.x.1). There is a small
// chance (~1/65k) of colliding with an existing Docker network, in which
// case network creation will fail with a "pool overlaps" error.
func randomSubnet() (string, net.IP, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, err
	}
	gateway := net.IPv4(10, b[0], b[1], 1)
	subnet := fmt.Sprintf("10.%d.%d.0/24", b[0], b[1])
	return subnet, gateway, nil
}

// nextIP returns the IP address immediately following ip.
func nextIP(ip net.IP) net.IP {
	ip = ip.To4()
	if ip == nil {
		return nil
	}
	n := binary.BigEndian.Uint32(ip)
	n++
	next := make(net.IP, 4)
	binary.BigEndian.PutUint32(next, n)
	return next
}

func (c *Client) RemoveNetwork(ctx context.Context, networkID string) error {
	return c.docker.NetworkRemove(ctx, networkID)
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
}

// StartProxyContainer creates and starts a minimal container that runs the
// vibepit proxy binary, then connects it to the bridge network so it can
// reach the internet. The control API port is published to 127.0.0.1 with
// an OS-assigned host port. Returns the container ID and the assigned host
// port.
func (c *Client) StartProxyContainer(ctx context.Context, cfg ProxyContainerConfig) (string, string, error) {
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

	containerPort, _ := nat.NewPort("tcp", portStr)

	resp, err := c.docker.ContainerCreate(ctx,
		&container.Config{
			Image:      ProxyImage,
			Cmd:        []string{ProxyBinaryPath, "proxy", "--config", ProxyConfigPath},
			Labels:     labels,
			Env:        env,
			WorkingDir: "/",
			ExposedPorts: nat.PortSet{
				containerPort: struct{}{},
			},
		},
		&container.HostConfig{
			Binds: []string{
				cfg.BinaryPath + ":" + ProxyBinaryPath + ":ro",
				cfg.ConfigPath + ":" + ProxyConfigPath + ":ro",
			},
			// The proxy container is created on the internal vibepit-net,
			// then connected to bridge after start. Docker generates
			// /etc/resolv.conf at creation time from the primary network,
			// so without explicit DNS the system resolver points at the
			// internal network gateway which can't resolve external names.
			DNS:           []string{"9.9.9.9"},
			ExtraHosts:    []string{"host-gateway:host-gateway"},
			RestartPolicy: container.RestartPolicy{Name: "no"},
			PortBindings: nat.PortMap{
				containerPort: []nat.PortBinding{
					{HostIP: "127.0.0.1", HostPort: "0"},
				},
			},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				cfg.NetworkID: {
					IPAMConfig: &network.EndpointIPAMConfig{
						IPv4Address: cfg.ProxyIP,
					},
				},
			},
		},
		nil,
		cfg.Name,
	)
	if err != nil {
		return "", "", fmt.Errorf("create proxy container: %w", err)
	}
	if err := c.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", "", fmt.Errorf("start proxy container: %w", err)
	}
	if err := c.docker.NetworkConnect(ctx, "bridge", resp.ID, nil); err != nil {
		return "", "", fmt.Errorf("connect proxy to bridge: %w", err)
	}

	// Inspect the container to discover the OS-assigned host port.
	info, err := c.docker.ContainerInspect(ctx, resp.ID)
	if err != nil {
		return "", "", fmt.Errorf("inspect proxy container: %w", err)
	}
	bindings := info.NetworkSettings.Ports[containerPort]
	if len(bindings) == 0 {
		return "", "", fmt.Errorf("no port binding found for control API")
	}
	controlPort := bindings[0].HostPort

	return resp.ID, controlPort, nil
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
// session metadata. The control port is read from the container label, falling
// back to the first published port binding for older containers.
func (c *Client) ListProxySessions(ctx context.Context) ([]ProxySession, error) {
	containers, err := c.docker.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", LabelVibepit+"=true"),
			filters.Arg("label", LabelRole+"="+RoleProxy),
		),
	})
	if err != nil {
		return nil, err
	}

	var sessions []ProxySession
	for _, ctr := range containers {
		// The control port label stores the container-internal port, but we
		// need the host-published port. Find the published binding that
		// corresponds to the labelled container port.
		var controlPort string
		labelPort := ctr.Labels[LabelControlPort]
		for _, p := range ctr.Ports {
			if p.PublicPort != 0 && (labelPort == "" || fmt.Sprintf("%d", p.PrivatePort) == labelPort) {
				controlPort = fmt.Sprintf("%d", p.PublicPort)
				break
			}
		}
		sessions = append(sessions, ProxySession{
			ContainerID: ctr.ID,
			SessionID:   ctr.Labels[LabelSessionID],
			ControlPort: controlPort,
			ProjectDir:  ctr.Labels[LabelProjectDir],
			StartedAt:   time.Unix(ctr.Created, 0),
		})
	}
	return sessions, nil
}

// DevContainerConfig holds the parameters for the sandboxed dev container.
type DevContainerConfig struct {
	Image      string
	ProjectDir string
	WorkDir    string
	RuntimeDir string
	VolumeName string
	NetworkID  string
	ProxyIP    string
	ProxyPort  int
	Name       string
	Term       string
	ColorTerm  string
	UID        int
	User       string
}

// StartDevContainer creates and starts the sandboxed development container
// with proxy environment variables and a read-only root filesystem.
func (c *Client) StartDevContainer(ctx context.Context, cfg DevContainerConfig) (string, error) {
	proxyURL := fmt.Sprintf("http://%s:%d", cfg.ProxyIP, cfg.ProxyPort)
	env := []string{
		fmt.Sprintf("TERM=%s", cfg.Term),
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		fmt.Sprintf("VIBEPIT_PROJECT_DIR=%s", cfg.ProjectDir),
		"HTTP_PROXY=" + proxyURL,
		"HTTPS_PROXY=" + proxyURL,
		"http_proxy=" + proxyURL,
		"https_proxy=" + proxyURL,
		"NO_PROXY=localhost,127.0.0.1",
		"no_proxy=localhost,127.0.0.1",
	}
	if cfg.ColorTerm != "" {
		env = append(env, fmt.Sprintf("COLORTERM=%s", cfg.ColorTerm))
	}

	binds := []string{
		cfg.VolumeName + ":" + HomeMountPath,
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
	if _, err := os.Stat("/etc/localtime"); err == nil {
		binds = append(binds, "/etc/localtime:/etc/localtime:ro")
	}

	resp, err := c.docker.ContainerCreate(ctx,
		&container.Config{
			Image:    cfg.Image,
			Env:      env,
			Hostname: ContainerHostname,
			Labels: map[string]string{
				LabelVibepit:    "true",
				LabelRole:       RoleDev,
				LabelUID:        fmt.Sprintf("%d", cfg.UID),
				LabelUser:       cfg.User,
				LabelVolume:     cfg.VolumeName,
				LabelProjectDir: cfg.ProjectDir,
			},
			Tty:        true,
			OpenStdin:  true,
			WorkingDir: cfg.WorkDir,
		},
		&container.HostConfig{
			Binds:          binds,
			DNS:            []string{cfg.ProxyIP},
			Init:           boolPtr(true),
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			Tmpfs:          map[string]string{"/tmp": "exec"},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				cfg.NetworkID: {},
			},
		},
		nil,
		cfg.Name,
	)
	if err != nil {
		return "", fmt.Errorf("create dev container: %w", err)
	}
	if err := c.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("start dev container: %w", err)
	}
	return resp.ID, nil
}

// StopAndRemove stops a container (best-effort) then forcibly removes it.
// Uses a short stop timeout since callers invoke this after the workload
// has already exited.
func (c *Client) StopAndRemove(ctx context.Context, containerID string) error {
	timeout := 2
	c.docker.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
	return c.docker.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
}

// EnsureVolume creates a named volume if it does not already exist, labelling
// it with the owner UID and username for later identification.
func (c *Client) EnsureVolume(ctx context.Context, name string, uid int, user string) error {
	list, err := c.docker.VolumeList(ctx, volume.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", name)),
	})
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	for _, v := range list.Volumes {
		if v.Name == name {
			return nil
		}
	}

	_, err = c.docker.VolumeCreate(ctx, volume.CreateOptions{
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

func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	return c.docker.VolumeRemove(ctx, name, false)
}

// StreamLogs follows the container log output and copies it to the given writer.
func (c *Client) StreamLogs(ctx context.Context, containerID string, w io.Writer) error {
	reader, err := c.docker.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		return err
	}
	defer reader.Close()
	_, err = io.Copy(w, reader)
	return err
}

func boolPtr(b bool) *bool { return &b }
