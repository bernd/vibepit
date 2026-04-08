package keygen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestGenerateEd25519Keypair(t *testing.T) {
	priv, pub, err := GenerateEd25519Keypair()
	require.NoError(t, err)
	assert.NotEmpty(t, priv)
	assert.NotEmpty(t, pub)

	// Verify private key is parseable
	signer, err := ssh.ParsePrivateKey(priv)
	require.NoError(t, err)
	assert.Equal(t, "ssh-ed25519", signer.PublicKey().Type())

	// Verify public key is parseable
	_, _, _, _, err = ssh.ParseAuthorizedKey(pub)
	require.NoError(t, err)
}
