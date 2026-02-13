package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

const LiteLLMURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// Entry captures the LiteLLM pricing fields used by this tool.
type Entry struct {
	InputPerToken                float64 `json:"input_cost_per_token"`
	OutputPerToken               float64 `json:"output_cost_per_token"`
	CacheReadPerToken            float64 `json:"cache_read_input_token_cost"`
	CacheCreatePerToken          float64 `json:"cache_creation_input_token_cost"`
	InputPerTokenAbove200k       float64 `json:"input_cost_per_token_above_200k_tokens"`
	OutputPerTokenAbove200k      float64 `json:"output_cost_per_token_above_200k_tokens"`
	CacheReadPerTokenAbove200k   float64 `json:"cache_read_input_token_cost_above_200k_tokens"`
	CacheCreatePerTokenAbove200k float64 `json:"cache_creation_input_token_cost_above_200k_tokens"`
}

// Client fetches and caches LiteLLM pricing data.
type Client struct {
	HTTP    *http.Client
	URL     string
	Offline bool

	cache map[string]Entry
}

// Fetch returns pricing data, using cache when available.
func (c *Client) Fetch(ctx context.Context) (map[string]Entry, error) {
	if c.cache != nil {
		return c.cache, nil
	}
	if c.Offline {
		c.cache = map[string]Entry{}
		return c.cache, nil
	}

	url := c.URL
	if strings.TrimSpace(url) == "" {
		url = LiteLLMURL
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.New("pricing fetch failed")
	}

	out := map[string]Entry{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}

	c.cache = out
	return out, nil
}

// ResolveModel finds the best pricing entry for a model.
func ResolveModel(dataset map[string]Entry, model string) (Entry, bool) {
	if len(dataset) == 0 {
		return Entry{}, false
	}

	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return Entry{}, false
	}

	if direct, ok := dataset[trimmed]; ok {
		return direct, true
	}

	alias := trimmed
	if trimmed == "gpt-5-codex" {
		alias = "gpt-5"
	}
	if direct, ok := dataset[alias]; ok {
		return direct, true
	}

	lower := strings.ToLower(alias)
	for k, v := range dataset {
		lk := strings.ToLower(k)
		if strings.Contains(lk, lower) || strings.Contains(lower, lk) {
			return v, true
		}
	}

	return Entry{}, false
}
