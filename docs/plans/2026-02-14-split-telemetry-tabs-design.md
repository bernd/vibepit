# Split Telemetry into Events + Metrics Tabs

**Date:** 2026-02-14
**Status:** Approved

## Problem

The current Telemetry tab crams metrics into a single-line-per-agent header
above the event stream. Metrics are hard to read and steal vertical space from
events.

## Design

Split the two-tab monitor (`Network | Telemetry`) into three tabs:

```
[Network]  Events  Metrics
```

Keys `1`/`2`/`3` select tabs directly; `tab` cycles.

### Events tab (refactored telemetryScreen)

- Full viewport for the event stream — no metrics header.
- Keeps cursor navigation, agent filter (`f`), auto-tail.
- Removes `renderMetricsHeader()` and `metricsHeaderHeight()`.
- Cursor VpHeight calculation simplifies to `w.VpHeight() - heightOffset`.

### Metrics tab (new metricsScreen)

- Grouped by agent, one metric per line:

  ```
  claude-code
    token_usage(input): 12450
    token_usage(output): 3200
    api_calls: 47
  aider
    token_usage(input): 8100
    api_calls: 23
  ```

- Agent name styled as a header line (orange), metrics indented below.
- Polls `TelemetryMetrics()` on the same 1s interval.
- Agent filter (`f`) cycles through known agents.
- Cursor navigation over lines.
- Shows disabled message when telemetry client is nil.

### Tab container changes

- `tabbedMonitorScreen` gains a third screen entry.
- Tab keys updated: `1`=Network, `2`=Events, `3`=Metrics.
- `heightOffset` set on all three sub-screens.

## Files changed

- `cmd/monitor_tabs.go` — third tab entry, key `3`.
- `cmd/monitor_telemetry.go` — remove metrics header logic, rename to events
  focus.
- `cmd/monitor_metrics.go` — new file, `metricsScreen` implementation.
- `cmd/monitor.go` — construct three screens.
