package session

import (
	"fmt"
	"sync"
	"time"
)

// SessionInfo holds a snapshot of session state for display purposes.
type SessionInfo struct {
	ID          string
	Command     string
	ClientCount int
	Status      string // "attached", "detached", "exited"
	ExitCode    int
	CreatedAt   time.Time
	ExitedAt    time.Time
}

// Manager owns all sessions and enforces the concurrency limit.
type Manager struct {
	mu            sync.Mutex
	sessions      map[string]*Session
	counter       int
	limit         int
	stateFilePath string
}

// NewManager creates a session manager with the given maximum number of
// concurrent active sessions.
func NewManager(limit int) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		limit:    limit,
	}
}

// Create starts a new shell session with the given terminal dimensions.
// The env parameter provides additional environment variables (e.g., from
// the SSH session) that are merged with the container's environment.
func (m *Manager) Create(cols, rows uint16, env []string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	active := 0
	for _, s := range m.sessions {
		if !s.Exited() {
			active++
		}
	}
	if active >= m.limit {
		return nil, fmt.Errorf("session limit reached (%d)", m.limit)
	}

	m.counter++
	id := fmt.Sprintf("session-%d", m.counter)
	s, err := newSession(id, cols, rows, env, m)
	if err != nil {
		return nil, err
	}
	m.sessions[id] = s
	m.writeStateFile()

	go func() {
		s.waitForCleanup()
		m.mu.Lock()
		delete(m.sessions, id)
		m.writeStateFile()
		m.mu.Unlock()
	}()

	return s, nil
}

// Get returns the session with the given ID, or nil if not found.
func (m *Manager) Get(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// List returns info snapshots for all current sessions.
func (m *Manager) List() []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	infos := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		infos = append(infos, s.Info())
	}
	return infos
}

// SetStateFilePath sets the path where session state is written on changes.
func (m *Manager) SetStateFilePath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateFilePath = path
}

// onSessionChanged is called by sessions when their state changes.
func (m *Manager) onSessionChanged() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeStateFile()
}
