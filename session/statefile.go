package session

import (
	"encoding/json"
	"os"
)

// writeStateFile atomically writes the session list to the state file.
// Must be called with m.mu held.
func (m *Manager) writeStateFile() {
	if m.stateFilePath == "" {
		return
	}
	infos := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		infos = append(infos, s.Info())
	}
	data, err := json.MarshalIndent(infos, "", "  ")
	if err != nil {
		return
	}
	tmp := m.stateFilePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, m.stateFilePath) //nolint:errcheck
}
