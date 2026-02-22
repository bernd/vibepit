package telemetry

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/bernd/vibepit/proxy"
)

// Column widths for aligned rendering.
const (
	ColAgent = 12 // "claude-code" = 11
	ColName  = 7  // "sonnet" = 6, tool names ≤ 6
	ColDur   = 7  // "4757ms" = 6
	ColCost  = 9  // "$0.00055" = 8
)

// NoiseKeys are attribute keys filtered out of detail views because they
// duplicate information already shown in the compact line or are uninteresting.
var NoiseKeys = map[string]bool{
	"user.id":           true,
	"user.email":        true,
	"organization.id":   true,
	"user.account_uuid": true,
	"session.id":        true,
	"terminal.type":     true,
	"event.sequence":    true,
	"event.timestamp":   true,
	"event.name":        true,
	"app.version":       true,
	"prompt.id":         true,
	"auth_mode":         true,
	"conversation.id":   true,
	"originator":        true,
	"slug":              true,
	"user.account_id":   true,
}

// StripControl removes ANSI escape sequences and control characters (except
// tab) from s.
func StripControl(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inEsc := false
	for _, r := range s {
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '~' {
				inEsc = false
			}
			continue
		}
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if r < 0x20 && r != '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ToolDescription extracts a human-readable description from a tool_parameters
// JSON string.
func ToolDescription(params string) string {
	if params == "" {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(params), &parsed); err != nil {
		return ""
	}
	for _, key := range []string{"description", "command"} {
		if v, ok := parsed[key].(string); ok && v != "" {
			return v
		}
	}
	for _, key := range []string{"file_path", "pattern", "url", "query", "skill", "prompt"} {
		if v, ok := parsed[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// FormatBytes converts a byte count string to a human-readable size.
func FormatBytes(s string) string {
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s + "B"
	}
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1fMB", n/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1fKB", n/1024)
	default:
		return fmt.Sprintf("%.0fB", n)
	}
}

// SortedAttrKeys returns the keys of attrs in sorted order.
func SortedAttrKeys(attrs map[string]string) []string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// RenderAttrDetails returns detail lines for an expanded event. Keys in shown
// and NoiseKeys are excluded. If expandParamKeys is non-empty, those attribute
// values are parsed as JSON and their fields are rendered as sub-items.
func RenderAttrDetails(e proxy.TelemetryEvent, shown map[string]bool, expandParamKeys []string) [][]EventSpan {
	var lines [][]EventSpan

	// Expand JSON parameter attributes into individual fields.
	for _, paramKey := range expandParamKeys {
		paramsJSON := e.Attrs[paramKey]
		if paramsJSON == "" {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(paramsJSON), &parsed); err != nil {
			continue
		}
		paramKeys := make([]string, 0, len(parsed))
		for k := range parsed {
			paramKeys = append(paramKeys, k)
		}
		slices.Sort(paramKeys)
		for _, k := range paramKeys {
			v := fmt.Sprintf("%v", parsed[k])
			lines = append(lines, []EventSpan{
				{Text: "  └ ", Role: RoleField},
				{Text: k + ": " + StripControl(v), Role: RoleText},
			})
		}
		break // only expand the first non-empty param key
	}

	keys := SortedAttrKeys(e.Attrs)
	for _, k := range keys {
		if NoiseKeys[k] || shown[k] {
			continue
		}
		lines = append(lines, []EventSpan{
			{Text: "  └ ", Role: RoleField},
			{Text: k + ": " + StripControl(e.Attrs[k]), Role: RoleText},
		})
	}

	return lines
}
