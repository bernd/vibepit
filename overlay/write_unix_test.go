//go:build unix

package overlay

import (
	"bytes"
	"io"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlockingWriterWaitsOutAFullNonBlockingFile(t *testing.T) {
	var fds [2]int
	require.NoError(t, syscall.Pipe(fds[:]))
	r := os.NewFile(uintptr(fds[0]), "r")
	w := os.NewFile(uintptr(fds[1]), "w")
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	// Like a terminal that another process switched to non-blocking after
	// Go took it for a blocking file: Go returns EAGAIN instead of waiting.
	require.NoError(t, syscall.SetNonblock(fds[1], true))
	data := bytes.Repeat([]byte("x"), 1<<20)
	got := make(chan []byte, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		b, _ := io.ReadAll(r)
		got <- b
	}()

	n, err := newBlockingWriter(w).Write(data)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)
	require.NoError(t, w.Close())
	assert.True(t, bytes.Equal(data, <-got), "the reader got every byte")
}

func TestNewWritesToAFileWithoutLosingBytes(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	term := New(Config{Stdout: w, NoShadow: true})
	assert.IsType(t, &blockingWriter{}, term.cfg.Stdout)
}
