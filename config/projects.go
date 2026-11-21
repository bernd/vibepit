package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ProjectsDir returns the directory that records the project roots vibepit
// started sessions in. It lies outside every project, so code in a sandbox
// can't add a root to it.
func ProjectsDir() string {
	return filepath.Join(StateDir(), "projects")
}

// IsRecordedProject reports whether root is a recorded project root.
func IsRecordedProject(root string) (bool, error) {
	return defaultRecords().has(root)
}

// RecordProject records root as a project root.
func RecordProject(root string) error {
	return defaultRecords().add(root)
}

func defaultRecords() projectRecords {
	return projectRecords(ProjectsDir())
}

// projectRecords is a directory with one file per recorded project root,
// named by the root's hash so that no path needs escaping and concurrent
// sessions need no lock.
type projectRecords string

func (r projectRecords) file(root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(string(r), hex.EncodeToString(sum[:]))
}

func (r projectRecords) has(root string) (bool, error) {
	data, err := os.ReadFile(r.file(root))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read project record: %w", err)
	}
	return string(data) == root+"\n", nil
}

func (r projectRecords) add(root string) error {
	if err := os.MkdirAll(string(r), 0o700); err != nil {
		return fmt.Errorf("create project records dir: %w", err)
	}
	if err := replaceFile(r.file(root), []byte(root+"\n"), 0o600, -1); err != nil {
		return fmt.Errorf("write project record: %w", err)
	}
	return nil
}
