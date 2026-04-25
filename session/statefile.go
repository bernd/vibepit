package session

import (
	"encoding/json"
	"os"
)

// stateFileWriteTestHook is a no-op in production; tests override it to
// slow down the file-write step so that the concurrent-write race is
// observable without depending on filesystem timing.
var stateFileWriteTestHook = func() {}

// writeStateFile writes the current session list to disk atomically.
// Must be called with m.mu held; returns with m.mu held. m.stateFileMu
// serializes write+rename so a stale snapshot can't overwrite a fresh
// one through the rename. Re-snapshots after acquiring m.stateFileMu so
// the last writer through always writes the newest state.
func (m *Manager) writeStateFile() {
	if m.stateFilePath == "" {
		return
	}
	path := m.stateFilePath

	m.mu.Unlock()
	stateFileWriteTestHook()
	m.stateFileMu.Lock()
	defer func() {
		m.stateFileMu.Unlock()
		m.mu.Lock()
	}()

	infos := m.List()

	data, err := json.MarshalIndent(infos, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, path) //nolint:errcheck
}
