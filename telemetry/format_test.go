package telemetry

import (
	"strings"
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

func TestFormatAgent_ClaudeCode(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "claude_code.cost.usage", Agent: "claude-code", Value: 0.0621, Attributes: map[string]string{"model": "claude-haiku-4-5-20251001"}},
		{Name: "claude_code.token.usage", Agent: "claude-code", Value: 4, Attributes: map[string]string{"type": "input", "model": "claude-haiku-4-5-20251001"}},
		{Name: "claude_code.token.usage", Agent: "claude-code", Value: 524, Attributes: map[string]string{"type": "output", "model": "claude-haiku-4-5-20251001"}},
		{Name: "claude_code.token.usage", Agent: "claude-code", Value: 40374, Attributes: map[string]string{"type": "cacheRead", "model": "claude-haiku-4-5-20251001"}},
		{Name: "claude_code.token.usage", Agent: "claude-code", Value: 4606, Attributes: map[string]string{"type": "cacheCreation", "model": "claude-haiku-4-5-20251001"}},
		{Name: "claude_code.active_time.total", Agent: "claude-code", Value: 29.0, Attributes: map[string]string{"type": "user"}},
		{Name: "claude_code.active_time.total", Agent: "claude-code", Value: 12.7, Attributes: map[string]string{"type": "cli"}},
		{Name: "claude_code.session.count", Agent: "claude-code", Value: 1},
	}
	lines := FormatAgent("claude-code", metrics)

	t.Run("shows models", func(t *testing.T) {
		assert.Contains(t, lines[0], "Models:")
		assert.Contains(t, lines[0], "claude-haiku-4-5-20251001")
	})
	t.Run("shows cost", func(t *testing.T) {
		assert.Contains(t, strings.Join(lines, "\n"), "$0.0621")
	})
	t.Run("shows tokens", func(t *testing.T) {
		joined := strings.Join(lines, "\n")
		assert.Contains(t, joined, "input")
		assert.Contains(t, joined, "output")
		assert.Contains(t, joined, "cache read")
		assert.Contains(t, joined, "cache write")
	})
	t.Run("shows active time", func(t *testing.T) {
		joined := strings.Join(lines, "\n")
		assert.Contains(t, joined, "29")
		assert.Contains(t, joined, "12.7")
	})
	t.Run("shows sessions", func(t *testing.T) {
		assert.Contains(t, strings.Join(lines, "\n"), "1")
	})
}

func TestFormatAgent_ClaudeCode_ZeroValuesSkipped(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "claude_code.cost.usage", Agent: "claude-code", Value: 0.05},
		{Name: "claude_code.session.count", Agent: "claude-code", Value: 0},
	}
	lines := FormatAgent("claude-code", metrics)
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "Cost")
	assert.NotContains(t, joined, "Sessions")
}

func TestDisplayName(t *testing.T) {
	t.Run("known prefix returns display name", func(t *testing.T) {
		metrics := []proxy.MetricSummary{
			{Name: "claude_code.cost.usage", Agent: "claude-code"},
		}
		assert.Equal(t, "Claude Code", DisplayName("claude-code", metrics))
	})
	t.Run("unknown prefix returns raw agent", func(t *testing.T) {
		metrics := []proxy.MetricSummary{
			{Name: "unknown.metric", Agent: "myagent"},
		}
		assert.Equal(t, "myagent", DisplayName("myagent", metrics))
	})
	t.Run("empty metrics returns raw agent", func(t *testing.T) {
		assert.Equal(t, "myagent", DisplayName("myagent", nil))
	})
}

func TestFormatAgent_Codex_FallsBackToGeneric(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "codex.api_request", Agent: "codex", Value: 10},
	}
	lines := FormatAgent("codex", metrics)
	assert.Contains(t, lines, "  codex.api_request: 10")
}
