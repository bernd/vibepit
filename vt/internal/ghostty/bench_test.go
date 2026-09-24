package ghostty

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// BenchmarkVTWrite is the spike's workload E: styled lines into a 200x60
// terminal. It isn't a pass/fail gate; upgrade PRs record it before and
// after.
func BenchmarkVTWrite(b *testing.B) {
	in := newTerm(b, 200, 60)
	require.NoError(b, in.TerminalSetSize(OptScrollbackMaxLines, 10000))
	chunk := styledLines(64 << 10)
	b.SetBytes(int64(len(chunk)))
	for b.Loop() {
		if err := in.VTWrite(chunk); err != nil {
			b.Fatal(err)
		}
	}
}
