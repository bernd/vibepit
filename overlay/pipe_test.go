package overlay

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBufPipe(t *testing.T) {
	t.Run("read what was written", func(t *testing.T) {
		p := newBufPipe(16)
		_, err := p.Write([]byte("abc"))
		require.NoError(t, err)
		assert.Equal(t, "abc", readN(t, p, 3))
	})

	t.Run("write never blocks and drops past max", func(t *testing.T) {
		p := newBufPipe(4)
		n, err := p.Write([]byte("abcdef"))
		require.NoError(t, err)
		assert.Equal(t, 6, n, "a full pipe still reports the whole write")
		_, _ = p.Write([]byte("g"))
		require.NoError(t, p.Close())
		rest, err := io.ReadAll(p)
		require.NoError(t, err)
		assert.Equal(t, "abcd", string(rest))
	})

	t.Run("close unblocks a reader with EOF", func(t *testing.T) {
		p := newBufPipe(4)
		done := make(chan error, 1)
		go func() {
			_, err := p.Read(make([]byte, 1))
			done <- err
		}()
		require.NoError(t, p.Close())
		assert.ErrorIs(t, <-done, io.EOF)
	})

	t.Run("write after close fails", func(t *testing.T) {
		p := newBufPipe(4)
		require.NoError(t, p.Close())
		_, err := p.Write([]byte("x"))
		assert.ErrorIs(t, err, io.ErrClosedPipe)
	})
}
