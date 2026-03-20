package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignArgsParsing(t *testing.T) {
	t.Run("sign request with agent", func(t *testing.T) {
		mockOriginRemote(t, "git@github.com:example/repo.git")

		dir := t.TempDir()
		keyPath := filepath.Join(dir, "key.pub")
		payloadPath := filepath.Join(dir, "payload")
		require.NoError(t, os.WriteFile(keyPath, []byte("dummy\n"), 0o600))
		require.NoError(t, os.WriteFile(payloadPath, []byte("data"), 0o600))

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(signResponse{
				Signature: "-----BEGIN SSH SIGNATURE-----\nFAKE\n-----END SSH SIGNATURE-----",
			})
		}))
		t.Cleanup(srv.Close)

		getenv := envMap(map[string]string{envSignerURL: srv.URL})
		err := execute([]string{
			"-Y", "sign", "-n", "git", "-f", keyPath, "-U", "-O", "hashalg=sha512", payloadPath,
		}, getenv, &bytes.Buffer{}, &bytes.Buffer{})
		require.NoError(t, err)
	})

	t.Run("rejects unknown option", func(t *testing.T) {
		var stderr bytes.Buffer
		err := execute([]string{
			"-Y", "sign", "-n", "git", "-f", "/tmp/key.pub", "-Z", "/tmp/payload",
		}, noenv, &bytes.Buffer{}, &stderr)
		require.Error(t, err)
	})
}

func TestValidateSignArgs(t *testing.T) {
	t.Run("missing operation", func(t *testing.T) {
		err := validateSignArgs(&signerArgs{namespace: "git", keyFile: "k", payloadFile: "p"}, 1)
		assert.EqualError(t, err, "missing -Y operation")
	})

	t.Run("missing namespace", func(t *testing.T) {
		err := validateSignArgs(&signerArgs{operation: "sign", keyFile: "k", payloadFile: "p"}, 1)
		assert.EqualError(t, err, "missing -n namespace")
	})

	t.Run("missing key file", func(t *testing.T) {
		err := validateSignArgs(&signerArgs{operation: "sign", namespace: "git", payloadFile: "p"}, 1)
		assert.EqualError(t, err, "missing -f key file")
	})

	t.Run("missing payload file", func(t *testing.T) {
		err := validateSignArgs(&signerArgs{operation: "sign", namespace: "git", keyFile: "k"}, 1)
		assert.EqualError(t, err, "missing payload file")
	})

	t.Run("multiple payload files", func(t *testing.T) {
		err := validateSignArgs(&signerArgs{operation: "sign", namespace: "git", keyFile: "k", payloadFile: "p"}, 2)
		assert.EqualError(t, err, "multiple payload files provided")
	})
}

func TestRunWritesSignatureFile(t *testing.T) {
	mockOriginRemote(t, "git@github.com:example/repo.git")

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

	getenv := envMap(map[string]string{
		envSignerURL:     srv.URL,
		envSignerToken:   "test-token",
		envSignerTimeout: "3s",
	})

	err := execute([]string{"-Y", "sign", "-n", "git", "-f", keyPath, "-U", payloadPath}, getenv, os.Stdout, os.Stderr)
	require.NoError(t, err)

	signature, err := os.ReadFile(payloadPath + ".sig")
	require.NoError(t, err)
	assert.Equal(t, "-----BEGIN SSH SIGNATURE-----\nFAKE\n-----END SSH SIGNATURE-----\n", string(signature))

	assert.Equal(t, "git", received.Namespace)
	assert.Equal(t, "payload-to-sign", decodePayload(t, received.Payload))
	assert.Equal(t, "git@github.com:example/repo.git", received.OriginRemote)
	assert.True(t, received.UseAgent)
}

func TestRunSignPlainTextResponse(t *testing.T) {
	mockOriginRemote(t, "https://github.com/example/repo.git")

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.pub")
	payloadPath := filepath.Join(dir, "payload")

	require.NoError(t, os.WriteFile(keyPath, []byte("dummy\n"), 0o600))
	require.NoError(t, os.WriteFile(payloadPath, []byte("data"), 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, err := fmt.Fprint(w, "-----BEGIN SSH SIGNATURE-----\nPLAIN\n-----END SSH SIGNATURE-----")
		require.NoError(t, err)
	}))
	t.Cleanup(srv.Close)

	getenv := envMap(map[string]string{envSignerURL: srv.URL})
	err := execute([]string{"-Y", "sign", "-n", "git", "-f", keyPath, payloadPath}, getenv, &bytes.Buffer{}, &bytes.Buffer{})
	require.NoError(t, err)

	sig, err := os.ReadFile(payloadPath + ".sig")
	require.NoError(t, err)
	assert.Contains(t, string(sig), "-----BEGIN SSH SIGNATURE-----")
}

func TestRunSignServerError(t *testing.T) {
	mockOriginRemote(t, "git@github.com:example/repo.git")

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.pub")
	payloadPath := filepath.Join(dir, "payload")

	require.NoError(t, os.WriteFile(keyPath, []byte("dummy\n"), 0o600))
	require.NoError(t, os.WriteFile(payloadPath, []byte("data"), 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal failure", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	getenv := envMap(map[string]string{envSignerURL: srv.URL})
	err := execute([]string{"-Y", "sign", "-n", "git", "-f", keyPath, payloadPath}, getenv, &bytes.Buffer{}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.Contains(t, err.Error(), "internal failure")
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

	getenv := envMap(map[string]string{
		envPubKeyURL:     srv.URL,
		envSignerToken:   "test-token",
		envSignerTimeout: "3s",
	})

	var stdout bytes.Buffer
	err := execute([]string{"pubkey"}, getenv, &stdout, os.Stderr)
	require.NoError(t, err)
	assert.Equal(t, "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest comment\n", stdout.String())
}

func TestRunPubKeyPlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, err := fmt.Fprint(w, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest comment")
		require.NoError(t, err)
	}))
	t.Cleanup(srv.Close)

	getenv := envMap(map[string]string{envPubKeyURL: srv.URL})
	var stdout bytes.Buffer
	err := execute([]string{"pubkey"}, getenv, &stdout, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest comment\n", stdout.String())
}

func TestRunPubKeyServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	getenv := envMap(map[string]string{envPubKeyURL: srv.URL})
	err := execute([]string{"pubkey"}, getenv, &bytes.Buffer{}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

func TestLoadTimeout(t *testing.T) {
	t.Run("empty returns default", func(t *testing.T) {
		d, err := loadTimeout("")
		require.NoError(t, err)
		assert.Equal(t, defaultTimeout, d)
	})

	t.Run("whitespace returns default", func(t *testing.T) {
		d, err := loadTimeout("  ")
		require.NoError(t, err)
		assert.Equal(t, defaultTimeout, d)
	})

	t.Run("valid duration", func(t *testing.T) {
		d, err := loadTimeout("5s")
		require.NoError(t, err)
		assert.Equal(t, 5*1e9, float64(d))
	})

	t.Run("zero rejected", func(t *testing.T) {
		_, err := loadTimeout("0s")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be positive")
	})

	t.Run("negative rejected", func(t *testing.T) {
		_, err := loadTimeout("-1s")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be positive")
	})

	t.Run("invalid format", func(t *testing.T) {
		_, err := loadTimeout("notaduration")
		require.Error(t, err)
		assert.Contains(t, err.Error(), envSignerTimeout)
	})
}

func TestReadBounded(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		data, err := readBounded(strings.NewReader("hello"), 100)
		require.NoError(t, err)
		assert.Equal(t, "hello", string(data))
	})

	t.Run("exactly at limit", func(t *testing.T) {
		data, err := readBounded(strings.NewReader("hello"), 5)
		require.NoError(t, err)
		assert.Equal(t, "hello", string(data))
	})

	t.Run("exceeds limit", func(t *testing.T) {
		_, err := readBounded(strings.NewReader("hello world"), 5)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds")
	})
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", truncate("short", 100))
	assert.Equal(t, "abc...", truncate("abcdef", 3))
	assert.Equal(t, "ab...", truncate("abcdef", 2))
}

func TestLooksLikeSSHSignature(t *testing.T) {
	assert.True(t, looksLikeSSHSignature("-----BEGIN SSH SIGNATURE-----\ndata\n-----END SSH SIGNATURE-----"))
	assert.False(t, looksLikeSSHSignature("not a signature"))
	assert.False(t, looksLikeSSHSignature("-----BEGIN SSH SIGNATURE-----"))
}

func TestLooksLikeSSHPublicKey(t *testing.T) {
	assert.True(t, looksLikeSSHPublicKey("ssh-ed25519 AAAAC3 comment"))
	assert.True(t, looksLikeSSHPublicKey("ecdsa-sha2-nistp256 AAAAE2 comment"))
	assert.True(t, looksLikeSSHPublicKey("sk-ssh-ed25519@openssh.com AAAAGn comment"))
	assert.False(t, looksLikeSSHPublicKey("onlyonefield"))
	assert.False(t, looksLikeSSHPublicKey("rsa-bad AAAA"))
}

func TestTrimKeyPrefix(t *testing.T) {
	assert.Equal(t, "ssh-ed25519 AAAA", trimKeyPrefix("key::ssh-ed25519 AAAA"))
	assert.Equal(t, "ssh-ed25519 AAAA", trimKeyPrefix("  key::ssh-ed25519 AAAA  "))
	assert.Equal(t, "ssh-ed25519 AAAA", trimKeyPrefix("ssh-ed25519 AAAA"))
}

func TestWriteSignatureFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.sig")

	err := writeSignatureFile(path, "-----BEGIN SSH SIGNATURE-----\nDATA\n-----END SSH SIGNATURE-----")
	require.NoError(t, err)

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "-----BEGIN SSH SIGNATURE-----\nDATA\n-----END SSH SIGNATURE-----\n", string(content))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestErrorBodyTruncation(t *testing.T) {
	mockOriginRemote(t, "git@github.com:example/repo.git")

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.pub")
	payloadPath := filepath.Join(dir, "payload")

	require.NoError(t, os.WriteFile(keyPath, []byte("dummy\n"), 0o600))
	require.NoError(t, os.WriteFile(payloadPath, []byte("data"), 0o600))

	longBody := strings.Repeat("x", 2000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, err := fmt.Fprint(w, longBody)
		require.NoError(t, err)
	}))
	t.Cleanup(srv.Close)

	getenv := envMap(map[string]string{envSignerURL: srv.URL})
	err := execute([]string{"-Y", "sign", "-n", "git", "-f", keyPath, payloadPath}, getenv, &bytes.Buffer{}, &bytes.Buffer{})
	require.Error(t, err)
	// Error message should be truncated, not contain the full 2000-char body.
	assert.Less(t, len(err.Error()), 700)
	assert.Contains(t, err.Error(), "...")
}

func TestOriginRemoteURL(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "remote", "add", "origin", "git@github.com:example/repo.git")

	t.Setenv("GIT_DIR", filepath.Join(repoDir, ".git"))
	assert.Equal(t, "git@github.com:example/repo.git", originRemoteURL())
}

func TestOriginRemoteURLEmptyWhenMissing(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")

	t.Setenv("GIT_DIR", filepath.Join(repoDir, ".git"))
	assert.Equal(t, "", originRemoteURL())
}

func decodePayload(t *testing.T, payload string) string {
	t.Helper()

	decoded, err := base64.StdEncoding.DecodeString(payload)
	require.NoError(t, err)
	return string(decoded)
}

func envMap(m map[string]string) func(string) string {
	return func(name string) string { return m[name] }
}

var noenv = envMap(map[string]string{})

func mockOriginRemote(t *testing.T, url string) {
	t.Helper()

	orig := getOriginRemote
	getOriginRemote = func() string { return url }
	t.Cleanup(func() { getOriginRemote = orig })
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
