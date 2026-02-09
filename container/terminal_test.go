package container

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

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

