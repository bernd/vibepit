package report_test

import (
	"math"
	"testing"

	"github.com/bernd/vibepit/tools/ccusage-go/internal/model"
	"github.com/bernd/vibepit/tools/ccusage-go/internal/pricing"
	"github.com/bernd/vibepit/tools/ccusage-go/internal/report"
)

func TestBuildSessionRows_CalculatesCostAndTotals(t *testing.T) {
	sessions := []model.SessionAggregate{
		{
			Provider:     model.ProviderCodex,
			SessionID:    "sess-a",
			ProjectOrDir: "proj",
			LastActivity: "2026-01-10",
			Models: map[string]*model.ModelUsage{
				"gpt-5": {
					Name:  "gpt-5",
					Usage: model.TokenUsage{InputTokens: 1000, CacheReadTokens: 100, OutputTokens: 500, TotalTokens: 1500},
				},
			},
		},
	}

	dataset := map[string]pricing.Entry{
		"gpt-5": {
			InputPerToken:     1.25e-6,
			OutputPerToken:    1e-5,
			CacheReadPerToken: 1.25e-7,
		},
	}

	rows := report.BuildSessionRows(sessions, dataset, "", "")
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}

	row := rows[0]
	if row.Tokens.InputTokens != 1000 || row.Tokens.OutputTokens != 500 || row.Tokens.CacheReadTokens != 100 {
		t.Fatalf("unexpected token totals: %+v", row.Tokens)
	}
	expected := (900 * 1.25e-6) + (100 * 1.25e-7) + (500 * 1e-5)
	if math.Abs(row.CostUSD-expected) > 1e-9 {
		t.Fatalf("cost = %.12f, want %.12f", row.CostUSD, expected)
	}
}

func TestBuildSessionRows_UsesExplicitCostAndDateFilter(t *testing.T) {
	sessions := []model.SessionAggregate{
		{
			Provider:     model.ProviderClaude,
			SessionID:    "keep",
			ProjectOrDir: "project-a",
			LastActivity: "2026-01-15",
			Models: map[string]*model.ModelUsage{
				"claude-sonnet-4-20250514": {
					Name:            "claude-sonnet-4-20250514",
					Usage:           model.TokenUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
					HasExplicitCost: true,
					ExplicitCostUSD: 0.42,
				},
			},
		},
		{
			Provider:     model.ProviderClaude,
			SessionID:    "drop",
			ProjectOrDir: "project-b",
			LastActivity: "2026-01-01",
			Models: map[string]*model.ModelUsage{
				"claude-sonnet-4-20250514": {Name: "claude-sonnet-4-20250514", Usage: model.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}},
			},
		},
	}

	rows := report.BuildSessionRows(sessions, map[string]pricing.Entry{}, "2026-01-10", "2026-01-31")
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].SessionID != "keep" {
		t.Fatalf("session = %q, want keep", rows[0].SessionID)
	}
	if math.Abs(rows[0].CostUSD-0.42) > 1e-9 {
		t.Fatalf("cost = %.12f, want 0.420000000000", rows[0].CostUSD)
	}
}

func TestBuildSessionRows_ClaudeMixedExplicitAndCalculatedCost(t *testing.T) {
	sessions := []model.SessionAggregate{
		{
			Provider:     model.ProviderClaude,
			SessionID:    "mix",
			ProjectOrDir: "project-a",
			LastActivity: "2026-01-20",
			Models: map[string]*model.ModelUsage{
				"claude-sonnet-4-20250514": {
					Name:            "claude-sonnet-4-20250514",
					Usage:           model.TokenUsage{InputTokens: 300, OutputTokens: 130, CacheReadTokens: 10, CacheCreateTokens: 20, TotalTokens: 460},
					HasExplicitCost: true,
					ExplicitCostUSD: 0.02,
					ExplicitUsage: &model.TokenUsage{
						InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10, CacheCreateTokens: 20, TotalTokens: 180,
					},
				},
			},
		},
	}

	dataset := map[string]pricing.Entry{
		"claude-sonnet-4-20250514": {
			InputPerToken:       2e-6,
			OutputPerToken:      4e-6,
			CacheReadPerToken:   1e-6,
			CacheCreatePerToken: 3e-6,
		},
	}

	rows := report.BuildSessionRows(sessions, dataset, "", "")
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}

	// Explicit cost applies to 100 in / 50 out / 10 cache read / 20 cache create.
	// Remaining tokens are priced from dataset: 200 in + 80 out.
	expected := 0.02 + (200 * 2e-6) + (80 * 4e-6)
	if math.Abs(rows[0].CostUSD-expected) > 1e-9 {
		t.Fatalf("cost = %.12f, want %.12f", rows[0].CostUSD, expected)
	}
}

func TestBuildSessionRows_ClaudeTieredPricing(t *testing.T) {
	sessions := []model.SessionAggregate{
		{
			Provider:     model.ProviderClaude,
			SessionID:    "tiered",
			ProjectOrDir: "project-a",
			LastActivity: "2026-01-20",
			Models: map[string]*model.ModelUsage{
				"claude-4-sonnet-20250514": {
					Name: "claude-4-sonnet-20250514",
					Usage: model.TokenUsage{
						InputTokens:       300_000,
						OutputTokens:      250_000,
						CacheCreateTokens: 300_000,
						CacheReadTokens:   250_000,
						TotalTokens:       1_100_000,
					},
				},
			},
		},
	}

	dataset := map[string]pricing.Entry{
		"claude-4-sonnet-20250514": {
			InputPerToken:                3e-6,
			OutputPerToken:               1.5e-5,
			CacheCreatePerToken:          3.75e-6,
			CacheReadPerToken:            3e-7,
			InputPerTokenAbove200k:       6e-6,
			OutputPerTokenAbove200k:      2.25e-5,
			CacheCreatePerTokenAbove200k: 7.5e-6,
			CacheReadPerTokenAbove200k:   6e-7,
		},
	}

	rows := report.BuildSessionRows(sessions, dataset, "", "")
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}

	expected :=
		(200_000 * 3e-6) + (100_000 * 6e-6) +
			(200_000 * 1.5e-5) + (50_000 * 2.25e-5) +
			(200_000 * 3.75e-6) + (100_000 * 7.5e-6) +
			(200_000 * 3e-7) + (50_000 * 6e-7)
	if math.Abs(rows[0].CostUSD-expected) > 1e-9 {
		t.Fatalf("cost = %.12f, want %.12f", rows[0].CostUSD, expected)
	}
}
