package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfirmProjectConfig(t *testing.T) {
	const content = "allow-http:\n  - attacker.example:443\n"
	tests := []struct {
		name        string
		hasConfig   bool
		answer      string
		interactive bool
		wantErr     string
	}{
		{name: "missing config needs no answer", interactive: true},
		{name: "missing config, not interactive"},
		{name: "yes", hasConfig: true, answer: "y\n", interactive: true},
		{name: "full yes in caps", hasConfig: true, answer: "YES\n", interactive: true},
		{name: "no", hasConfig: true, answer: "n\n", interactive: true, wantErr: "not using the project config"},
		{name: "empty answer means no", hasConfig: true, answer: "\n", interactive: true, wantErr: "not using the project config"},
		{name: "end of input means no", hasConfig: true, interactive: true, wantErr: "not using the project config"},
		{name: "not interactive refuses", hasConfig: true, wantErr: "run vibepit in a terminal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".vibepit", "network.yaml")
			if tt.hasConfig {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			}
			var out bytes.Buffer

			got, err := confirmProjectConfig(path, strings.NewReader(tt.answer), &out, tt.interactive)

			switch {
			case tt.wantErr != "":
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, got)
			case tt.hasConfig:
				require.NoError(t, err)
				assert.Equal(t, content, string(got), "returns the contents shown")
			default:
				require.NoError(t, err)
				assert.Nil(t, got)
			}
			if tt.hasConfig && tt.interactive {
				assert.Contains(t, out.String(), path)
				assert.Contains(t, out.String(), "attacker.example:443")
			} else {
				assert.Empty(t, out.String())
			}
		})
	}
}
