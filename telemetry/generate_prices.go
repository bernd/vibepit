//go:build ignore

// This program downloads LiteLLM model pricing data and produces a reduced
// JSON file bundled into the proxy package via go:embed.
//
//go:generate go run generate_prices.go

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	sourceURL  = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	outputPath = "../proxy/model_prices.json"
)

type pricing struct {
	Input     float64 `json:"input_cost_per_token"`
	Output    float64 `json:"output_cost_per_token"`
	CacheRead float64 `json:"cache_read_input_token_cost,omitempty"`
}

func main() {
	resp, err := http.Get(sourceURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "download: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "download: status %d\n", resp.StatusCode)
		os.Exit(1)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(1)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		os.Exit(1)
	}

	result := map[string]pricing{}
	for key, val := range raw {
		// Skip provider-prefixed entries (contain / or :).
		if strings.Contains(key, "/") || strings.Contains(key, ":") {
			continue
		}
		// Skip metadata keys like "sample_spec".
		var entry map[string]any
		if err := json.Unmarshal(val, &entry); err != nil {
			continue
		}
		if _, ok := entry["input_cost_per_token"]; !ok {
			continue
		}

		p := pricing{}
		if v, ok := entry["input_cost_per_token"].(float64); ok {
			p.Input = v
		}
		if v, ok := entry["output_cost_per_token"].(float64); ok {
			p.Output = v
		}
		if v, ok := entry["cache_read_input_token_cost"].(float64); ok {
			p.CacheRead = v
		}
		if p.Input > 0 || p.Output > 0 {
			result[key] = p
		}
	}

	out, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(outputPath, out, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("wrote %d models to %s (%d bytes)\n", len(result), outputPath, len(out))
}
