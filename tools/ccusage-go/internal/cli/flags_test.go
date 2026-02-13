package cli_test

import (
	"testing"

	"github.com/bernd/vibepit/tools/ccusage-go/internal/cli"
	"github.com/bernd/vibepit/tools/ccusage-go/internal/model"
)

func TestParseProvider(t *testing.T) {
	opts, err := cli.Parse([]string{"--provider", "codex"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if opts.Provider != model.ProviderCodex {
		t.Fatalf("provider = %q, want %q", opts.Provider, model.ProviderCodex)
	}
}

func TestParseVerboseFlag(t *testing.T) {
	opts, err := cli.Parse([]string{"--verbose"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !opts.Verbose {
		t.Fatalf("Verbose = false, want true")
	}
}
