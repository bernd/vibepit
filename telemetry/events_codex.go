package telemetry

import (
	"fmt"
	"strings"

	"github.com/bernd/vibepit/proxy"
)

type codexEventRenderer struct{}

func (codexEventRenderer) RenderLine(e proxy.TelemetryEvent) []EventSpan {
	switch e.EventName {
	case "codex.tool_result":
		return renderToolResult(e)
	case "codex.api_request":
		return renderAPIRequest(e)
	case "codex.tool_decision":
		return renderToolDecision(e)
	case "codex.sse_event":
		return renderSSEEvent(e)
	case "codex.user_prompt":
		return renderUserPrompt(e)
	default:
		return renderDefaultLine(e)
	}
}

func (codexEventRenderer) RenderDetails(e proxy.TelemetryEvent) [][]EventSpan {
	shown := map[string]bool{}
	var expandKeys []string
	switch e.EventName {
	case "codex.tool_result":
		shown["tool_name"] = true
		shown["success"] = true
		shown["duration_ms"] = true
		shown["tool_parameters"] = true
		shown["arguments"] = true
		shown["output"] = true
		expandKeys = []string{"tool_parameters", "arguments"}
	case "codex.api_request":
		shown["model"] = true
		shown["duration_ms"] = true
		shown["cost_usd"] = true
		shown["input_tokens"] = true
		shown["output_tokens"] = true
	case "codex.tool_decision":
		shown["tool_name"] = true
		shown["decision"] = true
		shown["source"] = true
	case "codex.sse_event":
		shown["model"] = true
		shown["event.kind"] = true
		shown["input_token_count"] = true
		shown["output_token_count"] = true
		shown["cached_token_count"] = true
		shown["reasoning_token_count"] = true
	case "codex.user_prompt":
		shown["prompt"] = true
		shown["prompt_length"] = true
		shown["model"] = true
	}
	return RenderAttrDetails(e, shown, expandKeys)
}

func (codexEventRenderer) IsNoise(e proxy.TelemetryEvent) bool {
	if e.EventName == "codex.tool_decision" && e.Attrs["decision"] == "approved" {
		return true
	}
	if e.EventName == "codex.sse_event" {
		if e.Attrs["event.kind"] != "response.completed" {
			return true
		}
		if e.Attrs["input_token_count"] == "" && e.Attrs["output_token_count"] == "" {
			return true
		}
	}
	return false
}

func renderSSEEvent(e proxy.TelemetryEvent) []EventSpan {
	model := ShortModelName(e.Attrs["model"])
	name := fmt.Sprintf("%-*s", ColName, model)

	var tokParts []string
	if v := e.Attrs["input_token_count"]; v != "" && v != "0" {
		tokParts = append(tokParts, v+"\u2191")
	}
	if v := e.Attrs["output_token_count"]; v != "" && v != "0" {
		tokParts = append(tokParts, v+"\u2193")
	}
	if v := e.Attrs["cached_token_count"]; v != "" && v != "0" {
		tokParts = append(tokParts, v+" cached")
	}
	if v := e.Attrs["reasoning_token_count"]; v != "" && v != "0" {
		tokParts = append(tokParts, v+" reasoning")
	}

	spans := []EventSpan{
		{Text: name, Role: RoleAccent},
		{Text: "   ", Role: RoleText},
	}
	if len(tokParts) > 0 {
		spans = append(spans, EventSpan{Text: strings.Join(tokParts, "  "), Role: RoleField})
	}
	return spans
}

func renderUserPrompt(e proxy.TelemetryEvent) []EventSpan {
	prompt := StripControl(e.Attrs["prompt"])
	return []EventSpan{
		{Text: "prompt", Role: RoleAccent},
		{Text: "  " + prompt, Role: RoleText},
	}
}
