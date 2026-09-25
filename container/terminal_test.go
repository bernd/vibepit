package container

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bernd/vibepit/overlay"
	"github.com/docker/docker/api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatchResizeSignalsStopsOnDone(t *testing.T) {
	sigCh := make(chan os.Signal, 4)
	done := make(chan struct{})
	exited := make(chan struct{})

	var calls atomic.Int32
	go func() {
		watchResizeSignals(sigCh, done, func() {
			calls.Add(1)
		})
		close(exited)
	}()

	// Trigger one resize event.
	sigCh <- os.Interrupt
	require.Eventually(t, func() bool { return calls.Load() == 1 }, time.Second, 10*time.Millisecond)

	// Closing done should stop the watcher promptly.
	close(done)
	require.Eventually(t, func() bool {
		select {
		case <-exited:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
}

func TestBuildAttachOptions(t *testing.T) {
	var got []string
	o := buildAttachOptions([]AttachOption{
		WithTerminal(func(*overlay.Terminal) { got = append(got, "terminal") }),
		WithLogf(func(string, ...any) { got = append(got, "log") }),
	})
	o.onTerminal(nil)
	o.logf("x")
	assert.Equal(t, []string{"terminal", "log"}, got)
	assert.Nil(t, buildAttachOptions(nil).onTerminal)
}

// lockedBuffer is a bytes.Buffer the session goroutines can share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestNewSessionTerminal(t *testing.T) {
	client, server := net.Pipe()
	resp := types.NewHijackedResponse(client, "")
	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close() })
	stdout := &lockedBuffer{}
	var resized [][2]uint
	st := newSessionTerminal(resp, stdinR, stdout,
		func() (int, int, error) { return 80, 24, nil },
		func(height, width uint) { resized = append(resized, [2]uint{height, width}) },
		buildAttachOptions(nil))
	runDone := make(chan error, 1)
	go func() { runDone <- st.Run(context.Background()) }()

	_, err := server.Write([]byte("hello"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return stdout.String() == "hello" }, 5*time.Second, time.Millisecond)

	go func() { _, _ = stdinW.Write([]byte("ls\r")) }()
	buf := make([]byte, 3)
	_, err = io.ReadFull(server, buf)
	require.NoError(t, err)
	assert.Equal(t, "ls\r", string(buf))

	st.Resize()
	assert.Equal(t, [][2]uint{{24, 80}}, resized, "Docker takes the height first")

	assert.ErrorIs(t, st.Show(context.Background(), nil), overlay.ErrUnavailable, "no shadow without WithTerminal")

	require.NoError(t, server.Close())
	select {
	case err := <-runDone:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't end with the container output")
	}
}
