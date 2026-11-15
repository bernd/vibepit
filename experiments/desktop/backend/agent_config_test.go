package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type callbackInput func([]byte) (int, error)

func (f callbackInput) Write(p []byte) (int, error) { return f(p) }
func (f callbackInput) Close() error                { return nil }

func configTestService(t *testing.T) (*Service, *agent) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := &connection{id: "test", ctx: ctx, cancel: cancel}
	c.active.Store(true)
	a := &agent{c: c, ready: make(chan struct{}), config: ClaudeConfig{Models: demoModels()}}
	close(a.ready)
	c.agent = a
	return &Service{conn: c}, a
}

func TestClaudeSettingsWaitForMatchingAcknowledgement(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprintf("reject=%v", reject), func(t *testing.T) {
			s, a := configTestService(t)
			a.input = callbackInput(func(p []byte) (int, error) {
				var frame struct {
					RequestID string         `json:"request_id"`
					Request   map[string]any `json:"request"`
				}
				require.NoError(t, json.Unmarshal(p, &frame))
				assert.Equal(t, map[string]any{"subtype": "set_model", "model": "opus"}, frame.Request)
				assert.Empty(t, a.config.Model, "do not claim a change before acknowledgement")
				require.Error(t, s.SendPrompt("test", "cannot race config"))
				_, err := s.SetClaudeOption("test", "effort", "high")
				require.Error(t, err)
				require.NoError(t, a.handle(json.RawMessage(`{"type":"control_response","response":{"request_id":"unrelated","subtype":"success"}}`)))
				assert.Empty(t, a.config.Model)
				subtype := "success"
				if reject {
					subtype = "error"
				}
				a.simulateFrame(map[string]any{"type": "control_response", "response": map[string]any{"request_id": frame.RequestID, "subtype": subtype, "error": "not supported"}})
				return len(p), nil
			})
			config, err := s.SetClaudeOption("test", "model", "opus")
			if reject {
				require.ErrorContains(t, err, "not supported")
				assert.Empty(t, a.config.Model)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "opus", config.Model)
			}
			assert.False(t, a.configuring)
			assert.Empty(t, a.controls)
		})
	}
}

func TestClaudeEffortWireFormatAndReset(t *testing.T) {
	s, a := configTestService(t)
	for _, value := range []string{"high", ""} {
		a.input = callbackInput(func(p []byte) (int, error) {
			var frame struct {
				RequestID string         `json:"request_id"`
				Request   map[string]any `json:"request"`
			}
			require.NoError(t, json.Unmarshal(p, &frame))
			var expected any
			if value != "" {
				expected = value
			}
			assert.Equal(t, map[string]any{"subtype": "apply_flag_settings", "settings": map[string]any{"effortLevel": expected}}, frame.Request)
			a.simulateFrame(map[string]any{"type": "control_response", "response": map[string]any{"request_id": frame.RequestID, "subtype": "success"}})
			return len(p), nil
		})
		config, err := s.SetClaudeOption("test", "effort", value)
		require.NoError(t, err)
		assert.Equal(t, value, config.Effort)
		assert.Equal(t, value == "", config.EffortReset)
	}
}

func TestClaudeSettingsValidationAndDisconnect(t *testing.T) {
	s, a := configTestService(t)
	for _, pair := range [][2]string{{"model", "invented"}, {"effort", "max"}, {"effort", "invalid"}, {"unknown", "high"}} {
		_, err := s.SetClaudeOption("test", pair[0], pair[1])
		require.Error(t, err)
	}
	a.config.Model = "haiku"
	_, err := s.SetClaudeOption("test", "effort", "high")
	require.Error(t, err)
	a.busy = true
	_, err = s.SetClaudeOption("test", "model", "opus")
	require.Error(t, err)
	a.busy = false
	_, err = s.SetClaudeOption("stale-token", "model", "opus")
	require.Error(t, err)
	a.input = callbackInput(func(p []byte) (int, error) { a.c.cancel(); return len(p), nil })
	_, err = s.SetClaudeOption("test", "model", "opus")
	require.ErrorContains(t, err, "disconnected")
	assert.Equal(t, "haiku", a.config.Model)
	assert.False(t, a.configuring)
}

func TestClaudeModelIndicatorUsesMainThreadReports(t *testing.T) {
	_, a := configTestService(t)
	for _, frame := range []string{
		`{"type":"system","subtype":"init","model":"real-model"}`,
		`{"type":"assistant","parent_tool_use_id":"child","message":{"model":"subagent-model"}}`,
		`{"type":"assistant","message":{"model":"<synthetic>"}}`,
	} {
		require.NoError(t, a.handle(json.RawMessage(frame)))
	}
	assert.Equal(t, "real-model", a.config.ReportedModel)
	require.NoError(t, a.handle(json.RawMessage(`{"type":"stream_event","event":{"type":"message_start","message":{"model":"new-model"}}}`)))
	assert.Equal(t, "new-model", a.config.ReportedModel)
	assert.Empty(t, a.config.Model, "observations do not invent an explicit override")
}

func TestStartClaudeLoadsChoicesWithoutPrompt(t *testing.T) {
	s := New(true, func(Event) {}, t.TempDir())
	token, err := s.Connect("demo")
	require.NoError(t, err)
	t.Cleanup(s.Disconnect)
	config, err := s.StartClaude(token, false)
	require.NoError(t, err)
	require.Len(t, config.Models, 3)
	assert.False(t, s.conn.agent.busy)
	assert.Equal(t, uint64(3), s.conn.agent.debugSequence, "startup diagnostic, initialize and its response only")
	config, err = s.SetClaudeOption(token, "model", "opus")
	require.NoError(t, err)
	assert.Equal(t, "opus", config.Model)
}
