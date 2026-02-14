package proxy

import (
	"cmp"
	"encoding/json"
	"slices"
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
	Attributes map[string]string `json:"attributes,omitempty"`
	RawMetric  json.RawMessage   `json:"raw_metric,omitempty"`
}

// metricKey uniquely identifies a metric series. Only name, agent, and the
// "type" attribute are used for identity -- other attributes are ignored to
// keep series count stable and avoid churn from varying resource attributes.
type metricKey struct {
	name       string
	agent      string
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

	key := metricKey{name: m.Name, agent: m.Agent, metricType: m.Attributes["type"]}
	if existing, ok := b.metrics[key]; ok {
		existing.Value = m.Value
		existing.RawMetric = m.RawMetric
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
	slices.SortFunc(result, func(a, b MetricSummary) int {
		if c := cmp.Compare(a.Agent, b.Agent); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return result
}

func truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Avoid splitting a multi-byte UTF-8 character.
	for maxBytes > 0 && s[maxBytes]>>6 == 0b10 {
		maxBytes--
	}
	return s[:maxBytes]
}
