package ghostty

import (
	"flag"
	"testing"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

// newTerm returns a terminal that is closed when the test ends.
func newTerm(t testing.TB, cols, rows uint16) *Instance {
	t.Helper()
	in := openTerm(t, cols, rows)
	t.Cleanup(func() { _ = in.Close() })
	return in
}

// withTerm runs fn with a terminal that is closed right after, for loops
// that would otherwise keep hundreds of instances (448 KiB of linear memory
// each) alive until the test ends.
func withTerm(t testing.TB, cols, rows uint16, fn func(in *Instance)) {
	t.Helper()
	in := openTerm(t, cols, rows)
	defer func() { _ = in.Close() }()
	fn(in)
}

// openTerm returns a terminal the caller must close.
func openTerm(t testing.TB, cols, rows uint16) *Instance {
	t.Helper()
	in, err := NewInstance(Config{})
	require.NoError(t, err)
	if err := in.TerminalNew(cols, rows); err != nil {
		_ = in.Close()
		require.NoError(t, err)
	}
	return in
}

func feed(t testing.TB, in *Instance, s string) {
	t.Helper()
	require.NoError(t, in.VTWrite([]byte(s)))
}
