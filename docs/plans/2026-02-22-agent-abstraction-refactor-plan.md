# Agent Abstraction Refactor Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Split agent-specific metric derivation into isolated per-agent files with prefixed metric names, extract shared formatting helpers, and fix single-prefix routing.

**Architecture:** Each agent gets its own deriver file that checks event names and returns early for non-matching events. All derived metrics use the agent's namespace prefix (`claude_code.` or `codex.`). Shared formatting helpers reduce duplication between per-agent formatters.

**Tech Stack:** Go, testify

---

### Task 1: Create Claude Code metric deriver

**Files:**
- Create: `proxy/derive_claude_code.go`

**Step 1: Write the deriver**

```go
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
```

**Step 2: Verify it compiles**

Run: `go build ./proxy/`
Expected: success (not yet wired in)

**Step 3: Commit**

```bash
git add proxy/derive_claude_code.go
git commit -m "Add Claude Code metric deriver with prefixed metric names"
```

---

### Task 2: Create Codex metric deriver

**Files:**
- Create: `proxy/derive_codex.go`

**Step 1: Write the deriver**

```go
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
```

Note: `codex.sse_event` has no `duration_ms` attribute, so event_type metrics won't
be emitted for it (the `durationStr` check at the bottom handles this correctly).

**Step 2: Verify it compiles**

Run: `go build ./proxy/`
Expected: success

**Step 3: Commit**

```bash
git add proxy/derive_codex.go
git commit -m "Add Codex metric deriver with prefixed metric names"
```

---

### Task 3: Wire up derivers and update dispatcher

**Files:**
- Modify: `proxy/telemetry.go:206-299`

**Step 1: Replace `deriveEventMetrics` body**

Replace the entire `deriveEventMetrics` method with a dispatcher that calls both
per-agent derivers:

```go
// deriveEventMetrics creates derived metric summaries from event attributes.
// Caller must hold b.mu.
func (b *TelemetryBuffer) deriveEventMetrics(e TelemetryEvent) {
	b.deriveClaudeCodeMetrics(e)
	b.deriveCodexMetrics(e)
}
```

**Step 2: Run existing tests**

Run: `go test ./proxy/ -run TestDeriveEventMetrics -v`
Expected: FAIL — tests assert unprefixed metric names (`api.count`, `tool.count`, etc.)

**Step 3: Commit**

```bash
git add proxy/telemetry.go
git commit -m "Replace deriveEventMetrics body with per-agent dispatcher"
```

---

### Task 4: Update derivation tests for prefixed metric names

**Files:**
- Create: `proxy/derive_claude_code_test.go`
- Create: `proxy/derive_codex_test.go`
- Modify: `proxy/telemetry_test.go`

**Step 1: Create `proxy/derive_claude_code_test.go`**

Move and update `TestDeriveEventMetrics_APIRequest` and `TestDeriveEventMetrics_ToolResult`
from `telemetry_test.go`. Update all metric names to `claude_code.` prefix:

- `api.count` -> `claude_code.api.count`
- `api.duration` -> `claude_code.api.duration`
- `event_type.count` -> `claude_code.event_type.count`
- `event_type.duration` -> `claude_code.event_type.duration`
- `tool.count` -> `claude_code.tool.count`
- `tool.duration` -> `claude_code.tool.duration`
- `tool.result_size` -> `claude_code.tool.result_size`
- `tool.result_size_max` -> `claude_code.tool.result_size_max`

**Step 2: Create `proxy/derive_codex_test.go`**

Move and update `TestDeriveEventMetrics_CodexSSEEvent`,
`TestDeriveEventMetrics_CodexSSEEvent_IgnoresNonCompleted`,
`TestDeriveEventMetrics_CodexAPIRequest`, and
`TestDeriveEventMetrics_CodexToolResult` from `telemetry_test.go`.
Update metric names to `codex.` prefix:

- `api.count` -> `codex.api.count`
- `api.duration` -> `codex.api.duration`
- `event_type.count` -> `codex.event_type.count`
- `event_type.duration` -> `codex.event_type.duration`
- `tool.count` -> `codex.tool.count`
- `tool.duration` -> `codex.tool.duration`

**Step 3: Remove moved tests from `proxy/telemetry_test.go`**

Keep: `TestTelemetryBuffer_Events`, `TestTelemetryBuffer_EventsAfter`,
`TestTelemetryBuffer_Metrics`, `TestUpdateMaxMetricLocked`.

Remove: all `TestDeriveEventMetrics_*` functions.

**Step 4: Run tests**

Run: `go test ./proxy/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add proxy/derive_claude_code_test.go proxy/derive_codex_test.go proxy/telemetry_test.go
git commit -m "Split derivation tests into per-agent files with prefixed metric names"
```

---

### Task 5: Create shared formatting helpers

**Files:**
- Create: `telemetry/sections.go`

**Step 1: Write shared helpers**

Extract the common rendering patterns from `claude_code.go` and `codex.go`:

```go
package telemetry

import "fmt"

// renderModelsSection renders a Models table with count, avg latency, and
// optional per-model cost.
func renderModelsSection(apiCount, apiDuration, modelCost map[string]float64, costAnnotation func(string) string) []string {
	if len(apiCount) == 0 {
		return nil
	}
	models := sortedKeys(apiCount)
	nameW := maxLen(models)
	countW := countWidth(apiCount)
	var lines []string
	lines = append(lines, "")
	lines = append(lines, "  Models")
	for _, model := range models {
		count := apiCount[model]
		avgMs := apiDuration[model] / count
		costStr := ""
		if c, ok := modelCost[model]; ok {
			costStr = fmt.Sprintf("   $%.4f", c)
			if costAnnotation != nil {
				if note := costAnnotation(model); note != "" {
					costStr += note
				}
			}
		}
		lines = append(lines, fmt.Sprintf("    %-*s  %*.0f req   avg %5.0fms%s", nameW, model, countW, count, avgMs, costStr))
	}
	return lines
}

// renderLatencySection renders a Latency table of event types with count and avg duration.
func renderLatencySection(eventCount, eventDuration map[string]float64) []string {
	if len(eventCount) == 0 {
		return nil
	}
	types := sortedKeys(eventCount)
	nameW := maxLen(types)
	countW := countWidth(eventCount)
	var lines []string
	lines = append(lines, "  Latency")
	for _, typ := range types {
		count := eventCount[typ]
		avgMs := eventDuration[typ] / count
		lines = append(lines, fmt.Sprintf("    %-*s  %*.0f calls   avg %5.0fms", nameW, typ, countW, count, avgMs))
	}
	return lines
}

// renderToolsSection renders a Tools table with count, avg duration, and
// optional result size columns.
func renderToolsSection(toolCount, toolDuration, toolSize, toolSizeMax map[string]float64) []string {
	if len(toolCount) == 0 {
		return nil
	}
	tools := sortedKeys(toolCount)
	nameW := maxLen(tools)
	countW := countWidth(toolCount)
	hasSize := len(toolSize) > 0
	var lines []string
	lines = append(lines, "  Tools")
	for _, tool := range tools {
		count := toolCount[tool]
		avgMs := toolDuration[tool] / count
		if hasSize {
			avgSize := toolSize[tool] / count
			maxSize := toolSizeMax[tool]
			lines = append(lines, fmt.Sprintf("    %-*s  %*.0f calls   avg %5.0fms   avg %5.0fB / max %5.0fB",
				nameW, tool, countW, count, avgMs, avgSize, maxSize))
		} else {
			lines = append(lines, fmt.Sprintf("    %-*s  %*.0f calls   avg %5.0fms", nameW, tool, countW, count, avgMs))
		}
	}
	return lines
}
```

**Step 2: Verify it compiles**

Run: `go build ./telemetry/`
Expected: success

**Step 3: Commit**

```bash
git add telemetry/sections.go
git commit -m "Add shared section rendering helpers for metric formatters"
```

---

### Task 6: Update Claude Code formatter to use prefixed names and shared helpers

**Files:**
- Modify: `telemetry/claude_code.go`

**Step 1: Update metric name cases**

Change the switch cases in `formatClaudeCode` to use prefixed names:
- `"api.count"` -> `"claude_code.api.count"`
- `"api.duration"` -> `"claude_code.api.duration"`
- `"event_type.count"` -> `"claude_code.event_type.count"`
- `"event_type.duration"` -> `"claude_code.event_type.duration"`
- `"tool.count"` -> `"claude_code.tool.count"`
- `"tool.duration"` -> `"claude_code.tool.duration"`
- `"tool.result_size"` -> `"claude_code.tool.result_size"`
- `"tool.result_size_max"` -> `"claude_code.tool.result_size_max"`

**Step 2: Replace Models, Latency, and Tools sections with shared helpers**

Replace the Models section (lines 154-169) with:
```go
lines = append(lines, renderModelsSection(apiCount, apiDuration, modelCost, nil)...)
```

Replace the Latency section (lines 203-213) with:
```go
lines = append(lines, renderLatencySection(eventCount, eventDuration)...)
```

Replace the Tools section (lines 216-229) with:
```go
lines = append(lines, renderToolsSection(toolCount, toolDuration, toolSize, toolSizeMax)...)
```

Keep the KPI, Tokens, and Efficiency sections inline (they have Claude Code-specific
logic: active time, cost/1k output, cache hit).

**Step 3: Run tests (expect failures from old metric names)**

Run: `go test ./telemetry/ -run TestFormatClaudeCode -v`
Expected: FAIL

**Step 4: Commit**

```bash
git add telemetry/claude_code.go
git commit -m "Update Claude Code formatter to use prefixed metric names and shared helpers"
```

---

### Task 7: Update Claude Code formatter tests

**Files:**
- Modify: `telemetry/claude_code_test.go`

**Step 1: Update metric names in test data**

- `"api.count"` -> `"claude_code.api.count"`
- `"api.duration"` -> `"claude_code.api.duration"`
- `"event_type.count"` -> `"claude_code.event_type.count"`
- `"event_type.duration"` -> `"claude_code.event_type.duration"`
- `"tool.count"` -> `"claude_code.tool.count"`
- `"tool.duration"` -> `"claude_code.tool.duration"`
- `"tool.result_size"` -> `"claude_code.tool.result_size"`
- `"tool.result_size_max"` -> `"claude_code.tool.result_size_max"`

**Step 2: Run tests**

Run: `go test ./telemetry/ -run TestFormatClaudeCode -v`
Expected: PASS

**Step 3: Commit**

```bash
git add telemetry/claude_code_test.go
git commit -m "Update Claude Code formatter tests for prefixed metric names"
```

---

### Task 8: Update Codex formatter to use prefixed names and shared helpers

**Files:**
- Modify: `telemetry/codex.go`

**Step 1: Update metric name cases**

Change the switch cases in `formatCodex` to use prefixed names:
- `"api.count"` -> `"codex.api.count"`
- `"api.duration"` -> `"codex.api.duration"`
- `"event_type.count"` -> `"codex.event_type.count"`
- `"event_type.duration"` -> `"codex.event_type.duration"`
- `"tool.count"` -> `"codex.tool.count"`
- `"tool.duration"` -> `"codex.tool.duration"`

**Step 2: Replace Models, Latency, and Tools sections with shared helpers**

Replace the Models section with:
```go
lines = append(lines, renderModelsSection(apiCount, apiDuration, modelCost, func(model string) string {
    if source, ok := proxy.PricingSource(model); ok && source != model {
        return fmt.Sprintf(" (priced as %s)", source)
    }
    return ""
})...)
```

Replace the Latency section with:
```go
lines = append(lines, renderLatencySection(eventCount, eventDuration)...)
```

Replace the Tools section with:
```go
lines = append(lines, renderToolsSection(toolCount, toolDuration, nil, nil)...)
```

Keep KPI, Tokens, and Efficiency sections inline (Codex-specific: pricing source
notes, no active time, different cache field names).

**Step 3: Run tests (expect failures)**

Run: `go test ./telemetry/ -run TestFormatCodex -v`
Expected: FAIL

**Step 4: Commit**

```bash
git add telemetry/codex.go
git commit -m "Update Codex formatter to use prefixed metric names and shared helpers"
```

---

### Task 9: Update Codex formatter tests

**Files:**
- Modify: `telemetry/codex_test.go`

**Step 1: Update metric names in test data**

- `"api.count"` -> `"codex.api.count"`
- `"api.duration"` -> `"codex.api.duration"`
- `"event_type.count"` -> `"codex.event_type.count"`
- `"event_type.duration"` -> `"codex.event_type.duration"`
- `"tool.count"` -> `"codex.tool.count"`
- `"tool.duration"` -> `"codex.tool.duration"`

**Step 2: Run tests**

Run: `go test ./telemetry/ -run TestFormatCodex -v`
Expected: PASS

**Step 3: Commit**

```bash
git add telemetry/codex_test.go
git commit -m "Update Codex formatter tests for prefixed metric names"
```

---

### Task 10: Fix FormatAgent single-prefix mode for unmatched metrics

**Files:**
- Modify: `telemetry/format.go:72-88`

**Step 1: Update single-prefix case**

The current single-prefix case passes all metrics to the formatter but never
calls `formatGeneric` for unmatched metrics. Fix by splitting matched and
unmatched, passing only matched to the formatter, then appending generic output:

```go
if len(prefixes) == 1 {
    fn := registry[prefixes[0]]
    lines = append(lines, fn(agent, append(matched[prefixes[0]], unmatched...))...)
    if len(unmatched) > 0 {
        lines = append(lines, formatGeneric(agent, unmatched)...)
    }
} else {
```

Wait — the current code passes `metrics` (all of them) to the single formatter.
The formatters need to see both prefixed metrics (e.g. `claude_code.cost.usage`)
AND derived prefixed metrics (e.g. `claude_code.api.count`). Since all derived
metrics are now prefixed, they'll all be in `matched[prefix]`. The `unmatched`
list would only contain truly unknown metrics. So we should:

1. Pass `matched[prefix]` to the formatter (not all metrics — all relevant ones
   are now prefixed and will be in `matched`).
2. If there are unmatched metrics, also pass them to `formatGeneric`.

```go
if len(prefixes) == 1 {
    fn := registry[prefixes[0]]
    lines = append(lines, fn(agent, matched[prefixes[0]])...)
    if len(unmatched) > 0 {
        lines = append(lines, formatGeneric(agent, unmatched)...)
    }
}
```

**Step 2: Run all tests**

Run: `go test ./telemetry/ -v`
Expected: PASS

**Step 3: Commit**

```bash
git add telemetry/format.go
git commit -m "Fix FormatAgent to show unmatched metrics in single-prefix mode"
```

---

### Task 11: Run full test suite and verify

**Step 1: Run all tests**

Run: `make test`
Expected: PASS

**Step 2: Verify no unprefixed derived metric names remain**

Run: `grep -rn '"api\.count"' proxy/ telemetry/` and
`grep -rn '"tool\.count"' proxy/ telemetry/` and
`grep -rn '"event_type\.count"' proxy/ telemetry/`

Expected: No matches (all should be prefixed now).

---

## Summary of metric name changes

| Old name | New name (Claude Code) | New name (Codex) |
|----------|----------------------|------------------|
| `api.count` | `claude_code.api.count` | `codex.api.count` |
| `api.duration` | `claude_code.api.duration` | `codex.api.duration` |
| `event_type.count` | `claude_code.event_type.count` | `codex.event_type.count` |
| `event_type.duration` | `claude_code.event_type.duration` | `codex.event_type.duration` |
| `tool.count` | `claude_code.tool.count` | `codex.tool.count` |
| `tool.duration` | `claude_code.tool.duration` | `codex.tool.duration` |
| `tool.result_size` | `claude_code.tool.result_size` | *(not used)* |
| `tool.result_size_max` | `claude_code.tool.result_size_max` | *(not used)* |
