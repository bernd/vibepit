package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bernd/vibepit/config"
)

// remoteWorkingDirectory returns the project-relative working directory to
// request from a running sandbox. When the current directory cannot be mapped
// into the project (deleted cwd, unresolvable path), it prints a warning and
// returns ok=false so callers skip the request and sessions start in the
// project root instead of failing the whole connection.
func remoteWorkingDirectory(projectRoot string, stderr io.Writer) (relativeDir string, ok bool) {
	_, relativeDir, err := resolveProjectWorkingDirectory(projectRoot, "")
	if err != nil {
		fmt.Fprintf(stderr, "vibepit: %s; starting in the project root\n", err)
		return "", false
	}
	return relativeDir, true
}

func resolveProjectWorkingDirectory(projectRoot, requestedDir string) (workingDir, relativeDir string, err error) {
	if requestedDir == "" {
		requestedDir, err = os.Getwd()
		if err != nil {
			return "", "", fmt.Errorf("get working directory: %w", err)
		}
	}

	workingDir, err = filepath.Abs(requestedDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve working directory: %w", err)
	}
	// Compare symlink-resolved paths so a directory reached through a symlink
	// (e.g. macOS /tmp -> /private/tmp) still maps into the project. The
	// returned path stays anchored at projectRoot, which is the path mounted
	// into the sandbox.
	resolvedRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve project root: %w", err)
	}
	resolvedDir, err := filepath.EvalSymlinks(workingDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve working directory: %w", err)
	}
	relativeDir, err = filepath.Rel(resolvedRoot, resolvedDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve working directory relative to project: %w", err)
	}
	if relativeDir != "." && !filepath.IsLocal(relativeDir) {
		return "", "", fmt.Errorf("working directory %q is outside project root %q", workingDir, projectRoot)
	}
	// The project's config directory is masked inside the sandbox, so a
	// working directory under it would not exist there.
	if relativeDir == config.ProjectConfigDirName ||
		strings.HasPrefix(relativeDir, config.ProjectConfigDirName+string(filepath.Separator)) {
		relativeDir = "."
	}
	return filepath.Join(projectRoot, relativeDir), relativeDir, nil
}
