package telemetry

import "github.com/bernd/vibepit/proxy"

// formatCodex is a stub that falls through to generic formatting.
// Codex currently emits events rather than aggregated metrics.
func formatCodex(agent string, metrics []proxy.MetricSummary) []string {
	return formatGeneric(agent, metrics)
}
