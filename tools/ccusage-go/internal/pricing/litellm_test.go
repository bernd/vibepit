package pricing_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bernd/vibepit/tools/ccusage-go/internal/pricing"
)

func TestFetchLoadsDataset(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"gpt-5":{"input_cost_per_token":1.25e-6,"output_cost_per_token":1e-5,"cache_read_input_token_cost":1.25e-7}}`))
	}))
	defer ts.Close()

	c := pricing.Client{
		HTTP: &http.Client{},
		URL:  ts.URL,
	}

	dataset, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if _, ok := dataset["gpt-5"]; !ok {
		t.Fatalf("dataset missing gpt-5")
	}
}

func TestFetchLoadsTieredFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"claude-4-sonnet-20250514":{"input_cost_per_token":3e-6,"output_cost_per_token":1.5e-5,"input_cost_per_token_above_200k_tokens":6e-6,"output_cost_per_token_above_200k_tokens":2.25e-5}}`))
	}))
	defer ts.Close()

	c := pricing.Client{
		HTTP: &http.Client{},
		URL:  ts.URL,
	}

	dataset, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	entry := dataset["claude-4-sonnet-20250514"]
	if entry.InputPerTokenAbove200k != 6e-6 {
		t.Fatalf("input above 200k = %g, want 6e-6", entry.InputPerTokenAbove200k)
	}
	if entry.OutputPerTokenAbove200k != 2.25e-5 {
		t.Fatalf("output above 200k = %g, want 2.25e-5", entry.OutputPerTokenAbove200k)
	}
}

func TestResolveModelAlias(t *testing.T) {
	dataset := map[string]pricing.Entry{"gpt-5": {InputPerToken: 1.25e-6}}
	_, ok := pricing.ResolveModel(dataset, "gpt-5-codex")
	if !ok {
		t.Fatalf("ResolveModel() did not resolve alias")
	}
}

func TestResolveModelFuzzyMatch(t *testing.T) {
	dataset := map[string]pricing.Entry{"openai/gpt-5": {InputPerToken: 1.25e-6}}
	_, ok := pricing.ResolveModel(dataset, "gpt-5")
	if !ok {
		t.Fatalf("ResolveModel() did not resolve fuzzy match")
	}
}
