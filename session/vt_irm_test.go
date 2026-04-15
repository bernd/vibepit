package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/x/vt"
)

// TestVT_InsertReplaceMode verifies the VT emulator implements IRM
// (ESC[4h / ESC[4l). When insert mode is on, typing a character at the
// cursor shifts existing cells right instead of overwriting. Applications
// like Claude Code rely on this for correct mid-line insertion; replay on
// SSH reattach reads the emulator's screen state, so a missing IRM
// implementation shows up as garbled text after reconnect.
//
// This test is expected to FAIL against charmbracelet/x/vt, which does not
// implement IRM, and PASS against unixshells/vt-go, which does.
func TestVT_InsertReplaceMode(t *testing.T) {
	e := vt.NewSafeEmulator(20, 2)
	defer e.Close() //nolint:errcheck

	// Write "ABCDE" at row 0. Cursor ends at column 5.
	_, err := e.Write([]byte("ABCDE"))
	require.NoError(t, err)

	// Move cursor back to column 2 (CHA = Cursor Horizontal Absolute, 1-based).
	_, err = e.Write([]byte("\x1b[3G"))
	require.NoError(t, err)

	// Enable Insert/Replace Mode.
	_, err = e.Write([]byte("\x1b[4h"))
	require.NoError(t, err)

	// Type 'X'. With IRM on, this should shift "CDE" right one column
	// and place 'X' at column 2, producing "ABXCDE".
	_, err = e.Write([]byte("X"))
	require.NoError(t, err)

	// Read back row 0.
	var got []byte
	for col := range 6 {
		c := e.CellAt(col, 0)
		require.NotNil(t, c, "cell at col %d nil", col)
		got = append(got, c.Content...)
	}
	assert.Equal(t, "ABXCDE", string(got),
		"IRM-mode insert should shift existing cells right, got %q", string(got))
}
