package netproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// The flow stream can consume this outcome without retaining handshake bytes.
type tunnelRefusal struct {
	Authority  string
	ServerName string
	Code       string
}

func (route *inspectedRoute) serveTunnel(w http.ResponseWriter, r *http.Request, report func(tunnelRefusal)) {
	up, err := route.dialTunnel(r.Context())
	if err != nil {
		writeUpstreamError(w, r, err)
		return
	}
	defer up.Close()
	client := acceptConnect(w, r)
	if client == nil {
		return
	}
	stop := context.AfterFunc(r.Context(), func() { client.Close(); up.Close() })
	defer stop()
	if route.tunnelName == "" {
		relayTunnel(client, up, func() error { return relayBytes(up, client) }, func() error { return relayBytes(client, up) })
		return
	}
	h := tunnelHandshake{client: client, up: up, host: route.tunnelName, replies: make(chan helloReply, 2)}
	relayTunnel(client, up, h.fromClient, h.fromServer)
	if h.refusal.Code != "" && report != nil {
		h.refusal.Authority = route.authority
		report(h.refusal)
	}
}

type helloReply uint8

const (
	unsolicited helloReply = iota
	proceed
	retryHello
)

type tunnelHandshake struct {
	client, up net.Conn
	host       string
	armed      atomic.Bool
	replies    chan helloReply
	refusal    tunnelRefusal // Read only after the client direction returns.
}

// Each handshake reader owns its direction until it switches to raw relay.
// Forward a clean EOF as a half-close so the peer can still send a response.
func relayTunnel(client, upstream net.Conn, send, receive func() error) {
	closeBoth := sync.OnceFunc(func() { client.Close(); upstream.Close() })
	defer closeBoth()
	transfer := func(dst net.Conn, run func() error) {
		if err := run(); err == nil {
			if half, ok := dst.(interface{ CloseWrite() error }); ok && half.CloseWrite() == nil {
				return
			}
		}
		closeBoth()
	}
	done := make(chan struct{})
	go func() { defer close(done); transfer(upstream, send) }()
	transfer(client, receive)
	<-done
}

func relayBytes(dst io.Writer, src io.Reader) error {
	_, err := io.Copy(dst, src)
	return err
}

func (h *tunnelHandshake) refuse(name string, err error) error {
	code := "tunnel_invalid_handshake"
	switch {
	case errors.Is(err, errECH):
		code = "tunnel_encrypted_client_hello"
	case errors.Is(err, errServerName):
		code = "tunnel_server_name"
	}
	h.refusal = tunnelRefusal{ServerName: name, Code: code}
	return errHandshake // A truncated hello must close both directions, even on clean EOF.
}

func (h *tunnelHandshake) fromClient() error {
	r := bufio.NewReader(h.client)
	first, err := r.Peek(1)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	switch b := first[0]; {
	case b == handshakeRecord:
	case b >= 20 && b <= 25:
		return h.refuse("", errHandshake)
	default:
		// Legacy SSL records use a two- or three-byte length header. Its
		// first byte alone also occurs in unrelated binary protocols.
		if b&0x80 != 0 || b < 0x40 {
			if err := h.client.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
				return err
			}
			legacy, err := legacyHelloPrefix(r, b)
			if err != nil || legacy {
				return h.refuse("", errHandshake)
			}
			if err := h.client.SetReadDeadline(time.Time{}); err != nil {
				return err
			}
		}
		return relayBytes(h.up, r)
	}
	// A single deadline covers all fragments and both flights. Idle raw
	// protocols have no handshake timer, including those that speak first.
	deadline := time.Now().Add(10 * time.Second)
	if err := h.client.SetDeadline(deadline); err != nil {
		return err
	}
	if err := h.up.SetDeadline(deadline); err != nil {
		return err
	}
	for retry := false; ; retry = true {
		hello, err := readHelloFlight(r, clientHello, retry, h.up)
		if err != nil {
			return h.refuse("", err)
		}
		name, err := checkClientHello(hello.body, h.host)
		if err != nil {
			return h.refuse(name, err)
		}
		h.armed.Store(true)
		if _, err = h.up.Write(hello.wire); err != nil {
			return err
		}
		reply, open := <-h.replies
		if !open {
			return net.ErrClosed
		}
		switch reply {
		case proceed:
			if err := h.up.SetDeadline(time.Time{}); err != nil {
				return err
			}
			if err := h.client.SetDeadline(time.Time{}); err != nil {
				return err
			}
			return relayBytes(h.up, r)
		case retryHello:
			if retry {
				return h.refuse("", errHandshake)
			}
		default:
			return h.refuse("", errHandshake)
		}
	}
}

func (h *tunnelHandshake) fromServer() error {
	defer close(h.replies)
	r := bufio.NewReader(h.up)
	if _, err := r.Peek(1); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if !h.armed.Load() {
		// Server-first protocols must be able to send their banner while
		// the other goroutine is still waiting for a first client byte.
		h.replies <- unsolicited
		return relayBytes(h.client, r)
	}
	for retry := false; ; retry = true {
		hello, err := readHelloFlight(r, serverHello, retry, h.client)
		if err != nil {
			return err
		}
		again, err := hello.isRetry()
		if err != nil || retry && again {
			return errHandshake
		}
		if _, err = h.client.Write(hello.wire); err != nil {
			return err
		}
		if !again {
			h.replies <- proceed
			return relayBytes(h.client, r)
		}
		h.replies <- retryHello
	}
}

// SSLv2-compatible ClientHellos have message type 1 directly after the
// length header. Leave the prefix untouched for the raw relay.
func legacyHelloPrefix(r *bufio.Reader, first byte) (bool, error) {
	header := 3
	if first&0x80 != 0 {
		header = 2
	}
	p, err := r.Peek(header + 1)
	if err != nil {
		return false, err
	}
	if p[header] != clientHello {
		return false, nil
	}
	p, err = r.Peek(header + 3)
	if err != nil {
		return false, err
	}
	return p[header+1] == 3 || (p[header+1] == 0 && p[header+2] == 2), nil
}
