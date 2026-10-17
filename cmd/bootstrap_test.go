package cmd

import (
	"context"
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

func TestIgnoredSandboxFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "no flags", args: nil, want: nil},
		{name: "memory only", args: []string{"-m", "8g"}, want: []string{"--memory"}},
		{
			name: "several flags in definition order",
			args: []string{"-p", "pkg-go", "-a", "example.com:443", "-L", "-r", "--memory", "8g"},
			want: []string{"--local", "--allow", "--preset", "--memory", "--reconfigure"},
		},
	}
	for _, tt := range tests {
		for _, c := range []*cli.Command{RunCommand(), UpCommand()} {
			t.Run(tt.name+"/"+c.Name, func(t *testing.T) {
				var got []string
				c.Action = func(_ context.Context, cmd *cli.Command) error {
					got = ignoredSandboxFlags(cmd)
					return nil
				}
				require.NoError(t, c.Run(context.Background(), append([]string{c.Name}, tt.args...)))
				assert.Equal(t, tt.want, got)
			})
		}
	}
}
