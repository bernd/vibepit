package telemetry

import (
	"strings"
	"testing"

	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
)

func TestFormatCodex_KPIAndTokens(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "codex.api.count", Agent: "codex", Value: 5, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.api.duration", Agent: "codex", Value: 10000, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.token.input", Agent: "codex", Value: 1000, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.token.output", Agent: "codex", Value: 500, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.token.cached", Agent: "codex", Value: 800, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.token.reasoning", Agent: "codex", Value: 200, Attributes: map[string]string{"model": "o3"}},
	}
	lines := formatCodex("codex", metrics)
	joined := strings.Join(lines, "\n")

	assert.Contains(t, joined, "Requests: 5")
	assert.Contains(t, joined, "1000 in")
	assert.Contains(t, joined, "500 out")
	assert.Contains(t, joined, "800 cached")
	assert.Contains(t, joined, "200 reasoning")
}

func TestFormatCodex_ModelsSection(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "codex.api.count", Agent: "codex", Value: 3, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.api.duration", Agent: "codex", Value: 6000, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.api.count", Agent: "codex", Value: 7, Attributes: map[string]string{"model": "o4-mini"}},
		{Name: "codex.api.duration", Agent: "codex", Value: 3500, Attributes: map[string]string{"model": "o4-mini"}},
	}
	lines := formatCodex("codex", metrics)
	joined := strings.Join(lines, "\n")

	assert.Contains(t, joined, "Models")
	assert.Contains(t, joined, "o3")
	assert.Contains(t, joined, "3 req")
	assert.Contains(t, joined, "o4-mini")
	assert.Contains(t, joined, "7 req")
}

func TestFormatCodex_Efficiency(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "codex.api.count", Agent: "codex", Value: 10, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.token.input", Agent: "codex", Value: 100, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.token.cached", Agent: "codex", Value: 900, Attributes: map[string]string{"model": "o3"}},
	}
	lines := formatCodex("codex", metrics)
	joined := strings.Join(lines, "\n")

	assert.Contains(t, joined, "Efficiency")
	assert.Contains(t, joined, "Cache hit: 90%")
	assert.Contains(t, joined, "(o3)")
}

func TestFormatCodex_CostInKPI(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "codex.cost.usage", Agent: "codex", Value: 0.0123, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.api.count", Agent: "codex", Value: 5, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.api.duration", Agent: "codex", Value: 10000, Attributes: map[string]string{"model": "o3"}},
	}
	lines := formatCodex("codex", metrics)
	joined := strings.Join(lines, "\n")

	assert.Contains(t, joined, "Cost: $0.0123")
	assert.Contains(t, joined, "Requests: 5")
}

func TestFormatCodex_CostPerModel(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "codex.cost.usage", Agent: "codex", Value: 0.05, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.cost.usage", Agent: "codex", Value: 0.02, Attributes: map[string]string{"model": "o4-mini"}},
		{Name: "codex.api.count", Agent: "codex", Value: 3, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.api.duration", Agent: "codex", Value: 6000, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.api.count", Agent: "codex", Value: 7, Attributes: map[string]string{"model": "o4-mini"}},
		{Name: "codex.api.duration", Agent: "codex", Value: 3500, Attributes: map[string]string{"model": "o4-mini"}},
	}
	lines := formatCodex("codex", metrics)
	joined := strings.Join(lines, "\n")

	assert.Contains(t, joined, "$0.0500")
	assert.Contains(t, joined, "$0.0200")
	// Exact matches should not show pricing source note.
	assert.NotContains(t, joined, "priced as")
}

func TestFormatCodex_CostFallbackNote(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "codex.cost.usage", Agent: "codex", Value: 0.05, Attributes: map[string]string{"model": "gpt-5.3-codex"}},
		{Name: "codex.api.count", Agent: "codex", Value: 3, Attributes: map[string]string{"model": "gpt-5.3-codex"}},
		{Name: "codex.api.duration", Agent: "codex", Value: 6000, Attributes: map[string]string{"model": "gpt-5.3-codex"}},
	}
	lines := formatCodex("codex", metrics)
	joined := strings.Join(lines, "\n")

	assert.Contains(t, joined, "priced as gpt-5.2-codex")
}

func TestFormatCodex_CostPerRequest(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "codex.cost.usage", Agent: "codex", Value: 0.10, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.api.count", Agent: "codex", Value: 5, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.token.input", Agent: "codex", Value: 1000, Attributes: map[string]string{"model": "o3"}},
	}
	lines := formatCodex("codex", metrics)
	joined := strings.Join(lines, "\n")

	assert.Contains(t, joined, "Efficiency")
	assert.Contains(t, joined, "Cost/request: $0.0200")
}

func TestFormatCodex_ToolsSection(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "codex.tool.count", Agent: "codex", Value: 4, Attributes: map[string]string{"type": "shell"}},
		{Name: "codex.tool.duration", Agent: "codex", Value: 2000, Attributes: map[string]string{"type": "shell"}},
	}
	lines := formatCodex("codex", metrics)
	joined := strings.Join(lines, "\n")

	assert.Contains(t, joined, "Tools")
	assert.Contains(t, joined, "shell")
	assert.Contains(t, joined, "4 calls")
	assert.Contains(t, joined, "avg   500ms")
}
