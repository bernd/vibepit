package model

// Provider identifies usage source.
type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
	ProviderAll    Provider = "all"
)

// TokenUsage stores usage counters in a normalized shape.
type TokenUsage struct {
	InputTokens       int64 `json:"inputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
	CacheReadTokens   int64 `json:"cacheReadTokens"`
	CacheCreateTokens int64 `json:"cacheCreateTokens"`
	ReasoningTokens   int64 `json:"reasoningTokens,omitempty"`
	TotalTokens       int64 `json:"totalTokens"`
}

// ModelUsage stores per-model usage and metadata.
type ModelUsage struct {
	Name            string      `json:"name"`
	Usage           TokenUsage  `json:"usage"`
	ExplicitUsage   *TokenUsage `json:"explicitUsage,omitempty"`
	ExplicitCostUSD float64     `json:"explicitCostUSD,omitempty"`
	HasExplicitCost bool        `json:"hasExplicitCost,omitempty"`
	IsFallback      bool        `json:"isFallback,omitempty"`
}

// SessionAggregate is provider-native aggregated data before pricing.
type SessionAggregate struct {
	Provider        Provider               `json:"provider"`
	SessionID       string                 `json:"sessionId"`
	ProjectOrDir    string                 `json:"projectOrDir"`
	LastActivity    string                 `json:"lastActivity"`
	Models          map[string]*ModelUsage `json:"models"`
	ExplicitCostUSD float64                `json:"explicitCostUSD"`
}

// SessionRow is rendered/serialized output with total cost and totals.
type SessionRow struct {
	Provider     Provider              `json:"provider"`
	SessionID    string                `json:"sessionId"`
	ProjectOrDir string                `json:"projectOrDir"`
	LastActivity string                `json:"lastActivity"`
	Models       []string              `json:"models"`
	ModelUsage   map[string]ModelUsage `json:"modelUsage,omitempty"`
	Tokens       TokenUsage            `json:"tokens"`
	CostUSD      float64               `json:"costUSD"`
}
