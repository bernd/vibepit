package telemetry

import (
	"fmt"

	"github.com/bernd/vibepit/proxy"
)

// SpanRole identifies the semantic role of an EventSpan for styling by the
// presentation layer.
type SpanRole int

const (
	RoleText   SpanRole = iota // default terminal color
	RoleField                  // dim info (timestamps, durations, sizes)
	RoleAccent                 // highlighted info (model names, tool names)
	RoleAgent                  // agent name
	RoleError                  // errors/rejections
)

// EventSpan is a styled text segment returned by event renderers.
type EventSpan struct {
	Text string
	Role SpanRole
}

// EventRenderer renders telemetry events for a specific agent.
type EventRenderer interface {
	RenderLine(e proxy.TelemetryEvent) []EventSpan
	RenderDetails(e proxy.TelemetryEvent) [][]EventSpan // each inner slice = one line
	IsNoise(e proxy.TelemetryEvent) bool
}

var eventRenderers = map[string]EventRenderer{
	"claude-code": claudeCodeEventRenderer{},
	"codex":       codexEventRenderer{},
}

var genericRenderer = genericEventRenderer{}

func rendererFor(agent string) EventRenderer {
	if r, ok := eventRenderers[agent]; ok {
		return r
	}
	return genericRenderer
}

// RenderEventLine returns styled spans for a compact one-line event summary.
// The timestamp and agent prefix are prepended by this function.
func RenderEventLine(e proxy.TelemetryEvent) []EventSpan {
	prefix := []EventSpan{
		{Text: "[", Role: RoleText},
		{Text: e.Time.Format("15:04:05"), Role: RoleField},
		{Text: "] ", Role: RoleText},
		{Text: fmt.Sprintf("%-*s", ColAgent, StripControl(e.Agent)), Role: RoleAgent},
		{Text: " ", Role: RoleText},
	}
	r := rendererFor(e.Agent)
	return append(prefix, r.RenderLine(e)...)
}

// RenderEventDetails returns styled spans for expanded event detail lines.
func RenderEventDetails(e proxy.TelemetryEvent) [][]EventSpan {
	r := rendererFor(e.Agent)
	return r.RenderDetails(e)
}

// IsEventNoise returns true if the event should be hidden in the default view.
func IsEventNoise(e proxy.TelemetryEvent) bool {
	if e.EventName == "" {
		return true
	}
	r := rendererFor(e.Agent)
	return r.IsNoise(e)
}
