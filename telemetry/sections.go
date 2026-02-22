package telemetry

import "fmt"

// renderModelsSection renders a Models table with count, avg latency, and
// optional per-model cost.
func renderModelsSection(apiCount, apiDuration, modelCost map[string]float64, costAnnotation func(string) string) []string {
	if len(apiCount) == 0 {
		return nil
	}
	models := sortedKeys(apiCount)
	nameW := maxLen(models)
	countW := countWidth(apiCount)
	var lines []string
	lines = append(lines, "")
	lines = append(lines, "  Models")
	for _, model := range models {
		count := apiCount[model]
		avgMs := apiDuration[model] / count
		costStr := ""
		if c, ok := modelCost[model]; ok {
			costStr = fmt.Sprintf("   $%.4f", c)
			if costAnnotation != nil {
				if note := costAnnotation(model); note != "" {
					costStr += note
				}
			}
		}
		lines = append(lines, fmt.Sprintf("    %-*s  %*.0f req   avg %5.0fms%s", nameW, model, countW, count, avgMs, costStr))
	}
	return lines
}

// renderLatencySection renders a Latency table of event types with count and avg duration.
func renderLatencySection(eventCount, eventDuration map[string]float64) []string {
	if len(eventCount) == 0 {
		return nil
	}
	types := sortedKeys(eventCount)
	nameW := maxLen(types)
	countW := countWidth(eventCount)
	var lines []string
	lines = append(lines, "  Latency")
	for _, typ := range types {
		count := eventCount[typ]
		avgMs := eventDuration[typ] / count
		lines = append(lines, fmt.Sprintf("    %-*s  %*.0f calls   avg %5.0fms", nameW, typ, countW, count, avgMs))
	}
	return lines
}

// renderToolsSection renders a Tools table with count, avg duration, and
// optional result size columns.
func renderToolsSection(toolCount, toolDuration, toolSize, toolSizeMax map[string]float64) []string {
	if len(toolCount) == 0 {
		return nil
	}
	tools := sortedKeys(toolCount)
	nameW := maxLen(tools)
	countW := countWidth(toolCount)
	hasSize := len(toolSize) > 0
	var lines []string
	lines = append(lines, "  Tools")
	for _, tool := range tools {
		count := toolCount[tool]
		avgMs := toolDuration[tool] / count
		if hasSize {
			avgSize := toolSize[tool] / count
			maxSize := toolSizeMax[tool]
			lines = append(lines, fmt.Sprintf("    %-*s  %*.0f calls   avg %5.0fms   avg %5.0fB / max %5.0fB",
				nameW, tool, countW, count, avgMs, avgSize, maxSize))
		} else {
			lines = append(lines, fmt.Sprintf("    %-*s  %*.0f calls   avg %5.0fms", nameW, tool, countW, count, avgMs))
		}
	}
	return lines
}
