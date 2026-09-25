package overlay

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// muxHarness runs an InputMux over a chunkReader, recording what reaches
// the container.
type muxHarness struct {
	mux  *InputMux
	src  *chunkReader
	cont *syncBuffer
	err  chan error
}

func newMuxHarness(t *testing.T) *muxHarness {
	t.Helper()
	h := &muxHarness{src: newChunkReader(), cont: &syncBuffer{}, err: make(chan error, 1)}
	h.mux = NewInputMux(h.src, h.cont)
	go func() { h.err <- h.mux.Run() }()
	t.Cleanup(func() {
		h.src.close()
		select {
		case <-h.mux.Done():
		case <-time.After(5 * time.Second):
			t.Error("Run didn't return")
		}
	})
	return h
}

// barrier drains and completes T2 with a lone barrier reply.
func (h *muxHarness) barrier(t *testing.T) io.Reader {
	t.Helper()
	h.mux.Drain()
	h.src.send(t, barrierReply)
	in, err := h.mux.AwaitBarrier(context.Background(), time.Second)
	require.NoError(t, err)
	return in
}

func TestInputMuxRoutesToContainer(t *testing.T) {
	h := newMuxHarness(t)
	h.src.send(t, "ls\r")
	h.src.send(t, "\x1b[A")
	h.src.send(t, barrierReply)
	assert.Equal(t, "ls\r\x1b[A"+barrierReply, h.cont.String(), "an app's DSR 5n reply passes while attached")
}

func TestInputMuxBarrier(t *testing.T) {
	tests := []struct {
		name          string
		chunks        []string
		wantContainer string
		wantPrompt    string
	}{
		{name: "reply alone", chunks: []string{"\x1b[0n"}},
		{name: "keys around the reply", chunks: []string{"ab\x1b[0ncd"}, wantContainer: "ab", wantPrompt: "cd"},
		{name: "reply split across reads", chunks: []string{"a\x1b[", "0nb"}, wantContainer: "a", wantPrompt: "b"},
		{name: "reply split at every byte", chunks: []string{"\x1b", "[", "0", "n", "x"}, wantPrompt: "x"},
		{name: "another reply before the barrier", chunks: []string{"\x1b[12;5R\x1b[0n"}, wantContainer: "\x1b[12;5R"},
		{name: "escape key before the reply", chunks: []string{"\x1b", "\x1b[0n"}, wantContainer: "\x1b"},
		{name: "arrow key split before the reply", chunks: []string{"\x1b[", "A\x1b[0nq"}, wantContainer: "\x1b[A", wantPrompt: "q"},
		{name: "app DSR 5n in flight gets one reply", chunks: []string{"\x1b[0n", "\x1b[0n"}, wantContainer: "\x1b[0n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newMuxHarness(t)
			h.mux.Drain()
			for _, c := range tt.chunks {
				h.src.send(t, c)
			}
			in, err := h.mux.AwaitBarrier(context.Background(), time.Second)
			require.NoError(t, err)
			assert.Equal(t, tt.wantContainer, h.cont.String())
			if tt.wantPrompt != "" {
				assert.Equal(t, tt.wantPrompt, readN(t, in, len(tt.wantPrompt)))
			}
			h.mux.Release(0, func() {})
			rest, err := io.ReadAll(in)
			require.NoError(t, err)
			assert.Empty(t, string(rest), "nothing else reached the prompt")
		})
	}
}

func TestInputMuxPromptGetsEscapeRightAway(t *testing.T) {
	h := newMuxHarness(t)
	in := h.barrier(t)
	h.src.send(t, "\x1b")
	assert.Equal(t, "\x1b", readN(t, in, 1))
}

func TestInputMuxReleaseHandsInputBack(t *testing.T) {
	h := newMuxHarness(t)
	in := h.barrier(t)
	h.src.send(t, "p")
	assert.Equal(t, "p", readN(t, in, 1))
	h.mux.Release(0, func() {})
	h.src.send(t, "c")
	assert.Equal(t, "c", h.cont.String())
	_, err := in.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF, "release closes the prompt's input")
}

func TestInputMuxReleaseRunsLeaveUnderLock(t *testing.T) {
	h := newMuxHarness(t)
	h.barrier(t)
	started, proceed := make(chan struct{}), make(chan struct{})
	released := make(chan struct{})
	go func() {
		h.mux.Release(0, func() {
			close(started)
			<-proceed
		})
		close(released)
	}()
	<-started
	routed := make(chan bool, 1)
	go func() { routed <- h.src.push("k") }()
	select {
	case <-routed:
		t.Fatal("input was routed while leave ran")
	case <-time.After(50 * time.Millisecond):
	}
	close(proceed)
	<-released
	require.True(t, <-routed)
	assert.Equal(t, "k", h.cont.String(), "input after T3 goes to the container")
}

func TestInputMuxReleaseFlushesHeldBytes(t *testing.T) {
	h := newMuxHarness(t)
	h.mux.Drain()
	h.src.send(t, "\x1b[")
	assert.Empty(t, h.cont.String(), "a possible reply prefix waits while draining")
	h.mux.Release(0, func() {})
	assert.Equal(t, "\x1b[", h.cont.String())
}

func TestInputMuxStripsLateBarrierReply(t *testing.T) {
	h := newMuxHarness(t)
	h.mux.Drain()
	_, err := h.mux.AwaitBarrier(context.Background(), 20*time.Millisecond)
	require.ErrorIs(t, err, ErrBarrierTimeout)
	h.mux.Release(time.Second, func() {})
	h.src.send(t, "a\x1b[0nb")
	h.src.send(t, "\x1b[0n")
	assert.Equal(t, "ab\x1b[0n", h.cont.String(), "only one reply is the barrier's")
}

func TestInputMuxStripWindowExpires(t *testing.T) {
	h := newMuxHarness(t)
	h.mux.Drain()
	_, err := h.mux.AwaitBarrier(context.Background(), 10*time.Millisecond)
	require.ErrorIs(t, err, ErrBarrierTimeout)
	h.mux.Release(20*time.Millisecond, func() {})
	time.Sleep(50 * time.Millisecond)
	h.src.send(t, "\x1b[0n")
	assert.Equal(t, "\x1b[0n", h.cont.String())
}

func TestInputMuxNoStripOnceTheReplyArrived(t *testing.T) {
	h := newMuxHarness(t)
	h.barrier(t)
	h.mux.Release(time.Second, func() {})
	h.src.send(t, "\x1b[0n")
	assert.Equal(t, "\x1b[0n", h.cont.String(), "a later reply belongs to the app")
}

func TestInputMuxAwaitBarrierCancelled(t *testing.T) {
	h := newMuxHarness(t)
	h.mux.Drain()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.mux.AwaitBarrier(ctx, time.Second)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestInputMuxAwaitSilence(t *testing.T) {
	h := newMuxHarness(t)
	h.mux.Drain()
	h.src.send(t, "k1")
	start := time.Now()
	in, err := h.mux.AwaitSilence(context.Background(), 30*time.Millisecond)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond, "waits for quiet")
	assert.Equal(t, "k1", h.cont.String())
	h.src.send(t, "p")
	assert.Equal(t, "p", readN(t, in, 1))
}

func TestInputMuxEOF(t *testing.T) {
	t.Run("while attached", func(t *testing.T) {
		h := newMuxHarness(t)
		h.src.close()
		<-h.mux.Done()
		assert.NoError(t, <-h.err)
		h.mux.Drain()
		_, err := h.mux.AwaitBarrier(context.Background(), time.Second)
		assert.ErrorIs(t, err, ErrClosed)
	})
	t.Run("while prompting", func(t *testing.T) {
		h := newMuxHarness(t)
		in := h.barrier(t)
		h.src.close()
		_, err := in.Read(make([]byte, 1))
		assert.ErrorIs(t, err, io.EOF, "the prompt sees EOF")
	})
}

func TestInputMuxPromptInputNeverBlocks(t *testing.T) {
	h := newMuxHarness(t)
	in := h.barrier(t)
	h.src.send(t, strings.Repeat("x", promptInputMax+100))
	assert.Len(t, readN(t, in, promptInputMax), promptInputMax)
	h.mux.Release(0, func() {})
	rest, err := io.ReadAll(in)
	require.NoError(t, err)
	assert.Empty(t, rest, "the excess was dropped")
}

func TestInputMuxPosition(t *testing.T) {
	tests := []struct {
		name     string
		chunks   []string
		row, col int
		cont     string
	}{
		{name: "reply alone", chunks: []string{"\x1b[12;3R"}, row: 12, col: 3},
		{name: "input around the reply", chunks: []string{"ab\x1b[7;1Rcd"}, row: 7, col: 1, cont: "abcd"},
		{name: "reply split across reads", chunks: []string{"\x1b", "[1", "2;4", "0R"}, row: 12, col: 40},
		{name: "other sequences pass", chunks: []string{"\x1b[A\x1b\x1b[1;2x", "\x1b[2;2R"}, row: 2, col: 2, cont: "\x1b[A\x1b\x1b[1;2x"},
		{name: "input after the reply", chunks: []string{"\x1b[3;1R\x1b[1;5R"}, row: 3, col: 1, cont: "\x1b[1;5R"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newMuxHarness(t)
			h.mux.ExpectPosition()
			for _, c := range tt.chunks {
				h.src.send(t, c)
			}
			row, col, err := h.mux.AwaitPosition(context.Background(), time.Second, time.Second)
			require.NoError(t, err)
			assert.Equal(t, tt.row, row)
			assert.Equal(t, tt.col, col)
			assert.Equal(t, tt.cont, h.cont.String())
		})
	}
}

func TestInputMuxPositionTimeoutStripsALateReply(t *testing.T) {
	h := newMuxHarness(t)
	h.mux.ExpectPosition()
	h.src.send(t, "\x1b[")
	_, _, err := h.mux.AwaitPosition(context.Background(), 10*time.Millisecond, time.Second)
	require.ErrorIs(t, err, errPositionTimeout)
	assert.Equal(t, "\x1b[", h.cont.String(), "held bytes are flushed at the timeout")
	h.src.send(t, "a\x1b[5;1Rb")
	assert.Equal(t, "\x1b[ab", h.cont.String(), "the late reply is dropped")
	h.src.send(t, "\x1b[5;1R")
	assert.Equal(t, "\x1b[ab\x1b[5;1R", h.cont.String(), "only one reply is dropped")
}

func TestInputMuxPositionStripWindowExpires(t *testing.T) {
	h := newMuxHarness(t)
	h.mux.ExpectPosition()
	_, _, err := h.mux.AwaitPosition(context.Background(), time.Millisecond, time.Millisecond)
	require.ErrorIs(t, err, errPositionTimeout)
	time.Sleep(5 * time.Millisecond)
	h.src.send(t, "\x1b[1;5R")
	assert.Equal(t, "\x1b[1;5R", h.cont.String())
}

func TestInputMuxPositionEOF(t *testing.T) {
	h := newMuxHarness(t)
	h.mux.ExpectPosition()
	h.src.close()
	_, _, err := h.mux.AwaitPosition(context.Background(), time.Second, time.Second)
	require.ErrorIs(t, err, ErrClosed)
}
