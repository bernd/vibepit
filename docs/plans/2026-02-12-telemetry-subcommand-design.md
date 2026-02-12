# Telemetry Subcommand Design

## Problem

No way to get raw OTLP event data from the CLI. The monitor TUI renders
events visually, but there's no machine-readable output for piping into
`jq`, logging to a file, or integrating with other tools.

## Solution

Add `vibepit telemetry` subcommand that streams raw telemetry data as JSON
lines to stdout.

## Command

```
vibepit telemetry [--events] [--metrics] [--agent <name>]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--events` | true | Stream telemetry events |
| `--metrics` | true | Stream metric snapshots |
| `--agent` | (all) | Filter by agent name |

### Behavior

1. Discover running session (same as `monitor`).
2. Create `ControlClient` with mTLS credentials.
3. Poll every 1 second:
   - Events: `GET /telemetry/events?after=<cursor>`, output each as JSON line.
   - Metrics: `GET /telemetry/metrics`, output each as JSON line.
4. Exit cleanly on SIGINT/SIGTERM.

### Output Format

Events:
```json
{"id":42,"time":"2026-02-12T10:00:00Z","agent":"claude","event_name":"tool_result","attrs":{"tool_name":"Read","duration_ms":"125"}}
```

Metrics (distinguished by `"type":"metric"`):
```json
{"type":"metric","name":"api_requests","agent":"claude","value":42.5,"attributes":{"model":"opus"}}
```

## Implementation

Single new file `cmd/telemetry.go`, registered in `cmd/root.go`.

Reuses existing `ControlClient.TelemetryEventsAfter()` and
`ControlClient.TelemetryMetrics()`. Session discovery via
`discoverSession()`. Polling pattern matches the monitor TUI (1s interval).
