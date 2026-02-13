package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/bernd/vibepit/tools/ccusage-go/internal/cli"
	"github.com/bernd/vibepit/tools/ccusage-go/internal/model"
	"github.com/bernd/vibepit/tools/ccusage-go/internal/pricing"
	"github.com/bernd/vibepit/tools/ccusage-go/internal/providers/claude"
	"github.com/bernd/vibepit/tools/ccusage-go/internal/providers/codex"
	"github.com/bernd/vibepit/tools/ccusage-go/internal/render"
	"github.com/bernd/vibepit/tools/ccusage-go/internal/report"
)

type totalsOutput struct {
	InputTokens       int64   `json:"inputTokens"`
	OutputTokens      int64   `json:"outputTokens"`
	CacheReadTokens   int64   `json:"cacheReadTokens"`
	CacheCreateTokens int64   `json:"cacheCreateTokens"`
	ReasoningTokens   int64   `json:"reasoningTokens"`
	TotalTokens       int64   `json:"totalTokens"`
	CostUSD           float64 `json:"costUSD"`
}

func main() {
	opts, err := cli.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	sessions := make([]model.SessionAggregate, 0)

	if opts.Provider == model.ProviderClaude || opts.Provider == model.ProviderAll {
		claudeSessions, err := claude.LoadSessionAggregates(nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: Claude usage not loaded: %v\n", err)
		} else {
			sessions = append(sessions, claudeSessions...)
		}
	}

	if opts.Provider == model.ProviderCodex || opts.Provider == model.ProviderAll {
		codexResult, err := codex.LoadSessionAggregates(nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: Codex usage not loaded: %v\n", err)
		} else {
			for _, missing := range codexResult.MissingDirectories {
				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "warning: Codex session directory not found: %s\n", missing)
				}
			}
			sessions = append(sessions, codexResult.Sessions...)
		}
	}

	pricingDataset := map[string]pricing.Entry{}
	priceClient := pricing.Client{Offline: opts.Offline}
	if len(sessions) > 0 && !opts.Offline {
		loaded, err := priceClient.Fetch(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: pricing fetch failed, continuing without pricing data: %v\n", err)
		} else {
			pricingDataset = loaded
		}
	}

	rows := report.BuildSessionRows(sessions, pricingDataset, opts.Since, opts.Until)
	if opts.JSON {
		payload := buildJSONPayload(rows, opts.Verbose)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			fmt.Fprintf(os.Stderr, "failed to encode JSON: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(rows) == 0 {
		fmt.Fprintln(os.Stdout, "No usage data found.")
		return
	}

	render.Table(os.Stdout, rows, opts.Verbose)
}

func computeTotals(rows []model.SessionRow) totalsOutput {
	var out totalsOutput
	for _, row := range rows {
		out.InputTokens += row.Tokens.InputTokens
		out.OutputTokens += row.Tokens.OutputTokens
		out.CacheReadTokens += row.Tokens.CacheReadTokens
		out.CacheCreateTokens += row.Tokens.CacheCreateTokens
		out.ReasoningTokens += row.Tokens.ReasoningTokens
		out.TotalTokens += row.Tokens.TotalTokens
		out.CostUSD += row.CostUSD
	}
	return out
}

type jsonPayload struct {
	Sessions []model.SessionRow `json:"sessions"`
	Totals   *totalsOutput      `json:"totals"`
}

func buildJSONPayload(rows []model.SessionRow, verbose bool) jsonPayload {
	payload := jsonPayload{
		Sessions: make([]model.SessionRow, 0, len(rows)),
	}
	if len(rows) > 0 {
		t := computeTotals(rows)
		payload.Totals = &t
	}

	for _, row := range rows {
		if verbose {
			payload.Sessions = append(payload.Sessions, row)
			continue
		}
		clean := row
		clean.ModelUsage = nil
		clean.Models = filterCompactModels(row.Models)
		payload.Sessions = append(payload.Sessions, clean)
	}

	return payload
}

func filterCompactModels(models []string) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		switch m {
		case "", "<unknown>", "<synthetic>":
			continue
		default:
			out = append(out, m)
		}
	}
	return out
}
