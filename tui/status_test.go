package tui

import (
	"bytes"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
)

func TestWriteStatus(t *testing.T) {
	// Use an unstyled style to get plain text without ANSI escapes.
	plain := lipgloss.NewStyle()

	tests := []struct {
		name   string
		verb   string
		format string
		args   []any
		want   string
	}{
		{
			name:   "short verb is right-padded to 12 chars",
			verb:   "Starting",
			format: "proxy container",
			want:   "    Starting proxy container\n",
		},
		{
			name:   "saved message format args",
			verb:   "Saved",
			format: "to %s",
			args:   []any{"/tmp/project"},
			want:   "       Saved to /tmp/project\n",
		},
		{
			name:   "format args are interpolated",
			verb:   "Starting",
			format: "container in %s",
			args:   []any{"/foo"},
			want:   "    Starting container in /foo\n",
		},
		{
			name:   "longest current verb aligns correctly",
			verb:   "Generating",
			format: "mTLS credentials",
			want:   "  Generating mTLS credentials\n",
		},
		{
			name:   "error verb aligns correctly",
			verb:   "error",
			format: "volume: permission denied",
			want:   "       error volume: permission denied\n",
		},
		{
			name:   "verb longer than 12 chars is not truncated",
			verb:   "VeryLongVerbHere",
			format: "message",
			want:   "VeryLongVerbHere message\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeStatus(&buf, tt.verb, plain, tt.format, tt.args...)
			assert.Equal(t, tt.want, buf.String())
		})
	}
}

func TestWriteStatus_WritesToProvidedWriter(t *testing.T) {
	plain := lipgloss.NewStyle()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	writeStatus(&stdout, "Allowed", plain, "%s", "example.com:443")
	writeStatus(&stderr, "error", plain, "%s", "permission denied")

	assert.Equal(t, "     Allowed example.com:443\n", stdout.String())
	assert.Equal(t, "       error permission denied\n", stderr.String())
	assert.NotContains(t, stdout.String(), "permission denied")
}

func TestWriteStatus_UnstyledHasNoANSI(t *testing.T) {
	var buf bytes.Buffer
	writeStatus(&buf, "Creating", lipgloss.NewStyle(), "network %s", "vibepit-abc")

	assert.Equal(t, "    Creating network vibepit-abc\n", buf.String())
}
