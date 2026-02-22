package proxy

import "strconv"

// deriveClaudeCodeMetrics creates derived metric summaries from Claude Code events.
// Caller must hold b.mu.
func (b *TelemetryBuffer) deriveClaudeCodeMetrics(e TelemetryEvent) {
	durationStr := e.Attrs["duration_ms"]
	duration, _ := strconv.ParseFloat(durationStr, 64)

	switch e.EventName {
	case "api_request":
		model := e.Attrs["model"]
		if model == "" {
			return
		}
		b.updateMetricLocked(MetricSummary{
			Name: "claude_code.api.count", Agent: e.Agent, Value: 1, IsDelta: true,
			Attributes: map[string]string{"model": model},
		})
		if durationStr != "" {
			b.updateMetricLocked(MetricSummary{
				Name: "claude_code.api.duration", Agent: e.Agent, Value: duration, IsDelta: true,
				Attributes: map[string]string{"model": model},
			})
		}

	case "tool_result":
		toolName := e.Attrs["tool_name"]
		if toolName == "" {
			return
		}
		b.updateMetricLocked(MetricSummary{
			Name: "claude_code.tool.count", Agent: e.Agent, Value: 1, IsDelta: true,
			Attributes: map[string]string{"type": toolName},
		})
		if durationStr != "" {
			b.updateMetricLocked(MetricSummary{
				Name: "claude_code.tool.duration", Agent: e.Agent, Value: duration, IsDelta: true,
				Attributes: map[string]string{"type": toolName},
			})
		}
		if sizeStr := e.Attrs["tool_result_size_bytes"]; sizeStr != "" {
			size, _ := strconv.ParseFloat(sizeStr, 64)
			b.updateMetricLocked(MetricSummary{
				Name: "claude_code.tool.result_size", Agent: e.Agent, Value: size, IsDelta: true,
				Attributes: map[string]string{"type": toolName},
			})
			b.updateMaxMetricLocked(MetricSummary{
				Name: "claude_code.tool.result_size_max", Agent: e.Agent, Value: size,
				Attributes: map[string]string{"type": toolName},
			})
		}

	default:
		return
	}

	// Per-event-type count and duration (only for matched events with duration).
	if durationStr != "" {
		b.updateMetricLocked(MetricSummary{
			Name: "claude_code.event_type.count", Agent: e.Agent, Value: 1, IsDelta: true,
			Attributes: map[string]string{"type": e.EventName},
		})
		b.updateMetricLocked(MetricSummary{
			Name: "claude_code.event_type.duration", Agent: e.Agent, Value: duration, IsDelta: true,
			Attributes: map[string]string{"type": e.EventName},
		})
	}
}
