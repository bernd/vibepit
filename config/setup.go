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
// allow and dns-only entries from the project config.
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

	return selected, writeReconfiguredProjectConfig(projectConfigPath, selected, cfg.Allow, cfg.DNSOnly)
}

func writeProjectConfig(path string, presets []string) error {
	return writeReconfiguredProjectConfig(path, presets, nil, nil)
}

// writeReconfiguredProjectConfig writes the config file with new presets while
// preserving existing allow and dns-only entries. When allow and dnsOnly are
// nil, commented-out placeholder sections are written instead.
func writeReconfiguredProjectConfig(path string, presets []string, allow []string, dnsOnly []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	var sb strings.Builder
	writeConfigHeader(&sb)
	writePresetsSection(&sb, presets)
	writeYAMLListSection(&sb,
		"# Additional domains to allow for this project.",
		"allow", allow,
		[]string{"api.openai.com", "api.anthropic.com"})
	writeYAMLListSection(&sb,
		"# Domains that only need DNS resolution (no HTTP proxy).",
		"dns-only", dnsOnly,
		[]string{"internal.corp.example.com"})

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// writeConfigHeader writes the shared file header comment block.
func writeConfigHeader(sb *strings.Builder) {
	sb.WriteString("# Vibepit network config for this project.\n")
	sb.WriteString("# All internet access from the dev container is filtered through\n")
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
			fmt.Fprintf(sb, "  - %s\n", d)
		}
	} else {
		fmt.Fprintf(sb, "# %s:\n", key)
		for _, d := range examples {
			fmt.Fprintf(sb, "#   - %s\n", d)
		}
	}
}

// containsLine checks if a string contains a line starting with the given prefix.
// It handles both start-of-file and after-newline positions.
func containsLine(content, prefix string) bool {
	return strings.HasPrefix(content, prefix) || strings.Contains(content, "\n"+prefix)
}

// AppendAllow adds domains to the allow list of an existing project config.
// It loads the current config, deduplicates, and writes back.
func AppendAllow(projectConfigPath string, domains []string) error {
	var cfg ProjectConfig
	if err := loadFile(projectConfigPath, &cfg); err != nil {
		return fmt.Errorf("load project config: %w", err)
	}

	existing := make(map[string]bool, len(cfg.Allow))
	for _, d := range cfg.Allow {
		existing[d] = true
	}

	var added []string
	for _, d := range domains {
		if !existing[d] {
			existing[d] = true
			cfg.Allow = append(cfg.Allow, d)
			added = append(added, d)
		}
	}

	if len(added) == 0 {
		return nil
	}

	// Re-read the raw file and append the new entries to preserve
	// comments and formatting. If the file has no "allow:" section yet,
	// add one.
	data, err := os.ReadFile(projectConfigPath)
	if err != nil {
		return fmt.Errorf("read project config: %w", err)
	}

	content := string(data)
	hasAllow := containsLine(content, "allow:")
	hasCommentedAllow := containsLine(content, "# allow:")

	var sb strings.Builder

	if hasAllow {
		// File already has an allow section — append new entries at end.
		sb.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			sb.WriteString("\n")
		}
		for _, d := range added {
			fmt.Fprintf(&sb, "  - %s\n", d)
		}
	} else if hasCommentedAllow {
		// Replace the commented-out allow section with a real one.
		lines := strings.Split(content, "\n")
		inCommentedAllow := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "# allow:" {
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
		// Add the allow section with all entries.
		sb.WriteString("allow:\n")
		for _, d := range cfg.Allow {
			fmt.Fprintf(&sb, "  - %s\n", d)
		}
	} else {
		// No allow section at all — append one.
		sb.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\nallow:\n")
		for _, d := range cfg.Allow {
			fmt.Fprintf(&sb, "  - %s\n", d)
		}
	}

	return os.WriteFile(projectConfigPath, []byte(sb.String()), 0o644)
}
