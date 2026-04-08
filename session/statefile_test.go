package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateFile_WrittenOnCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	m := NewManager(50)
	m.SetStateFilePath(path)

	_, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var sessions []SessionInfo
	require.NoError(t, json.Unmarshal(data, &sessions))
	require.Len(t, sessions, 1)
	assert.Equal(t, "session-1", sessions[0].ID)
}
