package main

import (
	"testing"

	"github.com/bernd/vibepit/tools/ccusage-go/internal/model"
)

func TestBuildJSONPayload_Compact(t *testing.T) {
	rows := []model.SessionRow{
		{
			Provider:     model.ProviderClaude,
			SessionID:    "session-a",
			ProjectOrDir: "proj/path",
			LastActivity: "2026-02-12",
			Models:       []string{"<unknown>", "claude-opus-4-6"},
			ModelUsage: map[string]model.ModelUsage{
				"claude-opus-4-6": {Name: "claude-opus-4-6"},
			},
			Tokens:  model.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
			CostUSD: 1.23,
		},
	}

	payload := buildJSONPayload(rows, false)
	if payload.Totals == nil {
		t.Fatalf("Totals = nil")
	}
	if len(payload.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(payload.Sessions))
	}
	session := payload.Sessions[0]
	if len(session.ModelUsage) != 0 {
		t.Fatalf("compact JSON should omit ModelUsage")
	}
	if len(session.Models) != 1 || session.Models[0] != "claude-opus-4-6" {
		t.Fatalf("compact JSON should filter noisy models, got %v", session.Models)
	}
}

func TestBuildJSONPayload_Verbose(t *testing.T) {
	rows := []model.SessionRow{
		{
			Provider:     model.ProviderClaude,
			SessionID:    "session-a",
			ProjectOrDir: "proj/path",
			LastActivity: "2026-02-12",
			Models:       []string{"<unknown>", "claude-opus-4-6"},
			ModelUsage: map[string]model.ModelUsage{
				"claude-opus-4-6": {Name: "claude-opus-4-6"},
			},
			Tokens:  model.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
			CostUSD: 1.23,
		},
	}

	payload := buildJSONPayload(rows, true)
	if len(payload.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(payload.Sessions))
	}
	session := payload.Sessions[0]
	if len(session.ModelUsage) == 0 {
		t.Fatalf("verbose JSON should keep ModelUsage")
	}
	if len(session.Models) != 2 {
		t.Fatalf("verbose JSON should keep full model list, got %v", session.Models)
	}
}
