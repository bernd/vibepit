package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectPresets(t *testing.T) {
	t.Run("detects go project", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example"), 0o644)
		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-go")
	})

	t.Run("detects node project", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644)
		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-node")
	})

	t.Run("detects multiple ecosystems", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example"), 0o644)
		os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644)
		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-go")
		assert.Contains(t, detected, "pkg-node")
	})

	t.Run("detects glob patterns like *.gemspec", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "mygem.gemspec"), []byte(""), 0o644)
		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-ruby")
	})

	t.Run("empty directory detects nothing", func(t *testing.T) {
		dir := t.TempDir()
		detected := DetectPresets(dir)
		assert.Empty(t, detected)
	})

	t.Run("does not detect non-pkg presets", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example"), 0o644)
		detected := DetectPresets(dir)
		assert.NotContains(t, detected, "default")
		assert.NotContains(t, detected, "anthropic")
		assert.NotContains(t, detected, "cloud")
	})

	t.Run("detects python project via pyproject.toml", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(""), 0o644)
		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-python")
	})

	t.Run("detects python project via requirements.txt", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte(""), 0o644)
		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-python")
	})

	t.Run("detects jvm project via pom.xml", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(""), 0o644)
		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-jvm")
	})

	t.Run("detects rust project", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(""), 0o644)
		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-rust")
	})

	t.Run("detects pkg-others via composer.json", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "composer.json"), []byte("{}"), 0o644)
		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-others")
	})

	t.Run("detects pkg-others via csproj glob", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "MyApp.csproj"), []byte(""), 0o644)
		detected := DetectPresets(dir)
		assert.Contains(t, detected, "pkg-others")
	})
}
