package container

import (
	"net"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
)

func TestNextIP(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"172.28.0.1", "172.28.0.2"},
		{"10.0.0.0", "10.0.0.1"},
		{"192.168.1.254", "192.168.1.255"},
	}
	for _, tt := range tests {
		got := nextIP(net.ParseIP(tt.input))
		assert.Equal(t, tt.expected, got.String(), "nextIP(%s)", tt.input)
	}
}

func TestSummarizeContainer(t *testing.T) {
	started := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		info container.InspectResponse
		want ContainerSummary
	}{
		{
			name: "running with memory limit",
			info: container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{
				State:      &container.State{Status: "running", StartedAt: started.Format(time.RFC3339Nano)},
				HostConfig: &container.HostConfig{Resources: container.Resources{Memory: 8 << 30}},
			}},
			want: ContainerSummary{Status: "running", StartedAt: started, MemoryLimit: 8 << 30, MemoryLimitKnown: true},
		},
		{
			name: "no limit is known and zero",
			info: container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{
				State:      &container.State{Status: "running"},
				HostConfig: &container.HostConfig{},
			}},
			want: ContainerSummary{Status: "running", MemoryLimitKnown: true},
		},
		{
			name: "missing host config leaves limit unknown",
			info: container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{
				State: &container.State{Status: "exited", ExitCode: 1},
			}},
			want: ContainerSummary{Status: "exited (exit 1)"},
		},
		{
			name: "missing base is unknown",
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
