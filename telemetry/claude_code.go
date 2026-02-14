package telemetry

import (
	"fmt"
	"strings"

	"github.com/bernd/vibepit/proxy"
)

func formatClaudeCode(_ string, metrics []proxy.MetricSummary) []string {
	var (
		model                                            string
		cost                                             float64
		tokInput, tokOutput, tokCacheRead, tokCacheWrite float64
		timeUser, timeCLI                                float64
		sessions                                         float64
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
