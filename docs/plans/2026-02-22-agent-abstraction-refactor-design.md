# Agent Abstraction Refactor

## Problem

The telemetry system has agent-specific logic scattered across three layers
with significant duplication:

- **Metric derivation** (`proxy/telemetry.go`): one growing `deriveEventMetrics`
  switch statement mixing generic and agent-specific logic, with hardcoded event
  name pairs (`"api_request", "codex.api_request"`).
- **Metric formatting** (`telemetry/claude_code.go`, `telemetry/codex.go`):
  ~70% duplicated structure (accumulation loops, section rendering, format strings).
- **Event rendering** (`cmd/monitor_telemetry.go`): dual-name case arms, manageable
  but growing.

Additionally, derived metrics today use unprefixed names (`api.count`,
`tool.count`) which means `FormatAgent`'s prefix-based routing can miss them
when falling back to generic output.

## Architecture

OTLP data flows through three layers:

1. **Ingestion** (`proxy/otlp.go`) — generic, stores raw events and metrics
   with the `Agent` field set from `service.name`. No agent-specific logic.
2. **Aggregation** (`proxy/telemetry.go`) — derives summary metrics from raw
   events. Currently one function; this is where agent-specific logic should
   be isolated.
3. **Presentation** (`telemetry/`, `cmd/monitor_telemetry.go`) — formats
   metrics and events for display. Already per-agent for formatting; event
   rendering uses dual-name case arms.

Event names are preserved as-is from each agent. Claude Code sends unprefixed
names (`api_request`, `tool_result`). Codex sends prefixed names
(`codex.api_request`, `codex.tool_result`, `codex.sse_event`). No
normalization — the Agent field carries identity, event names carry semantics.

## Design

### Metric Derivation

Split `deriveEventMetrics` into isolated per-agent functions. No common deriver.
Each agent handles all its own events including event-type counts and durations.

```go
// proxy/telemetry.go
func (b *TelemetryBuffer) deriveEventMetrics(e TelemetryEvent) {
    b.deriveClaudeCodeMetrics(e)
    b.deriveCodexMetrics(e)
}
```

Each deriver checks its own event names and returns early for non-matching
events. Lives in a separate file. All derived metrics use the agent's namespace
prefix, fixing the prefix-routing issue in `FormatAgent`.

**`proxy/derive_claude_code.go`** handles:
- `api_request` -> `claude_code.api.count`, `claude_code.api.duration`
- `tool_result` -> `claude_code.tool.count`, `claude_code.tool.duration`,
  `claude_code.tool.result_size`, `claude_code.tool.result_size_max`
- Any matched event with `duration_ms` -> `claude_code.event_type.count`,
  `claude_code.event_type.duration`

**`proxy/derive_codex.go`** handles:
- `codex.api_request` -> `codex.api.count`, `codex.api.duration`
- `codex.tool_result` -> `codex.tool.count`, `codex.tool.duration`
- `codex.sse_event` (response.completed) -> `codex.token.*`, `codex.cost.usage`
- Any matched event with `duration_ms` -> `codex.event_type.count`,
  `codex.event_type.duration`

### Metric Formatting

Keep per-agent formatters. Extract shared rendering helpers into
`telemetry/sections.go`:

- `renderModelsSection(apiCount, apiDuration, modelCost, countW)` -- the Models
  table with count, avg latency, and optional per-model cost.
- `renderLatencySection(eventCount, eventDuration)` -- the Latency table.
- `renderToolsSection(toolCount, toolDuration, optional size maps)` -- the
  Tools table.

Each agent formatter becomes an orchestrator: accumulate its own prefixed
metrics, then call shared section renderers, adding agent-specific sections
(active time, pricing source notes, cost/1k output) inline.

Also fix `FormatAgent` to pass unmatched metrics to `formatGeneric` even in the
single-prefix case, so new metrics are never silently hidden.

### Event Rendering

Minimal changes. The dual-name case arms in `renderEventLine` and
`renderEventDetails` are kept as-is -- they're simple, readable, and each
agent's events have genuinely different names.

## Files

| File | Change |
|------|--------|
| `proxy/telemetry.go` | Remove agent-specific cases from `deriveEventMetrics`, keep as dispatcher |
| `proxy/derive_claude_code.go` | New -- Claude Code metric derivation |
| `proxy/derive_codex.go` | New -- Codex metric derivation (includes cost) |
| `proxy/derive_claude_code_test.go` | New -- tests moved from `telemetry_test.go` |
| `proxy/derive_codex_test.go` | New -- tests moved from `telemetry_test.go` |
| `telemetry/sections.go` | New -- shared section rendering helpers |
| `telemetry/format.go` | Fix single-prefix mode to include unmatched metrics |
| `telemetry/claude_code.go` | Use shared helpers, update metric names to prefixed |
| `telemetry/codex.go` | Use shared helpers, update metric names to prefixed |
| `telemetry/claude_code_test.go` | Update metric names |
| `telemetry/codex_test.go` | Update metric names |
