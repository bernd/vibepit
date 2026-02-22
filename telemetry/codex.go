package telemetry

import (
	"fmt"
	"strings"

	"github.com/bernd/vibepit/proxy"
)

func formatCodex(_ string, metrics []proxy.MetricSummary) []string {
	var tokInput, tokOutput, tokCached, tokReasoning float64

	type modelTokens struct {
		input, output, cached, reasoning float64
	}
	modelTok := map[string]*modelTokens{}

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

	// KPI line.
	totalRequests := 0.0
	for _, c := range apiCount {
		totalRequests += c
	}
	if totalRequests > 0 {
		lines = append(lines, fmt.Sprintf("  Requests: %.0f", totalRequests))
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
		lines = append(lines, "")
		lines = append(lines, "  Models")
		for _, model := range models {
			count := apiCount[model]
			avgMs := apiDuration[model] / count
			lines = append(lines, fmt.Sprintf("    %-*s  %3.0f req   avg %5.0fms", nameW, model, count, avgMs))
		}
	}

	// Efficiency section.
	if len(modelTok) > 0 {
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
			lines = append(lines, "  Efficiency")
			lines = append(lines, "    Cache hit: "+strings.Join(cacheParts, "  "))
		}
	}

	// Latency section.
	if len(eventCount) > 0 {
		types := sortedKeys(eventCount)
		nameW := maxLen(types)
		lines = append(lines, "  Latency")
		for _, typ := range types {
			count := eventCount[typ]
			avgMs := eventDuration[typ] / count
			lines = append(lines, fmt.Sprintf("    %-*s  %3.0f calls   avg %5.0fms", nameW, typ, count, avgMs))
		}
	}

	// Tools section.
	if len(toolCount) > 0 {
		tools := sortedKeys(toolCount)
		nameW := maxLen(tools)
		lines = append(lines, "  Tools")
		for _, tool := range tools {
			count := toolCount[tool]
			avgMs := toolDuration[tool] / count
			lines = append(lines, fmt.Sprintf("    %-*s  %3.0f calls   avg %5.0fms", nameW, tool, count, avgMs))
		}
	}

	return lines
}
