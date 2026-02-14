package telemetry

import (
	"slices"
	"strings"

	"github.com/bernd/vibepit/proxy"
)

// MetricFormatter formats an agent's metrics into plain-text display lines.
type MetricFormatter func(agent string, metrics []proxy.MetricSummary) []string

var registry = map[string]MetricFormatter{
	"claude_code.": formatClaudeCode,
	"codex.":       formatCodex,
}

// FormatAgent formats all metrics for a single agent. Metrics matching a
// registered prefix use the agent-specific formatter; the rest use generic.
func FormatAgent(agent string, metrics []proxy.MetricSummary) []string {
	if len(metrics) == 0 {
		return nil
	}

	matched := make(map[string][]proxy.MetricSummary)
	var unmatched []proxy.MetricSummary

	for _, m := range metrics {
		prefix := detectPrefix(m.Name)
		if prefix != "" {
			matched[prefix] = append(matched[prefix], m)
		} else {
			unmatched = append(unmatched, m)
		}
	}

	// Sort prefixes for stable output order across refreshes.
	prefixes := make([]string, 0, len(matched))
	for p := range matched {
		prefixes = append(prefixes, p)
	}
	slices.Sort(prefixes)

	var lines []string
	for _, prefix := range prefixes {
		ms := matched[prefix]
		if fn, ok := registry[prefix]; ok {
			lines = append(lines, fn(agent, ms)...)
		} else {
			unmatched = append(unmatched, ms...)
		}
	}
	if len(unmatched) > 0 {
		lines = append(lines, formatGeneric(agent, unmatched)...)
	}
	return lines
}

// detectPrefix returns the first registered prefix matching name. If multiple
// prefixes overlap (e.g. "foo." and "foo.bar."), the match is arbitrary — keep
// registry prefixes non-overlapping.
func detectPrefix(name string) string {
	for prefix := range registry {
		if strings.HasPrefix(name, prefix) {
			return prefix
		}
	}
	return ""
}
