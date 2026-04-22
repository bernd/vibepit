package cmd

import (
	"net"
	"testing"

	"github.com/bernd/vibepit/keygen"
	"github.com/bernd/vibepit/session"
	"github.com/bernd/vibepit/sshd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
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

// TestSSHRoundTripPreservesLiteralArguments boots a real sshd.Server on a
// loopback listener, runs a command built by buildRemoteCommand, and
// asserts that shell metacharacters ($HOME, $(uname), spaces) reach the
// remote program as literal arguments rather than being expanded or
// resplit by the remote shell.
func TestSSHRoundTripPreservesLiteralArguments(t *testing.T) {
	hostPriv, _, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)
	clientPriv, clientPub, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close() //nolint:errcheck

	srv, err := sshd.NewServer(sshd.Config{
		HostKeyPEM:    hostPriv,
		AuthorizedKey: clientPub,
		Sessions:      session.NewManager(50),
	})
	require.NoError(t, err)
	go srv.Serve(listener) //nolint:errcheck
	defer srv.Close()      //nolint:errcheck

	signer, err := gossh.ParsePrivateKey(clientPriv)
	require.NoError(t, err)

	client, err := gossh.Dial("tcp", listener.Addr().String(), &gossh.ClientConfig{
		User:            "code",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec
	})
	require.NoError(t, err)
	defer client.Close() //nolint:errcheck

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close() //nolint:errcheck

	wireCmd := buildRemoteCommand([]string{
		"printf", "%s\n", "a b", "$HOME", "$(uname)",
	})

	output, err := sess.Output(wireCmd)
	require.NoError(t, err)
	assert.Equal(t, "a b\n$HOME\n$(uname)\n", string(output))
}
