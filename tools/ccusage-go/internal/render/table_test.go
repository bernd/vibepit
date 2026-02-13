package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bernd/vibepit/tools/ccusage-go/internal/model"
	"github.com/bernd/vibepit/tools/ccusage-go/internal/render"
)

func TestTableRendersRowsAndTotals(t *testing.T) {
	rows := []model.SessionRow{
		{
			Provider:     model.ProviderCodex,
			SessionID:    "session-a",
			ProjectOrDir: "project-a",
			LastActivity: "2026-01-10",
			Models:       []string{"<unknown>", "gpt-5"},
			Tokens: model.TokenUsage{
				InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 100, TotalTokens: 1500,
			},
			CostUSD: 0.123456,
		},
		{
			Provider:     model.ProviderCodex,
			SessionID:    "session-b",
			ProjectOrDir: "project-b",
			LastActivity: "2026-01-10",
			Models:       []string{"gpt-5-mini"},
			Tokens: model.TokenUsage{
				InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10, TotalTokens: 150,
			},
			CostUSD: 0.100000,
		},
	}

	var buf bytes.Buffer
	render.Table(&buf, rows, false)
	out := buf.String()

	checks := []string{"PROVIDER", "gpt-5,gpt-5-mini", "TOTAL", "$0.22", "1.100", "550"}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Fatalf("output missing %q:\n%s", c, out)
		}
	}
	if strings.Contains(out, "DIR") || strings.Contains(out, "SESSION") {
		t.Fatalf("compact output should not include DIR/SESSION columns:\n%s", out)
	}
	if strings.Contains(out, "<unknown>") {
		t.Fatalf("compact output should hide synthetic model names:\n%s", out)
	}
}

func TestTableVerboseShowsFullFields(t *testing.T) {
	rows := []model.SessionRow{
		{
			Provider:     model.ProviderCodex,
			SessionID:    "2026/02/12/rollout-very-long-session-abcdef1234567890",
			ProjectOrDir: "very/long/project/path/that/should/not/shrink",
			LastActivity: "2026-01-10",
			Models:       []string{"<unknown>", "gpt-5"},
			Tokens: model.TokenUsage{
				InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 100, TotalTokens: 1500,
			},
			CostUSD: 0.123456,
		},
	}

	var buf bytes.Buffer
	render.Table(&buf, rows, true)
	out := buf.String()
	if !strings.Contains(out, "DIR") || !strings.Contains(out, "SESSION") {
		t.Fatalf("verbose output should keep DIR/SESSION columns:\n%s", out)
	}
	if !strings.Contains(out, "very/long/project/path/that/should/not/shrink") {
		t.Fatalf("verbose output should keep full project path:\n%s", out)
	}
	if !strings.Contains(out, "<unknown>,gpt-5") {
		t.Fatalf("verbose output should keep full model list:\n%s", out)
	}
	if !strings.Contains(out, "$0.12") {
		t.Fatalf("verbose output should format cost as currency:\n%s", out)
	}
	if !strings.Contains(out, "1.500") {
		t.Fatalf("verbose output should format token counts with dot separators:\n%s", out)
	}
}
