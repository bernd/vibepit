package cmd

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Prompt logs hold what prompts couldn't show on the terminal, one file per
// session: the session owns the terminal, so there's nowhere else to put
// it. They live beside the session directories, not in one, because those
// are removed when the session stops, which is when a log gets read.
const (
	promptLogDirName = "prompt-logs"
	maxPromptLog     = 1 << 20 // past it, lines are dropped
	promptLogMaxAge  = 7 * 24 * time.Hour
)

// promptLogDir is where prompt logs go; tests point it elsewhere.
var promptLogDir = func() string {
	return filepath.Join(filepath.Dir(sessionBaseDir()), promptLogDirName)
}

// openPromptLog returns a logger that appends to the session's prompt log,
// and the log's path, after removing logs untouched for promptLogMaxAge.
// Logging is best effort: without the file, lines are dropped.
func openPromptLog(sessionID string) (*log.Logger, string) {
	dir := promptLogDir()
	path := filepath.Join(dir, sessionID+".log")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return log.New(io.Discard, "", 0), path
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > promptLogMaxAge {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	return log.New(&promptLogWriter{path: path}, "", log.LstdFlags), path
}

// promptLogWriter opens the file for every line. Lines are rare, the cap
// then holds across clients sharing the file, and a log removed as old
// comes back.
type promptLogWriter struct {
	mu   sync.Mutex
	path string
}

func (w *promptLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return len(p), nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Size()+int64(len(p)) > maxPromptLog {
		return len(p), nil
	}
	_, _ = f.Write(p)
	return len(p), nil
}
