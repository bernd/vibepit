package container

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
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
		wantClient bool
	}{
		{name: "supported daemon", apiVersion: "1.44", status: http.StatusOK, wantClient: true},
		{name: "too old daemon", apiVersion: "1.30", status: http.StatusOK},
		{name: "daemon error", status: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := pingedClient(false, "test", dockerclient.WithHost(fakePing(t, tt.apiVersion, tt.status)))
			if cli != nil {
				_ = cli.Close()
			}
			assert.Equal(t, tt.wantClient, cli != nil)
		})
	}
}
