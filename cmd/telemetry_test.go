package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPollTelemetry_Events(t *testing.T) {
	telBuf := proxy.NewTelemetryBuffer(100)
	telBuf.AddEvent(proxy.TelemetryEvent{Agent: "claude", EventName: "tool_result"})
	telBuf.AddEvent(proxy.TelemetryEvent{Agent: "codex", EventName: "api_request"})

	api := proxy.NewControlAPI(proxy.NewLogBuffer(10), nil, nil, nil, telBuf)
	client := testControlClient(t, api)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)

	cursor, err := pollTelemetry(context.Background(), client, enc, 0, "", false, true, false)
	require.NoError(t, err)

	assert.Greater(t, cursor, uint64(0))

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	require.Len(t, lines, 2)

	var event proxy.TelemetryEvent
	require.NoError(t, json.Unmarshal(lines[0], &event))
	assert.Equal(t, "claude", event.Agent)
}

func TestPollTelemetry_AgentFilter(t *testing.T) {
	telBuf := proxy.NewTelemetryBuffer(100)
	telBuf.AddEvent(proxy.TelemetryEvent{Agent: "claude", EventName: "tool_result"})
	telBuf.AddEvent(proxy.TelemetryEvent{Agent: "codex", EventName: "api_request"})

	api := proxy.NewControlAPI(proxy.NewLogBuffer(10), nil, nil, nil, telBuf)
	client := testControlClient(t, api)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)

	_, err := pollTelemetry(context.Background(), client, enc, 0, "codex", false, true, false)
	require.NoError(t, err)

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	require.Len(t, lines, 1)

	var event proxy.TelemetryEvent
	require.NoError(t, json.Unmarshal(lines[0], &event))
	assert.Equal(t, "codex", event.Agent)
}

func TestPollTelemetry_Metrics(t *testing.T) {
	telBuf := proxy.NewTelemetryBuffer(100)
	telBuf.UpdateMetric(proxy.MetricSummary{Name: "requests", Agent: "claude", Value: 42})

	api := proxy.NewControlAPI(proxy.NewLogBuffer(10), nil, nil, nil, telBuf)
	client := testControlClient(t, api)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)

	_, err := pollTelemetry(context.Background(), client, enc, 0, "", false, false, true)
	require.NoError(t, err)

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	require.Len(t, lines, 1)

	var result struct {
		Type  string  `json:"type"`
		Name  string  `json:"name"`
		Agent string  `json:"agent"`
		Value float64 `json:"value"`
	}
	require.NoError(t, json.Unmarshal(lines[0], &result))
	assert.Equal(t, "metric", result.Type)
	assert.Equal(t, "requests", result.Name)
	assert.Equal(t, float64(42), result.Value)
}

func TestPollTelemetry_CursorAdvances(t *testing.T) {
	telBuf := proxy.NewTelemetryBuffer(100)
	telBuf.AddEvent(proxy.TelemetryEvent{Agent: "claude", EventName: "e1"})

	api := proxy.NewControlAPI(proxy.NewLogBuffer(10), nil, nil, nil, telBuf)
	client := testControlClient(t, api)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)

	cursor, err := pollTelemetry(context.Background(), client, enc, 0, "", false, true, false)
	require.NoError(t, err)
	require.Greater(t, cursor, uint64(0))

	// Add another event, poll from cursor — should only get the new one.
	telBuf.AddEvent(proxy.TelemetryEvent{Agent: "claude", EventName: "e2"})
	buf.Reset()

	cursor2, err := pollTelemetry(context.Background(), client, enc, cursor, "", false, true, false)
	require.NoError(t, err)
	assert.Greater(t, cursor2, cursor)

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	require.Len(t, lines, 1)

	var event proxy.TelemetryEvent
	require.NoError(t, json.Unmarshal(lines[0], &event))
	assert.Equal(t, "e2", event.EventName)
}

func TestPollTelemetry_CancelledContext(t *testing.T) {
	telBuf := proxy.NewTelemetryBuffer(100)
	telBuf.AddEvent(proxy.TelemetryEvent{Agent: "claude", EventName: "e1"})

	api := proxy.NewControlAPI(proxy.NewLogBuffer(10), nil, nil, nil, telBuf)
	client := testControlClient(t, api)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cursor, err := pollTelemetry(ctx, client, enc, 0, "", false, true, true)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), cursor)
	assert.Empty(t, buf.String())
}
