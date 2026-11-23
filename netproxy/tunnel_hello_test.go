package netproxy

import (
	"bufio"
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/cryptobyte"
)

// All ECH keys and DNS names are local test data.
func testECHConfig(t *testing.T) []byte {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	b := cryptobyte.NewBuilder(nil)
	b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
		b.AddUint16(0xfe0d)
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			b.AddUint8(1)
			b.AddUint16(0x20) // DHKEM(X25519, HKDF-SHA256)
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(key.PublicKey().Bytes()) })
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint16(1); b.AddUint16(1) })
			b.AddUint8(0)
			b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes([]byte("origin.test")) })
			b.AddUint16(0)
		})
	})
	data, err := b.Bytes()
	require.NoError(t, err)
	return data
}

func TestRealECHRefusedBeforeForwarding(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	outcomes := make(chan tunnelRefusal, 1)
	proxy, authority := wholeHostProxy(t, "origin.test", ln.Addr().String(), func(f tunnelRefusal) { outcomes <- f })
	conn := openTunnel(t, proxy, authority)
	client := tls.Client(conn, &tls.Config{
		ServerName: "inner.test", EncryptedClientHelloConfigList: testECHConfig(t), MinVersion: tls.VersionTLS13,
	})
	require.Error(t, client.Handshake())
	select {
	case f := <-outcomes:
		require.Equal(t, "tunnel_encrypted_client_hello", f.Code)
		require.Equal(t, authority, f.Authority)
	case <-time.After(time.Second):
		t.Fatal("missing ECH refusal")
	}
	up, err := ln.Accept()
	require.NoError(t, err)
	defer up.Close()
	require.NoError(t, up.SetReadDeadline(time.Now().Add(time.Second)))
	data, err := io.ReadAll(up)
	require.NoError(t, err)
	require.Empty(t, data)
}

func tlsRecord(kind byte, data []byte) []byte {
	header := binary.BigEndian.AppendUint16([]byte{kind, 3, 3}, uint16(len(data)))
	return append(header, data...)
}

func TestHelloFlightFraming(t *testing.T) {
	// A split handshake header exercises record reassembly independently
	// from extension parsing. Only the finished flight may be forwarded.
	message := []byte{clientHello, 0, 0, 2, 3, 3}
	wire := append(tlsRecord(handshakeRecord, message[:2]), tlsRecord(handshakeRecord, message[2:])...)
	f, err := readHelloFlight(bufio.NewReader(bytes.NewReader(wire)), clientHello, false, io.Discard)
	require.NoError(t, err)
	require.Equal(t, wire, f.wire)
	require.Equal(t, []byte{3, 3}, f.body)

	for _, record := range [][]byte{
		tlsRecord(handshakeRecord, []byte{clientHello, 1, 0, 0}),
		tlsRecord(handshakeRecord, nil),
		tlsRecord(handshakeRecord, message[:2]),
		tlsRecord(changeCipherSpec, []byte{1}),
		tlsRecord(applicationData, []byte{0}),
		tlsRecord(alertRecord, []byte{2, 40}),
	} {
		_, err := readHelloFlight(bufio.NewReader(bytes.NewReader(record)), clientHello, false, io.Discard)
		require.Error(t, err)
	}
}

func TestClientHelloRequiresOneDNSName(t *testing.T) {
	name := []byte("origin.test")
	entry := append(binary.BigEndian.AppendUint16([]byte{0}, uint16(len(name))), name...)
	for _, tc := range []struct {
		name               string
		entries            [][]byte
		duplicateExtension bool
		want               error
	}{
		{"one name", [][]byte{entry}, false, nil},
		{"two names", [][]byte{entry, entry}, false, errHandshake},
		{"repeated extension", [][]byte{entry}, true, errHandshake},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := cryptobyte.NewBuilder(nil)
			b.AddUint16(0x0303)
			b.AddBytes(make([]byte, 32))
			b.AddUint8(0)
			b.AddUint16(2)
			b.AddUint16(0x1301)
			b.AddUint8(1)
			b.AddUint8(0)
			b.AddUint16LengthPrefixed(func(ext *cryptobyte.Builder) {
				sni := func() {
					ext.AddUint16(0)
					ext.AddUint16LengthPrefixed(func(data *cryptobyte.Builder) {
						data.AddUint16LengthPrefixed(func(names *cryptobyte.Builder) {
							for _, entry := range tc.entries {
								names.AddBytes(entry)
							}
						})
					})
				}
				sni()
				if tc.duplicateExtension {
					sni()
				}
			})
			body, err := b.Bytes()
			require.NoError(t, err)
			_, err = checkClientHello(body, "origin.test")
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func FuzzTunnelHello(f *testing.F) {
	f.Add(tlsRecord(handshakeRecord, []byte{clientHello, 0, 0, 2, 3, 3}))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, kind := range []byte{clientHello, serverHello} {
			flight, err := readHelloFlight(bufio.NewReader(bytes.NewReader(data)), kind, true, io.Discard)
			if err != nil {
				continue
			}
			if len(flight.wire) > maxHelloFlight {
				t.Fatal("hello exceeded the wire budget")
			}
			if kind == serverHello {
				_, _ = flight.isRetry()
			} else {
				_, _ = checkClientHello(flight.body, "origin.test")
			}
		}
		_, _ = checkClientHello(data, "origin.test")
	})
}
