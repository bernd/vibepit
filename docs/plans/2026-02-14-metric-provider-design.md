# Metric Provider Interface Design

## Problem

The Metrics tab currently displays raw metric name:value pairs for all agents.
Different agents structure their OTLP metrics differently (e.g. Claude Code uses
`claude_code.token.usage` with type attributes, Codex uses `codex.*`
namespacing). Raw display is hard to read and wastes screen space.

## Decision

Add a `telemetry/` package with a function registry that maps metric name
prefixes to agent-specific formatters. Each formatter takes an agent's raw
`[]proxy.MetricSummary` and returns `[]string` plain-text lines for display.

## Design

### Package: `telemetry/`

**`telemetry/format.go`** — Registry and dispatcher:

```go
type MetricFormatter func(agent string, metrics []proxy.MetricSummary) []string

var registry = map[string]MetricFormatter{
    "claude_code.": formatClaudeCode,
    "codex.":       formatCodex,
}

func FormatAgent(agent string, metrics []proxy.MetricSummary) []string
```

`FormatAgent` groups the agent's metrics by detected prefix, calls the matching
formatter for each group, and uses `formatGeneric` for unrecognized metrics.

**`telemetry/claude_code.go`** — Claude Code formatter:

Produces human-readable summary from `claude_code.*` metrics:

```
Cost:         $0.0621
Tokens:       4 input  524 output  40374 cache read  4606 cache write
Active time:  29.0s user  12.7s cli
Sessions:     1
```

Looks up specific metric names + type attributes. Skips lines where all values
are zero.

**`telemetry/codex.go`** — Codex formatter (stub):

Codex currently emits events not aggregated metrics. Stub falls through to
generic formatting until Codex adds real metrics.

**`telemetry/generic.go`** — Fallback:

Renders `name(type): value` for each metric. Same as current behavior.

**`telemetry/format_test.go`** — Table-driven tests for each formatter.

### Changes to `cmd/monitor_metrics.go`

`rebuildLines()` calls `telemetry.FormatAgent(agent, metrics)` instead of
building lines directly. Returned `[]string` become lines under agent headers.

### Detection

Metric name prefix matching (e.g. `claude_code.`, `codex.`). OTLP metric names
are namespaced by convention, so prefix detection is reliable.

### Fallback

Unknown agents (no matching prefix) get generic key:value rendering.

### No changes to `proxy/`

`MetricSummary` and `TelemetryBuffer` stay as-is. Formatting is a presentation
concern only.

## Future

The `telemetry/` package is designed to later host agent-specific event
formatters using the same registry pattern.
