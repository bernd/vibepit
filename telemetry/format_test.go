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
		{Name: "api.count", Agent: "claude-code", Value: 5, IsDelta: true, Attributes: map[string]string{"model": "claude-haiku-4-5-20251001"}},
		{Name: "api.duration", Agent: "claude-code", Value: 3500, IsDelta: true, Attributes: map[string]string{"model": "claude-haiku-4-5-20251001"}},
	}
	lines := FormatAgent("claude-code", metrics)
	joined := strings.Join(lines, "\n")

	t.Run("shows cost and requests", func(t *testing.T) {
		assert.Contains(t, joined, "$0.0621")
		assert.Contains(t, joined, "Requests: 5")
	})
	t.Run("shows tokens", func(t *testing.T) {
		assert.Contains(t, joined, "4 in")
		assert.Contains(t, joined, "524 out")
		assert.Contains(t, joined, "cache read")
		assert.Contains(t, joined, "cache write")
	})
	t.Run("shows active time", func(t *testing.T) {
		assert.Contains(t, joined, "29.0s user")
		assert.Contains(t, joined, "12.7s cli")
	})
	t.Run("shows models section", func(t *testing.T) {
		assert.Contains(t, joined, "Models")
		assert.Contains(t, joined, "claude-haiku-4-5-20251001")
		assert.Contains(t, joined, "5 req")
		assert.Contains(t, joined, "avg")
	})
	t.Run("shows efficiency section", func(t *testing.T) {
		assert.Contains(t, joined, "Efficiency")
		assert.Contains(t, joined, "Cost/request")
		assert.Contains(t, joined, "Cache hit")
	})
}

func TestFormatAgent_ClaudeCode_CostOnly(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "claude_code.cost.usage", Agent: "claude-code", Value: 0.05},
	}
	lines := FormatAgent("claude-code", metrics)
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "Cost: $0.0500")
}

func TestDisplayName(t *testing.T) {
	t.Run("known prefix returns display name", func(t *testing.T) {
		metrics := []proxy.MetricSummary{
			{Name: "claude_code.cost.usage", Agent: "claude-code"},
		}
		assert.Equal(t, "Claude Code", DisplayName("claude-code", metrics))
	})
	t.Run("includes version when app.version present", func(t *testing.T) {
		metrics := []proxy.MetricSummary{
			{Name: "claude_code.cost.usage", Agent: "claude-code", Attributes: map[string]string{"app.version": "2.1.50"}},
		}
		assert.Equal(t, "Claude Code v2.1.50", DisplayName("claude-code", metrics))
	})
	t.Run("unknown prefix returns raw agent without version", func(t *testing.T) {
		metrics := []proxy.MetricSummary{
			{Name: "unknown.metric", Agent: "myagent", Attributes: map[string]string{"app.version": "1.0"}},
		}
		assert.Equal(t, "myagent", DisplayName("myagent", metrics))
	})
	t.Run("empty metrics returns raw agent", func(t *testing.T) {
		assert.Equal(t, "myagent", DisplayName("myagent", nil))
	})
}

func TestFormatAgent_Codex_UsesCodexFormatter(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "codex.token.input", Agent: "codex", Value: 1000, Attributes: map[string]string{"model": "o3"}},
		{Name: "codex.token.output", Agent: "codex", Value: 500, Attributes: map[string]string{"model": "o3"}},
		{Name: "api.count", Agent: "codex", Value: 5, Attributes: map[string]string{"model": "o3"}},
		{Name: "api.duration", Agent: "codex", Value: 10000, Attributes: map[string]string{"model": "o3"}},
	}
	lines := FormatAgent("codex", metrics)
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "Requests: 5")
	assert.Contains(t, joined, "1000 in")
}
