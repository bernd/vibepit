package proxy

import "strconv"

// deriveCodexMetrics creates derived metric summaries from Codex events.
// Caller must hold b.mu.
func (b *TelemetryBuffer) deriveCodexMetrics(e TelemetryEvent) {
	durationStr := e.Attrs["duration_ms"]
	duration, _ := strconv.ParseFloat(durationStr, 64)

	switch e.EventName {
	case "codex.api_request":
		model := e.Attrs["model"]
		if model == "" {
			return
		}
		b.updateMetricLocked(MetricSummary{
			Name: "codex.api.count", Agent: e.Agent, Value: 1, IsDelta: true,
			Attributes: map[string]string{"model": model},
		})
		if durationStr != "" {
			b.updateMetricLocked(MetricSummary{
				Name: "codex.api.duration", Agent: e.Agent, Value: duration, IsDelta: true,
				Attributes: map[string]string{"model": model},
			})
		}

	case "codex.tool_result":
		toolName := e.Attrs["tool_name"]
		if toolName == "" {
			return
		}
		b.updateMetricLocked(MetricSummary{
			Name: "codex.tool.count", Agent: e.Agent, Value: 1, IsDelta: true,
			Attributes: map[string]string{"type": toolName},
		})
		if durationStr != "" {
			b.updateMetricLocked(MetricSummary{
				Name: "codex.tool.duration", Agent: e.Agent, Value: duration, IsDelta: true,
				Attributes: map[string]string{"type": toolName},
			})
		}

	case "codex.sse_event":
		if e.Attrs["event.kind"] != "response.completed" {
			return
		}
		model := e.Attrs["model"]
		var tokInput, tokOutput, tokCached float64
		for _, tok := range []struct {
			attr, metric string
			dest         *float64
		}{
			{"input_token_count", "codex.token.input", &tokInput},
			{"output_token_count", "codex.token.output", &tokOutput},
			{"cached_token_count", "codex.token.cached", &tokCached},
			{"reasoning_token_count", "codex.token.reasoning", nil},
		} {
			if valStr := e.Attrs[tok.attr]; valStr != "" {
				val, _ := strconv.ParseFloat(valStr, 64)
				b.updateMetricLocked(MetricSummary{
					Name: tok.metric, Agent: e.Agent, Value: val, IsDelta: true,
					Attributes: map[string]string{"model": model},
				})
				if tok.dest != nil {
					*tok.dest = val
				}
			}
		}
		if cost := tokenCost(model, tokInput, tokOutput, tokCached); cost > 0 {
			b.updateMetricLocked(MetricSummary{
				Name: "codex.cost.usage", Agent: e.Agent, Value: cost, IsDelta: true,
				Attributes: map[string]string{"model": model},
			})
		}

	default:
		return
	}

	// Per-event-type count and duration (only for matched events with duration).
	if durationStr != "" {
		b.updateMetricLocked(MetricSummary{
			Name: "codex.event_type.count", Agent: e.Agent, Value: 1, IsDelta: true,
			Attributes: map[string]string{"type": e.EventName},
		})
		b.updateMetricLocked(MetricSummary{
			Name: "codex.event_type.duration", Agent: e.Agent, Value: duration, IsDelta: true,
			Attributes: map[string]string{"type": e.EventName},
		})
	}
}
