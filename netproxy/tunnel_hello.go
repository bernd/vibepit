package netproxy

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"strings"

	"golang.org/x/crypto/cryptobyte"
)

const (
	changeCipherSpec = 20
	alertRecord      = 21
	handshakeRecord  = 22
	applicationData  = 23
	clientHello      = 1
	serverHello      = 2
	maxHelloFlight   = 64 << 10 // Includes record framing, also on a fragmented hello.
)

var (
	errUpstreamAlert = errors.New("upstream rejected TLS handshake")
	errHandshake     = errors.New("invalid or incomplete tunnel handshake")
	errECH           = errors.New("encrypted client hello")
	errServerName    = errors.New("tunnel server name mismatch")
	retryRandom      = sha256.Sum256([]byte("HelloRetryRequest"))
)

// helloFlight retains the original records until the complete message has
// passed policy. Servers can coalesce later handshake messages with ServerHello.
type helloFlight struct {
	wire     []byte
	body     []byte
	trailing bool
}

func readHelloFlight(r *bufio.Reader, want byte, retry bool, dst io.Writer) (helloFlight, error) {
	var f helloFlight
	var message []byte
	var header [5]byte
	for remaining := maxHelloFlight; remaining >= len(header); {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return f, err
		}
		n := int(binary.BigEndian.Uint16(header[3:]))
		remaining -= len(header) + n
		if remaining < 0 || n == 0 || header[1] != 3 || header[2] > 3 {
			return f, errHandshake
		}
		kind := header[0]
		auxiliary := retry && len(message) == 0 && (kind == changeCipherSpec || (want == clientHello && kind == applicationData))
		alert := want == serverHello && kind == alertRecord && len(message) == 0 && n == 2
		if (!alert && !auxiliary && kind != handshakeRecord) || n > 16384 && !(kind == applicationData && n <= 16640) {
			return f, errHandshake
		}
		start := len(f.wire)
		f.wire = append(f.wire, header[:]...)
		f.wire = append(f.wire, make([]byte, n)...)
		payload := f.wire[start+len(header):]
		if _, err := io.ReadFull(r, payload); err != nil {
			return f, err
		}
		if auxiliary || alert {
			if kind == changeCipherSpec && (n != 1 || payload[0] != 1) {
				return f, errHandshake
			}
			if _, err := dst.Write(f.wire); err != nil {
				return f, err
			}
			if alert && payload[0] != 1 {
				return f, errUpstreamAlert
			}
			f.wire = f.wire[:0]
			continue
		}
		message = append(message, payload...)
		if message[0] != want {
			return f, errHandshake
		}
		if len(message) < 4 {
			continue
		}
		size := int(message[1])<<16 | int(message[2])<<8 | int(message[3])
		if size > maxHelloFlight-4 {
			return f, errHandshake
		}
		if len(message)-4 < size {
			continue
		}
		f.trailing = len(message)-4 != size
		if want == clientHello && f.trailing {
			return f, errHandshake
		}
		f.body = message[4 : 4+size]
		return f, nil
	}
	return f, errHandshake
}

// Only the outer framing and the extensions used in the admission decision
// are interpreted. An ambiguous name or extension list has no valid binding.
func checkClientHello(body []byte, host string) (string, error) {
	s := cryptobyte.String(body)
	var session, suites, compression, extensions cryptobyte.String
	if !s.Skip(34) || !s.ReadUint8LengthPrefixed(&session) || len(session) > 32 ||
		!s.ReadUint16LengthPrefixed(&suites) || len(suites) == 0 || len(suites)%2 != 0 ||
		!s.ReadUint8LengthPrefixed(&compression) || len(compression) == 0 {
		return "", errHandshake
	}
	if s.Empty() {
		return "", errServerName
	}
	if !s.ReadUint16LengthPrefixed(&extensions) || !s.Empty() {
		return "", errHandshake
	}
	var name string
	seen := make(map[uint16]bool)
	for !extensions.Empty() {
		var id uint16
		var data cryptobyte.String
		if !extensions.ReadUint16(&id) || !extensions.ReadUint16LengthPrefixed(&data) || seen[id] {
			return "", errHandshake
		}
		seen[id] = true
		switch id {
		case 0xfe0d: // RFC 9849. GREASE has the same extension type.
			return "", errECH
		case 0: // RFC 6066 server_name list, with exactly one DNS host.
			var list, dns cryptobyte.String
			var nameType uint8
			if !data.ReadUint16LengthPrefixed(&list) || !data.Empty() || !list.ReadUint8(&nameType) || nameType != 0 ||
				!list.ReadUint16LengthPrefixed(&dns) || !list.Empty() || len(dns) == 0 || len(dns) > 253 {
				return "", errHandshake
			}
			for _, b := range dns {
				if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '.' || b == '-') {
					return "", errHandshake
				}
			}
			name = string(dns)
		}
	}
	if name == "" || !strings.EqualFold(name, host) {
		return name, errServerName
	}
	return name, nil
}

func (f helloFlight) isRetry() (bool, error) {
	s := cryptobyte.String(f.body)
	var random []byte
	var session, extensions cryptobyte.String
	if !s.Skip(2) || !s.ReadBytes(&random, 32) || !s.ReadUint8LengthPrefixed(&session) || len(session) > 32 || !s.Skip(3) ||
		!s.Empty() && (!s.ReadUint16LengthPrefixed(&extensions) || !s.Empty()) {
		return false, errHandshake
	}
	retry := bytes.Equal(random, retryRandom[:])
	if retry && f.trailing {
		return false, errHandshake
	}
	return retry, nil
}
