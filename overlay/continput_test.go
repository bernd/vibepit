package overlay

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stallWriter takes nothing until gate is closed.
type stallWriter struct {
	gate    chan struct{}
	entered atomic.Int32
	out     syncBuffer
}

func (w *stallWriter) Write(p []byte) (int, error) {
	w.entered.Add(1)
	<-w.gate
	return w.out.Write(p)
}

// newStalledInput runs a containerInput whose first write, "x", is stuck in
// the container.
func newStalledInput(t *testing.T) (*containerInput, *stallWriter) {
	t.Helper()
	w := &stallWriter{gate: make(chan struct{})}
	c := newContainerInput(w)
	done := make(chan struct{})
	go func() {
		c.run()
		close(done)
	}()
	t.Cleanup(func() {
		c.close()
		select {
		case <-w.gate:
		default:
			close(w.gate)
		}
		<-done
	})
	_, err := c.Write([]byte("x"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return w.entered.Load() == 1 }, 5*time.Second, time.Millisecond)
	return c, w
}

// blocked reports whether f is still running after a short wait, then lets
// it finish once release is called.
func blocked(f func()) (stillRunning func() bool, finished <-chan struct{}) {
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

func TestContainerInput(t *testing.T) {
	t.Run("writes never block and keep their order", func(t *testing.T) {
		c, w := newStalledInput(t)
		for _, s := range []string{"a", "b", "c"} {
			_, err := c.Write([]byte(s))
			require.NoError(t, err)
		}
		close(w.gate)
		require.Eventually(t, func() bool { return w.out.String() == "xabc" }, 5*time.Second, time.Millisecond)
	})

	t.Run("stdin waits for room", func(t *testing.T) {
		c, w := newStalledInput(t)
		_, err := c.Write([]byte(strings.Repeat("k", containerInputRoom)))
		require.NoError(t, err)
		running, done := blocked(c.waitRoom)
		assert.True(t, running(), "a full queue makes stdin wait")
		close(w.gate)
		<-done
	})

	t.Run("drops past the max", func(t *testing.T) {
		c, w := newStalledInput(t)
		_, err := c.Write([]byte(strings.Repeat("a", containerInputMax)))
		require.NoError(t, err)
		n, err := c.Write([]byte("y"))
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		close(w.gate)
		require.Eventually(t, func() bool { return len(w.out.String()) == 1+containerInputMax }, 5*time.Second, time.Millisecond)
		assert.NotContains(t, w.out.String(), "y")
	})

	t.Run("flush waits for a write in flight", func(t *testing.T) {
		c, w := newStalledInput(t)
		running, done := blocked(c.flush)
		assert.True(t, running())
		close(w.gate)
		<-done
		assert.Equal(t, "x", w.out.String())
	})

	t.Run("close releases waiters and refuses writes", func(t *testing.T) {
		c, _ := newStalledInput(t)
		_, err := c.Write([]byte(strings.Repeat("k", containerInputRoom)))
		require.NoError(t, err)
		running, done := blocked(c.waitRoom)
		require.True(t, running())
		c.close()
		<-done
		_, err = c.Write([]byte("z"))
		assert.Error(t, err)
	})
}
