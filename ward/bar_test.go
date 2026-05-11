package ward

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderStatusBarDefault(t *testing.T) {
	bar := RenderStatusBar("session active", 80, false)
	require.NotEmpty(t, bar)
	require.Contains(t, bar, "session active")
}

func TestRenderStatusBarAlert(t *testing.T) {
	bar := RenderStatusBar("blocked api.example.com:443", 80, true)
	require.NotEmpty(t, bar)
	require.Contains(t, bar, "api.example.com")
}

func TestRenderStatusBarSanitizesControlChars(t *testing.T) {
	bar := RenderStatusBar("bad\x01message\x7f", 80, false)
	assert.NotContains(t, bar, "\x01")
	assert.NotContains(t, bar, "\x7f")
	require.Contains(t, bar, "badmessage")
}

func TestRenderStatusBarSanitizesC1Controls(t *testing.T) {
	// U+009B (CSI) encoded as UTF-8 is 0xC2 0x9B
	bar := RenderStatusBar("before\xc2\x9b31mafter", 80, false)
	assert.NotContains(t, bar, "\xc2\x9b")
	require.Contains(t, bar, "before")
	require.Contains(t, bar, "after")
}

func TestRenderCommandBarSanitizesDesc(t *testing.T) {
	hints := []KeyHint{
		{Key: 'a', Desc: "allow\x1b[31m injected", RequireAlert: false},
	}
	bar := RenderCommandBar("", hints, 120, false)
	assert.NotContains(t, bar, "\x1b[31m")
	require.Contains(t, bar, "allow")
	require.Contains(t, bar, "injected")
}

func TestRenderStatusBarTruncatesLongMessage(t *testing.T) {
	bar := RenderStatusBar(strings.Repeat("x", 200), 80, false)
	require.NotEmpty(t, bar)
}

func TestRenderStatusBarDefaultStyle(t *testing.T) {
	bar := RenderStatusBar("session info", 40, false)
	require.Contains(t, bar, "session info")
}

func TestRenderStatusBarAlertStyle(t *testing.T) {
	bar := RenderStatusBar("blocked!", 40, true)
	require.Contains(t, bar, "blocked!")
}

func TestRenderStatusBarEmptyMessage(t *testing.T) {
	bar := RenderStatusBar("", 80, false)
	require.NotEmpty(t, bar)
}

func TestRenderCommandBarWithAlert(t *testing.T) {
	hints := []KeyHint{
		{Key: 'a', Desc: "allow", RequireAlert: true},
		{Key: 'A', Desc: "allow+save", RequireAlert: true},
	}
	bar := RenderCommandBar("github.com:443", hints, 120, true)
	require.Contains(t, bar, "github.com:443")
	require.Contains(t, bar, "[a]")
	require.Contains(t, bar, "allow")
	require.Contains(t, bar, "[A]")
	require.Contains(t, bar, "allow+save")
	require.Contains(t, bar, "[esc]")
	require.Contains(t, bar, "cancel")
}

func TestRenderCommandBarWithoutAlert(t *testing.T) {
	hints := []KeyHint{
		{Key: 'a', Desc: "allow", RequireAlert: true},
	}
	bar := RenderCommandBar("", hints, 80, false)
	assert.NotContains(t, bar, "[a]")
	require.Contains(t, bar, "[esc]")
	require.Contains(t, bar, "cancel")
}

func TestRenderCommandBarTruncates(t *testing.T) {
	hints := []KeyHint{
		{Key: 'a', Desc: "allow", RequireAlert: true},
	}
	bar := RenderCommandBar("very-long-domain.example.com:443", hints, 30, true)
	require.NotEmpty(t, bar)
}

func TestRenderCommandBarNoHints(t *testing.T) {
	bar := RenderCommandBar("", nil, 80, false)
	require.Contains(t, bar, "[esc]")
	require.Contains(t, bar, "cancel")
}

func TestWrapperShowsStatusBar(t *testing.T) {
	statusCh := make(chan StatusUpdate, 1)
	statusCh <- StatusUpdate{Message: "test-session · ~/project"}

	w := NewWrapper(Options{
		Command: []string{"echo", "hello"},
		Status:  statusCh,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := w.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, exitCode)
}

func TestWrapperNilStatusChannel(t *testing.T) {
	w := NewWrapper(Options{
		Command: []string{"echo", "hello"},
		// Status is nil — should work without error
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := w.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, exitCode)
}

func TestWrapperShutdownWithQueuedAlerts(t *testing.T) {
	// Flood the status channel with alerts before the command runs.
	// The event loop must drain or abandon them cleanly on shutdown
	// without hanging or racing with the terminal restore.
	statusCh := make(chan StatusUpdate, 64)
	for range 64 {
		statusCh <- StatusUpdate{Message: "alert", Alert: true, Timeout: time.Hour}
	}

	w := NewWrapper(Options{
		Command: []string{"true"},
		Status:  statusCh,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := w.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, exitCode)
}
