package ward

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutputGateEmitWritesImmediatelyInGround(t *testing.T) {
	var out bytes.Buffer
	g := newOutputGate(&out, &sync.Mutex{})

	g.Forward([]byte("hello ─"))
	g.Emit("<bar>")

	assert.Equal(t, "hello ─<bar>", out.String())
	assert.True(t, g.Ready())
}

func TestOutputGateDefersEmitInsideRune(t *testing.T) {
	var out bytes.Buffer
	g := newOutputGate(&out, &sync.Mutex{})

	g.Forward([]byte("\xe2\x94"))
	g.Emit("<bar>")
	require.Equal(t, "\xe2\x94", out.String(), "bar must not split the character")
	assert.False(t, g.Ready())

	g.Forward([]byte("\x80x"))
	assert.Equal(t, "\xe2\x94\x80<bar>x", out.String(), "bar goes in at the first safe byte, not the chunk end")
	assert.True(t, g.Ready())
}

func TestOutputGateDefersEmitInsideEscapeSequence(t *testing.T) {
	var out bytes.Buffer
	g := newOutputGate(&out, &sync.Mutex{})

	g.Forward([]byte("\x1b[38;2;1"))
	g.Emit("<bar>")
	require.Equal(t, "\x1b[38;2;1", out.String())

	g.Forward([]byte("2;3m─"))
	assert.Equal(t, "\x1b[38;2;12;3m<bar>─", out.String())
}

func TestOutputGateFlushesDeferredInOrder(t *testing.T) {
	var out bytes.Buffer
	g := newOutputGate(&out, &sync.Mutex{})

	g.Forward([]byte("\xe2"))
	g.Emit("<one>")
	g.Emit("<two>")
	g.Forward([]byte("\x94\x80"))

	assert.Equal(t, "\xe2\x94\x80<one><two>", out.String())
}

func TestOutputGateForwardReportsScanResult(t *testing.T) {
	var out bytes.Buffer
	g := newOutputGate(&out, &sync.Mutex{})

	r := g.Forward([]byte("\x1b[2J"))
	assert.True(t, r.BarErased)
	assert.Equal(t, "\x1b[2J", out.String())
}

func TestOutputGateForceFlushesAfterHoldTimeout(t *testing.T) {
	var out bytes.Buffer
	var mu sync.Mutex
	g := newOutputGate(&out, &mu)
	g.holdTimeout = 20 * time.Millisecond

	mu.Lock()
	g.Forward([]byte("\x1b]0;title with no terminator"))
	g.Emit("<bar>")
	require.Equal(t, "\x1b]0;title with no terminator", out.String())
	mu.Unlock()

	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return bytes.HasSuffix(out.Bytes(), []byte("<bar>"))
	}, time.Second, 5*time.Millisecond, "a stuck child must not hide the bar for good")

	mu.Lock()
	defer mu.Unlock()
	assert.True(t, g.Ready(), "the terminal restarted parsing on the forced ESC; so must the gate")
	g.Emit("<next>")
	assert.Equal(t, "\x1b]0;title with no terminator<bar><next>", out.String())
}

func TestOutputGateHoldTimerStopsOnNormalFlush(t *testing.T) {
	var out bytes.Buffer
	var mu sync.Mutex
	g := newOutputGate(&out, &mu)
	g.holdTimeout = 20 * time.Millisecond

	mu.Lock()
	g.Forward([]byte("\xe2"))
	g.Emit("<bar>")
	g.Forward([]byte("\x94\x80"))
	mu.Unlock()

	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "\xe2\x94\x80<bar>", out.String(), "the timer must not write the bar twice")
}

func TestOutputGateDrainWritesHeldBytes(t *testing.T) {
	var out bytes.Buffer
	g := newOutputGate(&out, &sync.Mutex{})

	g.Forward([]byte("\x1b["))
	g.Emit("<clear>")
	g.Drain()

	assert.Equal(t, "\x1b[<clear>", out.String())
	assert.Empty(t, g.deferred)
}
