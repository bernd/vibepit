package vt

import (
	"errors"
	"testing"

	"github.com/bernd/vibepit/vt/internal/ghostty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newInternalTerminal(t *testing.T) *Terminal {
	t.Helper()
	term, err := NewTerminal(80, 24)
	require.NoError(t, err)
	t.Cleanup(func() { _ = term.Close() })
	return term
}

func TestTrapMarksTerminalFailed(t *testing.T) {
	term := newInternalTerminal(t)
	trap := &ghostty.TrapError{Func: "ghostty_terminal_vt_write", Err: errors.New("unreachable")}
	err := term.do(func(*ghostty.Instance) error { return trap })
	require.ErrorIs(t, err, ErrFailed)

	_, err = term.Write([]byte("x"))
	assert.ErrorIs(t, err, ErrFailed)
	_, err = term.Format(FormatOptions{})
	assert.ErrorIs(t, err, ErrFailed)
	_, err = term.AltScreen()
	assert.ErrorIs(t, err, ErrFailed)
	assert.NoError(t, term.Close(), "a failed terminal still closes")
	_, err = term.Write([]byte("x"))
	assert.ErrorIs(t, err, ErrClosed, "Close wins over the failure")
	assert.NotErrorIs(t, err, ErrFailed)
}

func TestOtherErrorsDoNotFail(t *testing.T) {
	term := newInternalTerminal(t)
	err := term.do(func(*ghostty.Instance) error { return errors.New("plain") })
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrFailed)
	_, err = term.Write([]byte("x"))
	assert.NoError(t, err)
}
