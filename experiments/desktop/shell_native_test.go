//go:build !server

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestShutdownReleasesWaitingWindowAction(t *testing.T) {
	shell := newDesktopShell()
	result := make(chan error, 1)
	go func() {
		result <- shell.call(func() error { t.Error("queued native action ran after shutdown"); return nil })
	}()
	select {
	case <-shell.wake:
	case <-time.After(time.Second):
		t.Fatal("action was not queued")
	}
	shell.shutdown()
	select {
	case err := <-result:
		require.ErrorContains(t, err, "shutting down")
	case <-time.After(time.Second):
		t.Fatal("window action blocked during shutdown")
	}
	assert.False(t, shell.enqueue(func() { t.Error("accepted action after shutdown") }))
	shell.shutdown()
}

func TestNativeCallbacksCanQueueWithoutWaitingForUI(t *testing.T) {
	shell := newDesktopShell()
	defer shell.shutdown()
	// No consumer or native main loop: callbacks must still return promptly.
	for range 300 {
		require.True(t, shell.enqueue(func() {}))
	}
	require.Len(t, shell.queue, 300)
}

func TestClosePreservesTaskbarAccessWithoutTray(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(fmt.Sprint(available), func(t *testing.T) {
			shell := newDesktopShell()
			shell.trayAvailable = func(ctx context.Context) bool {
				deadline, ok := ctx.Deadline()
				require.True(t, ok, "tray probe must have a deadline")
				assert.LessOrEqual(t, time.Until(deadline), time.Second)
				return available
			}
			window := &fakeHideWindow{}
			shell.hideNativeWindow(window)
			assert.Equal(t, available, window.hidden)
			assert.Equal(t, !available, window.minimised)
		})
	}
}

type fakeHideWindow struct{ hidden, minimised bool }

func (w *fakeHideWindow) Hide() application.Window     { w.hidden = true; return nil }
func (w *fakeHideWindow) Minimise() application.Window { w.minimised = true; return nil }
