package ghostty

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWritePtyInstalledInTable(t *testing.T) {
	in := newTerm(t, 80, 24)
	before := len(*in.mod.X__indirect_function_table())
	require.NoError(t, in.SetWritePty(func([]byte) {}))
	assert.EqualValues(t, before, in.ptyIndex, "the callback is the new last entry")
	assert.Len(t, *in.mod.X__indirect_function_table(), before+1)

	require.NoError(t, in.SetWritePty(func([]byte) {}))
	assert.Len(t, *in.mod.X__indirect_function_table(), before+1, "a second SetWritePty reuses the entry")
}

func TestWritePtyReachesItsOwnInstance(t *testing.T) {
	a := newTerm(t, 80, 24)
	b := newTerm(t, 80, 24)
	var gotA, gotB []byte
	require.NoError(t, a.SetWritePty(func(p []byte) { gotA = append(gotA, p...) }))
	require.NoError(t, b.SetWritePty(func(p []byte) { gotB = append(gotB, p...) }))

	feed(t, a, "\x1b[6n")
	feed(t, b, "x\x1b[5n")
	assert.Equal(t, "\x1b[1;1R", string(gotA))
	assert.Equal(t, "\x1b[0n", string(gotB))
}

func TestWritePtyRemoved(t *testing.T) {
	in := newTerm(t, 80, 24)
	calls := 0
	require.NoError(t, in.SetWritePty(func([]byte) { calls++ }))
	require.NoError(t, in.SetWritePty(nil))
	feed(t, in, "\x1b[6n")
	assert.Zero(t, calls)
}

func TestWritePtyDataIsCopied(t *testing.T) {
	in := newTerm(t, 80, 24)
	var kept [][]byte
	require.NoError(t, in.SetWritePty(func(p []byte) { kept = append(kept, p) }))
	feed(t, in, "\x1b[6n")
	feed(t, in, "\x1b[5;5H\x1b[6n")
	require.Len(t, kept, 2)
	assert.Equal(t, "\x1b[1;1R", string(kept[0]), "later writes must not change earlier answers")
	assert.Equal(t, "\x1b[5;5R", string(kept[1]))
}

// The callback runs inside the module call, so its panic unwinds through
// translated code and leaves the instance in an unknown state.
func TestWritePtyPanicIsTrap(t *testing.T) {
	in := newTerm(t, 80, 24)
	require.NoError(t, in.SetWritePty(func([]byte) { panic("callback failed") }))
	err := in.VTWrite([]byte("\x1b[6n"))
	require.ErrorIs(t, err, ErrTrap)
	assert.ErrorContains(t, err, "callback failed")
}
