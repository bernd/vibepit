package ghostty

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The overlay forwards these answers to the app while a prompt shows. An
// upgrade that starts answering a new query, especially with made-up
// colours or sizes, needs a decision before it is adopted.
func TestQueryAnswers(t *testing.T) {
	tests := []struct {
		name, query, want string
	}{
		{"DA1", "\x1b[c", "\x1b[?62;22c"},
		{"DA2", "\x1b[>c", "\x1b[>1;0;0c"},
		{"DSR 5n", "\x1b[5n", "\x1b[0n"},
		{"CPR 6n", "\x1b[6n", "\x1b[1;1R"},
		{"kitty keyboard query", "\x1b[?u", "\x1b[?0u"},
		{"DECRQM", "\x1b[?2004$p", "\x1b[?2004;2$y"},
		{"XTVERSION", "\x1b[>q", "\x1bP>|libghostty\x1b\\"},
		{"OSC 10 query", "\x1b]10;?\x07", ""},
		{"OSC 11 query", "\x1b]11;?\x07", ""},
		{"XTWINOPS 14t", "\x1b[14t", ""},
		{"XTWINOPS 16t", "\x1b[16t", ""},
		{"XTWINOPS 18t", "\x1b[18t", ""},
		{"ENQ", "\x05", ""},
		{"colour scheme 996n", "\x1b[?996n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newTerm(t, 80, 24)
			var got []byte
			require.NoError(t, in.SetWritePty(func(p []byte) { got = append(got, p...) }))
			feed(t, in, tt.query)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestQueriesWithoutCallbackAreDropped(t *testing.T) {
	in := newTerm(t, 80, 24)
	feed(t, in, "\x1b[c\x1b[6n\x1b[?u\x1b[?2004$phello")
	x, err := in.GetU16(DataCursorX)
	require.NoError(t, err)
	assert.EqualValues(t, 5, x, "processing continues after unanswered queries")
}
