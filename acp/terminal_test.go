package acp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTerminalCreateAndWait(t *testing.T) {
	tm := NewTerminalManager()
	defer tm.CleanupAll()

	result, err := tm.Create(TerminalCreateParams{
		Command: "echo",
		Args:    []string{"hello world"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result.TerminalID)

	waitResult, err := tm.WaitForExit(TerminalWaitParams{
		TerminalID: result.TerminalID,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, waitResult.ExitCode)

	outputResult, err := tm.Output(TerminalOutputParams{
		TerminalID: result.TerminalID,
	})
	require.NoError(t, err)
	assert.Contains(t, outputResult.Output, "hello world")
	assert.False(t, outputResult.Truncated)
}

func TestTerminalCreateNonZeroExit(t *testing.T) {
	tm := NewTerminalManager()
	defer tm.CleanupAll()

	result, err := tm.Create(TerminalCreateParams{
		Command: "false",
	})
	require.NoError(t, err)

	waitResult, err := tm.WaitForExit(TerminalWaitParams{
		TerminalID: result.TerminalID,
	})
	require.NoError(t, err)
	assert.NotEqual(t, 0, waitResult.ExitCode)
}

func TestTerminalKill(t *testing.T) {
	tm := NewTerminalManager()
	defer tm.CleanupAll()

	result, err := tm.Create(TerminalCreateParams{
		Command: "sleep",
		Args:    []string{"60"},
	})
	require.NoError(t, err)

	err = tm.Kill(TerminalKillParams{TerminalID: result.TerminalID})
	require.NoError(t, err)

	waitResult, err := tm.WaitForExit(TerminalWaitParams{
		TerminalID: result.TerminalID,
	})
	require.NoError(t, err)
	assert.NotEqual(t, 0, waitResult.ExitCode)
}

func TestTerminalRelease(t *testing.T) {
	tm := NewTerminalManager()

	result, err := tm.Create(TerminalCreateParams{
		Command: "sleep",
		Args:    []string{"60"},
	})
	require.NoError(t, err)

	err = tm.Release(TerminalReleaseParams{TerminalID: result.TerminalID})
	require.NoError(t, err)

	// After release, operations should fail.
	_, err = tm.Output(TerminalOutputParams{TerminalID: result.TerminalID})
	assert.Error(t, err)
}

func TestTerminalUnknownID(t *testing.T) {
	tm := NewTerminalManager()

	_, err := tm.Output(TerminalOutputParams{TerminalID: "nonexistent"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown terminal")
}

func TestTerminalWithCwd(t *testing.T) {
	tm := NewTerminalManager()
	defer tm.CleanupAll()

	result, err := tm.Create(TerminalCreateParams{
		Command: "pwd",
		Cwd:     "/tmp",
	})
	require.NoError(t, err)

	_, err = tm.WaitForExit(TerminalWaitParams{TerminalID: result.TerminalID})
	require.NoError(t, err)

	outputResult, err := tm.Output(TerminalOutputParams{TerminalID: result.TerminalID})
	require.NoError(t, err)
	assert.Contains(t, outputResult.Output, "/tmp")
}
