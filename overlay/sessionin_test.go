package overlay

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stepWriter lets one write through per step, and all of them after
// release.
type stepWriter struct {
	step    chan struct{}
	free    chan struct{}
	once    sync.Once
	entered atomic.Int32
	largest atomic.Int64 // the largest write, set before entered counts it
	out     syncBuffer
}

func newStepWriter() *stepWriter {
	return &stepWriter{step: make(chan struct{}), free: make(chan struct{})}
}

func (w *stepWriter) Write(p []byte) (int, error) {
	if n := int64(len(p)); n > w.largest.Load() {
		w.largest.Store(n)
	}
	w.entered.Add(1)
	select {
	case <-w.step:
	case <-w.free:
	}
	return w.out.Write(p)
}

func (w *stepWriter) release() { w.once.Do(func() { close(w.free) }) }

// newStalledInput runs a sessionInput whose first write, "x", is stuck in
// the session.
func newStalledInput(t *testing.T) (*sessionInput, *stepWriter) {
	t.Helper()
	return newStalledInputWith(t, "x")
}

// newStalledInputWith runs a sessionInput with first queued before its
// writer starts, which is stuck in the session from its first write on.
func newStalledInputWith(t *testing.T, first string) (*sessionInput, *stepWriter) {
	t.Helper()
	w := newStepWriter()
	c := newSessionInput(w)
	_, err := c.Write([]byte(first))
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		c.run()
		close(done)
	}()
	t.Cleanup(func() {
		c.close()
		w.release()
		<-done
	})
	require.Eventually(t, func() bool { return w.entered.Load() == 1 }, 5*time.Second, time.Millisecond)
	return c, w
}

// blocked reports whether f is still running after a short wait, then lets
// it finish once release is called.
func blocked(f func()) (func() bool, <-chan struct{}) {
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	return func() bool {
		select {
		case <-done:
			return false
		case <-time.After(20 * time.Millisecond):
			return true
		}
	}, done
}

func TestSessionInput(t *testing.T) {
	t.Run("writes never block and keep their order", func(t *testing.T) {
		c, w := newStalledInput(t)
		for _, s := range []string{"a", "b", "c"} {
			_, err := c.Write([]byte(s))
			require.NoError(t, err)
		}
		w.release()
		require.Eventually(t, func() bool { return w.out.String() == "xabc" }, 5*time.Second, time.Millisecond)
	})

	t.Run("stdin waits for room", func(t *testing.T) {
		c, w := newStalledInput(t)
		_, err := c.Write([]byte(strings.Repeat("k", sessionInputRoom)))
		require.NoError(t, err)
		running, done := blocked(func() { c.waitRoom(sessionInputRoom, nil) })
		assert.True(t, running(), "a full queue makes stdin wait")
		w.release()
		<-done
	})

	t.Run("a write in flight takes room until it returns", func(t *testing.T) {
		c, w := newStalledInput(t)
		_, err := c.Write([]byte(strings.Repeat("k", sessionInputRoom-1)))
		require.NoError(t, err)
		running, done := blocked(func() { c.waitRoom(sessionInputRoom, nil) })
		assert.True(t, running(), "the stuck write counts toward the room")
		w.release()
		<-done
	})

	t.Run("a burst is written in chunks that keep their room", func(t *testing.T) {
		burst := strings.Repeat("b", sessionInputMax)
		c, w := newStalledInputWith(t, burst)
		assert.Equal(t, int64(sessionInputChunk), w.largest.Load(), "one write takes at most a chunk")
		assert.False(t, c.hasRoom(sessionInputMax), "the chunk in flight is still pending")
		w.release()
		require.Eventually(t, func() bool { return w.out.String() == burst }, 5*time.Second, time.Millisecond)
		assert.Equal(t, int64(sessionInputChunk), w.largest.Load())
	})

	t.Run("stdin has room while a chunk is written", func(t *testing.T) {
		c, _ := newStalledInputWith(t, strings.Repeat("c", sessionInputChunk))
		assert.True(t, c.hasRoom(sessionInputRoom))
	})

	t.Run("keeps the order while writes and chunks interleave", func(t *testing.T) {
		w := newStepWriter()
		c := newSessionInput(w)
		done := make(chan struct{})
		go func() {
			c.run()
			close(done)
		}()
		t.Cleanup(func() {
			c.close()
			w.release()
			<-done
		})
		var want strings.Builder
		for i := range 64 {
			// Sizes around the chunk, so writes end in and across chunks.
			s := strings.Repeat(string(rune('a'+i%26)), 1+i*7919%(3*sessionInputChunk))
			want.WriteString(s)
			_, err := c.Write([]byte(s))
			require.NoError(t, err)
			if i%3 == 0 {
				// Let one chunk through, then queue more while the next is
				// in flight.
				require.Eventually(t, func() bool { return w.entered.Load() == int32(i/3+1) }, 5*time.Second, time.Millisecond)
				w.step <- struct{}{}
			}
		}
		w.release()
		require.Eventually(t, func() bool { return w.out.String() == want.String() }, 5*time.Second, time.Millisecond)
		assert.LessOrEqual(t, w.largest.Load(), int64(sessionInputChunk))
	})

	t.Run("drops answers past the max, the write in flight included", func(t *testing.T) {
		c, w := newStalledInput(t)
		_, err := c.Write([]byte(strings.Repeat("a", sessionInputMax-1)))
		require.NoError(t, err)
		c.writeAnswers([]byte("y"))
		w.release()
		require.Eventually(t, func() bool { return len(w.out.String()) == sessionInputMax }, 5*time.Second, time.Millisecond)
		assert.NotContains(t, w.out.String(), "y")
	})

	t.Run("stop ends the wait", func(t *testing.T) {
		c, w := newStalledInput(t)
		_, err := c.Write([]byte(strings.Repeat("k", sessionInputRoom)))
		require.NoError(t, err)
		stop := make(chan struct{})
		running, done := blocked(func() { c.waitRoom(sessionInputRoom, stop) })
		require.True(t, running())
		close(stop)
		<-done
		w.release()
		require.Eventually(t, func() bool { return len(w.out.String()) == 1+sessionInputRoom }, 5*time.Second, time.Millisecond,
			"the queue is kept")
	})

	t.Run("drops answers past the max", func(t *testing.T) {
		c, w := newStalledInput(t)
		_, err := c.Write([]byte(strings.Repeat("a", sessionInputMax)))
		require.NoError(t, err)
		c.writeAnswers([]byte("y"))
		w.release()
		require.Eventually(t, func() bool { return len(w.out.String()) == 1+sessionInputMax }, 5*time.Second, time.Millisecond)
		assert.NotContains(t, w.out.String(), "y")
	})

	t.Run("keeps stdin past the max", func(t *testing.T) {
		c, w := newStalledInput(t)
		_, err := c.Write([]byte(strings.Repeat("a", sessionInputMax)))
		require.NoError(t, err)
		_, err = c.Write([]byte("y"))
		require.NoError(t, err)
		w.release()
		require.Eventually(t, func() bool { return len(w.out.String()) == 2+sessionInputMax }, 5*time.Second, time.Millisecond)
		assert.True(t, strings.HasSuffix(w.out.String(), "y"))
	})

	t.Run("flush waits for a write in flight", func(t *testing.T) {
		c, w := newStalledInput(t)
		running, done := blocked(c.flush)
		assert.True(t, running())
		w.release()
		<-done
		assert.Equal(t, "x", w.out.String())
	})

	t.Run("close releases waiters and refuses writes", func(t *testing.T) {
		c, _ := newStalledInput(t)
		_, err := c.Write([]byte(strings.Repeat("k", sessionInputRoom)))
		require.NoError(t, err)
		running, done := blocked(func() { c.waitRoom(sessionInputRoom, nil) })
		require.True(t, running())
		c.close()
		<-done
		_, err = c.Write([]byte("z"))
		assert.Error(t, err)
	})
}
