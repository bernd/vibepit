package telemetry

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bernd/vibepit/proxy"
)

func formatClaudeCode(_ string, metrics []proxy.MetricSummary) []string {
	var (
		cost                                             float64
		tokInput, tokOutput, tokCacheRead, tokCacheWrite float64
		timeUser, timeCLI                                float64
	)

	// Per-model cost from claude_code.cost.usage {model}.
	modelCost := map[string]float64{}

	// Per-model tokens from claude_code.token.usage {model, type}.
	type modelTokens struct {
		input, output, cacheRead, cacheWrite float64
	}
	modelTok := map[string]*modelTokens{}

	apiCount := map[string]float64{}
	apiDuration := map[string]float64{}
	eventCount := map[string]float64{}
	eventDuration := map[string]float64{}
	toolCount := map[string]float64{}
	toolDuration := map[string]float64{}
	toolSize := map[string]float64{}
	toolSizeMax := map[string]float64{}

	for _, m := range metrics {
		model := m.Attributes["model"]
		typ := m.Attributes["type"]

		switch m.Name {
		case "claude_code.cost.usage":
			cost += m.Value
			if model != "" {
				modelCost[model] += m.Value
			}
		case "claude_code.token.usage":
			switch typ {
			case "input":
				tokInput += m.Value
			case "output":
				tokOutput += m.Value
			case "cacheRead":
				tokCacheRead += m.Value
			case "cacheCreation":
				tokCacheWrite += m.Value
			}
			if model != "" {
				mt := modelTok[model]
				if mt == nil {
					mt = &modelTokens{}
					modelTok[model] = mt
				}
				switch typ {
				case "input":
					mt.input += m.Value
				case "output":
					mt.output += m.Value
				case "cacheRead":
					mt.cacheRead += m.Value
				case "cacheCreation":
					mt.cacheWrite += m.Value
				}
			}
		case "claude_code.active_time.total":
			switch typ {
			case "user":
				timeUser += m.Value
			case "cli":
				timeCLI += m.Value
			}
		case "claude_code.api.count":
			apiCount[model] += m.Value
		case "claude_code.api.duration":
			apiDuration[model] += m.Value
		case "claude_code.event_type.count":
			eventCount[typ] += m.Value
		case "claude_code.event_type.duration":
			eventDuration[typ] += m.Value
		case "claude_code.tool.count":
			toolCount[typ] += m.Value
		case "claude_code.tool.duration":
			toolDuration[typ] += m.Value
		case "claude_code.tool.result_size":
			toolSize[typ] += m.Value
		case "claude_code.tool.result_size_max":
			if m.Value > toolSizeMax[typ] {
				toolSizeMax[typ] = m.Value
			}
		}
	}

	var lines []string

	// KPI line: cost, requests, active time.
	totalRequests := 0.0
	for _, c := range apiCount {
		totalRequests += c
	}
	var kpiParts []string
	if cost > 0 {
		kpiParts = append(kpiParts, fmt.Sprintf("Cost: $%.4f", cost))
	}
	if totalRequests > 0 {
		kpiParts = append(kpiParts, fmt.Sprintf("Requests: %.0f", totalRequests))
	}
	if timeUser > 0 || timeCLI > 0 {
		var timeParts []string
		if timeUser > 0 {
			timeParts = append(timeParts, fmt.Sprintf("%.1fs user", timeUser))
		}
		if timeCLI > 0 {
			timeParts = append(timeParts, fmt.Sprintf("%.1fs cli", timeCLI))
		}
		kpiParts = append(kpiParts, "Active time: "+strings.Join(timeParts, "  "))
	}
	if len(kpiParts) > 0 {
		lines = append(lines, "  "+strings.Join(kpiParts, "   "))
	}

	// Tokens line.
	if tokInput > 0 || tokOutput > 0 || tokCacheRead > 0 || tokCacheWrite > 0 {
		var parts []string
		if tokInput > 0 {
			parts = append(parts, fmt.Sprintf("%.0f in", tokInput))
		}
		if tokOutput > 0 {
			parts = append(parts, fmt.Sprintf("%.0f out", tokOutput))
		}
		if tokCacheRead > 0 {
			parts = append(parts, fmt.Sprintf("%.0f cache read", tokCacheRead))
		}
		if tokCacheWrite > 0 {
			parts = append(parts, fmt.Sprintf("%.0f cache write", tokCacheWrite))
		}
		lines = append(lines, "  Tokens: "+strings.Join(parts, "  "))
	}

	// Models section.
	lines = append(lines, renderModelsSection(apiCount, apiDuration, modelCost, nil)...)

	// Efficiency section.
	if totalRequests > 0 {
		lines = append(lines, "  Efficiency")
		costPerReq := cost / totalRequests
		var effParts []string
		effParts = append(effParts, fmt.Sprintf("Cost/request: $%.4f", costPerReq))
		if tokOutput > 0 {
			costPer1k := cost / tokOutput * 1000
			effParts = append(effParts, fmt.Sprintf("Cost/1k output: $%.2f", costPer1k))
		}
		lines = append(lines, "    "+strings.Join(effParts, "   "))

		// Cache hit ratio per model.
		var cacheParts []string
		models := sortedKeys(apiCount)
		for _, model := range models {
			mt := modelTok[model]
			if mt == nil {
				continue
			}
			total := mt.cacheRead + mt.input
			if total > 0 {
				pct := mt.cacheRead / total * 100
				cacheParts = append(cacheParts, fmt.Sprintf("%.0f%% (%s)", pct, ShortModelName(model)))
			}
		}
		if len(cacheParts) > 0 {
			lines = append(lines, "    Cache hit: "+strings.Join(cacheParts, "  "))
		}
	}

	// Latency section (event types).
	lines = append(lines, renderLatencySection(eventCount, eventDuration)...)

	// Tools section.
	lines = append(lines, renderToolsSection(toolCount, toolDuration, toolSize, toolSizeMax)...)

	return lines
}

func maxLen(ss []string) int {
	n := 0
	for _, s := range ss {
		if len(s) > n {
			n = len(s)
		}
	}
	return n
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// countWidth returns the minimum field width needed to display the largest
// count value, with a floor of 3 to keep small tables tidy.
func countWidth(m map[string]float64) int {
	w := 3
	for _, v := range m {
		n := len(fmt.Sprintf("%.0f", v))
		if n > w {
			w = n
		}
	}
	return w
}

// ShortModelName extracts a short display name from a full model identifier.
// e.g. "claude-opus-4-6" -> "opus", "claude-haiku-4-5-20251001" -> "haiku".
func ShortModelName(model string) string {
	for _, name := range []string{"opus", "sonnet", "haiku"} {
		if strings.Contains(model, name) {
			return name
		}
	}
	return model
}
