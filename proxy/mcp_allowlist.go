package proxy

import (
	"fmt"
	"strings"
)

// MCPToolAllowlist validates MCP tool names against glob patterns.
// Tool names are flat strings (no dots), so patterns use simple prefix
// glob matching: "get_*" matches any tool starting with "get_".
type MCPToolAllowlist struct {
	patterns []string
}

func NewMCPToolAllowlist(entries []string) (*MCPToolAllowlist, error) {
	for _, e := range entries {
		if err := validateToolPattern(e); err != nil {
			return nil, err
		}
	}
	lowered := make([]string, len(entries))
	for i, e := range entries {
		lowered[i] = strings.ToLower(e)
	}
	return &MCPToolAllowlist{patterns: lowered}, nil
}

func (al *MCPToolAllowlist) Allows(tool string) bool {
	if tool == "" {
		return false
	}
	tool = strings.ToLower(tool)
	for _, p := range al.patterns {
		if toolMatches(p, tool) {
			return true
		}
	}
	return false
}

func toolMatches(pattern, tool string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == tool
	}
	// Only trailing * is supported: "get_*" matches "get_anything".
	if strings.HasSuffix(pattern, "*") {
		prefix := pattern[:len(pattern)-1]
		return strings.HasPrefix(tool, prefix)
	}
	return false
}

func validateToolPattern(entry string) error {
	if entry == "" {
		return fmt.Errorf("invalid tool pattern: empty string")
	}
	if strings.Contains(entry, " ") {
		return fmt.Errorf("invalid tool pattern %q: spaces not allowed", entry)
	}
	// Only trailing * is allowed (including bare "*" to allow all tools).
	starCount := strings.Count(entry, "*")
	if starCount > 1 {
		return fmt.Errorf("invalid tool pattern %q: at most one '*' allowed", entry)
	}
	if starCount == 1 && !strings.HasSuffix(entry, "*") {
		return fmt.Errorf("invalid tool pattern %q: '*' only allowed at the end", entry)
	}
	return nil
}
