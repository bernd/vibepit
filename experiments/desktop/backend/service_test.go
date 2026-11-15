package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bufferCloser struct{ bytes.Buffer }

func (b *bufferCloser) Close() error { return nil }

func TestToolDecisionsPreserveOriginalInput(t *testing.T) {
	for _, allow := range []bool{true, false} {
		name := "deny"
		if allow {
			name = "allow"
		}
		t.Run(name, func(t *testing.T) {
			input := &bufferCloser{}
			c := &connection{}
			c.active.Store(true)
			a := &agent{c: c, input: input, permissions: make(map[string]json.RawMessage)}
			c.agent = a
			s := &Service{conn: c}
			require.NoError(t, a.handle(json.RawMessage(`{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","input":{"command":"printf 'a\\nb'","nested":[1,true]},"tool_name":"Bash"}}`)))
			require.NoError(t, s.DecideTool("", "r1", allow))
			var response struct {
				Type     string
				Response struct {
					Subtype   string
					RequestID string `json:"request_id"`
					Response  map[string]any
				}
			}
			require.NoError(t, json.Unmarshal(input.Bytes(), &response))
			assert.Equal(t, "control_response", response.Type)
			assert.Equal(t, "r1", response.Response.RequestID)
			assert.Equal(t, "success", response.Response.Subtype)
			assert.Equal(t, name, response.Response.Response["behavior"])
			if allow {
				assert.Equal(t, map[string]any{"command": "printf 'a\\nb'", "nested": []any{float64(1), true}}, response.Response.Response["updatedInput"])
			}
			require.Error(t, s.DecideTool("", "r1", allow), "a request must not be approved twice")
		})
	}
}

func TestAgentControlErrorsAndCancellation(t *testing.T) {
	input := &bufferCloser{}
	c := &connection{}
	c.active.Store(true)
	a := &agent{c: c, input: input, ready: make(chan struct{}), permissions: map[string]json.RawMessage{"r1": json.RawMessage(`{}`)}}
	require.NoError(t, a.handle(json.RawMessage(`{"type":"control_cancel_request","request_id":"r1"}`)))
	assert.Empty(t, a.permissions)
	require.NoError(t, a.handle(json.RawMessage(`{"type":"control_request","request_id":"r2","request":{"subtype":"future_feature"}}`)))
	assert.Contains(t, input.String(), `"subtype":"error"`)
	require.NoError(t, a.handle(json.RawMessage(`{"type":"control_response","response":{"request_id":"initialize","subtype":"error","error":"unsupported"}}`)))
	select {
	case <-a.ready:
	default:
		t.Fatal("initialization failure did not unblock the caller")
	}
	require.ErrorContains(t, a.initErr, "unsupported")
	require.Error(t, a.handle(json.RawMessage(`not JSON`)))
}

func TestTerminalPreservesSplitUTF8(t *testing.T) {
	var got []byte
	c := &connection{emit: func(e Event) {
		data, err := base64.StdEncoding.DecodeString(e.Data.(string))
		require.NoError(t, err)
		got = append(got, data...)
	}}
	c.active.Store(true)
	w := terminalWriter{c}
	want := []byte("\x1b[31mHello 世界\x1b[0m")
	for _, b := range want {
		_, err := w.Write([]byte{b})
		require.NoError(t, err)
	}
	assert.Equal(t, want, got)
	c.active.Store(false)
	w.Write([]byte("stale"))
	assert.Equal(t, want, got, "disconnected output must not reach another terminal")
}

func TestNetworkRejectsStaleAndUnallowableRequests(t *testing.T) {
	c := &connection{pending: make(map[uint64]proxy.LogEntry)}
	c.active.Store(true)
	s := &Service{conn: c, demo: true}
	entry := proxy.LogEntry{ID: 1, Domain: "example.com", Port: "443", Source: proxy.SourceProxy, Action: proxy.ActionHold, Code: proxy.BlockNotAllowlisted, Deadline: time.Now().Add(time.Minute)}
	c.networkEvent(proxy.Event{Type: proxy.EventHoldOpen, Log: entry})
	require.Len(t, c.pending, 1)
	entry.ID = 2
	c.networkEvent(proxy.Event{Type: proxy.EventHoldOpen, Log: entry})
	require.Len(t, c.pending, 1)
	require.Error(t, s.DecideNetwork("", 1, true))
	c.networkEvent(proxy.Event{Type: proxy.EventHoldEnd, HoldEnd: proxy.HoldEnd{ID: 1}})
	require.Len(t, c.pending, 1, "old hold-end must not remove a newer hold")
	require.NoError(t, s.DecideNetwork("", 2, false))
	assert.Empty(t, c.pending)
	entry.ID = 3
	entry.Deadline = time.Now().Add(-time.Second)
	c.networkEvent(proxy.Event{Type: proxy.EventHoldOpen, Log: entry})
	require.Error(t, s.DecideNetwork("", 3, true))
	entry.ID = 4
	entry.Domain = "*.example.com"
	c.networkEvent(proxy.Event{Type: proxy.EventHoldOpen, Log: entry})
	assert.NotContains(t, c.pending, uint64(4))
}

func TestDemoCanInterruptBeforeWorkerStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	var once sync.Once
	c := &connection{ctx: ctx, pending: make(map[uint64]proxy.LogEntry), emit: func(e Event) {
		if e.Kind == "agent-status" && e.Data == "Ready" {
			once.Do(func() { close(finished) })
		}
	}}
	c.active.Store(true)
	a := &agent{c: c, demo: true, busy: true, demoInterrupt: make(chan struct{}), demoAnswer: make(chan bool, 1), permissions: make(map[string]json.RawMessage)}
	c.agent = a
	s := &Service{conn: c, demo: true}
	require.NoError(t, s.InterruptAgent(""))
	require.NoError(t, s.InterruptAgent(""))
	go a.simulate()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("interrupt was lost before the worker started")
	}
}

func TestOldConnectionCannotWriteToNewSandbox(t *testing.T) {
	s := New(true, func(Event) { t.Error("stale input reached the new sandbox") }, t.TempDir())
	token, err := s.Connect("demo")
	require.NoError(t, err)
	s.Disconnect()
	_, err = s.Connect("demo")
	require.NoError(t, err)
	defer s.Disconnect()
	require.Error(t, s.WriteTerminal(token, "rm important-file\n"))
	require.Error(t, s.OpenTerminal(token, 80, 24))
	require.Error(t, s.SendPrompt(token, "change this project"))
	require.Error(t, s.DecideNetwork(token, 1, true))
	require.Error(t, s.DecideTool(token, "r1", true))
}

func TestAgentDebugCapturesWireMessagesBeforeRouting(t *testing.T) {
	var records []AgentDebugMessage
	c := &connection{emit: func(e Event) {
		if e.Kind == "agent-debug" {
			records = append(records, e.Data.(AgentDebugMessage))
		}
	}}
	c.active.Store(true)
	input := &bufferCloser{}
	a := &agent{c: c, input: input, ready: make(chan struct{}), permissions: make(map[string]json.RawMessage)}
	require.NoError(t, a.write(controlRequest("initialize", "initialize")))
	require.Len(t, records, 1)
	assert.Equal(t, "stdin", records[0].Stream)
	assert.Equal(t, input.String(), records[0].Raw, "log must match the serialized input, including its newline")

	for _, tc := range []struct {
		name, raw string
		fails     bool
	}{
		{"initialize response", ` { "type": "control_response", "response": {"request_id":"initialize", "subtype":"success"} } `, false},
		{"unknown message", `{"type":"future_message","payload":{"preserve":"everything"}}`, false},
		{"malformed output", `{"type": invalid JSON`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(records)
			err := a.handle(json.RawMessage(tc.raw))
			if tc.fails {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, records, before+1)
			assert.Equal(t, "stdout", records[before].Stream)
			assert.Equal(t, tc.raw, records[before].Raw)
		})
	}

	before := len(records)
	require.NoError(t, a.handle(json.RawMessage(`{"type":"control_request","request_id":"unsupported","request":{"subtype":"future_feature"}}`)))
	require.Len(t, records, before+2, "unsupported request and automatic error response must both be captured")
	assert.Equal(t, "stdout", records[before].Stream)
	assert.Equal(t, "stdin", records[before+1].Stream)
	assert.Contains(t, records[before+1].Raw, `"subtype":"error"`)
	_, err := (agentErrorWriter{a}).Write([]byte("Claude stderr\n"))
	require.NoError(t, err)
	assert.Equal(t, "stderr", records[len(records)-1].Stream)
	assert.Equal(t, "Claude stderr\n", records[len(records)-1].Raw)

	a.input = nil
	require.Error(t, a.write(controlRequest("stop", "interrupt")))
	assert.Equal(t, "stdin", records[len(records)-2].Stream)
	assert.Equal(t, "transport", records[len(records)-1].Stream)
	assert.Contains(t, records[len(records)-1].Raw, "write failed")
	for i, record := range records {
		assert.Equal(t, uint64(i+1), record.Sequence)
		assert.False(t, record.Time.IsZero())
	}
}
