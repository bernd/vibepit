package telemetry

import (
	"fmt"
	"strings"

	"github.com/bernd/vibepit/proxy"
)

func formatCodex(_ string, metrics []proxy.MetricSummary) []string {
	var cost float64
	var tokInput, tokOutput, tokCached, tokReasoning float64

	type modelTokens struct {
		input, output, cached, reasoning float64
	}
	modelTok := map[string]*modelTokens{}
	modelCost := map[string]float64{}

	apiCount := map[string]float64{}
	apiDuration := map[string]float64{}

	eventCount := map[string]float64{}
	eventDuration := map[string]float64{}

	toolCount := map[string]float64{}
	toolDuration := map[string]float64{}

	for _, m := range metrics {
		model := m.Attributes["model"]
		typ := m.Attributes["type"]

		switch m.Name {
		case "codex.cost.usage":
			cost += m.Value
			if model != "" {
				modelCost[model] += m.Value
			}
		case "codex.token.input":
			tokInput += m.Value
			if model != "" {
				mt := modelTok[model]
				if mt == nil {
					mt = &modelTokens{}
					modelTok[model] = mt
				}
				mt.input += m.Value
			}
		case "codex.token.output":
			tokOutput += m.Value
			if model != "" {
				mt := modelTok[model]
				if mt == nil {
					mt = &modelTokens{}
					modelTok[model] = mt
				}
				mt.output += m.Value
			}
		case "codex.token.cached":
			tokCached += m.Value
			if model != "" {
				mt := modelTok[model]
				if mt == nil {
					mt = &modelTokens{}
					modelTok[model] = mt
				}
				mt.cached += m.Value
			}
		case "codex.token.reasoning":
			tokReasoning += m.Value
			if model != "" {
				mt := modelTok[model]
				if mt == nil {
					mt = &modelTokens{}
					modelTok[model] = mt
				}
				mt.reasoning += m.Value
			}
		case "api.count":
			apiCount[model] += m.Value
		case "api.duration":
			apiDuration[model] += m.Value
		case "event_type.count":
			eventCount[typ] += m.Value
		case "event_type.duration":
			eventDuration[typ] += m.Value
		case "tool.count":
			toolCount[typ] += m.Value
		case "tool.duration":
			toolDuration[typ] += m.Value
		}
	}

	var lines []string

	// KPI line: cost and requests.
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
	if len(kpiParts) > 0 {
		lines = append(lines, "  "+strings.Join(kpiParts, "   "))
	}

	// Tokens line.
	if tokInput > 0 || tokOutput > 0 || tokCached > 0 || tokReasoning > 0 {
		var parts []string
		if tokInput > 0 {
			parts = append(parts, fmt.Sprintf("%.0f in", tokInput))
		}
		if tokOutput > 0 {
			parts = append(parts, fmt.Sprintf("%.0f out", tokOutput))
		}
		if tokCached > 0 {
			parts = append(parts, fmt.Sprintf("%.0f cached", tokCached))
		}
		if tokReasoning > 0 {
			parts = append(parts, fmt.Sprintf("%.0f reasoning", tokReasoning))
		}
		lines = append(lines, "  Tokens: "+strings.Join(parts, "  "))
	}

	// Models section.
	if len(apiCount) > 0 {
		models := sortedKeys(apiCount)
		nameW := maxLen(models)
		countW := countWidth(apiCount)
		lines = append(lines, "")
		lines = append(lines, "  Models")
		for _, model := range models {
			count := apiCount[model]
			avgMs := apiDuration[model] / count
			costStr := ""
			if c, ok := modelCost[model]; ok {
				costStr = fmt.Sprintf("   $%.4f", c)
				if source, ok := proxy.PricingSource(model); ok && source != model {
					costStr += fmt.Sprintf(" (priced as %s)", source)
				}
			}
			lines = append(lines, fmt.Sprintf("    %-*s  %*.0f req   avg %5.0fms%s", nameW, model, countW, count, avgMs, costStr))
		}
	}

	// Efficiency section.
	if len(modelTok) > 0 {
		lines = append(lines, "  Efficiency")

		if cost > 0 && totalRequests > 0 {
			lines = append(lines, fmt.Sprintf("    Cost/request: $%.4f", cost/totalRequests))
		}

		var cacheParts []string
		models := sortedKeys(apiCount)
		if len(models) == 0 {
			for model := range modelTok {
				models = append(models, model)
			}
		}
		for _, model := range models {
			mt := modelTok[model]
			if mt == nil {
				continue
			}
			total := mt.cached + mt.input
			if total > 0 {
				pct := mt.cached / total * 100
				cacheParts = append(cacheParts, fmt.Sprintf("%.0f%% (%s)", pct, ShortModelName(model)))
			}
		}
		if len(cacheParts) > 0 {
			lines = append(lines, "    Cache hit: "+strings.Join(cacheParts, "  "))
		}
	}

	// Latency section.
	if len(eventCount) > 0 {
		types := sortedKeys(eventCount)
		nameW := maxLen(types)
		countW := countWidth(eventCount)
		lines = append(lines, "  Latency")
		for _, typ := range types {
			count := eventCount[typ]
			avgMs := eventDuration[typ] / count
			lines = append(lines, fmt.Sprintf("    %-*s  %*.0f calls   avg %5.0fms", nameW, typ, countW, count, avgMs))
		}
	}

	// Tools section.
	if len(toolCount) > 0 {
		tools := sortedKeys(toolCount)
		nameW := maxLen(tools)
		countW := countWidth(toolCount)
		lines = append(lines, "  Tools")
		for _, tool := range tools {
			count := toolCount[tool]
			avgMs := toolDuration[tool] / count
			lines = append(lines, fmt.Sprintf("    %-*s  %*.0f calls   avg %5.0fms", nameW, tool, countW, count, avgMs))
		}
	}

	return lines
}
