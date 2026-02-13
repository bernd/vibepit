package codex_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bernd/vibepit/tools/ccusage-go/internal/providers/codex"
)

func TestLoadSessionAggregates_ComputesDeltaFromTotals(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "sessions", "proj-a", "session-a.jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	content := "" +
		`{"timestamp":"2026-01-01T10:00:00Z","type":"turn_context","payload":{"model":"gpt-5"}}` + "\n" +
		`{"timestamp":"2026-01-01T10:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1200,"cached_input_tokens":200,"output_tokens":500,"reasoning_output_tokens":0,"total_tokens":1700}}}}` + "\n" +
		`{"timestamp":"2026-01-01T11:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":2000,"cached_input_tokens":300,"output_tokens":800,"reasoning_output_tokens":0,"total_tokens":2800}}}}` + "\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	res, err := codex.LoadSessionAggregates([]string{filepath.Join(root, "sessions")})
	if err != nil {
		t.Fatalf("LoadSessionAggregates() error = %v", err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(res.Sessions))
	}

	s := res.Sessions[0]
	m := s.Models["gpt-5"]
	if m == nil {
		t.Fatalf("missing model aggregate")
	}
	if m.Usage.InputTokens != 2000 {
		t.Fatalf("input = %d, want 2000", m.Usage.InputTokens)
	}
	if m.Usage.CacheReadTokens != 300 {
		t.Fatalf("cacheRead = %d, want 300", m.Usage.CacheReadTokens)
	}
	if m.Usage.OutputTokens != 800 {
		t.Fatalf("output = %d, want 800", m.Usage.OutputTokens)
	}
}

func TestLoadSessionAggregates_LegacyFallbackModel(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "sessions", "legacy.jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	content := `{"timestamp":"2026-01-03T10:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":5000,"cached_input_tokens":0,"output_tokens":1000,"reasoning_output_tokens":0,"total_tokens":6000}}}}` + "\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	res, err := codex.LoadSessionAggregates([]string{filepath.Join(root, "sessions")})
	if err != nil {
		t.Fatalf("LoadSessionAggregates() error = %v", err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(res.Sessions))
	}

	m := res.Sessions[0].Models["gpt-5"]
	if m == nil {
		t.Fatalf("missing fallback model gpt-5")
	}
	if !m.IsFallback {
		t.Fatalf("expected fallback model flag")
	}
}

func TestLoadSessionAggregates_SkipsFilesWithoutUsageEvents(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "sessions", "empty.jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(file, []byte(`{"timestamp":"2026-01-01T10:00:00Z","type":"turn_context","payload":{"model":"gpt-5"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	res, err := codex.LoadSessionAggregates([]string{filepath.Join(root, "sessions")})
	if err != nil {
		t.Fatalf("LoadSessionAggregates() error = %v", err)
	}
	if len(res.Sessions) != 0 {
		t.Fatalf("len(Sessions) = %d, want 0", len(res.Sessions))
	}
}

func TestLoadSessionAggregates_UsesMetadataModelAndCacheReadDelta(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "sessions", "proj-meta", "session-meta.jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	content := "" +
		`{"timestamp":"2026-01-01T10:00:00Z","type":"turn_context","payload":{"metadata":{"model":"gpt-5-mini"}}}` + "\n" +
		`{"timestamp":"2026-01-01T10:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1200,"cache_read_input_tokens":200,"output_tokens":500,"reasoning_output_tokens":0,"total_tokens":1700}}}}` + "\n" +
		`{"timestamp":"2026-01-01T11:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":2000,"cache_read_input_tokens":300,"output_tokens":800,"reasoning_output_tokens":0,"total_tokens":2800}}}}` + "\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	res, err := codex.LoadSessionAggregates([]string{filepath.Join(root, "sessions")})
	if err != nil {
		t.Fatalf("LoadSessionAggregates() error = %v", err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(res.Sessions))
	}

	m := res.Sessions[0].Models["gpt-5-mini"]
	if m == nil {
		t.Fatalf("missing metadata-derived model aggregate")
	}
	if m.Usage.InputTokens != 2000 {
		t.Fatalf("input = %d, want 2000", m.Usage.InputTokens)
	}
	if m.Usage.CacheReadTokens != 300 {
		t.Fatalf("cacheRead = %d, want 300", m.Usage.CacheReadTokens)
	}
	if m.Usage.OutputTokens != 800 {
		t.Fatalf("output = %d, want 800", m.Usage.OutputTokens)
	}
}
