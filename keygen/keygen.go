package keygen

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// GenerateEd25519Keypair returns PEM-encoded private key and
// OpenSSH authorized_keys formatted public key.
func GenerateEd25519Keypair() ([]byte, []byte, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ed25519 key: %w", err)
	}

	privBytes, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal private key: %w", err)
	}
	privateKeyPEM := pem.EncodeToMemory(privBytes)

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, nil, fmt.Errorf("create ssh public key: %w", err)
	}
	publicKey := ssh.MarshalAuthorizedKey(sshPub)

	return privateKeyPEM, publicKey, nil
}
