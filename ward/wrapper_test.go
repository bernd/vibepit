package ward

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrapperRunsCommandAndExits(t *testing.T) {
	w := NewWrapper(Options{
		Command: []string{"echo", "hello"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := w.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, exitCode)
}

func TestWrapperExitCodePreserved(t *testing.T) {
	w := NewWrapper(Options{
		Command: []string{"sh", "-c", "exit 42"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := w.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 42, exitCode)
}

func TestResizeRepairSeq(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		lastOutputAt time.Time
		want         string
	}{
		{
			name: "idle resize keeps cursor position",
			want: scrollRegionSeq(37),
		},
		{
			name:         "active output resize nudges cursor into scroll region",
			lastOutputAt: now.Add(-activeOutputResizeWindow + time.Nanosecond),
			want:         resizeScrollRegionSeq(37),
		},
		{
			name:         "resize at active output boundary is idle",
			lastOutputAt: now.Add(-activeOutputResizeWindow),
			want:         scrollRegionSeq(37),
		},
		{
			name:         "old output does not move cursor",
			lastOutputAt: now.Add(-activeOutputResizeWindow - time.Nanosecond),
			want:         scrollRegionSeq(37),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resizeRepairSeq(37, tt.lastOutputAt, now)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestResizeScrollRegionSeqMovesCursorUp(t *testing.T) {
	got := resizeScrollRegionSeq(37)
	want := scrollRegionSeq(37) + "\x1b[A"
	require.Equal(t, want, got)
}

func TestNewWrapperDefaultHotkey(t *testing.T) {
	w := NewWrapper(Options{Command: []string{"true"}})
	assert.Equal(t, byte(0x1D), w.opts.Hotkey)
}

func TestNewWrapperCustomHotkey(t *testing.T) {
	w := NewWrapper(Options{Command: []string{"true"}, Hotkey: 'x'})
	assert.Equal(t, byte('x'), w.opts.Hotkey)
}

func TestScrollRegionSeqFormat(t *testing.T) {
	tests := []struct {
		maxRow int
		want   string
	}{
		{23, "\x1b7\x1b[1;23r\x1b8"},
		{1, "\x1b7\x1b[1;1r\x1b8"},
		{100, "\x1b7\x1b[1;100r\x1b8"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("maxRow_%d", tt.maxRow), func(t *testing.T) {
			assert.Equal(t, tt.want, scrollRegionSeq(tt.maxRow))
		})
	}
}

func TestWrapperRunNonexistentCommand(t *testing.T) {
	w := NewWrapper(Options{
		Command: []string{"/nonexistent/binary"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := w.Run(ctx)
	require.Error(t, err)
}

func TestWrapperRunEnvPassthrough(t *testing.T) {
	w := NewWrapper(Options{
		Command: []string{"sh", "-c", `test "$WARD_TEST_VAR" = "hello"`},
		Env:     []string{"WARD_TEST_VAR=hello"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := w.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
}

func TestWrapperRunWithStatusChannel(t *testing.T) {
	statusCh := make(chan StatusUpdate, 1)
	statusCh <- StatusUpdate{Message: "test status"}

	w := NewWrapper(Options{
		Command: []string{"echo", "hello"},
		Status:  statusCh,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := w.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
}

func TestWrapperRunContextCancellation(t *testing.T) {
	w := NewWrapper(Options{
		Command: []string{"sleep", "60"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx) //nolint:errcheck
	}()

	select {
	case <-done:
		// Child was killed promptly
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
