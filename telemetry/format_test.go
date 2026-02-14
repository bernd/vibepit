package telemetry

import (
	"testing"

	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
)

func TestFormatAgent_Generic(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "unknown.requests", Agent: "myagent", Value: 42},
		{Name: "unknown.errors", Agent: "myagent", Value: 3, Attributes: map[string]string{"type": "timeout"}},
	}
	lines := FormatAgent("myagent", metrics)
	assert.Contains(t, lines, "  unknown.requests: 42")
	assert.Contains(t, lines, "  unknown.errors(timeout): 3")
}

func TestFormatAgent_EmptyMetrics(t *testing.T) {
	lines := FormatAgent("myagent", nil)
	assert.Empty(t, lines)
}
