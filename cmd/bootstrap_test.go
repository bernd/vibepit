package cmd

import (
	"testing"

	ctr "github.com/bernd/vibepit/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestSandboxFlags_Memory(t *testing.T) {
	for _, c := range []*cli.Command{RunCommand(), UpCommand()} {
		t.Run(c.Name, func(t *testing.T) {
			var found cli.Flag
			for _, f := range c.Flags {
				if f.Names()[0] == memoryFlag {
					found = f
				}
			}
			require.NotNil(t, found, "memory flag not registered")
			assert.Contains(t, found.Names(), "m")
		})
	}
}

func TestBaseSandboxConfig_PropagatesMemoryLimit(t *testing.T) {
	infra := &sessionInfra{
		SessionID:   "abc",
		NetworkInfo: ctr.NetworkInfo{ID: "net"},
		MemoryLimit: 8 << 30,
	}

	cfg := infra.baseSandboxConfig("/proj", &userInfo{Username: "code"})

	assert.Equal(t, int64(8<<30), cfg.Memory)
}
