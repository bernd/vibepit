package proxy

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

//go:embed model_prices.json
var modelPricesJSON []byte

type modelPricing struct {
	Input     float64 `json:"input_cost_per_token"`
	Output    float64 `json:"output_cost_per_token"`
	CacheRead float64 `json:"cache_read_input_token_cost"`
}

var modelPrices map[string]modelPricing

var dateSuffixRe = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

func init() {
	modelPrices = make(map[string]modelPricing)
	_ = json.Unmarshal(modelPricesJSON, &modelPrices)
}

// lookupPricing finds pricing for a model. It returns the pricing, the key
// that matched (which may differ from model when a fallback was used), and
// whether a match was found. It tries, in order:
//  1. Exact match
//  2. Strip trailing date suffix (-YYYY-MM-DD)
//  3. Strip -codex suffix (with and without date)
//  4. Decrement minor version one step at a time, preserving any suffix
//     (e.g. gpt-5.3-codex -> gpt-5.2-codex -> gpt-5.1-codex -> gpt-5-codex -> gpt-5)
func lookupPricing(model string) (modelPricing, string, bool) {
	if p, ok := modelPrices[model]; ok {
		return p, model, true
	}
	stripped := dateSuffixRe.ReplaceAllString(model, "")
	if stripped != model {
		if p, ok := modelPrices[stripped]; ok {
			return p, stripped, true
		}
	}
	// Try without -codex suffix.
	for _, base := range []string{model, stripped} {
		if len(base) > 6 && base[len(base)-6:] == "-codex" {
			noCodex := base[:len(base)-6]
			if p, ok := modelPrices[noCodex]; ok {
				return p, noCodex, true
			}
		}
	}
	// Decrement minor version, preserving suffix.
	// Split into versioned base and suffix: "gpt-5.3-codex" -> ("gpt-5.3", "-codex").
	if p, key, ok := lookupByVersion(stripped); ok {
		return p, key, true
	}
	return modelPricing{}, "", false
}

// lookupByVersion splits a model name into a versioned base and suffix,
// then decrements the minor version one step at a time looking for a match.
// For "gpt-5.3-codex" it tries: gpt-5.2-codex, gpt-5.1-codex, gpt-5-codex,
// then gpt-5.2, gpt-5.1, gpt-5 (without suffix).
func lookupByVersion(name string) (modelPricing, string, bool) {
	base, suffix := splitVersionSuffix(name)
	if base == name && suffix == "" {
		return modelPricing{}, "", false
	}

	// Try decrementing with suffix first, then without.
	suffixes := []string{suffix}
	if suffix != "" {
		suffixes = append(suffixes, "")
	}
	for _, sfx := range suffixes {
		cur := base
		for {
			prev, ok := decrementVersion(cur)
			if !ok {
				break
			}
			candidate := prev + sfx
			if p, ok := modelPrices[candidate]; ok {
				return p, candidate, true
			}
			cur = prev
		}
	}
	return modelPricing{}, "", false
}

// splitVersionSuffix splits "gpt-5.3-codex" into ("gpt-5.3", "-codex").
// If there's no dot-version segment, returns (name, "").
func splitVersionSuffix(name string) (base, suffix string) {
	// Walk backwards to find where the version ends and suffix begins.
	// A version segment is ".N" where N is digits. The suffix is everything
	// after the last version segment (e.g. "-codex", "-mini-codex").
	// We need to find the boundary between version and suffix.
	//
	// Strategy: find the last dot-digit segment. Everything after those digits
	// (if it's not another dot-digit) is the suffix.
	last := len(name)
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			// Check if everything from i+1 to last is digits.
			seg := name[i+1 : last]
			allDigits := len(seg) > 0
			for _, c := range seg {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return name[:last], name[last:]
			}
			// Not a version segment — this is part of the suffix.
			last = i
		} else if name[i] == '-' && i < last {
			last = i
		}
	}
	return name, ""
}

// decrementVersion takes "gpt-5.3" and returns ("gpt-5.2", true).
// For "gpt-5.0" or "gpt-5" it returns ("gpt-5", false) or strips the segment:
// "gpt-5.0" -> "gpt-5". For "gpt-5" (no dot version) it returns ("", false).
func decrementVersion(s string) (string, bool) {
	dot := strings.LastIndex(s, ".")
	if dot < 0 {
		return "", false
	}
	tail := s[dot+1:]
	// Parse the minor version number.
	v := 0
	for _, c := range tail {
		if c < '0' || c > '9' {
			return "", false
		}
		v = v*10 + int(c-'0')
	}
	if v > 0 {
		return fmt.Sprintf("%s.%d", s[:dot], v-1), true
	}
	// v == 0: drop this segment entirely (gpt-5.0 -> gpt-5).
	return s[:dot], true
}

// PricingSource returns the pricing entry used for a model. If the model
// matched exactly, source equals model. If a fallback was used (date strip,
// codex strip, minor version strip), source is the fallback key. Returns
// empty strings when no pricing is available.
func PricingSource(model string) (source string, ok bool) {
	_, source, ok = lookupPricing(model)
	return
}

// tokenCost calculates the total cost for a request given token counts.
// Cached tokens are priced at the cache-read rate instead of the input rate.
func tokenCost(model string, input, output, cached float64) float64 {
	p, _, ok := lookupPricing(model)
	if !ok {
		return 0
	}
	// Cached tokens are already included in input count for some providers,
	// but Codex reports them separately. Price cached at cache-read rate,
	// non-cached input at input rate.
	nonCachedInput := input - cached
	if nonCachedInput < 0 {
		nonCachedInput = 0
	}
	cost := nonCachedInput*p.Input + cached*p.CacheRead + output*p.Output
	return cost
}
