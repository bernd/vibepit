package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildRemoteCommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "nil args", args: nil, want: ""},
		{name: "empty slice", args: []string{}, want: ""},
		{name: "single safe arg", args: []string{"ls"}, want: "ls"},
		{
			name: "two safe args",
			args: []string{"ls", "-la"},
			want: "ls -la",
		},
		{
			name: "arg with space is single-quoted",
			args: []string{"cat", "file with spaces.txt"},
			want: "cat 'file with spaces.txt'",
		},
		{
			name: "single quote inside arg is escaped",
			args: []string{"echo", "it's"},
			want: `echo 'it'\''s'`,
		},
		{
			name: "dollar var is quoted literally",
			args: []string{"echo", "$HOME"},
			want: "echo '$HOME'",
		},
		{
			name: "command substitution is quoted literally",
			args: []string{"echo", "$(uname)"},
			want: "echo '$(uname)'",
		},
		{
			name: "backtick substitution is quoted literally",
			args: []string{"echo", "`uname`"},
			want: "echo '`uname`'",
		},
		{
			name: "glob wildcard is quoted literally",
			args: []string{"echo", "*.go"},
			want: "echo '*.go'",
		},
		{
			name: "question mark glob is quoted literally",
			args: []string{"echo", "file?.txt"},
			want: "echo 'file?.txt'",
		},
		{
			name: "semicolon metacharacter is quoted literally",
			args: []string{"echo", "a;rm -rf /"},
			want: "echo 'a;rm -rf /'",
		},
		{
			name: "empty arg becomes empty quotes",
			args: []string{"printf", "%s\n", ""},
			want: "printf '%s\n' ''",
		},
		{
			name: "printf with mixed literal metacharacters",
			args: []string{"printf", "%s\n", "a b", "$HOME", "$(uname)"},
			want: "printf '%s\n' 'a b' '$HOME' '$(uname)'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, buildRemoteCommand(tt.args))
		})
	}
}
