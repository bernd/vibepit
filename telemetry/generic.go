package telemetry

import (
	"fmt"

	"github.com/bernd/vibepit/proxy"
)

func formatGeneric(_ string, metrics []proxy.MetricSummary) []string {
	var lines []string
	for _, m := range metrics {
		label := m.Name
		if t, ok := m.Attributes["type"]; ok {
			label += "(" + t + ")"
		}
		lines = append(lines, fmt.Sprintf("  %s: %g", label, m.Value))
	}
	return lines
}
