package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignArgsFromCommand(t *testing.T) {
	t.Run("sign request with agent", func(t *testing.T) {
		cmd := newSignCommand()
		err := cmd.Run(context.Background(), []string{
			"vibesign",
			"-Y", "sign",
			"-n", "git",
			"-f", "/tmp/key.pub",
			"-U",
			"-O", "hashalg=sha512",
			"/tmp/payload",
		})
		require.NoError(t, err)
		parsed := signArgsFromCommand(cmd)

		assert.Equal(t, "sign", parsed.operation)
		assert.Equal(t, "git", parsed.namespace)
		assert.Equal(t, "/tmp/key.pub", parsed.keyFile)
		assert.Equal(t, "/tmp/payload", parsed.payloadFile)
		assert.True(t, parsed.useAgent)
		assert.Equal(t, []string{"hashalg=sha512"}, parsed.options)
	})

	t.Run("rejects unknown option", func(t *testing.T) {
		cmd := newSignCommand()
		err := cmd.Run(context.Background(), []string{
			"vibesign",
			"-Y", "sign",
			"-n", "git",
			"-f", "/tmp/key.pub",
			"-Z",
			"/tmp/payload",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "-Z")
	})
}

func TestRunWritesSignatureFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.pub")
	payloadPath := filepath.Join(dir, "payload")

	require.NoError(t, os.WriteFile(keyPath, []byte("dummy\n"), 0o600))
	require.NoError(t, os.WriteFile(payloadPath, []byte("payload-to-sign"), 0o600))

	var received signRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(signResponse{
			Signature: "-----BEGIN SSH SIGNATURE-----\nFAKE\n-----END SSH SIGNATURE-----",
		}))
	}))
	t.Cleanup(srv.Close)

	getenv := func(name string) string {
		switch name {
		case envSignerURL:
			return srv.URL
		case envSignerToken:
			return "test-token"
		case envSignerTimeout:
			return "3s"
		default:
			return ""
		}
	}

	err := execute([]string{"-Y", "sign", "-n", "git", "-f", keyPath, "-U", payloadPath}, getenv, os.Stdout, os.Stderr)
	require.NoError(t, err)

	signature, err := os.ReadFile(payloadPath + ".sig")
	require.NoError(t, err)
	assert.Equal(t, "-----BEGIN SSH SIGNATURE-----\nFAKE\n-----END SSH SIGNATURE-----\n", string(signature))

	assert.Equal(t, "git", received.Namespace)
	assert.Equal(t, "payload-to-sign", decodePayload(t, received.Payload))
	assert.True(t, received.UseAgent)
}

func TestRunPubKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(publicKeyResponse{
			PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest comment",
		}))
	}))
	t.Cleanup(srv.Close)

	getenv := func(name string) string {
		switch name {
		case envPubKeyURL:
			return srv.URL
		case envSignerToken:
			return "test-token"
		case envSignerTimeout:
			return "3s"
		default:
			return ""
		}
	}

	var stdout bytes.Buffer
	err := execute([]string{"pubkey"}, getenv, &stdout, os.Stderr)
	require.NoError(t, err)
	assert.Equal(t, "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest comment\n", stdout.String())
}

func decodePayload(t *testing.T, payload string) string {
	t.Helper()

	decoded, err := base64.StdEncoding.DecodeString(payload)
	require.NoError(t, err)
	return string(decoded)
}
