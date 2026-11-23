//go:build integration

package netproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"

	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Read the port assigned by the OS from each server's startup announcement.
func externalTLSServer(t *testing.T, implementation string) (string, *x509.CertPool) {
	t.Helper()
	bin, err := exec.LookPath(implementation)
	if err != nil {
		t.Skipf("%s unavailable", implementation)
	}
	dir := t.TempDir()
	cert, roots := testCertificate(t)
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	require.NoError(t, err)
	for filename, block := range map[string]*pem.Block{
		"cert.pem": {Type: "CERTIFICATE", Bytes: cert.Certificate[0]},
		"key.pem":  {Type: "PRIVATE KEY", Bytes: key},
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, filename), pem.EncodeToMemory(block), 0600))
	}

	args := []string{"s_server", "-accept", "127.0.0.1:0", "-cert", "cert.pem", "-key", "key.pem", "-groups", "P-256", "-tls1_3", "-www"}
	if implementation == "node" {
		args = []string{"-e", `
const fs = require("node:fs");
const https = require("node:https");
const server = https.createServer({
  cert: fs.readFileSync("cert.pem"), key: fs.readFileSync("key.pem"),
  minVersion: "TLSv1.3", ecdhCurve: "P-256"
}, (req, res) => res.end("ok"));
server.listen(0, "127.0.0.1", () => console.log("ACCEPT 127.0.0.1:" + server.address().port));`}
	}
	cmd := exec.CommandContext(t.Context(), bin, args...)
	cmd.Dir = dir
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = cmd.Stdout
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	ready := make(chan string, 1)
	go func() {
		defer close(ready)
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			if addr, ok := strings.CutPrefix(scanner.Text(), "ACCEPT "); ok {
				ready <- addr
				break
			}
		}
		io.Copy(io.Discard, out)
	}()
	select {
	case addr := <-ready:
		endpoint, err := netip.ParseAddrPort(addr)
		require.NoError(t, err, "TLS server failed to publish a listening address")
		require.True(t, endpoint.Addr().IsLoopback())
		return addr, roots
	case <-time.After(5 * time.Second):
		t.Fatal("TLS server startup timed out")
		return "", nil
	}
}

func TestTunnelTLSInteroperability(t *testing.T) {
	for _, implementation := range []string{"openssl", "node"} {
		t.Run(implementation, func(t *testing.T) {
			addr, roots := externalTLSServer(t, implementation)
			outcomes := make(chan tunnelRefusal, 1)
			proxy, authority := wholeHostProxy(t, "origin.test", addr, func(f tunnelRefusal) { outcomes <- f })
			proxyURL, err := url.Parse(proxy.URL)
			require.NoError(t, err)
			tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{
				RootCAs: roots, MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256},
			}}
			t.Cleanup(tr.CloseIdleConnections)
			client := http.Client{Transport: tr, Timeout: 5 * time.Second}
			res, err := client.Get("https://" + authority + "/")
			require.NoError(t, err)
			defer res.Body.Close()
			require.Equal(t, http.StatusOK, res.StatusCode)
			require.True(t, res.TLS.HelloRetryRequest, "the peer must exercise the second ClientHello")
			_, err = io.Copy(io.Discard, res.Body)
			require.NoError(t, err)
			for _, tc := range []struct {
				name, code string
				cfg        *tls.Config
			}{
				{"mismatched name", "tunnel_server_name", &tls.Config{ServerName: "other.test"}},
				{"missing name", "tunnel_server_name", &tls.Config{ServerName: "127.0.0.1"}},
				{"ECH", "tunnel_encrypted_client_hello", &tls.Config{ServerName: "inner.test", MinVersion: tls.VersionTLS13, EncryptedClientHelloConfigList: testECHConfig(t)}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					peer := tls.Client(openTunnel(t, proxy, authority), tc.cfg)
					require.Error(t, peer.Handshake())
					select {
					case f := <-outcomes:
						require.Equal(t, tc.code, f.Code)
					case <-time.After(time.Second):
						t.Fatal("missing tunnel refusal")
					}
				})
			}
		})
	}
}

func TestOpenSSLGREASERefusal(t *testing.T) {
	bin, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("OpenSSL unavailable")
	}
	help, _ := exec.Command(bin, "s_client", "-help").CombinedOutput()
	if !bytes.Contains(help, []byte("-ech_grease ")) {
		t.Skip("this OpenSSL has no ECH GREASE client")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	outcomes := make(chan tunnelRefusal, 1)
	proxy, authority := wholeHostProxy(t, "origin.test", ln.Addr().String(), func(f tunnelRefusal) { outcomes <- f })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "s_client", "-proxy", proxy.Listener.Addr().String(), "-connect", authority, "-servername", "origin.test", "-ech_grease")
	require.Error(t, cmd.Run())
	select {
	case outcome := <-outcomes:
		require.Equal(t, "tunnel_encrypted_client_hello", outcome.Code)
	case <-ctx.Done():
		t.Fatal("missing ECH refusal")
	}
	up, err := ln.Accept()
	require.NoError(t, err)
	defer up.Close()
	require.NoError(t, up.SetReadDeadline(time.Now().Add(time.Second)))
	b, err := io.ReadAll(up)
	require.NoError(t, err)
	require.Empty(t, b)
}
