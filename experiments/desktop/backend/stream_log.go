package backend

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type StreamLogStatus struct {
	Path   string `json:"path"`
	Error  string `json:"error,omitempty"`
	Closed bool   `json:"closed"`
}

type streamLog struct {
	mu      sync.Mutex
	file    *os.File
	encoder *json.Encoder
	state   StreamLogStatus
}

func newStreamLog(dir, connection string, sandbox Sandbox) (*streamLog, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	mode := "live"
	if sandbox.Demo {
		mode = "demo"
	}
	file, err := os.CreateTemp(dir, "claude-"+mode+"-"+time.Now().UTC().Format("20060102T150405Z")+"-*.jsonl")
	if err != nil {
		return nil, err
	}
	l := &streamLog{file: file, encoder: json.NewEncoder(file), state: StreamLogStatus{Path: file.Name()}}
	l.encoder.SetEscapeHTML(false)
	// Keep enough context to distinguish recordings without consulting app state.
	err = l.encoder.Encode(map[string]any{
		"kind": "session", "schema_version": 1, "started_at": time.Now().UTC(),
		"connection": connection, "sandbox": sandbox,
	})
	if err != nil {
		file.Close()
		return nil, err
	}
	log.Printf("Claude stream recording: %s", file.Name())
	return l, nil
}

func (l *streamLog) append(message AgentDebugMessage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state.Closed || l.state.Error != "" {
		return nil
	}
	record := struct {
		Kind string `json:"kind"`
		AgentDebugMessage
		Message json.RawMessage `json:"message,omitempty"`
	}{Kind: "stream", AgentDebugMessage: message}
	if (message.Stream == "stdin" || message.Stream == "stdout") && json.Valid([]byte(message.Raw)) {
		record.Message = json.RawMessage(message.Raw)
	}
	// No userspace buffer: each complete JSONL record is readable immediately.
	if err := l.encoder.Encode(record); err != nil {
		l.state.Error = fmt.Sprintf("stream recording stopped: %v", err)
		log.Printf("%s: %s", l.state.Path, l.state.Error)
		return err
	}
	return nil
}

func (l *streamLog) status() StreamLogStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

func (l *streamLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state.Closed {
		return
	}
	l.state.Closed = true
	// Sync at shutdown, not for every token, while keeping normal writes tail-able.
	if err := l.file.Sync(); err != nil && l.state.Error == "" {
		l.state.Error = fmt.Sprintf("sync stream recording: %v", err)
	}
	if err := l.file.Close(); err != nil && l.state.Error == "" {
		l.state.Error = fmt.Sprintf("close stream recording: %v", err)
	}
	if l.state.Error != "" {
		log.Printf("%s: %s", l.state.Path, l.state.Error)
	}
}
