package telemetry

import (
	"strings"

	"github.com/bernd/vibepit/proxy"
)

// MetricFormatter formats an agent's metrics into plain-text display lines.
type MetricFormatter func(agent string, metrics []proxy.MetricSummary) []string

var registry = map[string]MetricFormatter{
	"claude_code.": formatClaudeCode,
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

	var lines []string
	for prefix, ms := range matched {
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

func detectPrefix(name string) string {
	for prefix := range registry {
		if strings.HasPrefix(name, prefix) {
			return prefix
		}
	}
	return ""
}
