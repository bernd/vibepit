package proxy

import (
	"cmp"
	"encoding/json"
	"slices"
	"strconv"
	"sync"
	"time"
)

const (
	TelemetryBufferCapacity = 10000
	DefaultMaxMetricSeries  = 1000
	MaxAttrsPerEvent        = 64
	MaxAttrValueLen         = 256
)

type TelemetryEvent struct {
	ID        uint64            `json:"id"`
	Time      time.Time         `json:"time"`
	Agent     string            `json:"agent"`
	EventName string            `json:"event_name"`
	Attrs     map[string]string `json:"attrs,omitempty"`
	RawLog    json.RawMessage   `json:"raw_log,omitempty"`
}

type MetricSummary struct {
	Name       string            `json:"name"`
	Agent      string            `json:"agent"`
	Value      float64           `json:"value"`
	IsDelta    bool              `json:"is_delta,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	RawMetric  json.RawMessage   `json:"raw_metric,omitempty"`
}

// metricKey uniquely identifies a metric series by name, agent, and the "type"
// and "model" attributes. Other attributes are ignored to keep series count
// stable and avoid churn from varying resource attributes.
type metricKey struct {
	name       string
	agent      string
	metricType string // value of the "type" attribute, if any
	model      string // value of the "model" attribute, if any
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

	if len(event.Attrs) > MaxAttrsPerEvent {
		trimmed := make(map[string]string, MaxAttrsPerEvent)
		for k, v := range event.Attrs {
			if len(trimmed) >= MaxAttrsPerEvent {
				break
			}
			trimmed[k] = truncate(v, MaxAttrValueLen)
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

	b.deriveEventMetrics(event)
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

	for i, e := range all {
		if e.ID > afterID {
			return all[i:]
		}
	}
	return nil
}

// eventsLocked returns all events in chronological order. Caller must hold b.mu.
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
	b.updateMetricLocked(m)
}

func (b *TelemetryBuffer) Metrics() []MetricSummary {
	b.mu.Lock()
	defer b.mu.Unlock()

	result := make([]MetricSummary, 0, len(b.metrics))
	for _, m := range b.metrics {
		result = append(result, *m)
	}
	slices.SortFunc(result, func(a, b MetricSummary) int {
		if c := cmp.Compare(a.Agent, b.Agent); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Attributes["model"], b.Attributes["model"]); c != 0 {
			return c
		}
		return cmp.Compare(a.Attributes["type"], b.Attributes["type"])
	})
	return result
}

// updateMetricLocked updates a metric, caller must hold b.mu.
func (b *TelemetryBuffer) updateMetricLocked(m MetricSummary) {
	key := metricKey{name: m.Name, agent: m.Agent, metricType: m.Attributes["type"], model: m.Attributes["model"]}
	if existing, ok := b.metrics[key]; ok {
		if m.IsDelta {
			existing.Value += m.Value
		} else {
			existing.Value = m.Value
		}
		existing.RawMetric = m.RawMetric
		return
	}
	if len(b.metrics) >= b.maxMetricSeries {
		return
	}
	stored := m
	b.metrics[key] = &stored
}

// updateMaxMetricLocked stores the metric only if the new value exceeds the
// existing one. Caller must hold b.mu.
func (b *TelemetryBuffer) updateMaxMetricLocked(m MetricSummary) {
	key := metricKey{name: m.Name, agent: m.Agent, metricType: m.Attributes["type"], model: m.Attributes["model"]}
	if existing, ok := b.metrics[key]; ok {
		if m.Value > existing.Value {
			existing.Value = m.Value
		}
		return
	}
	if len(b.metrics) >= b.maxMetricSeries {
		return
	}
	stored := m
	b.metrics[key] = &stored
}

// deriveEventMetrics creates derived metric summaries from event attributes.
// Caller must hold b.mu.
func (b *TelemetryBuffer) deriveEventMetrics(e TelemetryEvent) {
	durationStr := e.Attrs["duration_ms"]
	duration, _ := strconv.ParseFloat(durationStr, 64)

	// Per-event-type count and duration (for all events with duration_ms).
	if durationStr != "" {
		b.updateMetricLocked(MetricSummary{
			Name: "event_type.count", Agent: e.Agent, Value: 1, IsDelta: true,
			Attributes: map[string]string{"type": e.EventName},
		})
		b.updateMetricLocked(MetricSummary{
			Name: "event_type.duration", Agent: e.Agent, Value: duration, IsDelta: true,
			Attributes: map[string]string{"type": e.EventName},
		})
	}

	switch e.EventName {
	case "api_request":
		model := e.Attrs["model"]
		if model == "" {
			return
		}
		b.updateMetricLocked(MetricSummary{
			Name: "api.count", Agent: e.Agent, Value: 1, IsDelta: true,
			Attributes: map[string]string{"model": model},
		})
		if durationStr != "" {
			b.updateMetricLocked(MetricSummary{
				Name: "api.duration", Agent: e.Agent, Value: duration, IsDelta: true,
				Attributes: map[string]string{"model": model},
			})
		}

	case "tool_result":
		toolName := e.Attrs["tool_name"]
		if toolName == "" {
			return
		}
		b.updateMetricLocked(MetricSummary{
			Name: "tool.count", Agent: e.Agent, Value: 1, IsDelta: true,
			Attributes: map[string]string{"type": toolName},
		})
		if durationStr != "" {
			b.updateMetricLocked(MetricSummary{
				Name: "tool.duration", Agent: e.Agent, Value: duration, IsDelta: true,
				Attributes: map[string]string{"type": toolName},
			})
		}
		if sizeStr := e.Attrs["tool_result_size_bytes"]; sizeStr != "" {
			size, _ := strconv.ParseFloat(sizeStr, 64)
			b.updateMetricLocked(MetricSummary{
				Name: "tool.result_size", Agent: e.Agent, Value: size, IsDelta: true,
				Attributes: map[string]string{"type": toolName},
			})
			b.updateMaxMetricLocked(MetricSummary{
				Name: "tool.result_size_max", Agent: e.Agent, Value: size,
				Attributes: map[string]string{"type": toolName},
			})
		}
	}
}

func truncate(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	// Avoid splitting a multi-byte UTF-8 character.
	for maxBytes > 0 && s[maxBytes]>>6 == 0b10 {
		maxBytes--
	}
	return s[:maxBytes]
}
