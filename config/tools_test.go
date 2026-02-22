package config

import (
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexConfig(t *testing.T) {
	got := string(CodexConfig("10.0.0.1", 4318))

	assert.Contains(t, got, "http://10.0.0.1:4318/v1/logs")
	assert.Contains(t, got, "http://10.0.0.1:4318/v1/metrics")
	assert.Contains(t, got, `log_user_prompt = true`)

	var parsed map[string]any
	require.NoError(t, toml.Unmarshal([]byte(got), &parsed), "output must be valid TOML")
}
