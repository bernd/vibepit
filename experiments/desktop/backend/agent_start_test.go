package backend

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func TestClaudeLaunchModes(t *testing.T) {
	for _, yolo := range []bool{false, true} {
		command := claudeCommand(yolo)
		assert.Contains(t, command, "--input-format stream-json --output-format stream-json")
		assert.Contains(t, command, "--permission-prompt-tool stdio")
		if yolo {
			assert.Contains(t, command, "--dangerously-skip-permissions")
			assert.NotContains(t, command, "--permission-mode default")
		} else {
			assert.Contains(t, command, "--permission-mode default")
			assert.NotContains(t, command, "--dangerously-skip-permissions")
		}
	}
}
func TestClaudeYOLOStartupAndDemo(t *testing.T) {
	var mu sync.Mutex
	var prompts, holds int
	finished := make(chan struct{}, 1)
	s := New(true, func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		if e.Kind == "network-hold" {
			holds++
		}
		if e.Kind == "agent-event" {
			var frame struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(e.Data.(json.RawMessage), &frame); err != nil {
				t.Error(err)
				return
			}
			if frame.Type == "control_request" {
				prompts++
			}
			if frame.Type == "result" {
				finished <- struct{}{}
			}
		}
	}, t.TempDir())
	token, err := s.Connect("demo")
	require.NoError(t, err)
	defer s.Disconnect()
	config, err := s.StartClaude(token, true)
	require.NoError(t, err)
	assert.True(t, config.YOLO)
	assert.Equal(t, "bypassPermissions", config.PermissionMode)
	_, err = s.StartClaude(token, false)
	require.ErrorContains(t, err, "reconnect")
	_, err = s.StartClaude("stale", true)
	require.Error(t, err)
	require.NoError(t, s.SendPrompt(token, "Run in YOLO mode"))
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("YOLO waited for permission")
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, prompts)
	assert.Equal(t, 1, holds, "network holds remain independent")
}
func TestReportedPermissionModeOverridesRequestedIndicator(t *testing.T) {
	_, a := configTestService(t)
	a.config.YOLO = true
	a.loadModels(json.RawMessage(`{"models":[],"current_permission_mode":"default"}`))
	assert.Equal(t, "default", a.config.PermissionMode)
	require.NoError(t, a.handle(json.RawMessage(`{"type":"system","subtype":"init","permissionMode":"bypassPermissions"}`)))
	assert.Equal(t, "bypassPermissions", a.config.PermissionMode)
}
