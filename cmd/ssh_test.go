package cmd

import (
	"os"
	"testing"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldAttachExecStdin(t *testing.T) {
	t.Run("nil stdin", func(t *testing.T) {
		assert.False(t, shouldAttachExecStdin(nil))
	})

	t.Run("redirected stdin", func(t *testing.T) {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		defer r.Close() //nolint:errcheck
		defer w.Close() //nolint:errcheck

		assert.True(t, shouldAttachExecStdin(r))
	})

	t.Run("terminal stdin", func(t *testing.T) {
		ptmx, tty, err := pty.Open()
		require.NoError(t, err)
		defer ptmx.Close() //nolint:errcheck
		defer tty.Close()  //nolint:errcheck

		assert.False(t, shouldAttachExecStdin(tty))
	})
}
