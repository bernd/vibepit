package cli

import (
	"errors"
	"flag"
	"strings"

	"github.com/bernd/vibepit/tools/ccusage-go/internal/model"
)

// Options controls report generation.
type Options struct {
	Provider model.Provider
	JSON     bool
	Since    string
	Until    string
	Offline  bool
	Verbose  bool
}

// Parse parses and validates CLI flags.
func Parse(args []string) (Options, error) {
	fs := flag.NewFlagSet("ccusage-go", flag.ContinueOnError)
	var provider string
	var out Options

	fs.StringVar(&provider, "provider", string(model.ProviderAll), "claude|codex|all")
	fs.BoolVar(&out.JSON, "json", false, "emit JSON")
	fs.StringVar(&out.Since, "since", "", "YYYY-MM-DD or YYYYMMDD")
	fs.StringVar(&out.Until, "until", "", "YYYY-MM-DD or YYYYMMDD")
	fs.BoolVar(&out.Offline, "offline", false, "do not fetch remote pricing")
	fs.BoolVar(&out.Verbose, "verbose", false, "show detailed output")

	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}

	switch model.Provider(strings.ToLower(provider)) {
	case model.ProviderClaude:
		out.Provider = model.ProviderClaude
	case model.ProviderCodex:
		out.Provider = model.ProviderCodex
	case model.ProviderAll:
		out.Provider = model.ProviderAll
	default:
		return Options{}, errors.New("invalid --provider (expected claude, codex, or all)")
	}

	return out, nil
}
