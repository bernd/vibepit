package ghostty

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func FuzzVTWrite(f *testing.F) {
	for _, sc := range scenarios {
		f.Add([]byte(sc.input))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		in := newTerm(t, 40, 10)
		require.NoError(t, in.TerminalSetSize(OptContinuationMaxBytes, 1024))
		require.NoError(t, in.VTWrite(data))
		_, err := in.FormatAlloc(FormatterOptions{Emit: FormatVT, Extra: restoreExtras, Selection: activeArea(t, in)})
		require.NoError(t, err)
		_, err = in.ContinuationAlloc()
		assert.NotErrorIs(t, err, ErrTrap)
	})
}

// build-wasm.sh sets the limit at link time. A module built without it
// could grow to 4 GiB.
func TestMemoryLimitIsBuiltIn(t *testing.T) {
	assert.Equal(t, 256<<20, MemoryLimit, "rebuild with make ghostty-wasm")
}

// Hitting the memory limit must not trap or panic.
func TestMemoryLimit(t *testing.T) {
	in := newTerm(t, 200, 60)
	require.NoError(t, in.TerminalSetPtr(OptScrollbackMaxBytes, 0), "no byte limit")
	require.NoError(t, in.TerminalSetPtr(OptScrollbackMaxLines, 0), "no line limit")

	// About 15 MiB of input fills the limit, up to the last page or so: a
	// grow larger than the room left fails as a whole.
	chunk := styledLines(1 << 20)
	for i := 0; in.MemorySize()+1<<20 < MemoryLimit; i++ {
		require.Less(t, i, 64, "64 MiB of input didn't reach the limit")
		require.NoError(t, in.VTWrite(chunk))
	}
	for range 8 {
		require.NoError(t, in.VTWrite(chunk), "writes past the limit")
	}
	assert.LessOrEqual(t, in.MemorySize(), uint64(MemoryLimit))
	_, err := in.FormatAlloc(FormatterOptions{Emit: FormatVT})
	assert.NotErrorIs(t, err, ErrTrap)
}

func TestTrapBecomesError(t *testing.T) {
	in := newTerm(t, 80, 24)
	// A pointer past the end of linear memory fails the generated code's
	// bounds check, which panics.
	err := in.guard("ghostty_terminal_vt_write", func() {
		in.mod.Xghostty_terminal_vt_write(int32(in.term), -256, 1000)
	})
	require.ErrorIs(t, err, ErrTrap)
	var te *TrapError
	require.True(t, errors.As(err, &te))
	assert.Equal(t, "ghostty_terminal_vt_write", te.Func)
}
