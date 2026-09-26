package overlay

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPassthroughIsByteExact(t *testing.T) {
	h := newHarness(t, 20, 5)
	chunks := []string{"plain ", "\x1b[3", "1mred\x1b[0m ", "\xe2\x82", "\xac\r\n", "\x1b]2;title\x07"}
	for _, c := range chunks {
		h.app(c)
	}
	assert.Equal(t, positionQuery+strings.Join(chunks, ""), h.stdout.String())
	h.keys("ls\r\x1b[A")
	h.waitSessionInput("ls\r\x1b[A")
}

func TestShadowAnswersAreDroppedWhileAttached(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("\x1b[6n")
	h.waitSessionInput("\x1b[1;1R")
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, "\x1b[1;1R", h.sessionIn.String(), "only the real terminal answers")
}

func TestWithoutShadow(t *testing.T) {
	h := newHarness(t, 20, 5, withoutShadow())
	h.app("abc")
	assert.Equal(t, "abc", h.stdout.String())
	assert.Nil(t, h.term.shadow)
}

func TestResize(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.resize(30, 6)
	assert.Equal(t, []string{"30x6"}, h.resizeLog())
	h.app(strings.Repeat("x", 25))
	assert.Equal(t, strings.Repeat("x", 25), firstLine(h.term.shadow), "the shadow has the new width")
	h.term.Resize()
	assert.Equal(t, []string{"30x6", "30x6"}, h.resizeLog(), "the session gets every resize")
}

func TestRunEndsWithTheOutput(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.out.close()
	select {
	case <-h.done:
		assert.NoError(t, h.err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't return")
	}
	assert.Zero(t, h.closedIn.Load(), "stdin is still open")
}

func TestStdinEOFClosesTheSessionInput(t *testing.T) {
	h := newHarness(t, 20, 5)
	require.NoError(t, h.stdin.Close())
	require.Eventually(t, func() bool { return h.closedIn.Load() == 1 }, 5*time.Second, time.Millisecond)
	h.app("bye")
	assert.Equal(t, positionQuery+"bye", h.stdout.String(), "output flows until it ends")
	select {
	case <-h.done:
		t.Fatal("Run returned before the output ended")
	default:
	}
}

func TestConcurrentResizesKeepTheLatestSize(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h := newHarness(t, 20, 5, onSessionResize(func(cols, rows int) {
		if cols == 30 {
			once.Do(func() { close(entered) })
			<-release
		}
	}))
	var wg sync.WaitGroup
	h.mu.Lock()
	h.cols, h.rows = 30, 6
	h.mu.Unlock()
	wg.Go(h.term.Resize) // the initial size, stalled in the session resize
	<-entered
	h.mu.Lock()
	h.cols, h.rows = 40, 7
	h.mu.Unlock()
	wg.Go(h.term.Resize) // SIGWINCH meanwhile
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	log := h.resizeLog()
	require.NotEmpty(t, log)
	assert.Equal(t, "40x7", log[len(log)-1], "the session is left at the latest size")
	h.app(strings.Repeat("x", 35))
	assert.Equal(t, strings.Repeat("x", 35), firstLine(h.term.shadow), "so does the shadow")
}

func TestAStalledSessionInputNeverStallsOutput(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	h := newHarness(t, 20, 5, stalledSessionInput(gate))
	t.Cleanup(release)
	h.keys("k")
	require.Eventually(t, func() bool { return h.stalled.Load() > 0 }, 5*time.Second, time.Millisecond)
	require.NoError(t, h.result(h.detachAsync(context.Background())), "the detach waited for the session")
	h.app("\x1b[6n") // the shadow answers into the stalled input
	h.app("more")
	left := make(chan struct{})
	go func() {
		h.term.leave(false)
		close(left)
	}()
	select {
	case <-left:
	case <-time.After(5 * time.Second):
		t.Fatal("the leave waited for the session")
	}
	assert.Contains(t, screenText(h.real), "more")
	release()
	h.waitSessionInput("k\x1b[1;1R")
}

func TestStdinEOFClosesTheSessionInputAfterQueuedKeys(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	h := newHarness(t, 20, 5, stalledSessionInput(gate))
	t.Cleanup(release)
	h.keys("bye")
	require.NoError(t, h.stdin.Close())
	require.Eventually(t, func() bool { return h.stalled.Load() > 0 }, 5*time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	assert.Zero(t, h.closedIn.Load(), "the queued keys go first")
	release()
	require.Eventually(t, func() bool { return h.closedIn.Load() == 1 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, "bye", h.sessionIn.String())
}
