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

var displayNames = map[string]string{
	"claude_code.": "Claude Code",
	"codex.":       "Codex",
}

// DisplayName returns a human-friendly name for the agent based on its metric
// prefixes. Falls back to the raw agent identifier.
func DisplayName(agent string, metrics []proxy.MetricSummary) string {
	var name string
	for _, m := range metrics {
		if prefix := detectPrefix(m.Name); prefix != "" {
			if n, ok := displayNames[prefix]; ok {
				name = n
				break
			}
		}
	}
	if name == "" {
		return agent
	}
	for _, m := range metrics {
		if v := m.Attributes["app.version"]; v != "" {
			return name + " v" + v
		}
	}
	return name
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

	// Stable output order across refreshes.
	prefixes := make([]string, 0, len(matched))
	for p := range matched {
		prefixes = append(prefixes, p)
	}
	slices.Sort(prefixes)

	var lines []string
	if len(prefixes) == 1 {
		// Single agent formatter: pass all metrics so it can access derived
		// metrics (api.count, tool.count, etc.) alongside its own prefix.
		fn := registry[prefixes[0]]
		lines = append(lines, fn(agent, metrics)...)
	} else {
		for _, prefix := range prefixes {
			fn := registry[prefix]
			lines = append(lines, fn(agent, matched[prefix])...)
		}
		if len(unmatched) > 0 {
			lines = append(lines, formatGeneric(agent, unmatched)...)
		}
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
