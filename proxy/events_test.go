package proxy

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recvEvent(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case e, ok := <-ch:
		require.True(t, ok, "channel closed")
		return e
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

func TestEventHub_SubscribeAndPublish(t *testing.T) {
	h := NewEventHub()
	assert.False(t, h.Watching())

	a, unsubA := h.Subscribe()
	b, unsubB := h.Subscribe()
	assert.True(t, h.Watching())

	h.Publish(Event{Type: EventCursor, Cursor: 7})
	assert.Equal(t, uint64(7), recvEvent(t, a).Cursor)
	assert.Equal(t, uint64(7), recvEvent(t, b).Cursor)

	unsubA()
	assert.True(t, h.Watching())
	unsubA() // idempotent
	assert.True(t, h.Watching())
	unsubB()
	assert.False(t, h.Watching())
}

func TestEventHub_DropsSlowSubscriber(t *testing.T) {
	h := NewEventHub()
	slow, _ := h.Subscribe()
	fast, unsubFast := h.Subscribe()
	defer unsubFast()

	received := make(chan int, 1)
	go func() {
		n := 0
		for range fast {
			n++
			if n == eventBuffer+1 {
				break
			}
		}
		received <- n
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < eventBuffer+1; i++ {
			h.Publish(Event{Type: EventCursor, Cursor: uint64(i)})
			// Let the draining subscriber keep up; only the slow one may fill.
			time.Sleep(time.Microsecond)
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked")
	}

	select {
	case n := <-received:
		assert.Equal(t, eventBuffer+1, n)
	case <-time.After(time.Second):
		t.Fatal("draining subscriber missed events")
	}

	// The slow channel holds its buffered events, then reports closed.
	n := 0
	for range slow {
		n++
	}
	assert.Equal(t, eventBuffer, n)
	assert.True(t, h.Watching(), "the draining subscriber remains")
}

func TestEventHub_OnEmpty(t *testing.T) {
	h := NewEventHub()
	fired := make(chan struct{}, 4)
	h.OnEmpty(func() { fired <- struct{}{} })

	_, unsubA := h.Subscribe()
	_, unsubB := h.Subscribe()

	unsubA()
	select {
	case <-fired:
		t.Fatal("hook fired with a subscriber left")
	case <-time.After(50 * time.Millisecond):
	}

	unsubB()
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("hook did not fire when the last subscriber left")
	}

	unsubB() // idempotent: no second firing
	select {
	case <-fired:
		t.Fatal("hook fired twice")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEventHub_OnEmptyAfterDrop(t *testing.T) {
	h := NewEventHub()
	fired := make(chan struct{}, 2)
	h.OnEmpty(func() { fired <- struct{}{} })
	_, _ = h.Subscribe()

	for i := 0; i <= eventBuffer; i++ {
		h.Publish(Event{Type: EventCursor})
	}
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("hook did not fire when the last subscriber was dropped")
	}
	assert.False(t, h.Watching())
}

func TestEventRoundTrip(t *testing.T) {
	ts := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	entry := LogEntry{
		ID: 3, Time: ts, Domain: "example.com", Port: "443",
		Action: ActionHold, Source: SourceProxy, Deadline: ts.Add(10 * time.Second),
	}
	tests := []struct {
		name string
		in   Event
	}{
		{"log", Event{Type: EventLog, Log: entry}},
		{"cursor", Event{Type: EventCursor, Cursor: 42}},
		{"hold-open", Event{Type: EventHoldOpen, Log: entry}},
		{"hold-end", Event{Type: EventHoldEnd, HoldEnd: HoldEnd{
			ID:      3,
			HostKey: HostKey{Source: SourceProxy, Domain: "example.com", Port: "443"},
			Outcome: HoldAllowed,
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			buf.WriteString(": keepalive\n\n")
			require.NoError(t, WriteEvent(&buf, tt.in))

			r := bufio.NewReader(&buf)
			got, err := ReadEvent(r)
			require.NoError(t, err)
			assert.Equal(t, tt.in, got)

			_, err = ReadEvent(r)
			assert.ErrorIs(t, err, io.EOF)
		})
	}
}

func TestEventWireFormat(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteEvent(&buf, Event{Type: EventCursor, Cursor: 5}))
	assert.Equal(t, "event: cursor\ndata: {\"id\":5}\n\n", buf.String())
}

func TestReadEvent(t *testing.T) {
	t.Run("unknown type is returned undecoded", func(t *testing.T) {
		r := bufio.NewReader(strings.NewReader("event: future\ndata: {\"x\":1}\n\nevent: cursor\ndata: {\"id\":9}\n\n"))
		e, err := ReadEvent(r)
		require.NoError(t, err)
		assert.Equal(t, Event{Type: "future"}, e)
		e, err = ReadEvent(r)
		require.NoError(t, err)
		assert.Equal(t, uint64(9), e.Cursor)
	})
	t.Run("malformed", func(t *testing.T) {
		for name, in := range map[string]string{
			"data without event": "data: {}\n\n",
			"bad json":           "event: log\ndata: nope\n\n",
		} {
			t.Run(name, func(t *testing.T) {
				_, err := ReadEvent(bufio.NewReader(strings.NewReader(in)))
				require.Error(t, err)
				assert.NotErrorIs(t, err, io.EOF)
			})
		}
	})
}
