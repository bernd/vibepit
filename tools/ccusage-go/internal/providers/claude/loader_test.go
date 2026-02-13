package claude_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bernd/vibepit/tools/ccusage-go/internal/providers/claude"
)

func TestLoadSessionAggregates_DeduplicatesAndExtractsSession(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "projects", "project1", "session123", "chat.jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	line1 := `{"timestamp":"2026-01-01T10:00:00Z","requestId":"req-1","costUSD":0.02,"message":{"id":"msg-1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10}}}`
	lineDup := line1
	line2 := `{"timestamp":"2026-01-02T12:00:00Z","requestId":"req-2","message":{"id":"msg-2","model":"claude-sonnet-4-20250514","usage":{"input_tokens":200,"output_tokens":80}}}`
	if err := os.WriteFile(file, []byte(line1+"\n"+lineDup+"\n"+line2+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	sessions, err := claude.LoadSessionAggregates([]string{root})
	if err != nil {
		t.Fatalf("LoadSessionAggregates() error = %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}

	s := sessions[0]
	if s.SessionID != "session123" {
		t.Fatalf("SessionID = %q, want session123", s.SessionID)
	}
	if s.ProjectOrDir != "project1" {
		t.Fatalf("ProjectOrDir = %q, want project1", s.ProjectOrDir)
	}
	if s.LastActivity != "2026-01-02" {
		t.Fatalf("LastActivity = %q, want 2026-01-02", s.LastActivity)
	}

	m := s.Models["claude-sonnet-4-20250514"]
	if m == nil {
		t.Fatalf("missing model aggregate")
	}
	if m.Usage.InputTokens != 300 {
		t.Fatalf("input = %d, want 300", m.Usage.InputTokens)
	}
	if m.Usage.OutputTokens != 130 {
		t.Fatalf("output = %d, want 130", m.Usage.OutputTokens)
	}
	if m.ExplicitUsage == nil {
		t.Fatalf("expected explicit usage aggregate")
	}
	if m.ExplicitUsage.InputTokens != 100 || m.ExplicitUsage.OutputTokens != 50 || m.ExplicitUsage.CacheReadTokens != 10 {
		t.Fatalf("explicit usage = %+v, want input=100 output=50 cacheRead=10", *m.ExplicitUsage)
	}
	if s.ExplicitCostUSD != 0.02 {
		t.Fatalf("explicit cost = %v, want 0.02", s.ExplicitCostUSD)
	}
}

func TestResolvePaths_UsesClaudeConfigDirEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", root)

	paths, err := claude.ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error = %v", err)
	}
	if len(paths) != 1 || paths[0] != root {
		t.Fatalf("ResolvePaths() = %v, want [%s]", paths, root)
	}
}
