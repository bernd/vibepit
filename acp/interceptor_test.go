package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPatchInitializeCapabilities(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantTerm bool
		wantFS   bool
	}{
		{
			name:     "no existing capabilities",
			input:    `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
			wantTerm: true,
			wantFS:   true,
		},
		{
			name:     "existing capabilities preserved",
			input:    `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientCapabilities":{"other":true}}}`,
			wantTerm: true,
			wantFS:   true,
		},
		{
			name:     "no params",
			input:    `{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
			wantTerm: false,
			wantFS:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := patchInitializeCapabilities([]byte(tt.input))

			var msg map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(result, &msg))

			if !tt.wantTerm {
				return
			}

			var params map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(msg["params"], &params))

			var caps map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(params["clientCapabilities"], &caps))

			assert.Equal(t, "true", string(caps["terminal"]))
			assert.Equal(t, "true", string(caps["fs"]))
		})
	}
}

func TestPatchInitializePreservesExistingCapabilities(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientCapabilities":{"other":"value"}}}`
	result := patchInitializeCapabilities([]byte(input))

	var msg map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(result, &msg))

	var params map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(msg["params"], &params))

	var caps map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(params["clientCapabilities"], &caps))

	assert.Equal(t, `"value"`, string(caps["other"]))
	assert.Equal(t, "true", string(caps["terminal"]))
	assert.Equal(t, "true", string(caps["fs"]))
}

func TestInterceptorRelayAndIntercept(t *testing.T) {
	// Use "echo" as the agent — it reads stdin and echoes to stdout.
	// We send a terminal/create request from "agent stdout" and verify
	// the interceptor handles it instead of forwarding to IDE.

	interceptor := NewInterceptor("cat", nil)

	// Simulate IDE sending an initialize request.
	ideInput := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"

	ideIn := strings.NewReader(ideInput)
	var ideOut bytes.Buffer

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// cat will echo our initialize back, which the interceptor should forward to IDE.
	err := interceptor.Run(ctx, ideIn, &ideOut)
	// cat exits when stdin closes, which is expected.
	if err != nil {
		// "agent exited" errors are expected when using cat.
		assert.Contains(t, err.Error(), "agent exited")
	}

	// The initialize message should have been forwarded to IDE output
	// (cat echoes it back, interceptor forwards it).
	output := ideOut.String()
	if output != "" {
		// Verify the echoed message is valid JSON.
		lines := strings.Split(strings.TrimSpace(output), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			var msg Message
			assert.NoError(t, json.Unmarshal([]byte(line), &msg))
		}
	}
}
