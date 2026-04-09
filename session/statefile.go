package session

import (
	"encoding/json"
	"os"
)

// writeStateFile atomically writes the session list to the state file.
// Must be called with m.mu held. Collects session info under lock, then
// performs file I/O after releasing it to avoid holding the lock during
// disk writes.
func (m *Manager) writeStateFile() {
	if m.stateFilePath == "" {
		return
	}
	infos := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		infos = append(infos, s.Info())
	}
	path := m.stateFilePath
	m.mu.Unlock()

	data, err := json.MarshalIndent(infos, "", "  ")
	if err != nil {
		m.mu.Lock()
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		m.mu.Lock()
		return
	}
	os.Rename(tmp, path) //nolint:errcheck
	m.mu.Lock()
}
