package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeriveEventMetrics_CodexSSEEvent(t *testing.T) {
	buf := NewTelemetryBuffer(100)
	buf.AddEvent(TelemetryEvent{
		Agent:     "codex",
		EventName: "codex.sse_event",
		Attrs: map[string]string{
			"event.kind":            "response.completed",
			"model":                 "o3",
			"input_token_count":     "1000",
			"output_token_count":    "500",
			"cached_token_count":    "800",
			"reasoning_token_count": "200",
		},
	})

	metrics := buf.Metrics()
	byName := map[string]float64{}
	for _, m := range metrics {
		key := m.Name
		if m.Attributes["model"] != "" {
			key += "{model=" + m.Attributes["model"] + "}"
		}
		byName[key] = m.Value
	}

	assert.Equal(t, 1000.0, byName["codex.token.input{model=o3}"])
	assert.Equal(t, 500.0, byName["codex.token.output{model=o3}"])
	assert.Equal(t, 800.0, byName["codex.token.cached{model=o3}"])
	assert.Equal(t, 200.0, byName["codex.token.reasoning{model=o3}"])

	// Cost should be derived from token counts and pricing data.
	costVal, hasCost := byName["codex.cost.usage{model=o3}"]
	assert.True(t, hasCost, "codex.cost.usage metric should be derived")
	assert.Greater(t, costVal, 0.0)
}

func TestDeriveEventMetrics_CodexSSEEvent_IgnoresNonCompleted(t *testing.T) {
	buf := NewTelemetryBuffer(100)
	buf.AddEvent(TelemetryEvent{
		Agent:     "codex",
		EventName: "codex.sse_event",
		Attrs: map[string]string{
			"event.kind":        "response.started",
			"input_token_count": "1000",
		},
	})

	metrics := buf.Metrics()
	assert.Empty(t, metrics)
}

func TestDeriveEventMetrics_CodexAPIRequest(t *testing.T) {
	buf := NewTelemetryBuffer(100)
	buf.AddEvent(TelemetryEvent{
		Agent:     "codex",
		EventName: "codex.api_request",
		Attrs: map[string]string{
			"model":       "o3",
			"duration_ms": "2000",
		},
	})

	metrics := buf.Metrics()
	byName := map[string]float64{}
	for _, m := range metrics {
		key := m.Name
		if m.Attributes["model"] != "" {
			key += "{model=" + m.Attributes["model"] + "}"
		}
		if m.Attributes["type"] != "" {
			key += "{type=" + m.Attributes["type"] + "}"
		}
		byName[key] = m.Value
	}

	assert.Equal(t, 1.0, byName["codex.api.count{model=o3}"])
	assert.Equal(t, 2000.0, byName["codex.api.duration{model=o3}"])
	assert.Equal(t, 1.0, byName["codex.event_type.count{type=codex.api_request}"])
	assert.Equal(t, 2000.0, byName["codex.event_type.duration{type=codex.api_request}"])
}

func TestDeriveEventMetrics_CodexToolResult(t *testing.T) {
	buf := NewTelemetryBuffer(100)
	buf.AddEvent(TelemetryEvent{
		Agent:     "codex",
		EventName: "codex.tool_result",
		Attrs: map[string]string{
			"tool_name":   "shell",
			"duration_ms": "150",
		},
	})

	metrics := buf.Metrics()
	byName := map[string]float64{}
	for _, m := range metrics {
		key := m.Name
		if m.Attributes["type"] != "" {
			key += "{type=" + m.Attributes["type"] + "}"
		}
		byName[key] = m.Value
	}

	assert.Equal(t, 1.0, byName["codex.tool.count{type=shell}"])
	assert.Equal(t, 150.0, byName["codex.tool.duration{type=shell}"])
}
