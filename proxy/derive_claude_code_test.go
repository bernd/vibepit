package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeriveEventMetrics_APIRequest(t *testing.T) {
	buf := NewTelemetryBuffer(100)
	buf.AddEvent(TelemetryEvent{
		Agent:     "claude-code",
		EventName: "api_request",
		Attrs: map[string]string{
			"model":       "claude-opus-4-6",
			"duration_ms": "1500",
			"cost_usd":    "0.10",
		},
	})
	buf.AddEvent(TelemetryEvent{
		Agent:     "claude-code",
		EventName: "api_request",
		Attrs: map[string]string{
			"model":       "claude-opus-4-6",
			"duration_ms": "2500",
			"cost_usd":    "0.15",
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

	assert.Equal(t, 2.0, byName["claude_code.api.count{model=claude-opus-4-6}"])
	assert.Equal(t, 4000.0, byName["claude_code.api.duration{model=claude-opus-4-6}"])
	assert.Equal(t, 2.0, byName["claude_code.event_type.count{type=api_request}"])
	assert.Equal(t, 4000.0, byName["claude_code.event_type.duration{type=api_request}"])
}

func TestDeriveEventMetrics_ToolResult(t *testing.T) {
	buf := NewTelemetryBuffer(100)
	buf.AddEvent(TelemetryEvent{
		Agent:     "claude-code",
		EventName: "tool_result",
		Attrs: map[string]string{
			"tool_name":              "Bash",
			"duration_ms":            "120",
			"tool_result_size_bytes": "200",
		},
	})
	buf.AddEvent(TelemetryEvent{
		Agent:     "claude-code",
		EventName: "tool_result",
		Attrs: map[string]string{
			"tool_name":              "Bash",
			"duration_ms":            "80",
			"tool_result_size_bytes": "510",
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

	assert.Equal(t, 2.0, byName["claude_code.tool.count{type=Bash}"])
	assert.Equal(t, 200.0, byName["claude_code.tool.duration{type=Bash}"])
	assert.Equal(t, 710.0, byName["claude_code.tool.result_size{type=Bash}"])
	assert.Equal(t, 510.0, byName["claude_code.tool.result_size_max{type=Bash}"])
}
