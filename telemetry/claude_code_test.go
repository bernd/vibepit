package telemetry

import (
	"strings"
	"testing"

	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
)

func TestFormatClaudeCode_ModelsSection(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "claude_code.api.count", Agent: "cc", Value: 12, Attributes: map[string]string{"model": "claude-opus-4-6"}},
		{Name: "claude_code.api.duration", Agent: "cc", Value: 47208, Attributes: map[string]string{"model": "claude-opus-4-6"}},
		{Name: "claude_code.api.count", Agent: "cc", Value: 22, Attributes: map[string]string{"model": "claude-haiku-4-5"}},
		{Name: "claude_code.api.duration", Agent: "cc", Value: 17534, Attributes: map[string]string{"model": "claude-haiku-4-5"}},
		{Name: "claude_code.cost.usage", Agent: "cc", Value: 0.3421, Attributes: map[string]string{"model": "claude-opus-4-6"}},
		{Name: "claude_code.cost.usage", Agent: "cc", Value: 0.0111, Attributes: map[string]string{"model": "claude-haiku-4-5"}},
	}
	lines := formatClaudeCode("cc", metrics)
	joined := strings.Join(lines, "\n")

	assert.Contains(t, joined, "Models")
	assert.Contains(t, joined, "claude-opus-4-6")
	assert.Contains(t, joined, "12 req")
	assert.Contains(t, joined, "claude-haiku-4-5")
	assert.Contains(t, joined, "22 req")
	assert.Contains(t, joined, "$0.3421")
	assert.Contains(t, joined, "$0.0111")
}

func TestFormatClaudeCode_ToolsSection(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "claude_code.tool.count", Agent: "cc", Value: 5, Attributes: map[string]string{"type": "Bash"}},
		{Name: "claude_code.tool.duration", Agent: "cc", Value: 600, Attributes: map[string]string{"type": "Bash"}},
		{Name: "claude_code.tool.result_size", Agent: "cc", Value: 1000, Attributes: map[string]string{"type": "Bash"}},
		{Name: "claude_code.tool.result_size_max", Agent: "cc", Value: 510, Attributes: map[string]string{"type": "Bash"}},
	}
	lines := formatClaudeCode("cc", metrics)
	joined := strings.Join(lines, "\n")

	assert.Contains(t, joined, "Tools")
	assert.Contains(t, joined, "Bash")
	assert.Contains(t, joined, "5 calls")
	assert.Contains(t, joined, "avg   120ms")
	assert.Contains(t, joined, "max   510B")
}

func TestFormatClaudeCode_EfficiencySection(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "claude_code.cost.usage", Agent: "cc", Value: 0.34, Attributes: map[string]string{"model": "claude-opus-4-6"}},
		{Name: "claude_code.token.usage", Agent: "cc", Value: 100, Attributes: map[string]string{"type": "input", "model": "claude-opus-4-6"}},
		{Name: "claude_code.token.usage", Agent: "cc", Value: 2000, Attributes: map[string]string{"type": "output", "model": "claude-opus-4-6"}},
		{Name: "claude_code.token.usage", Agent: "cc", Value: 9900, Attributes: map[string]string{"type": "cacheRead", "model": "claude-opus-4-6"}},
		{Name: "claude_code.api.count", Agent: "cc", Value: 10, Attributes: map[string]string{"model": "claude-opus-4-6"}},
	}
	lines := formatClaudeCode("cc", metrics)
	joined := strings.Join(lines, "\n")

	assert.Contains(t, joined, "Efficiency")
	assert.Contains(t, joined, "Cost/request: $0.0340")
	assert.Contains(t, joined, "Cost/1k output: $0.17")
	assert.Contains(t, joined, "Cache hit: 99%")
	assert.Contains(t, joined, "(opus)")
}

func TestFormatClaudeCode_LatencySection(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "claude_code.event_type.count", Agent: "cc", Value: 34, Attributes: map[string]string{"type": "api_request"}},
		{Name: "claude_code.event_type.duration", Agent: "cc", Value: 68000, Attributes: map[string]string{"type": "api_request"}},
	}
	lines := formatClaudeCode("cc", metrics)
	joined := strings.Join(lines, "\n")

	assert.Contains(t, joined, "Latency")
	assert.Contains(t, joined, "api_request")
	assert.Contains(t, joined, "34 calls")
	assert.Contains(t, joined, "avg  2000ms")
}

func TestShortModelName(t *testing.T) {
	assert.Equal(t, "opus", ShortModelName("claude-opus-4-6"))
	assert.Equal(t, "haiku", ShortModelName("claude-haiku-4-5-20251001"))
	assert.Equal(t, "sonnet", ShortModelName("claude-sonnet-4-6"))
	assert.Equal(t, "unknown-model", ShortModelName("unknown-model"))
}
