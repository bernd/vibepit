package proxy

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelemetryBuffer_Events(t *testing.T) {
	t.Run("stores events up to capacity", func(t *testing.T) {
		buf := NewTelemetryBuffer(3)
		buf.AddEvent(TelemetryEvent{Time: time.Now(), Agent: "claude-code", EventName: "tool_result"})
		buf.AddEvent(TelemetryEvent{Time: time.Now(), Agent: "claude-code", EventName: "api_request"})
		buf.AddEvent(TelemetryEvent{Time: time.Now(), Agent: "codex", EventName: "tool_result"})

		events := buf.Events()
		require.Len(t, events, 3)
		assert.Equal(t, "tool_result", events[0].EventName)
		assert.Equal(t, "codex", events[2].Agent)
	})

	t.Run("assigns sequential IDs", func(t *testing.T) {
		buf := NewTelemetryBuffer(10)
		buf.AddEvent(TelemetryEvent{EventName: "a"})
		buf.AddEvent(TelemetryEvent{EventName: "b"})
		buf.AddEvent(TelemetryEvent{EventName: "c"})

		events := buf.Events()
		require.Len(t, events, 3)
		assert.Equal(t, uint64(1), events[0].ID)
		assert.Equal(t, uint64(2), events[1].ID)
		assert.Equal(t, uint64(3), events[2].ID)
	})

	t.Run("overwrites oldest when full", func(t *testing.T) {
		buf := NewTelemetryBuffer(2)
		buf.AddEvent(TelemetryEvent{EventName: "a"})
		buf.AddEvent(TelemetryEvent{EventName: "b"})
		buf.AddEvent(TelemetryEvent{EventName: "c"})

		events := buf.Events()
		require.Len(t, events, 2)
		assert.Equal(t, "b", events[0].EventName)
		assert.Equal(t, "c", events[1].EventName)
	})
}

func TestTelemetryBuffer_EventsAfter(t *testing.T) {
	t.Run("zero afterID returns last 25", func(t *testing.T) {
		buf := NewTelemetryBuffer(100)
		for range 30 {
			buf.AddEvent(TelemetryEvent{EventName: "x"})
		}

		events := buf.EventsAfter(0)
		require.Len(t, events, 25)
		assert.Equal(t, uint64(6), events[0].ID)
	})

	t.Run("returns entries after given ID", func(t *testing.T) {
		buf := NewTelemetryBuffer(100)
		for range 10 {
			buf.AddEvent(TelemetryEvent{EventName: "x"})
		}

		events := buf.EventsAfter(7)
		require.Len(t, events, 3)
		assert.Equal(t, uint64(8), events[0].ID)
	})

	t.Run("returns nil when no new entries", func(t *testing.T) {
		buf := NewTelemetryBuffer(100)
		for range 5 {
			buf.AddEvent(TelemetryEvent{EventName: "x"})
		}
		assert.Nil(t, buf.EventsAfter(5))
	})
}

func TestTelemetryBuffer_Metrics(t *testing.T) {
	t.Run("stores and retrieves metrics", func(t *testing.T) {
		buf := NewTelemetryBuffer(100)
		buf.UpdateMetric(MetricSummary{
			Name:       "claude_code.token.usage",
			Agent:      "claude-code",
			Value:      1234,
			Attributes: map[string]string{"type": "input"},
		})
		buf.UpdateMetric(MetricSummary{
			Name:  "claude_code.cost.usage",
			Agent: "claude-code",
			Value: 0.42,
		})

		metrics := buf.Metrics()
		require.Len(t, metrics, 2)
	})

	t.Run("upserts existing metric", func(t *testing.T) {
		buf := NewTelemetryBuffer(100)
		buf.UpdateMetric(MetricSummary{
			Name: "claude_code.token.usage", Agent: "claude-code", Value: 100,
			Attributes: map[string]string{"type": "input"},
		})
		buf.UpdateMetric(MetricSummary{
			Name: "claude_code.token.usage", Agent: "claude-code", Value: 200,
			Attributes: map[string]string{"type": "input"},
		})

		metrics := buf.Metrics()
		require.Len(t, metrics, 1)
		assert.Equal(t, float64(200), metrics[0].Value)
	})

	t.Run("different model produces separate series", func(t *testing.T) {
		buf := NewTelemetryBuffer(100)
		buf.UpdateMetric(MetricSummary{
			Name: "claude_code.cost.usage", Agent: "claude-code", Value: 0.10,
			Attributes: map[string]string{"model": "opus"},
		})
		buf.UpdateMetric(MetricSummary{
			Name: "claude_code.cost.usage", Agent: "claude-code", Value: 0.20,
			Attributes: map[string]string{"model": "haiku"},
		})

		metrics := buf.Metrics()
		require.Len(t, metrics, 2)
	})

	t.Run("respects cardinality cap", func(t *testing.T) {
		buf := NewTelemetryBuffer(100)
		buf.maxMetricSeries = 3 // override for test
		for i := range 5 {
			buf.UpdateMetric(MetricSummary{
				Name: fmt.Sprintf("metric_%d", i), Agent: "test", Value: float64(i),
			})
		}

		metrics := buf.Metrics()
		assert.Len(t, metrics, 3)
	})
}

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

	assert.Equal(t, 2.0, byName["api.count{model=claude-opus-4-6}"])
	assert.Equal(t, 4000.0, byName["api.duration{model=claude-opus-4-6}"])
	assert.Equal(t, 2.0, byName["event_type.count{type=api_request}"])
	assert.Equal(t, 4000.0, byName["event_type.duration{type=api_request}"])
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

	assert.Equal(t, 2.0, byName["tool.count{type=Bash}"])
	assert.Equal(t, 200.0, byName["tool.duration{type=Bash}"])
	assert.Equal(t, 710.0, byName["tool.result_size{type=Bash}"])
	assert.Equal(t, 510.0, byName["tool.result_size_max{type=Bash}"])
}

func TestUpdateMaxMetricLocked(t *testing.T) {
	buf := NewTelemetryBuffer(100)

	// First value sets the baseline.
	buf.mu.Lock()
	buf.updateMaxMetricLocked(MetricSummary{
		Name: "tool.result_size_max", Agent: "test", Value: 100,
		Attributes: map[string]string{"type": "Read"},
	})
	buf.mu.Unlock()

	// Smaller value does not replace.
	buf.mu.Lock()
	buf.updateMaxMetricLocked(MetricSummary{
		Name: "tool.result_size_max", Agent: "test", Value: 50,
		Attributes: map[string]string{"type": "Read"},
	})
	buf.mu.Unlock()

	metrics := buf.Metrics()
	require.Len(t, metrics, 1)
	assert.Equal(t, 100.0, metrics[0].Value)

	// Larger value replaces.
	buf.mu.Lock()
	buf.updateMaxMetricLocked(MetricSummary{
		Name: "tool.result_size_max", Agent: "test", Value: 200,
		Attributes: map[string]string{"type": "Read"},
	})
	buf.mu.Unlock()

	metrics = buf.Metrics()
	require.Len(t, metrics, 1)
	assert.Equal(t, 200.0, metrics[0].Value)
}
