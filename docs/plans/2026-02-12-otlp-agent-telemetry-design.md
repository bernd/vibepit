# OTLP Agent Telemetry

The vibepit proxy receives OpenTelemetry data from AI agents (Claude Code,
Codex, etc.) running in the dev container, stores it, and displays it in a new
"Telemetry" tab in the monitoring TUI.

## Approach

Native Go OTLP receiver: lightweight HTTP handlers deserializing protobuf using
the official `go.opentelemetry.io/proto/otlp` types. Fits the existing pattern
of everything in one binary. No gRPC, no collector SDK.

Alternatives considered:

- **OpenTelemetry Collector SDK** — battle-tested but massive dependency tree
  (~100+ modules), fights the existing server architecture.
- **Raw protobuf parsing** — smallest footprint but fragile across schema
  changes, loses type safety.

## Components

### 1. OTLP Receiver (`proxy/otlp.go`)

HTTP server on the isolated network, random high port (same allocation as proxy
and control API ports via `config.RandomProxyPort`).

Endpoints:

- `POST /v1/logs` — protobuf-encoded `ExportLogsServiceRequest`
- `POST /v1/metrics` — protobuf-encoded `ExportMetricsServiceRequest`

No authentication — only reachable from the isolated network. Only starts when
`agent_telemetry.enable` config is true.

**Ingestion limits** (defense against malicious/runaway processes in the dev
container):

- **Request body size:** 4 MB max (`http.MaxBytesReader`). OTLP exports are
  typically small; this stops memory exhaustion from oversized payloads.
- **Rate limiting:** Token bucket, ~100 requests/second. Enough for multiple
  agents exporting at short intervals, blocks flood attempts.
- **Metric cardinality cap:** Maximum 1,000 unique metric series (name + agent +
  attribute key). New series beyond the cap are silently dropped. Prevents
  unbounded map growth from crafted high-cardinality attributes.
- **Attribute limits:** Maximum 64 attributes per event, attribute keys/values
  truncated at 256 characters. Prevents memory bloat from oversized payloads that
  pass the body size check after decompression.

Decodes protobuf, extracts `service.name` from OTLP resource attributes as the
`Agent` field (falls back to `"unknown"`), flattens event attributes, and writes
into a `TelemetryBuffer`.

### 2. Telemetry Buffer (`proxy/telemetry.go`)

Circular ring buffer (10,000 events) plus a metric summary map. Same
thread-safe, ID-paginated pattern as `LogBuffer`.

```go
type TelemetryEvent struct {
    ID        uint64            `json:"id"`
    Time      time.Time         `json:"time"`
    Agent     string            `json:"agent"`
    EventName string            `json:"event_name"`
    Attrs     map[string]string `json:"attrs"`
}

type MetricSummary struct {
    Name       string            `json:"name"`
    Agent      string            `json:"agent"`
    Value      float64           `json:"value"`
    Attributes map[string]string `json:"attributes,omitempty"`
}
```

Buffer API:

- `AddEvent(event)` — thread-safe append with ID assignment.
- `EventsAfter(afterID)` — ID-based pagination for polling.
- `UpdateMetrics(...)` — upsert metric summaries from each export.
- `Metrics()` — snapshot of current metric values.

Metrics keyed by name + agent, storing the latest cumulative value.

### 3. Control API Extensions (`proxy/api.go`)

New endpoints on the existing mTLS-secured control API:

- `GET /telemetry/events?after=<id>[&agent=<name>]`
- `GET /telemetry/metrics`

`NewControlAPI` takes `TelemetryBuffer` as an additional parameter.

### 4. Control Client Extensions (`cmd/control.go`)

```go
func (c *ControlClient) TelemetryEventsAfter(afterID uint64) ([]proxy.TelemetryEvent, error)
func (c *ControlClient) TelemetryMetrics() ([]proxy.MetricSummary, error)
```

Same mTLS transport and JSON encoding as existing methods.

### 5. Tab Navigation (`cmd/monitor_tabs.go`)

`tabbedMonitorScreen` wrapping `monitorScreen` and `telemetryScreen`:

```go
type tabbedMonitorScreen struct {
    tabs      []string // ["Network", "Telemetry"]
    activeTab int
    network   *monitorScreen
    telemetry *telemetryScreen
}
```

Implements `tui.Screen`. Intercepts `Tab`/`1`/`2` for switching, delegates
everything else to the active sub-screen. Renders a one-line tab bar above
content. `vpHeight` shrinks by 1 for the tab bar.

### 6. Telemetry Screen (`cmd/monitor_telemetry.go`)

Implements `tui.Screen`. 1-second polling via the control client.

**Header area** (top lines): compact metric summary per agent:

```
tokens: 12.4k in / 3.2k out   cost: $0.42   active: 5m 12s
```

**Activity stream** (remaining viewport): scrollable event list with cursor
navigation:

```
[15:04:05] claude-code  tool_result   Read  ✓  42ms
[15:04:06] claude-code  api_request   opus-4  1.2k→384 tok  $0.03  820ms
[15:04:08] codex        tool_result   shell  ✓  112ms
```

Events rendered with context-specific columns extracted from `Attrs` (tool name,
model, tokens, cost, duration, success/error). Unknown event types show raw name
and key attrs.

**Agent filtering:** `f` key cycles through available agents (all → agent1 →
agent2 → all). Filter state shown in footer. Client-side filtering.

### 7. Dev Container Wiring (`cmd/run.go`, `container/client.go`)

When `agent_telemetry.enable` is true, inject into the dev container
environment:

- `OTEL_EXPORTER_OTLP_ENDPOINT=http://<proxyIP>:<otlpPort>`
- `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`
- `CLAUDE_CODE_ENABLE_TELEMETRY=1`

**NO_PROXY fix:** The dev container currently sets
`NO_PROXY=localhost,127.0.0.1`, so requests to `<proxyIP>` would be routed
through the HTTP proxy and blocked by allowlist checks. Add `<proxyIP>` to
`NO_PROXY`/`no_proxy` so all direct-to-proxy traffic (OTLP endpoint, and any
future proxy-hosted services) bypasses the HTTP proxy. This change applies
unconditionally — not just when telemetry is enabled — since the proxy IP should
never be proxied through itself.

### 8. Config (`config/`)

New option `agent_telemetry.enable` (default: `true`).

When enabled: OTLP receiver starts, env vars injected, telemetry tab active.

When disabled: no OTLP receiver, no env injection, telemetry tab shows
"telemetry disabled" message.

## New Dependencies

- `go.opentelemetry.io/proto/otlp/collector/logs/v1`
- `go.opentelemetry.io/proto/otlp/collector/metrics/v1`

OTel proto types are already transitively in `go.mod`.

## What Doesn't Change

- `LogBuffer`, HTTP proxy, DNS server, existing control API endpoints.
- `tui.Window`, `tui.Screen` interface.
- mTLS setup, session management, container lifecycle.
