package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookupPricing(t *testing.T) {
	t.Run("exact match", func(t *testing.T) {
		p, source, ok := lookupPricing("o3")
		require.True(t, ok)
		assert.Equal(t, "o3", source)
		assert.Greater(t, p.Input, 0.0)
		assert.Greater(t, p.Output, 0.0)
	})

	t.Run("date suffix fallback", func(t *testing.T) {
		p, source, ok := lookupPricing("o3-2099-01-01")
		require.True(t, ok)
		assert.Equal(t, "o3", source)
		assert.Greater(t, p.Input, 0.0)
	})

	t.Run("codex suffix fallback", func(t *testing.T) {
		p, source, ok := lookupPricing("o3-codex")
		require.True(t, ok)
		assert.Equal(t, "o3", source)
		assert.Greater(t, p.Input, 0.0)
	})

	t.Run("codex and date suffix fallback", func(t *testing.T) {
		p, source, ok := lookupPricing("o4-mini-codex-2099-01-01")
		require.True(t, ok)
		assert.Equal(t, "o4-mini", source)
		assert.Greater(t, p.Input, 0.0)
	})

	t.Run("minor version decrements to nearest match", func(t *testing.T) {
		// gpt-5.3-codex -> gpt-5.2-codex (exists in pricing data).
		p, source, ok := lookupPricing("gpt-5.3-codex")
		require.True(t, ok)
		assert.Equal(t, "gpt-5.2-codex", source)
		assert.Greater(t, p.Input, 0.0)
	})

	t.Run("minor version with date fallback", func(t *testing.T) {
		p, source, ok := lookupPricing("gpt-5.3-codex-2099-01-01")
		require.True(t, ok)
		assert.Equal(t, "gpt-5.2-codex", source)
		assert.Greater(t, p.Input, 0.0)
	})

	t.Run("unknown model", func(t *testing.T) {
		_, _, ok := lookupPricing("nonexistent-model-xyz")
		assert.False(t, ok)
	})
}

func TestPricingSource(t *testing.T) {
	t.Run("exact match returns model", func(t *testing.T) {
		source, ok := PricingSource("o3")
		require.True(t, ok)
		assert.Equal(t, "o3", source)
	})

	t.Run("fallback returns matched key", func(t *testing.T) {
		source, ok := PricingSource("gpt-5.3-codex")
		require.True(t, ok)
		assert.Equal(t, "gpt-5.2-codex", source)
	})

	t.Run("unknown returns empty", func(t *testing.T) {
		_, ok := PricingSource("nonexistent")
		assert.False(t, ok)
	})
}

func TestSplitVersionSuffix(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantBase     string
		wantSuffix   string
	}{
		{"no version", "o3", "o3", ""},
		{"simple version", "gpt-5.3", "gpt-5.3", ""},
		{"version with suffix", "gpt-5.3-codex", "gpt-5.3", "-codex"},
		{"no dot version with suffix", "o4-mini-codex", "o4-mini-codex", ""},
		{"no dot version", "o4-mini", "o4-mini", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, suffix := splitVersionSuffix(tt.input)
			assert.Equal(t, tt.wantBase, base)
			assert.Equal(t, tt.wantSuffix, suffix)
		})
	}
}

func TestDecrementVersion(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantOK  bool
	}{
		{"gpt-5.3", "gpt-5.2", true},
		{"gpt-5.1", "gpt-5.0", true},
		{"gpt-5.0", "gpt-5", true},
		{"gpt-5", "", false},
		{"o3", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := decrementVersion(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestTokenCost(t *testing.T) {
	t.Run("calculates cost from tokens", func(t *testing.T) {
		// o3: input=2e-6, output=8e-6, cache_read=5e-7
		cost := tokenCost("o3", 1000, 500, 800)
		// non-cached input: 200 * 2e-6 = 0.0004
		// cached: 800 * 5e-7 = 0.0004
		// output: 500 * 8e-6 = 0.004
		expected := 200*2e-6 + 800*5e-7 + 500*8e-6
		assert.InDelta(t, expected, cost, 1e-10)
	})

	t.Run("unknown model returns zero", func(t *testing.T) {
		cost := tokenCost("nonexistent", 1000, 500, 0)
		assert.Equal(t, 0.0, cost)
	})

	t.Run("cached exceeds input clamps to zero", func(t *testing.T) {
		cost := tokenCost("o3", 100, 500, 200)
		// non-cached input clamped to 0
		expected := 0*2e-6 + 200*5e-7 + 500*8e-6
		assert.InDelta(t, expected, cost, 1e-10)
	})
}
