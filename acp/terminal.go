package acp

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
)

// TerminalManager manages terminal sessions (child processes) inside the sandbox.
type TerminalManager struct {
	mu        sync.Mutex
	terminals map[string]*terminalState
	nextID    atomic.Int64
}

type terminalState struct {
	cmd      *exec.Cmd
	output   bytes.Buffer
	mu       sync.Mutex // protects output and exitCode
	exitCode *int
	done     chan struct{} // closed when process exits
	cancel   context.CancelFunc
}

// NewTerminalManager creates a new terminal manager.
func NewTerminalManager() *TerminalManager {
	return &TerminalManager{
		terminals: make(map[string]*terminalState),
	}
}

// TerminalCreateParams are the parameters for terminal/create.
type TerminalCreateParams struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
	Env     []string `json:"env,omitempty"`
}

// TerminalCreateResult is the result of terminal/create.
type TerminalCreateResult struct {
	TerminalID string `json:"terminalId"`
}

// TerminalOutputParams are the parameters for terminal/output.
type TerminalOutputParams struct {
	TerminalID string `json:"terminalId"`
}

// TerminalOutputResult is the result of terminal/output.
type TerminalOutputResult struct {
	Output    string `json:"output"`
	Truncated bool   `json:"truncated"`
}

// TerminalWaitParams are the parameters for terminal/wait_for_exit.
type TerminalWaitParams struct {
	TerminalID string `json:"terminalId"`
}

// TerminalWaitResult is the result of terminal/wait_for_exit.
type TerminalWaitResult struct {
	ExitCode int `json:"exitCode"`
}

// TerminalKillParams are the parameters for terminal/kill.
type TerminalKillParams struct {
	TerminalID string `json:"terminalId"`
}

// TerminalReleaseParams are the parameters for terminal/release.
type TerminalReleaseParams struct {
	TerminalID string `json:"terminalId"`
}

const maxOutputBytes = 1 * 1024 * 1024 // 1 MiB

// Create starts a new terminal process.
func (m *TerminalManager) Create(params TerminalCreateParams) (*TerminalCreateResult, error) {
	id := fmt.Sprintf("term-%d", m.nextID.Add(1))

	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, params.Command, params.Args...)
	if params.Cwd != "" {
		cmd.Dir = params.Cwd
	}
	if len(params.Env) > 0 {
		cmd.Env = params.Env
	}

	ts := &terminalState{
		cmd:    cmd,
		done:   make(chan struct{}),
		cancel: cancel,
	}

	cmd.Stdout = &limitedWriter{ts: ts, max: maxOutputBytes}
	cmd.Stderr = &limitedWriter{ts: ts, max: maxOutputBytes}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start command: %w", err)
	}

	// Wait for the process in background.
	go func() {
		defer close(ts.done)
		err := cmd.Wait()
		ts.mu.Lock()
		defer ts.mu.Unlock()
		code := cmd.ProcessState.ExitCode()
		if err != nil && code == -1 {
			code = 1
		}
		ts.exitCode = &code
	}()

	m.mu.Lock()
	m.terminals[id] = ts
	m.mu.Unlock()

	return &TerminalCreateResult{TerminalID: id}, nil
}

// Output returns the buffered output for a terminal.
func (m *TerminalManager) Output(params TerminalOutputParams) (*TerminalOutputResult, error) {
	ts, err := m.get(params.TerminalID)
	if err != nil {
		return nil, err
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return &TerminalOutputResult{
		Output:    ts.output.String(),
		Truncated: ts.output.Len() >= maxOutputBytes,
	}, nil
}

// WaitForExit blocks until the terminal process exits.
func (m *TerminalManager) WaitForExit(params TerminalWaitParams) (*TerminalWaitResult, error) {
	ts, err := m.get(params.TerminalID)
	if err != nil {
		return nil, err
	}
	<-ts.done
	ts.mu.Lock()
	defer ts.mu.Unlock()
	code := 1
	if ts.exitCode != nil {
		code = *ts.exitCode
	}
	return &TerminalWaitResult{ExitCode: code}, nil
}

// Kill sends a kill signal to the terminal process.
func (m *TerminalManager) Kill(params TerminalKillParams) error {
	ts, err := m.get(params.TerminalID)
	if err != nil {
		return err
	}
	ts.cancel()
	return nil
}

// Release kills the process if still running and removes it from the manager.
func (m *TerminalManager) Release(params TerminalReleaseParams) error {
	m.mu.Lock()
	ts, ok := m.terminals[params.TerminalID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown terminal: %s", params.TerminalID)
	}
	delete(m.terminals, params.TerminalID)
	m.mu.Unlock()

	ts.cancel()
	<-ts.done
	return nil
}

// CleanupAll kills and releases all terminals.
func (m *TerminalManager) CleanupAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.terminals))
	for id := range m.terminals {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.Release(TerminalReleaseParams{TerminalID: id})
	}
}

func (m *TerminalManager) get(id string) (*terminalState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ts, ok := m.terminals[id]
	if !ok {
		return nil, fmt.Errorf("unknown terminal: %s", id)
	}
	return ts, nil
}

// limitedWriter writes to the terminal's output buffer up to a maximum.
type limitedWriter struct {
	ts  *terminalState
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.ts.mu.Lock()
	defer w.ts.mu.Unlock()
	remaining := w.max - w.ts.output.Len()
	if remaining <= 0 {
		return n, nil // discard but don't error
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	w.ts.output.Write(p)
	return n, nil
}
