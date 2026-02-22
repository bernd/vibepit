package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RunFirstTimeSetup shows an interactive preset selector and writes the project
// config file. Returns the selected preset names.
func RunFirstTimeSetup(projectDir, projectConfigPath string) ([]string, error) {
	detected := DetectPresets(projectDir)

	preChecked := make(map[string]bool)
	preChecked["default"] = true
	for _, d := range detected {
		preChecked[d] = true
	}

	selected, err := runPresetSelectorTUI(preChecked, detected)
	if err != nil {
		return nil, err
	}

	return selected, writeProjectConfig(projectConfigPath, selected)
}

// RunReconfigure re-runs the interactive preset selector, preserving existing
// allow-http and allow-dns entries from the project config.
func RunReconfigure(projectConfigPath, projectDir string) ([]string, error) {
	var cfg ProjectConfig
	if err := loadFile(projectConfigPath, &cfg); err != nil {
		return nil, fmt.Errorf("load project config: %w", err)
	}

	detected := DetectPresets(projectDir)

	preChecked := make(map[string]bool)
	for _, p := range cfg.Presets {
		preChecked[p] = true
	}

	selected, err := runPresetSelectorTUI(preChecked, detected)
	if err != nil {
		return nil, err
	}

	return selected, writeReconfiguredProjectConfig(projectConfigPath, selected, cfg.AllowHTTP, cfg.AllowDNS)
}

func writeProjectConfig(path string, presets []string) error {
	return writeReconfiguredProjectConfig(path, presets, nil, nil)
}

// writeReconfiguredProjectConfig writes the config file with new presets while
// preserving existing allow-http and allow-dns entries. When allowHTTP and allowDNS
// are nil, commented-out placeholder sections are written instead.
func writeReconfiguredProjectConfig(path string, presets []string, allowHTTP []string, allowDNS []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	var sb strings.Builder
	writeConfigHeader(&sb)
	writePresetsSection(&sb, presets)
	writeYAMLListSection(&sb,
		"# Additional domains to allow HTTP access for this project.",
		"allow-http", allowHTTP,
		[]string{"api.openai.com:443", "api.anthropic.com:443"})
	writeYAMLListSection(&sb,
		"# Domains that only need DNS resolution (no HTTP proxy).",
		"allow-dns", allowDNS,
		[]string{"internal.corp.example.com"})

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// writeConfigHeader writes the shared file header comment block.
func writeConfigHeader(sb *strings.Builder) {
	sb.WriteString("# Vibepit network config for this project.\n")
	sb.WriteString("# All internet access from the sandbox container is filtered through\n")
	sb.WriteString("# a proxy that only allows domains listed here.\n")
	sb.WriteString("#\n")
	sb.WriteString("# Docs: https://github.com/bernd/vibepit\n\n")
}

// writePresetsSection writes the presets block with its help comments.
func writePresetsSection(sb *strings.Builder, presets []string) {
	sb.WriteString("# Presets bundle common domains for a language ecosystem.\n")
	sb.WriteString("# Use 'vibepit --reconfigure' to change presets interactively.\n")
	sb.WriteString("# Available: default, anthropic, vcs-github, vcs-other, containers,\n")
	sb.WriteString("# cloud, pkg-node, pkg-python, pkg-ruby, pkg-rust, pkg-go, pkg-jvm,\n")
	sb.WriteString("# pkg-others, linux-distros, devtools, monitoring, cdn, schema, mcp\n")
	if len(presets) > 0 {
		sb.WriteString("presets:\n")
		for _, p := range presets {
			fmt.Fprintf(sb, "  - %s\n", p)
		}
	} else {
		sb.WriteString("# presets:\n")
		sb.WriteString("#   - default\n")
		sb.WriteString("#   - pkg-go\n")
	}
}

// writeYAMLListSection writes a named YAML list section. If entries is non-empty,
// it writes the active section; otherwise it writes a commented-out placeholder
// using the example values.
func writeYAMLListSection(sb *strings.Builder, comment, key string, entries []string, examples []string) {
	fmt.Fprintf(sb, "\n%s\n", comment)
	if len(entries) > 0 {
		fmt.Fprintf(sb, "%s:\n", key)
		for _, d := range entries {
			fmt.Fprintf(sb, "  - %s\n", formatYAMLListValue(d))
		}
	} else {
		fmt.Fprintf(sb, "# %s:\n", key)
		for _, d := range examples {
			fmt.Fprintf(sb, "#   - %s\n", d)
		}
	}
}

// formatYAMLListValue quotes values that would otherwise be parsed as aliases.
func formatYAMLListValue(v string) string {
	if strings.HasPrefix(v, "*") {
		return fmt.Sprintf("%q", v)
	}
	return v
}

// containsLine checks if a string contains a line starting with the given prefix.
// It handles both start-of-file and after-newline positions.
func containsLine(content, prefix string) bool {
	return strings.HasPrefix(content, prefix) || strings.Contains(content, "\n"+prefix)
}

// AppendAllowHTTP adds entries to the allow-http list of an existing project config.
// It loads the current config, deduplicates, and writes back.
func AppendAllowHTTP(projectConfigPath string, entries []string) error {
	return appendAllowEntries(projectConfigPath, "allow-http", entries)
}

// AppendAllowDNS adds entries to the allow-dns list of an existing project config.
// It loads the current config, deduplicates, and writes back.
func AppendAllowDNS(projectConfigPath string, entries []string) error {
	return appendAllowEntries(projectConfigPath, "allow-dns", entries)
}

func appendAllowEntries(projectConfigPath, sectionKey string, entries []string) error {
	var cfg ProjectConfig
	if err := loadFile(projectConfigPath, &cfg); err != nil {
		return fmt.Errorf("load project config: %w", err)
	}

	var current []string
	switch sectionKey {
	case "allow-http":
		current = cfg.AllowHTTP
	case "allow-dns":
		current = cfg.AllowDNS
	default:
		return fmt.Errorf("unknown config section %q", sectionKey)
	}

	existing := make(map[string]bool, len(current))
	for _, d := range current {
		existing[d] = true
	}

	var added []string
	for _, d := range entries {
		if !existing[d] {
			existing[d] = true
			current = append(current, d)
			added = append(added, d)
		}
	}
	switch sectionKey {
	case "allow-http":
		cfg.AllowHTTP = current
	case "allow-dns":
		cfg.AllowDNS = current
	}

	if len(added) == 0 {
		return nil
	}

	// Re-read the raw file and append the new entries to preserve
	// comments and formatting. If the file has no "allow-http:" section yet,
	// add one.
	data, err := os.ReadFile(projectConfigPath)
	if err != nil {
		return fmt.Errorf("read project config: %w", err)
	}

	content := string(data)
	activeLine := sectionKey + ":"
	commentedLine := "# " + sectionKey + ":"
	hasAllow := containsLine(content, activeLine)
	hasCommentedAllow := containsLine(content, commentedLine)

	var sb strings.Builder

	if hasAllow {
		// File already has the section — append only newly added entries.
		sb.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			sb.WriteString("\n")
		}
		for _, d := range added {
			fmt.Fprintf(&sb, "  - %s\n", formatYAMLListValue(d))
		}
	} else if hasCommentedAllow {
		// Replace the commented-out section with a real one.
		lines := strings.Split(content, "\n")
		inCommentedAllow := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == commentedLine {
				inCommentedAllow = true
				continue
			}
			if inCommentedAllow && strings.HasPrefix(trimmed, "#   - ") {
				continue
			}
			inCommentedAllow = false
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		// Add the active section with all entries.
		fmt.Fprintf(&sb, "%s\n", activeLine)
		for _, d := range current {
			fmt.Fprintf(&sb, "  - %s\n", formatYAMLListValue(d))
		}
	} else {
		// No section at all — append one.
		sb.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "\n%s\n", activeLine)
		for _, d := range current {
			fmt.Fprintf(&sb, "  - %s\n", formatYAMLListValue(d))
		}
	}

	return os.WriteFile(projectConfigPath, []byte(sb.String()), 0o644)
}
