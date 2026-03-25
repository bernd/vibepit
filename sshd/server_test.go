package sshd

import (
	"net"
	"testing"

	"github.com/bernd/vibepit/keygen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

func TestServerAcceptsAuthorizedKey(t *testing.T) {
	hostPriv, _, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)
	clientPriv, clientPub, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close() //nolint:errcheck

	srv, err := NewServer(Config{
		HostKeyPEM:    hostPriv,
		AuthorizedKey: clientPub,
	})
	require.NoError(t, err)
	go srv.Serve(listener) //nolint:errcheck
	defer srv.Close() //nolint:errcheck

	signer, err := gossh.ParsePrivateKey(clientPriv)
	require.NoError(t, err)

	client, err := gossh.Dial("tcp", listener.Addr().String(), &gossh.ClientConfig{
		User:            "code",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec
	})
	require.NoError(t, err)
	defer client.Close() //nolint:errcheck

	session, err := client.NewSession()
	require.NoError(t, err)
	defer session.Close() //nolint:errcheck

	output, err := session.Output("echo hello")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(output))
}

func TestServerRejectsUnauthorizedKey(t *testing.T) {
	hostPriv, _, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)
	_, clientPub, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)
	unauthorizedPriv, _, err := keygen.GenerateEd25519Keypair()
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close() //nolint:errcheck

	srv, err := NewServer(Config{
		HostKeyPEM:    hostPriv,
		AuthorizedKey: clientPub,
	})
	require.NoError(t, err)
	go srv.Serve(listener) //nolint:errcheck
	defer srv.Close() //nolint:errcheck

	signer, err := gossh.ParsePrivateKey(unauthorizedPriv)
	require.NoError(t, err)

	_, err = gossh.Dial("tcp", listener.Addr().String(), &gossh.ClientConfig{
		User:            "code",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec
	})
	require.Error(t, err)
}
