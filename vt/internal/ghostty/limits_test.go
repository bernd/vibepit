package ghostty

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// styledLines returns about n bytes of coloured lines of varying length.
func styledLines(n int) []byte {
	var buf bytes.Buffer
	for i := 0; buf.Len() < n; i++ {
		fmt.Fprintf(&buf, "\x1b[3%dmline %d \x1b[1mbold\x1b[0m plain %s\r\n", i%8, i, strings.Repeat("x", i%120))
	}
	return buf.Bytes()[:n]
}

// Both scrollback limits bound history and memory. Limits apply per page,
// so the row count only has an upper bound.
func TestScrollbackLimitsBoundMemory(t *testing.T) {
	total := 100 << 20
	if testing.Short() {
		total = 16 << 20
	}
	in := newTerm(t, 80, 24)
	require.NoError(t, in.TerminalSetSize(OptScrollbackMaxLines, 1000))
	require.NoError(t, in.TerminalSetSize(OptScrollbackMaxBytes, 4<<20))

	lines, err := in.GetU32(DataScrollbackMaxLines)
	require.NoError(t, err)
	assert.EqualValues(t, 1000, lines)

	chunk := styledLines(1 << 20)
	for written := 0; written < total; written += len(chunk) {
		require.NoError(t, in.VTWrite(chunk))
	}

	rows, err := in.GetU32(DataScrollbackRows)
	require.NoError(t, err)
	assert.LessOrEqual(t, rows, uint32(2000))
	assert.LessOrEqual(t, in.MemorySize(), uint64(16<<20))
}
