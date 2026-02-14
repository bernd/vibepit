# Metric Provider Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add agent-specific metric formatting so the Metrics tab shows human-readable summaries instead of raw name:value pairs.

**Architecture:** New `telemetry/` package with a prefix→formatter function registry. `FormatAgent()` dispatches to the right formatter based on metric name prefix, falling back to generic rendering. The metrics screen calls this instead of building lines itself.

**Tech Stack:** Go, `github.com/bernd/vibepit/proxy` (MetricSummary type)

---

### Task 1: Generic formatter with FormatAgent dispatcher

**Files:**
- Create: `telemetry/format.go`
- Create: `telemetry/generic.go`
- Create: `telemetry/format_test.go`

**Step 1: Write the failing test**

Create `telemetry/format_test.go`:

```go
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
```

**Step 2: Run test to verify it fails**

Run: `go test ./telemetry/ -run TestFormatAgent -v`
Expected: FAIL — package does not exist yet.

**Step 3: Write minimal implementation**

Create `telemetry/format.go`:

```go
package telemetry

import (
	"strings"

	"github.com/bernd/vibepit/proxy"
)

// MetricFormatter formats an agent's metrics into plain-text display lines.
type MetricFormatter func(agent string, metrics []proxy.MetricSummary) []string

var registry = map[string]MetricFormatter{}

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
```

Create `telemetry/generic.go`:

```go
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
```

**Step 4: Run tests to verify they pass**

Run: `go test ./telemetry/ -run TestFormatAgent -v`
Expected: PASS

**Step 5: Commit**

```bash
git add telemetry/format.go telemetry/generic.go telemetry/format_test.go
git commit -m "feat: add telemetry package with generic metric formatter"
```

---

### Task 2: Claude Code metric formatter

**Files:**
- Create: `telemetry/claude_code.go`
- Modify: `telemetry/format.go` (add registry entry)
- Modify: `telemetry/format_test.go` (add tests)

**Step 1: Write the failing test**

Add to `telemetry/format_test.go`:

```go
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

	t.Run("shows model", func(t *testing.T) {
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
```

**Step 2: Run test to verify it fails**

Run: `go test ./telemetry/ -run TestFormatAgent_ClaudeCode -v`
Expected: FAIL — no claude_code formatter registered, falls back to generic.

**Step 3: Write implementation**

Add to `telemetry/format.go` registry:

```go
var registry = map[string]MetricFormatter{
	"claude_code.": formatClaudeCode,
}
```

Create `telemetry/claude_code.go`:

```go
package telemetry

import (
	"fmt"
	"strings"

	"github.com/bernd/vibepit/proxy"
)

func formatClaudeCode(_ string, metrics []proxy.MetricSummary) []string {
	var (
		model                                          string
		cost                                           float64
		tokInput, tokOutput, tokCacheRead, tokCacheWrite float64
		timeUser, timeCLI                              float64
		sessions                                       float64
	)

	for _, m := range metrics {
		if model == "" {
			if v, ok := m.Attributes["model"]; ok {
				model = v
			}
		}
		switch m.Name {
		case "claude_code.cost.usage":
			cost += m.Value
		case "claude_code.token.usage":
			switch m.Attributes["type"] {
			case "input":
				tokInput += m.Value
			case "output":
				tokOutput += m.Value
			case "cacheRead":
				tokCacheRead += m.Value
			case "cacheCreation":
				tokCacheWrite += m.Value
			}
		case "claude_code.active_time.total":
			switch m.Attributes["type"] {
			case "user":
				timeUser += m.Value
			case "cli":
				timeCLI += m.Value
			}
		case "claude_code.session.count":
			sessions += m.Value
		}
	}

	var lines []string

	if model != "" {
		lines = append(lines, fmt.Sprintf("  Model:        %s", model))
	}
	if cost > 0 {
		lines = append(lines, fmt.Sprintf("  Cost:         $%.4g", cost))
	}
	if tokInput > 0 || tokOutput > 0 || tokCacheRead > 0 || tokCacheWrite > 0 {
		var parts []string
		if tokInput > 0 {
			parts = append(parts, fmt.Sprintf("%g input", tokInput))
		}
		if tokOutput > 0 {
			parts = append(parts, fmt.Sprintf("%g output", tokOutput))
		}
		if tokCacheRead > 0 {
			parts = append(parts, fmt.Sprintf("%g cache read", tokCacheRead))
		}
		if tokCacheWrite > 0 {
			parts = append(parts, fmt.Sprintf("%g cache write", tokCacheWrite))
		}
		lines = append(lines, fmt.Sprintf("  Tokens:       %s", strings.Join(parts, "  ")))
	}
	if timeUser > 0 || timeCLI > 0 {
		var parts []string
		if timeUser > 0 {
			parts = append(parts, fmt.Sprintf("%.4gs user", timeUser))
		}
		if timeCLI > 0 {
			parts = append(parts, fmt.Sprintf("%.4gs cli", timeCLI))
		}
		lines = append(lines, fmt.Sprintf("  Active time:  %s", strings.Join(parts, "  ")))
	}
	if sessions > 0 {
		lines = append(lines, fmt.Sprintf("  Sessions:     %g", sessions))
	}

	return lines
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./telemetry/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add telemetry/claude_code.go telemetry/format.go telemetry/format_test.go
git commit -m "feat: add Claude Code metric formatter"
```

---

### Task 3: Codex stub formatter

**Files:**
- Create: `telemetry/codex.go`
- Modify: `telemetry/format.go` (add registry entry)
- Modify: `telemetry/format_test.go` (add test)

**Step 1: Write the failing test**

Add to `telemetry/format_test.go`:

```go
func TestFormatAgent_Codex_FallsBackToGeneric(t *testing.T) {
	metrics := []proxy.MetricSummary{
		{Name: "codex.api_request", Agent: "codex", Value: 10},
	}
	lines := FormatAgent("codex", metrics)
	assert.Contains(t, lines, "  codex.api_request: 10")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./telemetry/ -run TestFormatAgent_Codex -v`
Expected: FAIL — no codex formatter registered.

**Step 3: Write implementation**

Create `telemetry/codex.go`:

```go
package telemetry

import "github.com/bernd/vibepit/proxy"

// formatCodex is a stub that falls through to generic formatting.
// Codex currently emits events rather than aggregated metrics.
func formatCodex(agent string, metrics []proxy.MetricSummary) []string {
	return formatGeneric(agent, metrics)
}
```

Add to registry in `telemetry/format.go`:

```go
var registry = map[string]MetricFormatter{
	"claude_code.": formatClaudeCode,
	"codex.":       formatCodex,
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./telemetry/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add telemetry/codex.go telemetry/format.go telemetry/format_test.go
git commit -m "feat: add Codex metric formatter stub"
```

---

### Task 4: Wire telemetry package into metrics screen

**Files:**
- Modify: `cmd/monitor_metrics.go:75-104` (rebuildLines)
- Modify: `cmd/monitor_metrics_test.go` (update expectations)

**Step 1: Update tests for new formatted output**

In `cmd/monitor_metrics_test.go`, update `TestMetricsScreen_View`:

The test uses `sampleMetrics()` which returns `token_usage` and `api_calls` —
these don't match any registered prefix, so they'll use generic formatting.
The existing test assertions (`Contains "token_usage(input)"`, `Contains "1234"`)
should still pass because generic format produces the same output.

Verify existing tests still pass before changing anything:

Run: `go test ./cmd/ -run TestMetricsScreen -v`
Expected: PASS

**Step 2: Modify rebuildLines to use telemetry.FormatAgent**

In `cmd/monitor_metrics.go`, replace `rebuildLines()` (lines 75-104):

```go
func (s *metricsScreen) rebuildLines() {
	byAgent := make(map[string][]proxy.MetricSummary)
	for _, m := range s.summaries {
		if s.filter.active != "" && m.Agent != s.filter.active {
			continue
		}
		byAgent[m.Agent] = append(byAgent[m.Agent], m)
	}

	s.lines = nil
	for _, agent := range s.filter.agents {
		metrics, ok := byAgent[agent]
		if !ok {
			continue
		}
		s.lines = append(s.lines, metricsLine{isAgent: true, text: agent})
		for _, line := range telemetry.FormatAgent(agent, metrics) {
			s.lines = append(s.lines, metricsLine{text: line})
		}
	}
	s.cursor.ItemCount = len(s.lines)
	s.cursor.EnsureVisible()
}
```

Add import: `"github.com/bernd/vibepit/telemetry"`

Remove import if no longer needed: `"fmt"`

**Step 3: Run all tests**

Run: `go test ./cmd/ ./telemetry/ -v`
Expected: PASS

**Step 4: Commit**

```bash
git add cmd/monitor_metrics.go
git commit -m "feat: wire telemetry.FormatAgent into metrics screen"
```

---

### Task 5: Final verification

**Step 1: Run full test suite**

Run: `make test`
Expected: PASS

**Step 2: Run integration tests if available**

Run: `make test-integration`
Expected: PASS (or skip if no container runtime)

**Step 3: Commit design doc**

```bash
git add docs/plans/2026-02-14-metric-provider-design.md docs/plans/2026-02-14-metric-provider-plan.md
git commit -m "docs: add metric provider design and implementation plan"
```
