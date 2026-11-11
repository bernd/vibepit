package container

import (
	"context"
	"encoding/binary"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/network"
	dockerclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummarizeContainer(t *testing.T) {
	started := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		info container.InspectResponse
		want ContainerSummary
	}{
		{
			name: "running with memory limit",
			info: container.InspectResponse{
				State:      &container.State{Status: "running", StartedAt: started.Format(time.RFC3339Nano)},
				HostConfig: &container.HostConfig{Resources: container.Resources{Memory: 8 << 30}},
			},
			want: ContainerSummary{Status: "running", StartedAt: started, MemoryLimit: 8 << 30, MemoryLimitKnown: true},
		},
		{
			name: "no limit is known and zero",
			info: container.InspectResponse{
				State:      &container.State{Status: "running"},
				HostConfig: &container.HostConfig{},
			},
			want: ContainerSummary{Status: "running", MemoryLimitKnown: true},
		},
		{
			name: "missing host config leaves limit unknown",
			info: container.InspectResponse{
				State: &container.State{Status: "exited", ExitCode: 1},
			},
			want: ContainerSummary{Status: "exited (exit 1)"},
		},
		{
			name: "missing state is unknown",
			info: container.InspectResponse{},
			want: ContainerSummary{Status: "unknown"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, summarizeContainer(tt.info))
		})
	}
}

func TestRandomSubnet(t *testing.T) {
	subnet, gateway, err := randomSubnet()
	require.NoError(t, err)
	assert.Equal(t, 24, subnet.Bits())
	assert.True(t, subnet.Contains(gateway), "gateway %s in %s", gateway, subnet)
	assert.Equal(t, byte(10), gateway.As4()[0])
	assert.Equal(t, byte(1), gateway.As4()[3])
	assert.Equal(t, subnet.Addr().Next(), gateway)
}

func TestPublishedPort(t *testing.T) {
	ssh := sshContainerPort
	assert.Equal(t, SSHContainerPort, ssh.String())
	info := container.InspectResponse{NetworkSettings: &container.NetworkSettings{
		Ports: network.PortMap{
			ssh: {{HostIP: loopback, HostPort: "49153"}},
		},
	}}
	tests := []struct {
		name    string
		info    container.InspectResponse
		port    string
		want    int
		wantErr bool
	}{
		{name: "published", info: info, port: "2222/tcp", want: 49153},
		{name: "not published", info: info, port: "8080/tcp", wantErr: true},
		{name: "invalid port", info: info, port: "nope", wantErr: true},
		{name: "no network settings", info: container.InspectResponse{}, port: "2222/tcp", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := publishedPort(tt.info, tt.port)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDisplayPullProgress(t *testing.T) {
	streamErr := errors.New("connection reset")
	tests := []struct {
		name    string
		msgs    []jsonstream.Message
		err     error
		wantErr string
	}{
		{
			name: "progress then done",
			msgs: []jsonstream.Message{
				{ID: "layer1", Progress: &jsonstream.Progress{Current: 1, Total: 2}},
				{ID: "layer1", Progress: &jsonstream.Progress{Current: 2, Total: 2}},
				{Status: "Status: Downloaded newer image"},
			},
		},
		{
			name: "error message in stream",
			msgs: []jsonstream.Message{
				{ID: "layer1", Progress: &jsonstream.Progress{Current: 1, Total: 2}},
				{Error: &jsonstream.Error{Message: "manifest unknown"}},
			},
			wantErr: "manifest unknown",
		},
		{
			name:    "stream error",
			msgs:    []jsonstream.Message{{Status: "Pulling fs layer"}},
			err:     streamErr,
			wantErr: "connection reset",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seq iter.Seq2[jsonstream.Message, error] = func(yield func(jsonstream.Message, error) bool) {
				for _, m := range tt.msgs {
					if !yield(m, nil) {
						return
					}
				}
				if tt.err != nil {
					yield(jsonstream.Message{}, tt.err)
				}
			}
			err := displayPullProgress(seq)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestStartProxyContainerRejectsInvalidPort(t *testing.T) {
	// Validation runs before any API call, so no daemon is needed.
	tests := []struct {
		name string
		port int
	}{
		{name: "zero", port: 0},
		{name: "negative", port: -1},
		{name: "above range", port: 70000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := (&Client{}).StartProxyContainer(context.Background(), ProxyContainerConfig{
				ControlAPIPort: tt.port,
				ProxyIP:        "10.0.0.2",
			})
			assert.ErrorContains(t, err, "invalid control API port")
		})
	}
}

func TestCreateSandboxContainerRejectsBadIPWithoutSideEffects(t *testing.T) {
	tests := []struct {
		name      string
		proxyIP   string
		sandboxIP string
		wantErr   string
	}{
		{name: "bad proxy IP", proxyIP: "not-an-ip", wantErr: "parse proxy IP"},
		{name: "bad sandbox IP", proxyIP: "10.0.0.2", sandboxIP: "10.0.0.300", wantErr: "parse sandbox IP"},
		{name: "IPv6 proxy IP", proxyIP: "fd00::2", wantErr: "parse proxy IP"},
		{name: "IPv4-mapped sandbox IP", proxyIP: "10.0.0.2", sandboxIP: "::ffff:10.0.0.3", wantErr: "parse sandbox IP"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectDir, runtimeDir := t.TempDir(), t.TempDir()
			_, err := (&Client{}).CreateSandboxContainer(context.Background(), SandboxContainerConfig{
				ProjectDir: projectDir,
				RuntimeDir: runtimeDir,
				ProxyIP:    tt.proxyIP,
				SandboxIP:  tt.sandboxIP,
			})
			assert.ErrorContains(t, err, tt.wantErr)
			for _, dir := range []string{projectDir, runtimeDir} {
				entries, err := os.ReadDir(dir)
				require.NoError(t, err)
				assert.Empty(t, entries, "no files created in %s", dir)
			}
		})
	}
}

func TestParseIPv4(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{in: "10.1.2.3"},
		{in: "fd00::1", wantErr: true},
		{in: "::ffff:10.1.2.3", wantErr: true},
		{in: "", wantErr: true},
		{in: "10.1.2", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseIPv4(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.in, got.String())
		})
	}
}

// fakeDaemon serves the Docker API from handler and returns a client for it.
func fakeDaemon(t *testing.T, handler http.HandlerFunc) *dockerclient.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cli, err := dockerclient.New(dockerclient.WithHost("tcp://" + strings.TrimPrefix(srv.URL, "http://")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// fakePing serves /_ping with the given API version and status.
func fakePing(t *testing.T, apiVersion string, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apiVersion != "" {
			w.Header().Set("Api-Version", apiVersion)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return "tcp://" + strings.TrimPrefix(srv.URL, "http://")
}

func TestPingedClient(t *testing.T) {
	tests := []struct {
		name       string
		apiVersion string
		status     int
		wantErr    string
	}{
		{name: "supported daemon", apiVersion: "1.44", status: http.StatusOK},
		{name: "too old daemon", apiVersion: "1.30", status: http.StatusOK, wantErr: "not supported"},
		{name: "daemon error", status: http.StatusInternalServerError, wantErr: "tcp://"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli, err := pingedClient(false, dockerclient.WithHost(fakePing(t, tt.apiVersion, tt.status)))
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, cli)
				return
			}
			require.NoError(t, err)
			_ = cli.Close()
		})
	}
}

func TestFindSocketSkipsUnusableDaemons(t *testing.T) {
	old := fakePing(t, "1.30", http.StatusOK)
	broken := fakePing(t, "", http.StatusInternalServerError)
	good := fakePing(t, "1.44", http.StatusOK)

	cli, err := findSocket(false, old, broken, good)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	assert.Equal(t, good, cli.DaemonHost())

	_, err = findSocket(false, old, broken)
	assert.ErrorContains(t, err, old+": ", "reports why each socket failed")
	assert.ErrorContains(t, err, "not supported")
	assert.ErrorContains(t, err, broken+": ")
}

func TestNewClientDoesNotFallBackFromDockerHost(t *testing.T) {
	host := fakePing(t, "", http.StatusInternalServerError)
	t.Setenv("DOCKER_HOST", host)
	_, err := NewClient()
	assert.ErrorContains(t, err, "DOCKER_HOST="+host)
}

// stdFrame encodes p as one stdcopy frame for stream (1 stdout, 2 stderr).
func stdFrame(stream byte, p string) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(p)))
	return append(header, p...)
}

func TestContainerLogs(t *testing.T) {
	framed := append(stdFrame(1, "out line\n"), stdFrame(2, "err line\n")...)
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{name: "framed logs are demultiplexed", body: framed, want: "out line\nerr line\n"},
		{name: "TTY logs are raw", body: []byte("raw\r\n"), want: "raw\r\n"},
		{name: "truncated last frame keeps arrived payload", body: framed[:len(framed)-3], want: "out line\nerr li"},
		{name: "daemon error frame is kept", body: append(stdFrame(1, "out\n"), stdFrame(3, "boom")...), want: "out\nboom"},
		{name: "unrecognised header keeps rest raw", body: append(stdFrame(1, "out\n"), "junk"...), want: "out\njunk"},
		{name: "empty", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/containers/c1/logs") {
					http.NotFound(w, r)
					return
				}
				assert.Equal(t, "20", r.URL.Query().Get("tail"))
				_, _ = w.Write(tt.body)
			})
			got, err := (&Client{docker: cli}).ContainerLogs(context.Background(), "c1", 20)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestContainerLogsMarksIncompleteOutput(t *testing.T) {
	cli := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		// Promise more than is sent, so the read ends with an unexpected EOF.
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write(stdFrame(1, "out line\n"))
	})
	got, err := (&Client{docker: cli}).ContainerLogs(context.Background(), "c1", 20)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got, "out line\n[logs incomplete: "), "got %q", got)
}

func TestNewClientDoesNotFallBackFromInvalidSettings(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", filepath.Join(t.TempDir(), "missing"))
	_, err := NewClient()
	assert.ErrorIs(t, err, errClientConfig)
}
