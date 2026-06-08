package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/bernd/vibepit/keygen"
	"github.com/bernd/vibepit/session"
	"github.com/bernd/vibepit/sshd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// exitStatusErr mimics *ssh.ExitError, whose fields are unexported and so
// cannot be constructed directly. isSandboxDisconnect matches on the
// ExitStatus() method, which the real ssh error also satisfies.
type exitStatusErr struct{ code int }

func (e exitStatusErr) Error() string   { return fmt.Sprintf("exited with %d", e.code) }
func (e exitStatusErr) ExitStatus() int { return e.code }

func TestIsSandboxDisconnect(t *testing.T) {
	t.Run("disconnect exit code matches", func(t *testing.T) {
		assert.True(t, isSandboxDisconnect(exitStatusErr{sshd.DisconnectExitCode}))
	})
	t.Run("wrapped disconnect exit code matches", func(t *testing.T) {
		err := fmt.Errorf("ssh: %w", exitStatusErr{sshd.DisconnectExitCode})
		assert.True(t, isSandboxDisconnect(err))
	})
	t.Run("clean exit does not match", func(t *testing.T) {
		assert.False(t, isSandboxDisconnect(exitStatusErr{0}))
	})
	t.Run("other non-zero exit does not match", func(t *testing.T) {
		assert.False(t, isSandboxDisconnect(exitStatusErr{1}))
	})
	t.Run("non-exit error does not match", func(t *testing.T) {
		assert.False(t, isSandboxDisconnect(errors.New("connection reset")))
	})
	t.Run("nil does not match", func(t *testing.T) {
		assert.False(t, isSandboxDisconnect(nil))
	})
}

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
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
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

type fakeTransport struct {
	sendRequestFn func(name string, wantReply bool, payload []byte) (bool, []byte, error)
	closed        bool
}

func (f *fakeTransport) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	return f.sendRequestFn(name, wantReply, payload)
}

func (f *fakeTransport) Close() error {
	f.closed = true
	return nil
}

func TestHandleLastExit(t *testing.T) {
	makeReply := func(ptyConns, attached, detached, exec uint32, info string) []byte {
		reply := sshd.SessionCountReply{
			PTYConns:     ptyConns,
			AttachedPTY:  attached,
			DetachedPTY:  detached,
			ExecCount:    exec,
			DetachedInfo: info,
		}
		return gossh.Marshal(reply)
	}

	t.Run("not a TTY skips request", func(t *testing.T) {
		requestSent := false
		transport := &fakeTransport{
			sendRequestFn: func(string, bool, []byte) (bool, []byte, error) {
				requestSent = true
				return true, nil, nil
			},
		}
		err := handleLastExit(handleLastExitParams{
			transport:  transport,
			stdin:      strings.NewReader(""),
			stderr:     io.Discard,
			isTerminal: false,
			shutdownFn: func() error { return nil },
		})
		assert.NoError(t, err)
		assert.False(t, requestSent)
	})

	t.Run("SendRequest error exits silently", func(t *testing.T) {
		transport := &fakeTransport{
			sendRequestFn: func(string, bool, []byte) (bool, []byte, error) {
				return false, nil, fmt.Errorf("connection reset")
			},
		}
		err := handleLastExit(handleLastExitParams{
			transport:  transport,
			stdin:      strings.NewReader(""),
			stderr:     io.Discard,
			isTerminal: true,
			shutdownFn: func() error { return nil },
		})
		assert.NoError(t, err)
		assert.True(t, transport.closed)
	})

	t.Run("SendRequest ok=false exits silently", func(t *testing.T) {
		transport := &fakeTransport{
			sendRequestFn: func(string, bool, []byte) (bool, []byte, error) {
				return false, nil, nil
			},
		}
		err := handleLastExit(handleLastExitParams{
			transport:  transport,
			stdin:      strings.NewReader(""),
			stderr:     io.Discard,
			isTerminal: true,
			shutdownFn: func() error { return nil },
		})
		assert.NoError(t, err)
		assert.True(t, transport.closed)
	})

	t.Run("PTYConns > 0 no prompt", func(t *testing.T) {
		transport := &fakeTransport{
			sendRequestFn: func(string, bool, []byte) (bool, []byte, error) {
				return true, makeReply(2, 0, 0, 0, ""), nil
			},
		}
		var stderr bytes.Buffer
		err := handleLastExit(handleLastExitParams{
			transport:  transport,
			stdin:      strings.NewReader(""),
			stderr:     &stderr,
			isTerminal: true,
			shutdownFn: func() error { return nil },
		})
		assert.NoError(t, err)
		assert.Empty(t, stderr.String())
	})

	t.Run("ExecCount > 0 no prompt", func(t *testing.T) {
		transport := &fakeTransport{
			sendRequestFn: func(string, bool, []byte) (bool, []byte, error) {
				return true, makeReply(0, 0, 0, 1, ""), nil
			},
		}
		var stderr bytes.Buffer
		err := handleLastExit(handleLastExitParams{
			transport:  transport,
			stdin:      strings.NewReader(""),
			stderr:     &stderr,
			isTerminal: true,
			shutdownFn: func() error { return nil },
		})
		assert.NoError(t, err)
		assert.Empty(t, stderr.String())
	})

	t.Run("last exit user types n", func(t *testing.T) {
		transport := &fakeTransport{
			sendRequestFn: func(string, bool, []byte) (bool, []byte, error) {
				return true, makeReply(0, 0, 1, 0, "session-1\tshell\t3m"), nil
			},
		}
		shutdownCalled := false
		var stderr bytes.Buffer
		err := handleLastExit(handleLastExitParams{
			transport:  transport,
			stdin:      strings.NewReader("n\n"),
			stderr:     &stderr,
			isTerminal: true,
			shutdownFn: func() error { shutdownCalled = true; return nil },
		})
		assert.NoError(t, err)
		assert.False(t, shutdownCalled)
		assert.Contains(t, stderr.String(), "last connection")
		assert.Contains(t, stderr.String(), "session-1")
	})

	t.Run("last exit user types y", func(t *testing.T) {
		transport := &fakeTransport{
			sendRequestFn: func(string, bool, []byte) (bool, []byte, error) {
				return true, makeReply(0, 0, 0, 0, ""), nil
			},
		}
		shutdownCalled := false
		err := handleLastExit(handleLastExitParams{
			transport:  transport,
			stdin:      strings.NewReader("y\n"),
			stderr:     io.Discard,
			isTerminal: true,
			shutdownFn: func() error { shutdownCalled = true; return nil },
		})
		assert.NoError(t, err)
		assert.True(t, shutdownCalled)
	})

	t.Run("last exit user types yes", func(t *testing.T) {
		transport := &fakeTransport{
			sendRequestFn: func(string, bool, []byte) (bool, []byte, error) {
				return true, makeReply(0, 0, 0, 0, ""), nil
			},
		}
		shutdownCalled := false
		err := handleLastExit(handleLastExitParams{
			transport:  transport,
			stdin:      strings.NewReader("YES\n"),
			stderr:     io.Discard,
			isTerminal: true,
			shutdownFn: func() error { shutdownCalled = true; return nil },
		})
		assert.NoError(t, err)
		assert.True(t, shutdownCalled)
	})

	t.Run("stdin EOF means no", func(t *testing.T) {
		transport := &fakeTransport{
			sendRequestFn: func(string, bool, []byte) (bool, []byte, error) {
				return true, makeReply(0, 0, 0, 0, ""), nil
			},
		}
		shutdownCalled := false
		err := handleLastExit(handleLastExitParams{
			transport:  transport,
			stdin:      strings.NewReader(""),
			stderr:     io.Discard,
			isTerminal: true,
			shutdownFn: func() error { shutdownCalled = true; return nil },
		})
		assert.NoError(t, err)
		assert.False(t, shutdownCalled)
	})

	t.Run("shutdownFn error is propagated", func(t *testing.T) {
		transport := &fakeTransport{
			sendRequestFn: func(string, bool, []byte) (bool, []byte, error) {
				return true, makeReply(0, 0, 0, 0, ""), nil
			},
		}
		err := handleLastExit(handleLastExitParams{
			transport:  transport,
			stdin:      strings.NewReader("y\n"),
			stderr:     io.Discard,
			isTerminal: true,
			shutdownFn: func() error { return fmt.Errorf("down failed") },
		})
		assert.EqualError(t, err, "down failed")
	})

	t.Run("detached info rendering", func(t *testing.T) {
		transport := &fakeTransport{
			sendRequestFn: func(string, bool, []byte) (bool, []byte, error) {
				return true, makeReply(0, 0, 2, 0, "session-1\tshell\t3m\nsession-2\tbash\t1h2m"), nil
			},
		}
		var stderr bytes.Buffer
		err := handleLastExit(handleLastExitParams{
			transport:  transport,
			stdin:      strings.NewReader("n\n"),
			stderr:     &stderr,
			isTerminal: true,
			shutdownFn: func() error { return nil },
		})
		assert.NoError(t, err)
		output := stderr.String()
		assert.Contains(t, output, "2 detached session(s) will be killed:")
		assert.Contains(t, output, "session-1")
		assert.Contains(t, output, "session-2")
		assert.Contains(t, output, "shell")
		assert.Contains(t, output, "1h2m")
	})
}
