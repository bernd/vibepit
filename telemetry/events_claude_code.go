package telemetry

import (
	"fmt"
	"strconv"

	"github.com/bernd/vibepit/proxy"
)

type claudeCodeEventRenderer struct{}

func (claudeCodeEventRenderer) RenderLine(e proxy.TelemetryEvent) []EventSpan {
	switch e.EventName {
	case "tool_result":
		return renderToolResult(e)
	case "api_request":
		return renderAPIRequest(e)
	case "tool_decision":
		return renderToolDecision(e)
	default:
		return renderDefaultLine(e)
	}
}

func (claudeCodeEventRenderer) RenderDetails(e proxy.TelemetryEvent) [][]EventSpan {
	shown := map[string]bool{}
	var expandKeys []string
	switch e.EventName {
	case "tool_result":
		shown["tool_name"] = true
		shown["success"] = true
		shown["duration_ms"] = true
		shown["tool_parameters"] = true
		shown["arguments"] = true
		shown["output"] = true
		expandKeys = []string{"tool_parameters", "arguments"}
	case "api_request":
		shown["model"] = true
		shown["duration_ms"] = true
		shown["cost_usd"] = true
		shown["input_tokens"] = true
		shown["output_tokens"] = true
	case "tool_decision":
		shown["tool_name"] = true
		shown["decision"] = true
		shown["source"] = true
	}
	return RenderAttrDetails(e, shown, expandKeys)
}

func (claudeCodeEventRenderer) IsNoise(e proxy.TelemetryEvent) bool {
	return e.EventName == "tool_decision" && e.Attrs["decision"] == "accept"
}

// Shared rendering helpers used by both Claude Code and Codex renderers.

func renderToolResult(e proxy.TelemetryEvent) []EventSpan {
	name := fmt.Sprintf("%-*s", ColName, StripControl(e.Attrs["tool_name"]))
	var statusSpan EventSpan
	if v := e.Attrs["success"]; v == "true" {
		statusSpan = EventSpan{Text: "\u2713", Role: RoleAccent}
	} else if v == "false" {
		statusSpan = EventSpan{Text: "\u2717", Role: RoleError}
	} else {
		statusSpan = EventSpan{Text: " ", Role: RoleText}
	}
	dur := fmt.Sprintf("%*s", ColDur, e.Attrs["duration_ms"]+"ms")

	spans := []EventSpan{
		{Text: name, Role: RoleAccent},
		{Text: " ", Role: RoleText},
		statusSpan,
		{Text: " ", Role: RoleText},
		{Text: dur, Role: RoleField},
	}

	if desc := ToolDescription(e.Attrs["tool_parameters"]); desc != "" {
		spans = append(spans, EventSpan{Text: "  " + StripControl(desc), Role: RoleText})
	} else if desc := ToolDescription(e.Attrs["arguments"]); desc != "" {
		spans = append(spans, EventSpan{Text: "  " + StripControl(desc), Role: RoleText})
	} else if size := e.Attrs["tool_result_size_bytes"]; size != "" {
		spans = append(spans, EventSpan{Text: "  " + FormatBytes(size), Role: RoleField})
	}
	return spans
}

func renderAPIRequest(e proxy.TelemetryEvent) []EventSpan {
	model := ShortModelName(e.Attrs["model"])
	name := fmt.Sprintf("%-*s", ColName, model)
	dur := fmt.Sprintf("%*s", ColDur, e.Attrs["duration_ms"]+"ms")

	spans := []EventSpan{
		{Text: name, Role: RoleAccent},
		{Text: "   ", Role: RoleText},
		{Text: dur, Role: RoleField},
	}

	if v, ok := e.Attrs["cost_usd"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			v = fmt.Sprintf("$%.4g", f)
		} else {
			v = "$" + v
		}
		spans = append(spans, EventSpan{Text: " " + fmt.Sprintf("%*s", ColCost, v), Role: RoleField})
	}

	if v, ok := e.Attrs["input_tokens"]; ok {
		tok := fmt.Sprintf("%5s\u2191", v)
		if out, ok2 := e.Attrs["output_tokens"]; ok2 {
			tok += fmt.Sprintf(" %4s\u2193", out)
		}
		spans = append(spans, EventSpan{Text: "  " + tok, Role: RoleField})
	}
	return spans
}

func renderToolDecision(e proxy.TelemetryEvent) []EventSpan {
	toolName := StripControl(e.Attrs["tool_name"])
	source := StripControl(e.Attrs["source"])
	spans := []EventSpan{
		{Text: "\u2717", Role: RoleError},
		{Text: " ", Role: RoleText},
		{Text: toolName, Role: RoleAccent},
		{Text: " rejected", Role: RoleText},
	}
	if source != "" {
		spans = append(spans, EventSpan{Text: " (" + source + ")", Role: RoleText})
	}
	return spans
}

func renderDefaultLine(e proxy.TelemetryEvent) []EventSpan {
	event := fmt.Sprintf("%-*s", ColName, StripControl(e.EventName))
	spans := []EventSpan{
		{Text: event, Role: RoleAccent},
	}
	var kvParts []string
	for _, k := range SortedAttrKeys(e.Attrs) {
		if NoiseKeys[k] {
			continue
		}
		kvParts = append(kvParts, k+"="+StripControl(e.Attrs[k]))
	}
	if len(kvParts) > 0 {
		spans = append(spans, EventSpan{Text: "   ", Role: RoleText})
		for i, kv := range kvParts {
			if i > 0 {
				spans = append(spans, EventSpan{Text: " ", Role: RoleText})
			}
			spans = append(spans, EventSpan{Text: kv, Role: RoleText})
		}
	}
	return spans
}
