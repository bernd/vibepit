package ghostty

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withContinuation(t *testing.T, in *Instance, limit uint32) {
	t.Helper()
	require.NoError(t, in.TerminalSetSize(OptContinuationMaxBytes, limit))
}

// The C API's default is 0, tracking off. vt must always set it.
func TestContinuationOffByDefault(t *testing.T) {
	in := newTerm(t, 80, 24)
	limit, err := in.GetU32(DataContinuationMaxBytes)
	require.NoError(t, err)
	assert.Zero(t, limit)

	feed(t, in, "abc\x1b[3")
	_, err = in.ContinuationAlloc()
	assert.ErrorIs(t, err, InvalidValue)
}

func TestContinuationBytes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty at ground", "abc", ""},
		{"CSI", "abc\x1b[3", "\x1b[3"},
		{"OSC", "\x1b]2;tit", "\x1b]2;tit"},
		{"DCS", "\x1bP+q54", "\x1bP+q54"},
		{"APC", "\x1b_Gf=1;AAA", "\x1b_Gf=1;AAA"},
		{"lone ESC", "\x1b", "\x1b"},
		{"UTF-8", "abc\xe2\x82", "\xe2\x82"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTerm(t, 80, 24)
			withContinuation(t, in, 64<<10)
			feed(t, in, tt.input)
			got, err := in.ContinuationAlloc()
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestContinuationOverLimit(t *testing.T) {
	in := newTerm(t, 80, 24)
	withContinuation(t, in, 1024)
	feed(t, in, "\x1b]2;"+strings.Repeat("x", 1100))

	_, err := in.ContinuationAlloc()
	require.ErrorIs(t, err, InvalidValue, "too long to reconstruct")
	assert.NotErrorIs(t, err, ErrTrap)

	feed(t, in, "\x07")
	got, err := in.ContinuationAlloc()
	require.NoError(t, err, "tracking recovers at ground")
	assert.Empty(t, got)
}
