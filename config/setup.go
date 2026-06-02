package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
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
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
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

	return os.WriteFile(path, []byte(sb.String()), 0o600)
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
		if existing[d] {
			continue
		}
		existing[d] = true
		added = append(added, d)
	}
	if len(added) == 0 {
		return nil
	}

	data, err := os.ReadFile(projectConfigPath)
	if err != nil {
		return fmt.Errorf("read project config: %w", err)
	}

	updated, err := appendYAMLListEntries(data, sectionKey, added)
	if err != nil {
		return err
	}
	return os.WriteFile(projectConfigPath, updated, 0o600)
}

// appendYAMLListEntries adds entries to a YAML list keyed by sectionKey in the
// given config bytes. It prefers line-based edits to preserve comments and
// blank lines verbatim, and only falls back to a full YAML re-encode for
// awkward shapes (flow style, nested items, explicit-null values on their own
// line) where surgical editing isn't safe.
func appendYAMLListEntries(data []byte, sectionKey string, added []string) ([]byte, error) {
	doc, root, err := parseProjectConfigYAML(data)
	if err != nil {
		return nil, err
	}

	keyNode, valNode := findYAMLMappingPair(root, sectionKey)

	switch {
	case keyNode == nil:
		if out, ok := replaceCommentedYAMLListSection(data, sectionKey, added); ok {
			return out, nil
		}
		return appendNewYAMLListSection(data, sectionKey, added), nil

	case valNode.Kind == yaml.SequenceNode:
		if out, ok := appendToExistingBlockYAMLList(data, valNode, added); ok {
			return out, nil
		}
		valNode.Style = 0
		for _, d := range added {
			valNode.Content = append(valNode.Content, newYAMLStringScalar(d))
		}
		return encodeYAMLDocument(doc)

	case valNode.Kind == yaml.ScalarNode && valNode.Tag == "!!null":
		// Only edit in place when the null is implicit (the key line has no
		// value text after the colon). An explicit `null`/`~` token still has
		// tag !!null but a non-empty Value; inserting a block list under it
		// would yield `allow-http: ~\n  - entry`, which YAML reparses as a
		// single mangled scalar. Those fall through to the re-encode below.
		if valNode.Value == "" && valNode.Line == keyNode.Line && keyNode.Line > 0 {
			if out, ok := insertAfterKeyLine(data, keyNode.Line, added); ok {
				return out, nil
			}
		}
		valNode.Kind = yaml.SequenceNode
		valNode.Tag = "!!seq"
		valNode.Value = ""
		valNode.Style = 0
		for _, d := range added {
			valNode.Content = append(valNode.Content, newYAMLStringScalar(d))
		}
		return encodeYAMLDocument(doc)

	default:
		return nil, fmt.Errorf("%s: expected YAML list", sectionKey)
	}
}

func encodeYAMLDocument(doc *yaml.Node) ([]byte, error) {
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode project config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode project config: %w", err)
	}
	return out.Bytes(), nil
}

func appendToExistingBlockYAMLList(data []byte, section *yaml.Node, entries []string) ([]byte, bool) {
	if section.Style&yaml.FlowStyle != 0 || len(section.Content) == 0 {
		return nil, false
	}

	last := section.Content[len(section.Content)-1]
	if last.Kind != yaml.ScalarNode ||
		last.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 ||
		last.Line <= 0 {
		return nil, false
	}

	lines := strings.SplitAfter(string(data), "\n")
	lineIndex := last.Line - 1
	if lineIndex < 0 || lineIndex >= len(lines) {
		return nil, false
	}

	itemLine := lines[lineIndex]
	trimmedLeft := strings.TrimLeft(itemLine, " \t")
	if !strings.HasPrefix(trimmedLeft, "-") {
		return nil, false
	}
	indent := itemLine[:len(itemLine)-len(trimmedLeft)]

	nl := lineEnding(lines[lineIndex])
	if !strings.HasSuffix(lines[lineIndex], "\n") {
		lines[lineIndex] += nl
	}

	insertAt := lineIndex + 1
	inserted := make([]string, 0, len(entries))
	for _, d := range entries {
		inserted = append(inserted, fmt.Sprintf("%s- %s%s", indent, formatYAMLListValue(d), nl))
	}

	out := make([]string, 0, len(lines)+len(inserted))
	out = append(out, lines[:insertAt]...)
	out = append(out, inserted...)
	out = append(out, lines[insertAt:]...)
	return []byte(strings.Join(out, "")), true
}

func parseProjectConfigYAML(data []byte) (*yaml.Node, *yaml.Node, error) {
	var doc yaml.Node
	if strings.TrimSpace(string(data)) == "" {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
		return &doc, doc.Content[0], nil
	}

	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse project config: %w", err)
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
		return &doc, doc.Content[0], nil
	}

	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("project config: expected YAML mapping")
	}
	return &doc, root, nil
}

func findYAMLMappingPair(root *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		k := root.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return k, root.Content[i+1]
		}
	}
	return nil, nil
}

// lineEnding returns the trailing newline sequence of a line so inserted lines
// match the surrounding file (avoids mixing "\n" into a CRLF file).
func lineEnding(line string) string {
	if strings.HasSuffix(line, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func newYAMLStringScalar(value string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str"}
	node.SetString(value)
	return node
}

// insertAfterKeyLine inserts a fresh block list of entries directly under the
// 1-based line containing a `<key>:` whose value is null on the same line.
func insertAfterKeyLine(data []byte, keyLine int, added []string) ([]byte, bool) {
	lines := strings.SplitAfter(string(data), "\n")
	idx := keyLine - 1
	if idx < 0 || idx >= len(lines) {
		return nil, false
	}
	nl := lineEnding(lines[idx])
	if !strings.HasSuffix(lines[idx], "\n") {
		lines[idx] += nl
	}
	inserted := make([]string, 0, len(added))
	for _, d := range added {
		inserted = append(inserted, fmt.Sprintf("  - %s%s", formatYAMLListValue(d), nl))
	}
	out := make([]string, 0, len(lines)+len(inserted))
	out = append(out, lines[:idx+1]...)
	out = append(out, inserted...)
	out = append(out, lines[idx+1:]...)
	return []byte(strings.Join(out, "")), true
}

// replaceCommentedYAMLListSection finds a "# <key>:" placeholder header line
// and the run of "# - ..." commented example entries directly beneath it, and
// rewrites that block in place as an active section listing `added`. Header
// comments above the placeholder and any content elsewhere in the file are
// left untouched.
func replaceCommentedYAMLListSection(data []byte, sectionKey string, added []string) ([]byte, bool) {
	lines := strings.Split(string(data), "\n")
	headerIdx := -1
	for i, line := range lines {
		if isCommentedYAMLSectionHeader(line, sectionKey) {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 {
		return nil, false
	}

	endIdx := headerIdx
	for j := headerIdx + 1; j < len(lines); j++ {
		if !isCommentedYAMLListEntry(lines[j]) {
			break
		}
		endIdx = j
	}

	replacement := make([]string, 0, 1+len(added))
	replacement = append(replacement, sectionKey+":")
	for _, d := range added {
		replacement = append(replacement, "  - "+formatYAMLListValue(d))
	}

	out := make([]string, 0, len(lines)-(endIdx-headerIdx+1)+len(replacement))
	out = append(out, lines[:headerIdx]...)
	out = append(out, replacement...)
	out = append(out, lines[endIdx+1:]...)
	return []byte(strings.Join(out, "\n")), true
}

// appendNewYAMLListSection appends a brand-new YAML list section at the end of
// the file, leaving exactly one blank line between any existing content and
// the new section.
func appendNewYAMLListSection(data []byte, sectionKey string, added []string) []byte {
	var sb bytes.Buffer
	sb.Write(data)
	if len(data) > 0 {
		if !bytes.HasSuffix(data, []byte("\n")) {
			sb.WriteByte('\n')
		}
		if !bytes.HasSuffix(data, []byte("\n\n")) {
			sb.WriteByte('\n')
		}
	}
	sb.WriteString(sectionKey)
	sb.WriteString(":\n")
	for _, d := range added {
		fmt.Fprintf(&sb, "  - %s\n", formatYAMLListValue(d))
	}
	return sb.Bytes()
}

func isCommentedYAMLSectionHeader(line, key string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#") {
		return false
	}
	rest := strings.TrimLeft(trimmed[1:], " \t")
	return rest == key+":"
}

func isCommentedYAMLListEntry(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#") {
		return false
	}
	rest := strings.TrimLeft(trimmed[1:], " \t")
	return rest == "-" || strings.HasPrefix(rest, "- ")
}
