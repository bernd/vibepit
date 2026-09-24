package ghostty

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTerminalLifecycle(t *testing.T) {
	in := newTerm(t, 80, 24)
	size := func() (uint16, uint16) {
		cols, err := in.GetU16(DataCols)
		require.NoError(t, err)
		rows, err := in.GetU16(DataRows)
		require.NoError(t, err)
		return cols, rows
	}

	cols, rows := size()
	assert.EqualValues(t, 80, cols)
	assert.EqualValues(t, 24, rows)

	require.NoError(t, in.TerminalResize(100, 30, 0, 0))
	cols, rows = size()
	assert.EqualValues(t, 100, cols)
	assert.EqualValues(t, 30, rows)

	bracketedPaste := EncodeMode(2004, false)
	feed(t, in, "\x1b[?2004h")
	on, err := in.GetMode(bracketedPaste)
	require.NoError(t, err)
	assert.True(t, on)

	require.NoError(t, in.TerminalReset())
	on, err = in.GetMode(bracketedPaste)
	require.NoError(t, err)
	assert.False(t, on, "reset restores mode defaults")

	require.NoError(t, in.TerminalFree())
	require.NoError(t, in.TerminalNew(10, 5), "an instance can hold a new terminal after free")
	cols, _ = size()
	assert.EqualValues(t, 10, cols)

	require.NoError(t, in.Close())
	require.NoError(t, in.Close(), "Close is idempotent")
}

// OPT_MODE sets a mode without a VT sequence. The overlay uses it to turn
// synchronized output off around formatting.
func TestSetModeOption(t *testing.T) {
	in := newTerm(t, 80, 24)
	sync := EncodeMode(2026, false)
	for _, want := range []bool{true, false} {
		require.NoError(t, in.TerminalSetMode(sync, want))
		got, err := in.GetMode(sync)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestUnknownModeIsInvalidValue(t *testing.T) {
	in := newTerm(t, 80, 24)
	_, err := in.GetMode(EncodeMode(9999, false))
	assert.ErrorIs(t, err, InvalidValue)
}

func TestWriteLargerThanBuffer(t *testing.T) {
	in := newTerm(t, 80, 24)
	data := make([]byte, 0, inputBufSize*2+100)
	for len(data) < inputBufSize*2 {
		data = append(data, "0123456789abcdef"...)
	}
	data = append(data, "\x1b]2;end\x07"...)
	require.NoError(t, in.VTWrite(data))
	title, err := in.GetString(DataTitle)
	require.NoError(t, err)
	assert.Equal(t, "end", title, "bytes after the first 64 KiB chunk are parsed")
}
