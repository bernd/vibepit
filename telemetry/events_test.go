package telemetry

import (
	"strings"
	"testing"
	"time"

	"github.com/bernd/vibepit/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeEvent(agent, name string, attrs map[string]string) proxy.TelemetryEvent {
	return proxy.TelemetryEvent{
		ID:        1,
		Time:      time.Date(2026, 1, 15, 10, 30, 45, 0, time.UTC),
		Agent:     agent,
		EventName: name,
		Attrs:     attrs,
	}
}

func spansText(spans []EventSpan) string {
	var s strings.Builder
	for _, span := range spans {
		s.WriteString(span.Text)
	}
	return s.String()
}

func TestStripControl(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"plain text", "hello", "hello"},
		{"strips ANSI escape", "hello\x1b[31mworld\x1b[0m", "helloworld"},
		{"strips control chars", "hello\x00world", "helloworld"},
		{"preserves tabs", "hello\tworld", "hello\tworld"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, StripControl(tt.input))
		})
	}
}

func TestToolDescription(t *testing.T) {
	tests := []struct {
		name   string
		params string
		want   string
	}{
		{"empty", "", ""},
		{"invalid json", "not json", ""},
		{"description field", `{"description":"Run Go vet","command":"go vet"}`, "Run Go vet"},
		{"command fallback", `{"command":"go vet ./..."}`, "go vet ./..."},
		{"file_path fallback", `{"file_path":"/home/user/main.go"}`, "/home/user/main.go"},
		{"pattern fallback", `{"pattern":"func Test.*"}`, "func Test.*"},
		{"url fallback", `{"url":"https://example.com"}`, "https://example.com"},
		{"description wins over file_path", `{"description":"Read config","file_path":"/etc/foo"}`, "Read config"},
		{"no useful fields", `{"timeout":30}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ToolDescription(tt.params))
		})
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"100", "100B"},
		{"1024", "1.0KB"},
		{"1536", "1.5KB"},
		{"1048576", "1.0MB"},
		{"bad", "badB"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatBytes(tt.input))
		})
	}
}

func TestIsEventNoise(t *testing.T) {
	tests := []struct {
		name  string
		event proxy.TelemetryEvent
		noise bool
	}{
		{
			"empty event name is noise",
			makeEvent("claude-code", "", nil),
			true,
		},
		{
			"accepted claude-code tool_decision is noise",
			makeEvent("claude-code", "tool_decision", map[string]string{"decision": "accept"}),
			true,
		},
		{
			"rejected claude-code tool_decision is not noise",
			makeEvent("claude-code", "tool_decision", map[string]string{"decision": "reject"}),
			false,
		},
		{
			"approved codex tool_decision is noise",
			makeEvent("codex", "codex.tool_decision", map[string]string{"decision": "approved"}),
			true,
		},
		{
			"codex sse_event non-completed is noise",
			makeEvent("codex", "codex.sse_event", map[string]string{"event.kind": "response.output_text.delta"}),
			true,
		},
		{
			"codex sse_event completed with tokens is not noise",
			makeEvent("codex", "codex.sse_event", map[string]string{
				"event.kind":         "response.completed",
				"input_token_count":  "100",
				"output_token_count": "50",
			}),
			false,
		},
		{
			"codex sse_event completed without tokens is noise",
			makeEvent("codex", "codex.sse_event", map[string]string{"event.kind": "response.completed"}),
			true,
		},
		{
			"regular event is not noise",
			makeEvent("claude-code", "tool_result", map[string]string{"tool_name": "Bash"}),
			false,
		},
		{
			"unknown agent event is not noise",
			makeEvent("unknown", "some_event", map[string]string{"key": "val"}),
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.noise, IsEventNoise(tt.event))
		})
	}
}

func TestRenderEventLine(t *testing.T) {
	t.Run("claude-code tool_result", func(t *testing.T) {
		e := makeEvent("claude-code", "tool_result", map[string]string{
			"tool_name":       "Bash",
			"success":         "true",
			"duration_ms":     "70",
			"tool_parameters": `{"command":"go vet ./...","description":"Run Go vet"}`,
		})
		spans := RenderEventLine(e)
		text := spansText(spans)
		assert.Contains(t, text, "10:30:45")
		assert.Contains(t, text, "claude-code")
		assert.Contains(t, text, "Bash")
		assert.Contains(t, text, "\u2713")
		assert.Contains(t, text, "70ms")
		assert.Contains(t, text, "Run Go vet")
	})

	t.Run("claude-code api_request", func(t *testing.T) {
		e := makeEvent("claude-code", "api_request", map[string]string{
			"model":         "claude-opus-4-6",
			"duration_ms":   "741",
			"cost_usd":      "0.0005",
			"input_tokens":  "311",
			"output_tokens": "32",
		})
		spans := RenderEventLine(e)
		text := spansText(spans)
		assert.Contains(t, text, "opus")
		assert.Contains(t, text, "741ms")
		assert.Contains(t, text, "$0.0005")
		assert.Contains(t, text, "311\u2191")
		assert.Contains(t, text, "32\u2193")
	})

	t.Run("claude-code tool_decision rejected", func(t *testing.T) {
		e := makeEvent("claude-code", "tool_decision", map[string]string{
			"decision":  "reject",
			"tool_name": "Bash",
			"source":    "user",
		})
		spans := RenderEventLine(e)
		text := spansText(spans)
		assert.Contains(t, text, "\u2717")
		assert.Contains(t, text, "Bash")
		assert.Contains(t, text, "rejected")
		assert.Contains(t, text, "(user)")
	})

	t.Run("codex sse_event", func(t *testing.T) {
		e := makeEvent("codex", "codex.sse_event", map[string]string{
			"event.kind":         "response.completed",
			"model":              "claude-sonnet-4-6",
			"input_token_count":  "500",
			"output_token_count": "100",
		})
		spans := RenderEventLine(e)
		text := spansText(spans)
		assert.Contains(t, text, "sonnet")
		assert.Contains(t, text, "500\u2191")
		assert.Contains(t, text, "100\u2193")
	})

	t.Run("codex user_prompt", func(t *testing.T) {
		e := makeEvent("codex", "codex.user_prompt", map[string]string{
			"prompt": "fix the bug",
		})
		spans := RenderEventLine(e)
		text := spansText(spans)
		assert.Contains(t, text, "prompt")
		assert.Contains(t, text, "fix the bug")
	})

	t.Run("unknown agent uses generic renderer", func(t *testing.T) {
		e := makeEvent("unknown-agent", "custom_event", map[string]string{
			"key1": "val1",
			"key2": "val2",
		})
		spans := RenderEventLine(e)
		text := spansText(spans)
		assert.Contains(t, text, "custom_event")
		assert.Contains(t, text, "key1=val1")
		assert.Contains(t, text, "key2=val2")
	})

	t.Run("tool_result with size fallback", func(t *testing.T) {
		e := makeEvent("claude-code", "tool_result", map[string]string{
			"tool_name":              "Read",
			"success":                "true",
			"duration_ms":            "10",
			"tool_result_size_bytes": "2048",
		})
		spans := RenderEventLine(e)
		text := spansText(spans)
		assert.Contains(t, text, "2.0KB")
	})
}

func TestRenderEventDetails(t *testing.T) {
	t.Run("tool_result expands parameters", func(t *testing.T) {
		e := makeEvent("claude-code", "tool_result", map[string]string{
			"tool_name":       "Bash",
			"success":         "true",
			"duration_ms":     "70",
			"tool_parameters": `{"command":"go vet ./...","description":"Run Go vet"}`,
			"result_size":     "86",
		})
		details := RenderEventDetails(e)
		require.NotEmpty(t, details)

		var allText string
		for _, line := range details {
			allText += spansText(line) + "\n"
		}
		assert.Contains(t, allText, "command:")
		assert.Contains(t, allText, "description:")
		assert.Contains(t, allText, "result_size:")
	})

	t.Run("api_request hides shown keys", func(t *testing.T) {
		e := makeEvent("claude-code", "api_request", map[string]string{
			"model":         "claude-opus-4-6",
			"duration_ms":   "741",
			"cost_usd":      "0.0005",
			"input_tokens":  "311",
			"output_tokens": "32",
			"extra_field":   "visible",
		})
		details := RenderEventDetails(e)
		var allText string
		for _, line := range details {
			allText += spansText(line) + "\n"
		}
		assert.Contains(t, allText, "extra_field")
		assert.NotContains(t, allText, "model:")
		assert.NotContains(t, allText, "duration_ms:")
	})

	t.Run("noise keys excluded from details", func(t *testing.T) {
		e := makeEvent("claude-code", "tool_result", map[string]string{
			"tool_name":   "Read",
			"success":     "true",
			"session.id":  "abc123",
			"extra_field": "visible",
		})
		details := RenderEventDetails(e)
		var allText string
		for _, line := range details {
			allText += spansText(line) + "\n"
		}
		assert.NotContains(t, allText, "session.id")
		assert.Contains(t, allText, "extra_field")
	})

	t.Run("generic renderer shows all non-noise attrs", func(t *testing.T) {
		e := makeEvent("unknown", "custom", map[string]string{
			"key1":       "val1",
			"session.id": "noise",
		})
		details := RenderEventDetails(e)
		var allText string
		for _, line := range details {
			allText += spansText(line) + "\n"
		}
		assert.Contains(t, allText, "key1")
		assert.NotContains(t, allText, "session.id")
	})
}
