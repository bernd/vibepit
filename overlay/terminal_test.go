package overlay

import (
	"strings"
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
	assert.Equal(t, strings.Join(chunks, ""), h.stdout.String())
	h.keys("ls\r\x1b[A")
	h.waitContainer("ls\r\x1b[A")
}

func TestShadowAnswersAreDroppedWhileAttached(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.app("\x1b[6n")
	h.waitContainer("\x1b[1;1R")
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, "\x1b[1;1R", h.toCont.String(), "only the real terminal answers")
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
	assert.Equal(t, []string{"30x6", "30x6"}, h.resizeLog(), "the container gets every resize")
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

func TestStdinEOFClosesTheContainerInput(t *testing.T) {
	h := newHarness(t, 20, 5)
	require.NoError(t, h.stdin.Close())
	require.Eventually(t, func() bool { return h.closedIn.Load() == 1 }, 5*time.Second, time.Millisecond)
	h.app("bye")
	assert.Equal(t, "bye", h.stdout.String(), "output flows until it ends")
	select {
	case <-h.done:
		t.Fatal("Run returned before the output ended")
	default:
	}
}
