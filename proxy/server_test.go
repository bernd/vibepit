package proxy

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServerHoldTimeout(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		want    time.Duration
		wantErr bool
	}{
		{"set", `{"hold-timeout":"3s"}`, 3 * time.Second, false},
		{"absent", `{}`, 0, false},
		{"zero", `{"hold-timeout":"0"}`, 0, false},
		{"invalid", `{"hold-timeout":"abc"}`, 0, true},
		{"negative", `{"hold-timeout":"-1s"}`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			require.NoError(t, os.WriteFile(path, []byte(tt.json), 0o644))
			s, err := NewServer(path)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "hold-timeout")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, s.holdTimeout())
		})
	}
}
