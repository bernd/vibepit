package report

import (
	"sort"
	"strings"

	"github.com/bernd/vibepit/tools/ccusage-go/internal/model"
	"github.com/bernd/vibepit/tools/ccusage-go/internal/pricing"
)

// BuildSessionRows converts provider aggregates into rendered rows.
func BuildSessionRows(
	sessions []model.SessionAggregate,
	pricingDataset map[string]pricing.Entry,
	since string,
	until string,
) []model.SessionRow {
	normSince := normalizeDate(since)
	normUntil := normalizeDate(until)

	rows := make([]model.SessionRow, 0, len(sessions))
	for _, s := range sessions {
		if !withinRange(s.LastActivity, normSince, normUntil) {
			continue
		}

		row := model.SessionRow{
			Provider:     s.Provider,
			SessionID:    s.SessionID,
			ProjectOrDir: s.ProjectOrDir,
			LastActivity: s.LastActivity,
			ModelUsage:   map[string]model.ModelUsage{},
		}

		modelNames := make([]string, 0, len(s.Models))
		for modelName, usagePtr := range s.Models {
			if usagePtr == nil {
				continue
			}
			u := *usagePtr
			row.ModelUsage[modelName] = u
			modelNames = append(modelNames, modelName)

			row.Tokens.InputTokens += u.Usage.InputTokens
			row.Tokens.OutputTokens += u.Usage.OutputTokens
			row.Tokens.CacheReadTokens += u.Usage.CacheReadTokens
			row.Tokens.CacheCreateTokens += u.Usage.CacheCreateTokens
			row.Tokens.ReasoningTokens += u.Usage.ReasoningTokens
			row.Tokens.TotalTokens += u.Usage.TotalTokens

			entry, ok := pricing.ResolveModel(pricingDataset, modelName)
			if u.HasExplicitCost {
				row.CostUSD += u.ExplicitCostUSD
				if !ok || u.ExplicitUsage == nil {
					continue
				}
				residual := subtractUsage(u.Usage, *u.ExplicitUsage)
				row.CostUSD += calculateCost(s.Provider, residual, entry)
				continue
			}
			if ok {
				row.CostUSD += calculateCost(s.Provider, u.Usage, entry)
			}
		}

		sort.Strings(modelNames)
		row.Models = modelNames
		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].LastActivity == rows[j].LastActivity {
			return rows[i].SessionID < rows[j].SessionID
		}
		return rows[i].LastActivity < rows[j].LastActivity
	})

	return rows
}

func calculateCost(provider model.Provider, tokens model.TokenUsage, entry pricing.Entry) float64 {
	if provider == model.ProviderClaude {
		return calculateClaudeCost(tokens, entry)
	}
	return calculateCodexCost(tokens, entry)
}

func calculateClaudeCost(tokens model.TokenUsage, entry pricing.Entry) float64 {
	return calculateTieredCost(tokens.InputTokens, entry.InputPerToken, entry.InputPerTokenAbove200k) +
		calculateTieredCost(tokens.OutputTokens, entry.OutputPerToken, entry.OutputPerTokenAbove200k) +
		calculateTieredCost(tokens.CacheCreateTokens, entry.CacheCreatePerToken, entry.CacheCreatePerTokenAbove200k) +
		calculateTieredCost(tokens.CacheReadTokens, entry.CacheReadPerToken, entry.CacheReadPerTokenAbove200k)
}

func calculateCodexCost(tokens model.TokenUsage, entry pricing.Entry) float64 {
	nonCachedInput := tokens.InputTokens - tokens.CacheReadTokens
	if nonCachedInput < 0 {
		nonCachedInput = 0
	}
	cachedInput := tokens.CacheReadTokens
	if cachedInput < 0 {
		cachedInput = 0
	}
	if cachedInput > tokens.InputTokens {
		cachedInput = tokens.InputTokens
	}
	cacheReadPrice := entry.CacheReadPerToken
	if cacheReadPrice == 0 {
		cacheReadPrice = entry.InputPerToken
	}
	return float64(nonCachedInput)*entry.InputPerToken +
		float64(cachedInput)*cacheReadPrice +
		float64(tokens.OutputTokens)*entry.OutputPerToken
}

func calculateTieredCost(tokens int64, basePrice float64, tieredPrice float64) float64 {
	if tokens <= 0 {
		return 0
	}
	const threshold int64 = 200_000
	if tokens > threshold && tieredPrice != 0 {
		below := threshold
		above := tokens - threshold
		return float64(below)*basePrice + float64(above)*tieredPrice
	}
	return float64(tokens) * basePrice
}

func subtractUsage(total, explicit model.TokenUsage) model.TokenUsage {
	return model.TokenUsage{
		InputTokens:       max(total.InputTokens-explicit.InputTokens, 0),
		OutputTokens:      max(total.OutputTokens-explicit.OutputTokens, 0),
		CacheReadTokens:   max(total.CacheReadTokens-explicit.CacheReadTokens, 0),
		CacheCreateTokens: max(total.CacheCreateTokens-explicit.CacheCreateTokens, 0),
		ReasoningTokens:   max(total.ReasoningTokens-explicit.ReasoningTokens, 0),
		TotalTokens:       max(total.TotalTokens-explicit.TotalTokens, 0),
	}
}

func normalizeDate(v string) string {
	x := strings.TrimSpace(v)
	if x == "" {
		return ""
	}
	x = strings.ReplaceAll(x, "-", "")
	if len(x) != 8 {
		return ""
	}
	return x[:4] + "-" + x[4:6] + "-" + x[6:8]
}

func withinRange(date, since, until string) bool {
	if strings.TrimSpace(date) == "" {
		return false
	}
	if since != "" && date < since {
		return false
	}
	if until != "" && date > until {
		return false
	}
	return true
}
