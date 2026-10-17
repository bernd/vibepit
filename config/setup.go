package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

// RunReconfigure re-runs the interactive preset selector and rewrites only the
// presets section of the project config. Every other key (allow-http,
// allow-dns, allow-host-ports, memory, ...) and all comments are left as they
// are, so settings the template writer doesn't know about survive.
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

	data, err := os.ReadFile(projectConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		return selected, writeProjectConfig(projectConfigPath, selected)
	}
	if err != nil {
		return nil, fmt.Errorf("read project config: %w", err)
	}

	updated, err := replaceYAMLListSection(data, "presets", selected)
	if err != nil {
		return nil, err
	}
	return selected, os.WriteFile(projectConfigPath, updated, 0o600)
}

// writeProjectConfig writes a fresh config file with the given presets and
// commented-out placeholder sections for the other keys.
func writeProjectConfig(path string, presets []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}

	var sb strings.Builder
	writeConfigHeader(&sb)
	writePresetsSection(&sb, presets)
	writeYAMLListSection(&sb,
		"# Additional domains to allow HTTP access for this project.",
		"allow-http",
		[]string{"api.openai.com:443", "api.anthropic.com:443"})
	writeYAMLListSection(&sb,
		"# Domains that only need DNS resolution (no HTTP proxy).",
		"allow-dns",
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

// writeYAMLListSection writes a commented-out placeholder for a named YAML
// list section using the example values.
func writeYAMLListSection(sb *strings.Builder, comment, key string, examples []string) {
	fmt.Fprintf(sb, "\n%s\n", comment)
	fmt.Fprintf(sb, "# %s:\n", key)
	for _, d := range examples {
		fmt.Fprintf(sb, "#   - %s\n", d)
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
		appendYAMLStringSeq(valNode, added)
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
		appendYAMLStringSeq(valNode, added)
		return encodeYAMLDocument(doc)

	default:
		return nil, fmt.Errorf("%s: expected YAML list", sectionKey)
	}
}

// replaceYAMLListSection replaces the whole list keyed by sectionKey with
// entries. Like appendYAMLListEntries it edits lines in place where the shape
// is simple (a block list of plain scalars on consecutive lines) and only
// re-encodes the document for shapes where that isn't safe. A missing section
// is created the same way appendYAMLListEntries creates one. Empty entries
// remove the section.
func replaceYAMLListSection(data []byte, sectionKey string, entries []string) ([]byte, error) {
	doc, root, err := parseProjectConfigYAML(data)
	if err != nil {
		return nil, err
	}

	i := findYAMLMappingIndex(root, sectionKey)
	if i < 0 {
		if len(entries) == 0 {
			return data, nil
		}
		return appendYAMLListEntries(data, sectionKey, entries)
	}
	keyNode, valNode := root.Content[i], root.Content[i+1]

	if out, ok := replaceBlockYAMLList(data, keyNode, valNode, entries); ok {
		return out, nil
	}

	if len(entries) > 0 {
		valNode.Content = nil
		appendYAMLStringSeq(valNode, entries)
		return encodeYAMLDocument(doc)
	}

	// The encoder attaches comments above a key to that key, which for the
	// first key includes the file header. Hand them to the next key (or the
	// document) so removing the section doesn't drop them.
	if keyNode.HeadComment != "" {
		if i+2 < len(root.Content) {
			next := root.Content[i+2]
			next.HeadComment = joinYAMLComments(keyNode.HeadComment, next.HeadComment)
		} else {
			doc.FootComment = joinYAMLComments(doc.FootComment, keyNode.HeadComment)
		}
	}
	root.Content = slices.Delete(root.Content, i, i+2)
	return encodeYAMLDocument(doc)
}

// appendYAMLStringSeq turns n into a block sequence (if it isn't one already)
// and appends entries as string scalars.
func appendYAMLStringSeq(n *yaml.Node, entries []string) {
	n.Kind = yaml.SequenceNode
	n.Tag = "!!seq"
	n.Value = ""
	n.Style = 0
	for _, d := range entries {
		n.Content = append(n.Content, newYAMLStringScalar(d))
	}
}

func joinYAMLComments(first, second string) string {
	if first == "" || second == "" {
		return first + second
	}
	return first + "\n\n" + second
}

// replaceBlockYAMLList swaps the items of an existing block list for entries
// by editing lines. It handles two shapes: a `key:` line with nothing after
// the colon (implicit null), and a block list whose items are plain scalars
// on consecutive lines directly below the key. Empty entries remove the key
// line as well. Anything else (flow style, comments between items, multi-line
// scalars) reports false so the caller can fall back to re-encoding.
func replaceBlockYAMLList(data []byte, keyNode, valNode *yaml.Node, entries []string) ([]byte, bool) {
	lines := strings.SplitAfter(string(data), "\n")
	keyIdx := keyNode.Line - 1
	if keyIdx < 0 || keyIdx >= len(lines) {
		return nil, false
	}

	// [firstIdx, lastIdx] is the 0-based line span of the existing items.
	firstIdx, lastIdx := keyIdx+1, keyIdx
	indent := "  "

	switch {
	case valNode.Kind == yaml.ScalarNode && valNode.Tag == "!!null" &&
		valNode.Value == "" && valNode.Line == keyNode.Line:
		// Nothing to remove; new items go directly under the key line.

	case valNode.Kind == yaml.SequenceNode:
		if valNode.Style&yaml.FlowStyle != 0 || len(valNode.Content) == 0 {
			return nil, false
		}
		first := valNode.Content[0]
		if first.Line <= keyNode.Line {
			return nil, false
		}
		for i, item := range valNode.Content {
			if item.Kind != yaml.ScalarNode ||
				item.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 ||
				item.Line != first.Line+i {
				return nil, false
			}
		}
		firstIdx = first.Line - 1
		lastIdx = firstIdx + len(valNode.Content) - 1
		if lastIdx >= len(lines) {
			return nil, false
		}
		for _, l := range lines[firstIdx : lastIdx+1] {
			if !strings.HasPrefix(strings.TrimLeft(l, " \t"), "-") {
				return nil, false
			}
		}
		itemLine := lines[firstIdx]
		indent = itemLine[:len(itemLine)-len(strings.TrimLeft(itemLine, " \t"))]
		if !yamlItemEndsOnLine(lines, lastIdx, indent) {
			return nil, false
		}

	default:
		return nil, false
	}

	if len(entries) == 0 {
		firstIdx = keyIdx
	}
	return spliceYAMLLines(lines, firstIdx, lastIdx+1, indent, entries), true
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
	if !yamlItemEndsOnLine(lines, lineIndex, indent) {
		return nil, false
	}

	return spliceYAMLLines(lines, lineIndex+1, lineIndex+1, indent, entries), true
}

// yamlItemEndsOnLine reports whether the block list item starting on line idx
// ends there. yaml.Node only records where a node starts, and a plain or
// quoted scalar can continue on following lines indented deeper than its
// dash, so any deeper-indented line after it (even a comment) counts as a
// possible continuation.
func yamlItemEndsOnLine(lines []string, idx int, indent string) bool {
	for _, l := range lines[idx+1:] {
		trimmed := strings.TrimLeft(l, " \t")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		return len(l)-len(trimmed) <= len(indent)
	}
	return true
}

// spliceYAMLLines replaces lines[from:to] with one block list item per entry
// at the given indent. The line before from is kept and gets a line ending if
// it lacks one (last line of the file), whose style the new items reuse.
func spliceYAMLLines(lines []string, from, to int, indent string, entries []string) []byte {
	nl := "\n"
	if from > 0 {
		nl = lineEnding(lines[from-1])
		if !strings.HasSuffix(lines[from-1], "\n") {
			lines[from-1] += nl
		}
	}
	out := make([]string, 0, len(lines)-(to-from)+len(entries))
	out = append(out, lines[:from]...)
	for _, d := range entries {
		out = append(out, fmt.Sprintf("%s- %s%s", indent, formatYAMLListValue(d), nl))
	}
	out = append(out, lines[to:]...)
	return []byte(strings.Join(out, ""))
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
	i := findYAMLMappingIndex(root, key)
	if i < 0 {
		return nil, nil
	}
	return root.Content[i], root.Content[i+1]
}

// findYAMLMappingIndex returns the index of key's key node in root.Content,
// or -1 if the mapping has no such key.
func findYAMLMappingIndex(root *yaml.Node, key string) int {
	for i := 0; i+1 < len(root.Content); i += 2 {
		k := root.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return i
		}
	}
	return -1
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
	return spliceYAMLLines(lines, idx+1, idx+1, "  ", added), true
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
