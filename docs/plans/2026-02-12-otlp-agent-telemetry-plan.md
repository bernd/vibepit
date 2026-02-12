# OTLP Agent Telemetry Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** The vibepit proxy receives OTLP telemetry from AI agents in the dev container and displays it in a new "Telemetry" tab in the monitoring TUI.

**Architecture:** A new HTTP server in the proxy container handles OTLP HTTP/protobuf on the isolated network. Decoded events and metrics are stored in a `TelemetryBuffer` (circular ring buffer, same pattern as `LogBuffer`). The control API exposes the data over mTLS. The monitoring TUI gains tab navigation and a telemetry screen.

**Tech Stack:** Go, `go.opentelemetry.io/proto/otlp` (protobuf types), `google.golang.org/protobuf/proto` (deserializer), Bubble Tea (TUI), existing proxy/mTLS infrastructure.

**Design doc:** `docs/plans/2026-02-12-otlp-agent-telemetry-design.md`

---

### Task 1: Add OTLP proto dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

**Step 1: Add the direct dependency**

Pin specific versions to ensure reproducibility. If running in a sandbox without
network access, edit `go.mod` directly instead:

```bash
go get go.opentelemetry.io/proto/otlp@v1.5.0
go get google.golang.org/protobuf@v1.36.6
```

These provide:
- `go.opentelemetry.io/proto/otlp/collector/logs/v1` — `ExportLogsServiceRequest`
- `go.opentelemetry.io/proto/otlp/collector/metrics/v1` — `ExportMetricsServiceRequest`
- `go.opentelemetry.io/proto/otlp/common/v1` — `AnyValue`, `KeyValue`
- `go.opentelemetry.io/proto/otlp/logs/v1` — `LogRecord`, `ResourceLogs`
- `go.opentelemetry.io/proto/otlp/metrics/v1` — `Metric`, `Sum`, `NumberDataPoint`
- `go.opentelemetry.io/proto/otlp/resource/v1` — `Resource`

**Step 2: Tidy**

```bash
go mod tidy
```

**Step 3: Verify existing tests still pass**

```bash
make test
```

Expected: PASS

**Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "Add OTLP proto dependency for telemetry receiver"
```

---

### Task 2: Telemetry buffer — types and core buffer

**Files:**
- Create: `proxy/telemetry.go`
- Create: `proxy/telemetry_test.go`

**Step 1: Write the failing tests**

Create `proxy/telemetry_test.go`. Follow the patterns in `proxy/log_test.go` — table-driven subtests, `testify/assert` and `testify/require`.

```go
package proxy

import (
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
			Name:  "claude_code.token.usage",
			Agent: "claude-code",
			Value: 1234,
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
```

Note: add `"fmt"` to imports for the cardinality test.

**Step 2: Run tests to verify they fail**

```bash
go test ./proxy/ -run TestTelemetryBuffer -v
```

Expected: FAIL — types don't exist yet.

**Step 3: Write the implementation**

Create `proxy/telemetry.go`:

```go
package proxy

import (
	"sync"
	"time"
)

const (
	TelemetryBufferCapacity  = 10000
	DefaultMaxMetricSeries   = 1000
	MaxAttrsPerEvent         = 64
	MaxAttrValueLen          = 256
)

type TelemetryEvent struct {
	ID        uint64            `json:"id"`
	Time      time.Time         `json:"time"`
	Agent     string            `json:"agent"`
	EventName string            `json:"event_name"`
	Attrs     map[string]string `json:"attrs,omitempty"`
}

type MetricSummary struct {
	Name       string            `json:"name"`
	Agent      string            `json:"agent"`
	Value      float64           `json:"value"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// metricKey uniquely identifies a metric series. Only name, agent, and the
// "type" attribute are used for identity — other attributes are ignored to
// keep series count stable and avoid churn from varying resource attributes.
type metricKey struct {
	name     string
	agent    string
	metricType string // value of the "type" attribute, if any
}

type TelemetryBuffer struct {
	mu      sync.Mutex
	events  []TelemetryEvent
	cap     int
	pos     int
	full    bool
	nextID  uint64
	metrics map[metricKey]*MetricSummary

	maxMetricSeries int // exposed for testing
}

func NewTelemetryBuffer(capacity int) *TelemetryBuffer {
	return &TelemetryBuffer{
		events:          make([]TelemetryEvent, capacity),
		cap:             capacity,
		nextID:          1,
		metrics:         make(map[metricKey]*MetricSummary),
		maxMetricSeries: DefaultMaxMetricSeries,
	}
}

func (b *TelemetryBuffer) AddEvent(event TelemetryEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Enforce attribute limits.
	if len(event.Attrs) > MaxAttrsPerEvent {
		trimmed := make(map[string]string, MaxAttrsPerEvent)
		i := 0
		for k, v := range event.Attrs {
			if i >= MaxAttrsPerEvent {
				break
			}
			trimmed[k] = truncate(v, MaxAttrValueLen)
			i++
		}
		event.Attrs = trimmed
	} else {
		for k, v := range event.Attrs {
			event.Attrs[k] = truncate(v, MaxAttrValueLen)
		}
	}

	event.ID = b.nextID
	b.nextID++
	b.events[b.pos] = event
	b.pos = (b.pos + 1) % b.cap
	if b.pos == 0 && !b.full {
		b.full = true
	}
}

func (b *TelemetryBuffer) Events() []TelemetryEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.eventsLocked()
}

func (b *TelemetryBuffer) EventsAfter(afterID uint64) []TelemetryEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	all := b.eventsLocked()

	if afterID == 0 {
		if len(all) > 25 {
			all = all[len(all)-25:]
		}
		return all
	}

	start := -1
	for i, e := range all {
		if e.ID > afterID {
			start = i
			break
		}
	}
	if start == -1 {
		return nil
	}
	result := make([]TelemetryEvent, len(all)-start)
	copy(result, all[start:])
	return result
}

func (b *TelemetryBuffer) eventsLocked() []TelemetryEvent {
	if !b.full {
		result := make([]TelemetryEvent, b.pos)
		copy(result, b.events[:b.pos])
		return result
	}
	result := make([]TelemetryEvent, b.cap)
	copy(result, b.events[b.pos:])
	copy(result[b.cap-b.pos:], b.events[:b.pos])
	return result
}

func (b *TelemetryBuffer) UpdateMetric(m MetricSummary) {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := metricKey{name: m.Name, agent: m.Agent, metricType: m.Attributes["type"]}
	if existing, ok := b.metrics[key]; ok {
		existing.Value = m.Value
		return
	}
	if len(b.metrics) >= b.maxMetricSeries {
		return // silently drop
	}
	stored := m // copy
	b.metrics[key] = &stored
}

func (b *TelemetryBuffer) Metrics() []MetricSummary {
	b.mu.Lock()
	defer b.mu.Unlock()

	result := make([]MetricSummary, 0, len(b.metrics))
	for _, m := range b.metrics {
		result = append(result, *m)
	}
	return result
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

```

**Step 4: Run tests to verify they pass**

```bash
go test ./proxy/ -run TestTelemetryBuffer -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add proxy/telemetry.go proxy/telemetry_test.go
git commit -m "Add TelemetryBuffer for OTLP event and metric storage"
```

---

### Task 3: OTLP HTTP receiver

**Files:**
- Create: `proxy/otlp.go`
- Create: `proxy/otlp_test.go`

**Step 1: Write the failing tests**

Create `proxy/otlp_test.go`. Test the HTTP handlers by constructing real OTLP protobuf payloads and posting them:

```go
package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	collectorlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func TestOTLPReceiver_Logs(t *testing.T) {
	buf := NewTelemetryBuffer(100)
	receiver := NewOTLPReceiver(buf)

	t.Run("accepts valid log export", func(t *testing.T) {
		req := &collectorlogs.ExportLogsServiceRequest{
			ResourceLogs: []*logspb.ResourceLogs{{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{{
						Key:   "service.name",
						Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-code"}},
					}},
				},
				ScopeLogs: []*logspb.ScopeLogs{{
					LogRecords: []*logspb.LogRecord{{
						TimeUnixNano: uint64(time.Now().UnixNano()),
						Attributes: []*commonpb.KeyValue{
							{Key: "event.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "tool_result"}}},
							{Key: "tool_name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "Read"}}},
							{Key: "success", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "true"}}},
							{Key: "duration_ms", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "42"}}},
						},
					}},
				}},
			}},
		}
		body, err := proto.Marshal(req)
		require.NoError(t, err)

		w := httptest.NewRecorder()
		httpReq := httptest.NewRequest("POST", "/v1/logs", bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/x-protobuf")
		receiver.ServeHTTP(w, httpReq)

		assert.Equal(t, http.StatusOK, w.Code)

		events := buf.Events()
		require.Len(t, events, 1)
		assert.Equal(t, "claude-code", events[0].Agent)
		assert.Equal(t, "tool_result", events[0].EventName)
		assert.Equal(t, "Read", events[0].Attrs["tool_name"])
	})

	t.Run("rejects oversized body", func(t *testing.T) {
		w := httptest.NewRecorder()
		bigBody := bytes.NewReader(make([]byte, 5*1024*1024))
		httpReq := httptest.NewRequest("POST", "/v1/logs", bigBody)
		httpReq.Header.Set("Content-Type", "application/x-protobuf")
		receiver.ServeHTTP(w, httpReq)

		assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	})

	t.Run("rejects non-POST", func(t *testing.T) {
		w := httptest.NewRecorder()
		httpReq := httptest.NewRequest("GET", "/v1/logs", nil)
		receiver.ServeHTTP(w, httpReq)

		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})

	t.Run("falls back to unknown agent when service.name missing", func(t *testing.T) {
		req := &collectorlogs.ExportLogsServiceRequest{
			ResourceLogs: []*logspb.ResourceLogs{{
				ScopeLogs: []*logspb.ScopeLogs{{
					LogRecords: []*logspb.LogRecord{{
						TimeUnixNano: uint64(time.Now().UnixNano()),
						Attributes: []*commonpb.KeyValue{
							{Key: "event.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "user_prompt"}}},
						},
					}},
				}},
			}},
		}
		body, _ := proto.Marshal(req)

		w := httptest.NewRecorder()
		httpReq := httptest.NewRequest("POST", "/v1/logs", bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/x-protobuf")
		receiver.ServeHTTP(w, httpReq)

		assert.Equal(t, http.StatusOK, w.Code)
		events := buf.Events()
		last := events[len(events)-1]
		assert.Equal(t, "unknown", last.Agent)
	})
}

func TestOTLPReceiver_Metrics(t *testing.T) {
	buf := NewTelemetryBuffer(100)
	receiver := NewOTLPReceiver(buf)

	t.Run("accepts valid metric export", func(t *testing.T) {
		req := &collectormetrics.ExportMetricsServiceRequest{
			ResourceMetrics: []*metricspb.ResourceMetrics{{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{{
						Key:   "service.name",
						Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-code"}},
					}},
				},
				ScopeMetrics: []*metricspb.ScopeMetrics{{
					Metrics: []*metricspb.Metric{{
						Name: "claude_code.token.usage",
						Data: &metricspb.Metric_Sum{
							Sum: &metricspb.Sum{
								DataPoints: []*metricspb.NumberDataPoint{{
									Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 1234},
									Attributes: []*commonpb.KeyValue{
										{Key: "type", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "input"}}},
									},
								}},
							},
						},
					}},
				}},
			}},
		}
		body, err := proto.Marshal(req)
		require.NoError(t, err)

		w := httptest.NewRecorder()
		httpReq := httptest.NewRequest("POST", "/v1/metrics", bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/x-protobuf")
		receiver.ServeHTTP(w, httpReq)

		assert.Equal(t, http.StatusOK, w.Code)

		metrics := buf.Metrics()
		require.Len(t, metrics, 1)
		assert.Equal(t, "claude_code.token.usage", metrics[0].Name)
		assert.Equal(t, "claude-code", metrics[0].Agent)
		assert.Equal(t, float64(1234), metrics[0].Value)
		assert.Equal(t, "input", metrics[0].Attributes["type"])
	})
}
```

Note: add `"bytes"` to imports.

**Step 2: Run tests to verify they fail**

```bash
go test ./proxy/ -run TestOTLPReceiver -v
```

Expected: FAIL — `OTLPReceiver` doesn't exist.

**Step 3: Write the implementation**

Create `proxy/otlp.go`:

```go
package proxy

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/protobuf/proto"

	collectorlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

const maxOTLPBodySize = 4 * 1024 * 1024 // 4 MB

// OTLPReceiver handles incoming OTLP HTTP/protobuf requests and writes
// decoded telemetry into a TelemetryBuffer.
type OTLPReceiver struct {
	mux     *http.ServeMux
	buf     *TelemetryBuffer
	limiter *rate.Limiter
}

func NewOTLPReceiver(buf *TelemetryBuffer) *OTLPReceiver {
	r := &OTLPReceiver{
		mux:     http.NewServeMux(),
		buf:     buf,
		limiter: rate.NewLimiter(100, 20), // 100 req/s, burst 20
	}
	r.mux.HandleFunc("POST /v1/logs", r.handleLogs)
	r.mux.HandleFunc("POST /v1/metrics", r.handleMetrics)
	return r
}

func (r *OTLPReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !r.limiter.Allow() {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	r.mux.ServeHTTP(w, req)
}

func (r *OTLPReceiver) handleLogs(w http.ResponseWriter, req *http.Request) {
	body, err := readLimited(req, maxOTLPBodySize)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}

	var exportReq collectorlogs.ExportLogsServiceRequest
	if err := proto.Unmarshal(body, &exportReq); err != nil {
		http.Error(w, "invalid protobuf", http.StatusBadRequest)
		return
	}

	for _, rl := range exportReq.ResourceLogs {
		agent := extractServiceName(rl.Resource)
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				event := TelemetryEvent{
					Time:  time.Unix(0, int64(lr.TimeUnixNano)),
					Agent: agent,
					Attrs: flattenAttributes(lr.Attributes),
				}
				if name, ok := event.Attrs["event.name"]; ok {
					event.EventName = name
					delete(event.Attrs, "event.name")
				}
				r.buf.AddEvent(event)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "{}")
}

func (r *OTLPReceiver) handleMetrics(w http.ResponseWriter, req *http.Request) {
	body, err := readLimited(req, maxOTLPBodySize)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}

	var exportReq collectormetrics.ExportMetricsServiceRequest
	if err := proto.Unmarshal(body, &exportReq); err != nil {
		http.Error(w, "invalid protobuf", http.StatusBadRequest)
		return
	}

	for _, rm := range exportReq.ResourceMetrics {
		agent := extractServiceName(rm.Resource)
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				for _, dp := range extractDataPoints(m) {
					r.buf.UpdateMetric(MetricSummary{
						Name:       m.Name,
						Agent:      agent,
						Value:      dp.value,
						Attributes: dp.attrs,
					})
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "{}")
}

type dataPoint struct {
	value float64
	attrs map[string]string
}

func extractDataPoints(m *metricspb.Metric) []dataPoint {
	// Handle Sum (counters) and Gauge types.
	switch d := m.Data.(type) {
	case *metricspb.Metric_Sum:
		return numberDataPoints(d.Sum.DataPoints)
	case *metricspb.Metric_Gauge:
		return numberDataPoints(d.Gauge.DataPoints)
	}
	return nil
}

func numberDataPoints(dps []*metricspb.NumberDataPoint) []dataPoint {
	result := make([]dataPoint, 0, len(dps))
	for _, dp := range dps {
		var val float64
		switch v := dp.Value.(type) {
		case *metricspb.NumberDataPoint_AsDouble:
			val = v.AsDouble
		case *metricspb.NumberDataPoint_AsInt:
			val = float64(v.AsInt)
		}
		result = append(result, dataPoint{
			value: val,
			attrs: flattenAttributes(dp.Attributes),
		})
	}
	return result
}

func readLimited(req *http.Request, maxBytes int64) ([]byte, error) {
	limited := http.MaxBytesReader(nil, req.Body, maxBytes)
	defer limited.Close()
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func extractServiceName(res *resourcepb.Resource) string {
	if res == nil {
		return "unknown"
	}
	for _, attr := range res.Attributes {
		if attr.Key == "service.name" {
			if sv, ok := attr.Value.Value.(*commonpb.AnyValue_StringValue); ok {
				name := sv.StringValue
				if len(name) > 64 {
					name = name[:64]
				}
				return name
			}
		}
	}
	return "unknown"
}

func flattenAttributes(attrs []*commonpb.KeyValue) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	result := make(map[string]string, len(attrs))
	for _, kv := range attrs {
		result[kv.Key] = anyValueToString(kv.Value)
	}
	return result
}

func anyValueToString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch val := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return val.StringValue
	case *commonpb.AnyValue_IntValue:
		return fmt.Sprintf("%d", val.IntValue)
	case *commonpb.AnyValue_DoubleValue:
		return fmt.Sprintf("%g", val.DoubleValue)
	case *commonpb.AnyValue_BoolValue:
		return fmt.Sprintf("%t", val.BoolValue)
	default:
		return fmt.Sprintf("%v", v.Value)
	}
}
```

Note: add the `metricspb` import:
`metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"`

**Step 4: Run tests to verify they pass**

```bash
go test ./proxy/ -run TestOTLPReceiver -v
```

Expected: PASS

**Step 5: Run full proxy test suite**

```bash
go test ./proxy/ -v
```

Expected: PASS (no regressions)

**Step 6: Commit**

```bash
git add proxy/otlp.go proxy/otlp_test.go
git commit -m "Add OTLP HTTP/protobuf receiver for logs and metrics"
```

---

### Task 4: Control API telemetry endpoints

**Files:**
- Modify: `proxy/api.go`
- Modify: `proxy/api_test.go`

**Step 1: Write the failing tests**

Add to `proxy/api_test.go`. The existing `TestControlAPI` creates a `ControlAPI` — update that call site and add new subtests:

```go
// Update the existing TestControlAPI setup to include TelemetryBuffer:
// Change:  api := NewControlAPI(log, mergedConfig, allowlist, dnsAllowlist)
// To:      telBuf := NewTelemetryBuffer(100)
//          api := NewControlAPI(log, mergedConfig, allowlist, dnsAllowlist, telBuf)

// Then add these subtests inside TestControlAPI:

t.Run("GET /telemetry/events returns events", func(t *testing.T) {
    telBuf.AddEvent(TelemetryEvent{Time: time.Now(), Agent: "claude-code", EventName: "tool_result"})
    telBuf.AddEvent(TelemetryEvent{Time: time.Now(), Agent: "codex", EventName: "api_request"})

    req := httptest.NewRequest("GET", "/telemetry/events", nil)
    w := httptest.NewRecorder()
    api.ServeHTTP(w, req)
    require.Equal(t, http.StatusOK, w.Code)

    var events []TelemetryEvent
    require.NoError(t, json.Unmarshal(w.Body.Bytes(), &events))
    assert.Len(t, events, 2)
})

t.Run("GET /telemetry/events with after param", func(t *testing.T) {
    req := httptest.NewRequest("GET", "/telemetry/events?after=1", nil)
    w := httptest.NewRecorder()
    api.ServeHTTP(w, req)
    require.Equal(t, http.StatusOK, w.Code)

    var events []TelemetryEvent
    require.NoError(t, json.Unmarshal(w.Body.Bytes(), &events))
    assert.Len(t, events, 1)
    assert.Equal(t, "codex", events[0].Agent)
})

t.Run("GET /telemetry/metrics returns metrics", func(t *testing.T) {
    telBuf.UpdateMetric(MetricSummary{Name: "tokens", Agent: "claude-code", Value: 42})

    req := httptest.NewRequest("GET", "/telemetry/metrics", nil)
    w := httptest.NewRecorder()
    api.ServeHTTP(w, req)
    require.Equal(t, http.StatusOK, w.Code)

    var metrics []MetricSummary
    require.NoError(t, json.Unmarshal(w.Body.Bytes(), &metrics))
    assert.Len(t, metrics, 1)
})
```

Also update `"time"` import.

**Step 2: Run tests to verify they fail**

```bash
go test ./proxy/ -run TestControlAPI -v
```

Expected: FAIL — `NewControlAPI` signature mismatch and missing endpoints.

**Step 3: Update the implementation**

In `proxy/api.go`:

1. Add `telemetry *TelemetryBuffer` field to `ControlAPI` struct.
2. Update `NewControlAPI` to accept `*TelemetryBuffer` as a 5th parameter.
3. Register two new handlers: `"GET /telemetry/events"` and `"GET /telemetry/metrics"`.
4. Implement `handleTelemetryEvents` (parse `after` and `agent` query params, call `buf.EventsAfter`, optionally filter by agent) and `handleTelemetryMetrics` (call `buf.Metrics`, return JSON).

```go
// Updated struct:
type ControlAPI struct {
	mux           *http.ServeMux
	log           *LogBuffer
	telemetry     *TelemetryBuffer
	config        any
	httpAllowlist *HTTPAllowlist
	dnsAllowlist  *DNSAllowlist
}

// Updated constructor:
func NewControlAPI(log *LogBuffer, config any, httpAllowlist *HTTPAllowlist, dnsAllowlist *DNSAllowlist, telemetry *TelemetryBuffer) *ControlAPI {
	api := &ControlAPI{
		mux:           http.NewServeMux(),
		log:           log,
		telemetry:     telemetry,
		config:        config,
		httpAllowlist: httpAllowlist,
		dnsAllowlist:  dnsAllowlist,
	}
	// ... existing routes ...
	api.mux.HandleFunc("GET /telemetry/events", api.handleTelemetryEvents)
	api.mux.HandleFunc("GET /telemetry/metrics", api.handleTelemetryMetrics)
	return api
}

func (a *ControlAPI) handleTelemetryEvents(w http.ResponseWriter, r *http.Request) {
	var afterID uint64
	if s := r.URL.Query().Get("after"); s != "" {
		afterID, _ = strconv.ParseUint(s, 10, 64)
	}
	events := a.telemetry.EventsAfter(afterID)
	if agent := r.URL.Query().Get("agent"); agent != "" {
		filtered := events[:0]
		for _, e := range events {
			if e.Agent == agent {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}
	writeJSON(w, events)
}

func (a *ControlAPI) handleTelemetryMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.telemetry.Metrics())
}
```

**Step 4: Fix all callers of NewControlAPI**

Update `proxy/server.go` line 64 — pass `nil` for now (will be wired in Task 7):
```go
controlAPI := NewControlAPI(log, s.config, allowlist, dnsAllowlist, nil)
```

Update all test files that call `NewControlAPI`:
- `proxy/api_test.go` — pass `telBuf` (as shown in step 1)
- `cmd/control_test.go` — pass `nil` for the telemetry param in all `NewControlAPI` calls
- `cmd/monitor_ui_test.go:332` — pass `nil` for the telemetry param

**Step 5: Run tests to verify they pass**

```bash
go test ./proxy/ ./cmd/ -v
```

Expected: PASS

**Step 6: Commit**

```bash
git add proxy/api.go proxy/api_test.go proxy/server.go cmd/control_test.go cmd/monitor_ui_test.go
git commit -m "Add telemetry event and metric endpoints to control API"
```

---

### Task 5: Control client telemetry methods

**Files:**
- Modify: `cmd/control.go`
- Modify: `cmd/control_test.go`

**Step 1: Write the failing tests**

Add to `cmd/control_test.go`:

```go
func TestControlClient_TelemetryEventsAfter(t *testing.T) {
	telBuf := proxy.NewTelemetryBuffer(100)
	telBuf.AddEvent(proxy.TelemetryEvent{Agent: "claude-code", EventName: "tool_result"})
	telBuf.AddEvent(proxy.TelemetryEvent{Agent: "claude-code", EventName: "api_request"})
	telBuf.AddEvent(proxy.TelemetryEvent{Agent: "codex", EventName: "tool_result"})

	api := proxy.NewControlAPI(proxy.NewLogBuffer(100), nil, proxy.NewHTTPAllowlist(nil), proxy.NewDNSAllowlist(nil), telBuf)
	client := testControlClient(t, api)

	t.Run("returns last events for initial request", func(t *testing.T) {
		events, err := client.TelemetryEventsAfter(0)
		require.NoError(t, err)
		require.Len(t, events, 3)
	})

	t.Run("returns only new events after cursor", func(t *testing.T) {
		events, err := client.TelemetryEventsAfter(2)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "codex", events[0].Agent)
	})
}

func TestControlClient_TelemetryMetrics(t *testing.T) {
	telBuf := proxy.NewTelemetryBuffer(100)
	telBuf.UpdateMetric(proxy.MetricSummary{Name: "tokens", Agent: "claude-code", Value: 42})

	api := proxy.NewControlAPI(proxy.NewLogBuffer(100), nil, proxy.NewHTTPAllowlist(nil), proxy.NewDNSAllowlist(nil), telBuf)
	client := testControlClient(t, api)

	metrics, err := client.TelemetryMetrics()
	require.NoError(t, err)
	require.Len(t, metrics, 1)
	assert.Equal(t, "tokens", metrics[0].Name)
	assert.Equal(t, float64(42), metrics[0].Value)
}
```

**Step 2: Run tests to verify they fail**

```bash
go test ./cmd/ -run TestControlClient_Telemetry -v
```

Expected: FAIL — methods don't exist.

**Step 3: Implement the client methods**

Add to `cmd/control.go`:

```go
func (c *ControlClient) TelemetryEventsAfter(afterID uint64) ([]proxy.TelemetryEvent, error) {
	var events []proxy.TelemetryEvent
	if err := c.get(fmt.Sprintf("/telemetry/events?after=%d", afterID), &events); err != nil {
		return nil, err
	}
	return events, nil
}

func (c *ControlClient) TelemetryMetrics() ([]proxy.MetricSummary, error) {
	var metrics []proxy.MetricSummary
	if err := c.get("/telemetry/metrics", &metrics); err != nil {
		return nil, err
	}
	return metrics, nil
}
```

**Step 4: Run tests to verify they pass**

```bash
go test ./cmd/ -run TestControlClient_Telemetry -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add cmd/control.go cmd/control_test.go
git commit -m "Add telemetry client methods for events and metrics"
```

---

### Task 6: Config — agent-telemetry

**Files:**
- Modify: `config/config.go`
- Modify: `config/config_test.go`

**Step 1: Write the failing test**

Add to `config/config_test.go`:

```go
func TestAgentTelemetryConfig(t *testing.T) {
	t.Run("defaults to true when not set", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		os.WriteFile(path, []byte("presets:\n  - node\n"), 0o644)

		cfg, err := Load(filepath.Join(dir, "global.yaml"), path)
		require.NoError(t, err)
		assert.True(t, cfg.Project.AgentTelemetry)
	})

	t.Run("can be disabled explicitly", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "network.yaml")
		os.WriteFile(path, []byte("agent-telemetry: false\n"), 0o644)

		cfg, err := Load(filepath.Join(dir, "global.yaml"), path)
		require.NoError(t, err)
		assert.False(t, cfg.Project.AgentTelemetry)
	})
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./config/ -run TestAgentTelemetryConfig -v
```

Expected: FAIL

**Step 3: Implement**

In `config/config.go`, add `AgentTelemetry` to `ProjectConfig`:

```go
type ProjectConfig struct {
	Presets        []string `koanf:"presets"`
	AllowHTTP      []string `koanf:"allow-http"`
	AllowDNS       []string `koanf:"allow-dns"`
	AllowHostPorts []int    `koanf:"allow-host-ports"`
	AgentTelemetry *bool    `koanf:"agent-telemetry"`
}
```

Use `*bool` so we can distinguish "not set" (nil → default true) from "explicitly false". Add a helper method:

```go
func (p ProjectConfig) AgentTelemetryEnabled() bool {
	if p.AgentTelemetry == nil {
		return true
	}
	return *p.AgentTelemetry
}
```

Update the test to use `cfg.Project.AgentTelemetryEnabled()` instead of the raw field.

Also add `AgentTelemetry bool` to `MergedConfig`:

```go
type MergedConfig struct {
	// ... existing fields ...
	AgentTelemetry bool `json:"agent-telemetry,omitempty"`
}
```

Update `Merge()` to carry it through:
```go
func (c *Config) Merge(...) MergedConfig {
	// ... existing ...
	return MergedConfig{
		// ... existing fields ...
		AgentTelemetry: c.Project.AgentTelemetryEnabled(),
	}
}
```

**Step 4: Run tests to verify they pass**

```bash
go test ./config/ -v
```

Expected: PASS

**Step 5: Run full test suite**

```bash
make test
```

Expected: PASS

**Step 6: Commit**

```bash
git add config/config.go config/config_test.go
git commit -m "Add agent-telemetry config option (default: true)"
```

---

### Task 7: Server wiring — OTLP receiver + ProxyConfig

**Files:**
- Modify: `proxy/server.go`

**Step 1: Add OTLPPort to ProxyConfig**

```go
type ProxyConfig struct {
	// ... existing fields ...
	OTLPPort int `json:"otlp-port,omitempty"`
}
```

**Step 2: Wire up the OTLP receiver in Server.Run**

Update `Server.Run()` to conditionally start the OTLP receiver as a 4th goroutine:

```go
func (s *Server) Run(ctx context.Context) error {
	allowlist := NewHTTPAllowlist(s.config.AllowHTTP)
	dnsAllowlist := NewDNSAllowlist(s.config.AllowDNS)
	cidr := NewCIDRBlocker(s.config.BlockCIDR)
	log := NewLogBuffer(LogBufferCapacity)
	telemetry := NewTelemetryBuffer(TelemetryBufferCapacity)

	httpProxy := NewHTTPProxy(allowlist, cidr, log, s.config.Upstream)
	dnsServer := NewDNSServer(dnsAllowlist, cidr, log, s.config.Upstream)
	controlAPI := NewControlAPI(log, s.config, allowlist, dnsAllowlist, telemetry)

	// ... existing host.vibepit setup ...

	serviceCount := 3
	if s.config.OTLPPort > 0 {
		serviceCount = 4
	}
	errCh := make(chan error, serviceCount)

	// ... existing 3 goroutines ...

	if s.config.OTLPPort > 0 {
		otlpReceiver := NewOTLPReceiver(telemetry)
		otlpAddr := fmt.Sprintf(":%d", s.config.OTLPPort)
		go func() {
			fmt.Printf("proxy: OTLP receiver listening on %s\n", otlpAddr)
			srv := &http.Server{
				Addr:         otlpAddr,
				Handler:      otlpReceiver,
				ReadTimeout:  30 * time.Second,
				WriteTimeout: 30 * time.Second,
				IdleTimeout:  60 * time.Second,
			}
			errCh <- srv.ListenAndServe()
		}()
	}

	// ... existing select ...
}
```

**Step 3: Run tests**

```bash
make test
```

Expected: PASS

**Step 4: Commit**

```bash
git add proxy/server.go
git commit -m "Wire OTLP receiver into proxy server"
```

---

### Task 8: Dev container wiring — NO_PROXY fix + OTLP env vars

**Files:**
- Modify: `container/client.go` (DevContainerConfig struct + CreateDevContainer)
- Modify: `cmd/run.go`

**Step 1: Add OTLPPort to DevContainerConfig**

In `container/client.go`:

```go
type DevContainerConfig struct {
	// ... existing fields ...
	OTLPPort int // 0 means telemetry disabled
}
```

**Step 2: Fix NO_PROXY and add OTLP env vars**

In `CreateDevContainer`, update the env block. Change the NO_PROXY lines to include ProxyIP:

```go
fmt.Sprintf("NO_PROXY=localhost,127.0.0.1,%s", cfg.ProxyIP),
fmt.Sprintf("no_proxy=localhost,127.0.0.1,%s", cfg.ProxyIP),
```

Then, after the ColorTerm block, add:

```go
if cfg.OTLPPort > 0 {
	env = append(env,
		fmt.Sprintf("OTEL_EXPORTER_OTLP_ENDPOINT=http://%s:%d", cfg.ProxyIP, cfg.OTLPPort),
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
	)
}
```

**Step 3: Wire OTLP port allocation in cmd/run.go**

After the existing controlAPIPort allocation (around line 217), add:

```go
var otlpPort int
if merged.AgentTelemetry {
	excluded[controlAPIPort] = true
	otlpPort, err = config.RandomProxyPort(excluded)
	if err != nil {
		return fmt.Errorf("OTLP port: %w", err)
	}
	merged.OTLPPort = otlpPort
}
```

Note: also add `OTLPPort int` to `MergedConfig` in `config/config.go` if not already done in Task 6 (it maps to `ProxyConfig.OTLPPort` which is the JSON config passed to the proxy container).

Wait — `MergedConfig` in config and `ProxyConfig` in proxy are separate types. `MergedConfig` is marshaled to JSON and loaded as `ProxyConfig` in the proxy container. So `MergedConfig` needs the field too:

```go
type MergedConfig struct {
	// ... existing fields ...
	OTLPPort       int  `json:"otlp-port,omitempty"`
	AgentTelemetry bool `json:"-"` // not sent to proxy; controls run.go behavior
}
```

Actually, `AgentTelemetry` doesn't need to go to the proxy — the proxy just checks `OTLPPort > 0`. So mark it `json:"-"`.

Pass OTLPPort to the dev container:

```go
devContainerID, err := client.CreateDevContainer(ctx, ctr.DevContainerConfig{
	// ... existing fields ...
	OTLPPort: otlpPort,
})
```

**Step 4: Run tests**

```bash
make test
```

Expected: PASS

**Step 5: Commit**

```bash
git add container/client.go cmd/run.go config/config.go
git commit -m "Wire OTLP port allocation and dev container env injection"
```

---

### Task 9: Tab navigation

**Files:**
- Create: `cmd/monitor_tabs.go`
- Create: `cmd/monitor_tabs_test.go`

**Step 1: Write the failing tests**

```go
package cmd

import (
	"testing"

	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTabbedSetup() (*tabbedMonitorScreen, *tui.Window) {
	network := newMonitorScreen(&SessionInfo{
		SessionID:  "test123",
		ProjectDir: "/test",
	}, nil)
	telemetry := newTelemetryScreen(nil)
	tabbed := newTabbedMonitorScreen(network, telemetry)

	header := &tui.HeaderInfo{ProjectDir: "/test", SessionID: "test123"}
	w := tui.NewWindow(header, tabbed)
	w.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return tabbed, w
}

func TestTabbedMonitorScreen_TabSwitch(t *testing.T) {
	t.Run("starts on network tab", func(t *testing.T) {
		tabbed, _ := makeTabbedSetup()
		assert.Equal(t, 0, tabbed.activeTab)
	})

	t.Run("tab key switches to telemetry", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		tabbed.Update(tea.KeyMsg{Type: tea.KeyTab}, w)
		assert.Equal(t, 1, tabbed.activeTab)
	})

	t.Run("tab key wraps back to network", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		tabbed.Update(tea.KeyMsg{Type: tea.KeyTab}, w)
		tabbed.Update(tea.KeyMsg{Type: tea.KeyTab}, w)
		assert.Equal(t, 0, tabbed.activeTab)
	})

	t.Run("number 1 switches to network", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		tabbed.activeTab = 1
		tabbed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}}, w)
		assert.Equal(t, 0, tabbed.activeTab)
	})

	t.Run("number 2 switches to telemetry", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		tabbed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}}, w)
		assert.Equal(t, 1, tabbed.activeTab)
	})

	t.Run("view contains tab bar", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		view := tabbed.View(w)
		assert.Contains(t, view, "Network")
		assert.Contains(t, view, "Telemetry")
	})

	t.Run("footer includes tab hint", func(t *testing.T) {
		tabbed, w := makeTabbedSetup()
		keys := tabbed.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "switch tab")
	})
}
```

**Step 2: Run tests to verify they fail**

```bash
go test ./cmd/ -run TestTabbedMonitorScreen -v
```

Expected: FAIL

**Step 3: Implement**

Create `cmd/monitor_tabs.go`:

```go
package cmd

import (
	"strings"

	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tabbedMonitorScreen struct {
	tabs      []string
	activeTab int
	screens   []tui.Screen
}

func newTabbedMonitorScreen(network *monitorScreen, telemetry *telemetryScreen) *tabbedMonitorScreen {
	return &tabbedMonitorScreen{
		tabs:    []string{"Network", "Telemetry"},
		screens: []tui.Screen{network, telemetry},
	}
}

func (t *tabbedMonitorScreen) activeScreen() tui.Screen {
	return t.screens[t.activeTab]
}

func (t *tabbedMonitorScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "tab":
			t.activeTab = (t.activeTab + 1) % len(t.tabs)
			// Notify new screen of window size.
			sizeMsg := tea.WindowSizeMsg{Width: w.Width(), Height: w.Height()}
			t.activeScreen().Update(sizeMsg, w)
			return t, nil
		case "1":
			t.activeTab = 0
			return t, nil
		case "2":
			t.activeTab = 1
			return t, nil
		}
	}

	screen, cmd := t.activeScreen().Update(msg, w)
	// If the active sub-screen returns itself, keep it; don't switch.
	t.screens[t.activeTab] = screen
	return t, cmd
}

func (t *tabbedMonitorScreen) View(w *tui.Window) string {
	tabBar := t.renderTabBar()
	content := t.activeScreen().View(w)
	return tabBar + "\n" + content
}

func (t *tabbedMonitorScreen) renderTabBar() string {
	activeStyle := lipgloss.NewStyle().Foreground(tui.ColorCyan).Bold(true)
	inactiveStyle := lipgloss.NewStyle().Foreground(tui.ColorField)

	var parts []string
	for i, name := range t.tabs {
		if i == t.activeTab {
			parts = append(parts, activeStyle.Render("["+name+"]"))
		} else {
			parts = append(parts, inactiveStyle.Render(" "+name+" "))
		}
	}
	return strings.Join(parts, "  ")
}

func (t *tabbedMonitorScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	keys := t.activeScreen().FooterKeys(w)
	keys = append(keys, tui.FooterKey{Key: "tab", Desc: "switch tab"})
	return keys
}

func (t *tabbedMonitorScreen) FooterStatus(w *tui.Window) string {
	return t.activeScreen().FooterStatus(w)
}
```

Note: the `View` method produces an extra line (the tab bar). The sub-screen's `VpHeight` needs to account for this. Handle this by adjusting `VpHeight` in the `WindowSizeMsg` forwarding — subtract 1 from height before forwarding to the sub-screen. This will need care during implementation; the tab bar takes 1 line from the viewport.

**Step 4: Run tests to verify they pass**

```bash
go test ./cmd/ -run TestTabbedMonitorScreen -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add cmd/monitor_tabs.go cmd/monitor_tabs_test.go
git commit -m "Add tab navigation for monitor TUI"
```

---

### Task 10: Telemetry screen

**Files:**
- Create: `cmd/monitor_telemetry.go`
- Create: `cmd/monitor_telemetry_test.go`

**Step 1: Write the failing tests**

```go
package cmd

import (
	"fmt"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTelemetrySetup(n int) (*telemetryScreen, *tui.Window) {
	s := newTelemetryScreen(nil)
	header := &tui.HeaderInfo{ProjectDir: "/test", SessionID: "test123"}
	w := tui.NewWindow(header, s)
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	for i := range n {
		s.events = append(s.events, proxy.TelemetryEvent{
			ID:        uint64(i + 1),
			Time:      time.Now(),
			Agent:     "claude-code",
			EventName: fmt.Sprintf("event_%d", i),
			Attrs:     map[string]string{"tool_name": "Read"},
		})
	}
	s.cursor.ItemCount = len(s.events)
	if len(s.events) > 0 {
		s.cursor.Pos = len(s.events) - 1
	}
	return s, w
}

func TestTelemetryScreen_CursorNavigation(t *testing.T) {
	t.Run("j moves cursor down", func(t *testing.T) {
		s, w := makeTelemetrySetup(20)
		s.cursor.Pos = 5
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, w)
		assert.Equal(t, 6, s.cursor.Pos)
	})

	t.Run("G jumps to end", func(t *testing.T) {
		s, w := makeTelemetrySetup(20)
		s.cursor.Pos = 0
		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}, w)
		assert.Equal(t, 19, s.cursor.Pos)
	})
}

func TestTelemetryScreen_AgentFilter(t *testing.T) {
	t.Run("f key cycles agent filter", func(t *testing.T) {
		s, w := makeTelemetrySetup(0)
		s.agents = []string{"claude-code", "codex"}

		assert.Equal(t, "", s.agentFilter) // all

		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, w)
		assert.Equal(t, "claude-code", s.agentFilter)

		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, w)
		assert.Equal(t, "codex", s.agentFilter)

		s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, w)
		assert.Equal(t, "", s.agentFilter) // back to all
	})
}

func TestTelemetryScreen_View(t *testing.T) {
	t.Run("renders event lines", func(t *testing.T) {
		s, w := makeTelemetrySetup(5)
		view := s.View(w)
		assert.Contains(t, view, "claude-code")
		assert.Contains(t, view, "Read")
	})

	t.Run("shows metrics header", func(t *testing.T) {
		s, w := makeTelemetrySetup(0)
		s.metricSummaries = []proxy.MetricSummary{
			{Name: "claude_code.token.usage", Agent: "claude-code", Value: 1234, Attributes: map[string]string{"type": "input"}},
		}
		view := s.View(w)
		assert.Contains(t, view, "1234")
	})
}

func TestTelemetryScreen_Footer(t *testing.T) {
	t.Run("shows filter key", func(t *testing.T) {
		s, w := makeTelemetrySetup(5)
		keys := s.FooterKeys(w)
		descs := footerKeyDescs(keys)
		assert.Contains(t, descs, "filter agent")
	})

	t.Run("shows active filter in footer", func(t *testing.T) {
		s, w := makeTelemetrySetup(0)
		s.agentFilter = "claude-code"
		status := s.FooterStatus(w)
		assert.Contains(t, status, "claude-code")
	})
}
```

**Step 2: Run tests to verify they fail**

```bash
go test ./cmd/ -run TestTelemetryScreen -v
```

Expected: FAIL

**Step 3: Implement**

Create `cmd/monitor_telemetry.go`. Follow the patterns in `cmd/monitor_ui.go`:

```go
package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const telemetryPollInterval = time.Second

type telemetryScreen struct {
	client         *ControlClient
	cursor         tui.Cursor
	pollCursor     uint64
	events         []proxy.TelemetryEvent
	metricSummaries []proxy.MetricSummary
	agents         []string
	agentFilter    string
	newCount       int
	firstTickSeen  bool
}

func newTelemetryScreen(client *ControlClient) *telemetryScreen {
	return &telemetryScreen{
		client: client,
	}
}

func (s *telemetryScreen) filteredEvents() []proxy.TelemetryEvent {
	if s.agentFilter == "" {
		return s.events
	}
	var filtered []proxy.TelemetryEvent
	for _, e := range s.events {
		if e.Agent == s.agentFilter {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func (s *telemetryScreen) Update(msg tea.Msg, w *tui.Window) (tui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "f":
			s.cycleAgentFilter()
			filtered := s.filteredEvents()
			s.cursor.ItemCount = len(filtered)
			s.cursor.EnsureVisible()
			return s, nil
		case "q", "ctrl+c":
			return s, tea.Quit
		default:
			if s.cursor.HandleKey(msg) {
				return s, nil
			}
		}

	case tea.WindowSizeMsg:
		s.cursor.VpHeight = w.VpHeight() - s.metricsHeaderHeight()
		s.cursor.EnsureVisible()

	case tui.TickMsg:
		if s.client != nil && (w.IntervalElapsed(telemetryPollInterval) || !s.firstTickSeen) {
			events, err := s.client.TelemetryEventsAfter(s.pollCursor)
			if err != nil {
				w.SetError(err)
			} else {
				w.ClearError()
				wasAtEnd := len(s.events) == 0 || s.cursor.AtEnd()
				for _, e := range events {
					s.events = append(s.events, e)
					s.pollCursor = e.ID
					s.trackAgent(e.Agent)
				}
				filtered := s.filteredEvents()
				s.cursor.ItemCount = len(filtered)
				if wasAtEnd && len(filtered) > 0 {
					s.cursor.Pos = len(filtered) - 1
					s.cursor.EnsureVisible()
				}
			}

			metrics, err := s.client.TelemetryMetrics()
			if err == nil {
				s.metricSummaries = metrics
			}
		}
		s.firstTickSeen = true
	}

	return s, nil
}

func (s *telemetryScreen) cycleAgentFilter() {
	if len(s.agents) == 0 {
		return
	}
	if s.agentFilter == "" {
		s.agentFilter = s.agents[0]
		return
	}
	for i, a := range s.agents {
		if a == s.agentFilter {
			if i+1 < len(s.agents) {
				s.agentFilter = s.agents[i+1]
			} else {
				s.agentFilter = "" // back to all
			}
			return
		}
	}
	s.agentFilter = ""
}

func (s *telemetryScreen) trackAgent(agent string) {
	for _, a := range s.agents {
		if a == agent {
			return
		}
	}
	s.agents = append(s.agents, agent)
}

func (s *telemetryScreen) metricsHeaderHeight() int {
	if len(s.metricSummaries) == 0 {
		return 0
	}
	// One line per agent.
	agents := make(map[string]bool)
	for _, m := range s.metricSummaries {
		agents[m.Agent] = true
	}
	return len(agents)
}

func (s *telemetryScreen) View(w *tui.Window) string {
	var lines []string

	// Metrics header.
	lines = append(lines, s.renderMetricsHeader()...)

	// Event stream.
	filtered := s.filteredEvents()
	vpHeight := w.VpHeight() - len(lines)
	end := min(s.cursor.Offset+vpHeight, len(filtered))
	for i := s.cursor.Offset; i < end; i++ {
		lines = append(lines, renderTelemetryLine(filtered[i], i == s.cursor.Pos))
	}
	for len(lines) < w.VpHeight() {
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

func (s *telemetryScreen) renderMetricsHeader() []string {
	if len(s.metricSummaries) == 0 {
		return nil
	}

	// Group metrics by agent.
	byAgent := make(map[string][]proxy.MetricSummary)
	for _, m := range s.metricSummaries {
		byAgent[m.Agent] = append(byAgent[m.Agent], m)
	}

	style := lipgloss.NewStyle().Foreground(tui.ColorField)
	valueStyle := lipgloss.NewStyle().Foreground(tui.ColorCyan)

	var lines []string
	for agent, metrics := range byAgent {
		var parts []string
		parts = append(parts, lipgloss.NewStyle().Foreground(tui.ColorOrange).Render(agent))
		for _, m := range metrics {
			label := m.Name
			if t, ok := m.Attributes["type"]; ok {
				label += "(" + t + ")"
			}
			parts = append(parts, style.Render(label+":")+valueStyle.Render(fmt.Sprintf(" %.4g", m.Value)))
		}
		lines = append(lines, strings.Join(parts, "  "))
	}
	return lines
}

// stripControl removes ANSI escape sequences and control characters (except
// tab) from s. Applied at render time as defensive terminal hygiene.
func stripControl(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inEsc := false
	for _, r := range s {
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '~' {
				inEsc = false
			}
			continue
		}
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if r < 0x20 && r != '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func renderTelemetryLine(e proxy.TelemetryEvent, highlighted bool) string {
	base, marker := tui.LineStyle(highlighted)

	ts := base.Foreground(tui.ColorField).Render(e.Time.Format("15:04:05"))
	agent := base.Foreground(tui.ColorOrange).Render(fmt.Sprintf("%-12s", stripControl(e.Agent)))
	event := base.Foreground(tui.ColorCyan).Render(fmt.Sprintf("%-14s", stripControl(e.EventName)))

	// Build detail string from known attributes. stripControl is applied to
	// all attribute values to prevent terminal escape injection.
	var details []string
	if v, ok := e.Attrs["tool_name"]; ok {
		details = append(details, base.Render(stripControl(v)))
	}
	if v, ok := e.Attrs["model"]; ok {
		details = append(details, base.Render(stripControl(v)))
	}
	if v, ok := e.Attrs["success"]; ok {
		if v == "true" {
			details = append(details, base.Foreground(tui.ColorCyan).Render("✓"))
		} else {
			details = append(details, base.Foreground(tui.ColorError).Render("✗"))
		}
	}
	if v, ok := e.Attrs["duration_ms"]; ok {
		details = append(details, base.Foreground(tui.ColorField).Render(v+"ms"))
	}
	if v, ok := e.Attrs["cost_usd"]; ok {
		details = append(details, base.Foreground(tui.ColorField).Render("$"+v))
	}
	if v, ok := e.Attrs["input_tokens"]; ok {
		tok := v
		if out, ok2 := e.Attrs["output_tokens"]; ok2 {
			tok += "→" + out
		}
		details = append(details, base.Foreground(tui.ColorField).Render(tok+" tok"))
	}

	sp := base.Render(" ")
	detail := base.Render(strings.Join(details, sp))

	return marker + base.Render("[") + ts + base.Render("]") + sp + agent + sp + event + sp + detail
}

func (s *telemetryScreen) FooterStatus(w *tui.Window) string {
	var parts []string

	// Tailing indicator.
	isTailing := len(s.events) == 0 || s.cursor.AtEnd()
	if isTailing {
		glyph := spinnerFrames[w.TickFrame()%len(spinnerFrames)]
		parts = append(parts, lipgloss.NewStyle().Foreground(tui.ColorCyan).Render(glyph))
	} else {
		parts = append(parts, lipgloss.NewStyle().Foreground(tui.ColorField).Render("⠿"))
	}

	// Agent filter.
	if s.agentFilter != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(tui.ColorOrange).Render("agent:"+s.agentFilter))
	}

	return strings.Join(parts, " ")
}

func (s *telemetryScreen) FooterKeys(w *tui.Window) []tui.FooterKey {
	keys := []tui.FooterKey{
		{Key: "f", Desc: "filter agent"},
	}
	keys = append(keys, s.cursor.FooterKeys()...)
	return keys
}
```

**Step 4: Run tests to verify they pass**

```bash
go test ./cmd/ -run TestTelemetryScreen -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add cmd/monitor_telemetry.go cmd/monitor_telemetry_test.go
git commit -m "Add telemetry screen for event stream and metric display"
```

---

### Task 11: Wire monitor entry point

**Files:**
- Modify: `cmd/monitor.go`

**Step 1: Update runMonitor to use tabbed screen**

Change `runMonitor` to create both screens and wrap them. Check `GET /config`
for `otlp-port` to determine whether telemetry is enabled. If disabled, pass
`nil` client to the telemetry screen so it shows a "telemetry disabled" message:

```go
func runMonitor(session *SessionInfo) error {
	client, err := NewControlClient(session)
	if err != nil {
		return err
	}

	// Check if telemetry is enabled by inspecting the proxy config.
	var telemetryClient *ControlClient
	if cfg, err := client.Config(); err == nil && cfg.OTLPPort > 0 {
		telemetryClient = client
	}

	network := newMonitorScreen(session, client)
	telemetry := newTelemetryScreen(telemetryClient)
	screen := newTabbedMonitorScreen(network, telemetry)
	header := &tui.HeaderInfo{ProjectDir: session.ProjectDir, SessionID: session.SessionID}
	w := tui.NewWindow(header, screen)
	p := tea.NewProgram(w, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("monitor UI: %w", err)
	}
	return nil
}
```

Also update the `onSelect` callback in `MonitorCommand` similarly.

**Step 2: Handle disabled state in telemetryScreen.View**

When `client` is nil, the telemetry screen's `View` should render a centered
"telemetry disabled" message instead of the event stream. Add to
`cmd/monitor_telemetry.go`:

```go
func (s *telemetryScreen) View(w *tui.Window) string {
	if s.client == nil {
		msg := lipgloss.NewStyle().Foreground(tui.ColorField).
			Render("Agent telemetry is disabled. Set agent-telemetry: true in .vibepit/network.yaml to enable.")
		// Center vertically.
		pad := w.VpHeight() / 2
		var lines []string
		for range pad {
			lines = append(lines, "")
		}
		lines = append(lines, "  "+msg)
		for len(lines) < w.VpHeight() {
			lines = append(lines, "")
		}
		return strings.Join(lines, "\n")
	}
	// ... existing rendering ...
}
```

**Step 3: Add OTLPPort to MergedConfig JSON**

In `config/config.go`, ensure `MergedConfig.OTLPPort` is serialized so the
monitor can read it via `GET /config`:

```go
OTLPPort int `json:"otlp-port,omitempty"`
```

**Step 2: Run full test suite**

```bash
make test
```

Expected: PASS

**Step 3: Commit**

```bash
git add cmd/monitor.go
git commit -m "Wire tabbed monitor with network and telemetry tabs"
```

---

### Task 12: Integration verification

**Step 1: Run full test suite**

```bash
make test
```

Expected: PASS

**Step 2: Run integration tests**

```bash
make test-integration
```

Expected: PASS (or expected failures due to no container runtime in sandbox)

**Step 3: Verify build**

```bash
make build
```

Expected: binary builds successfully

**Step 4: Verify help output**

```bash
go run . --help
```

Expected: no changes to CLI help (all changes are internal)
