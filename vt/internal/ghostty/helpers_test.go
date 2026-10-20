package ghostty

import (
	"flag"
	"testing"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

// newTerm returns a terminal that is closed when the test ends.
func newTerm(tb testing.TB, cols, rows uint16) *Instance {
	tb.Helper()
	in := openTerm(tb, cols, rows)
	tb.Cleanup(func() { _ = in.Close() })
	return in
}

// withScenarioTerm runs fn with a scenario-sized terminal that is closed
// right after, for loops that would otherwise keep hundreds of instances
// (448 KiB of linear memory each) alive until the test ends.
func withScenarioTerm(tb testing.TB, fn func(in *Instance)) {
	tb.Helper()
	in := openTerm(tb, scenarioCols, scenarioRows)
	defer func() { _ = in.Close() }()
	fn(in)
}

// openTerm returns a terminal the caller must close.
func openTerm(tb testing.TB, cols, rows uint16) *Instance {
	tb.Helper()
	in, err := NewInstance(Config{})
	require.NoError(tb, err)
	if err := in.TerminalNew(cols, rows); err != nil {
		_ = in.Close()
		require.NoError(tb, err)
	}
	return in
}

func feed(tb testing.TB, in *Instance, s string) {
	tb.Helper()
	require.NoError(tb, in.VTWrite([]byte(s)))
}
