package cmd

import (
	"fmt"
	"os"
	"path/filepath"
)

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
	relativeDir, err = filepath.Rel(projectRoot, workingDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve working directory relative to project: %w", err)
	}
	if !filepath.IsLocal(relativeDir) {
		return "", "", fmt.Errorf("working directory %q is outside project root %q", workingDir, projectRoot)
	}
	return filepath.Join(projectRoot, relativeDir), relativeDir, nil
}
