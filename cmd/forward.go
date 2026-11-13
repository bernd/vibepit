package cmd

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/bernd/vibepit/netpipe"
	"github.com/bernd/vibepit/sshd"
)

// forwardSpec is one "-L" local port forward: a host listener whose
// connections are tunnelled over SSH to a port inside the sandbox.
type forwardSpec struct {
	bindAddr   string
	localPort  int
	remoteHost string
	remotePort int
}

func (f forwardSpec) localAddr() string {
	return net.JoinHostPort(f.bindAddr, strconv.Itoa(f.localPort))
}

func (f forwardSpec) remoteAddr() string {
	return net.JoinHostPort(f.remoteHost, strconv.Itoa(f.remotePort))
}

// parseForwardSpec accepts the OpenSSH -L forms plus two shorthands:
//
//	PORT                              same port on both sides
//	LOCAL:REMOTE
//	LOCAL:HOST:REMOTE                 (OpenSSH)
//	BIND:LOCAL:HOST:REMOTE            (OpenSSH)
//
// The bind address defaults to 127.0.0.1 so a forward never exposes the
// sandbox beyond the host unless asked to; an empty or "*" bind address
// listens on all interfaces. IPv6 addresses go in brackets.
func parseForwardSpec(s string) (forwardSpec, error) {
	parts, err := splitForwardSpec(s)
	if err != nil {
		return forwardSpec{}, err
	}
	f := forwardSpec{bindAddr: "127.0.0.1", remoteHost: "localhost"}
	var local, remote string
	switch len(parts) {
	case 1:
		local, remote = parts[0], parts[0]
	case 2:
		local, remote = parts[0], parts[1]
	case 3:
		local, f.remoteHost, remote = parts[0], parts[1], parts[2]
	case 4:
		f.bindAddr, local, f.remoteHost, remote = parts[0], parts[1], parts[2], parts[3]
	default:
		return forwardSpec{}, fmt.Errorf("invalid forward %q: want [[BIND:]LOCAL:[HOST:]]REMOTE", s)
	}
	if f.bindAddr == "*" {
		f.bindAddr = ""
	}
	if f.remoteHost == "" {
		return forwardSpec{}, fmt.Errorf("invalid forward %q: empty host", s)
	}
	// vibed refuses other hosts; failing here beats a silently closed
	// connection mid-session.
	if !sshd.ForwardHostAllowed(f.remoteHost) {
		return forwardSpec{}, fmt.Errorf("invalid forward %q: host %q is not a sandbox loopback address", s, f.remoteHost)
	}
	if f.localPort, err = parsePort(local); err != nil {
		return forwardSpec{}, fmt.Errorf("invalid forward %q: local port: %w", s, err)
	}
	if f.remotePort, err = parsePort(remote); err != nil {
		return forwardSpec{}, fmt.Errorf("invalid forward %q: remote port: %w", s, err)
	}
	return f, nil
}

// splitForwardSpec splits on colons outside of [...] and strips the
// brackets, so IPv6 bind and host addresses survive. Brackets must wrap a
// whole field.
func splitForwardSpec(s string) ([]string, error) {
	var parts []string
	var cur strings.Builder
	inBracket, closed := false, false
	for _, r := range s {
		switch {
		case r == ':' && !inBracket:
			parts = append(parts, cur.String())
			cur.Reset()
			closed = false
		case closed:
			return nil, fmt.Errorf("invalid forward %q: unexpected %q after ']'", s, r)
		case r == '[' && !inBracket && cur.Len() == 0:
			inBracket = true
		case r == ']' && inBracket:
			// "[]" would otherwise become an empty bind address, which
			// listens on all interfaces.
			if cur.Len() == 0 {
				return nil, fmt.Errorf("invalid forward %q: empty brackets", s)
			}
			inBracket, closed = false, true
		case r == '[' || r == ']':
			return nil, fmt.Errorf("invalid forward %q: misplaced %q", s, r)
		default:
			cur.WriteRune(r)
		}
	}
	if inBracket {
		return nil, fmt.Errorf("invalid forward %q: unclosed '['", s)
	}
	return append(parts, cur.String()), nil
}

// forwardsRequest describes forwards for vibed's session banner.
func forwardsRequest(specs []forwardSpec) *sshd.ForwardsRequest {
	req := &sshd.ForwardsRequest{}
	for _, f := range specs {
		req.Local = append(req.Local, f.localAddr())
		req.Remote = append(req.Remote, f.remoteAddr())
	}
	return req
}

// parsePort parses a TCP port, 1-65535. Port 0 (let the OS pick) is not
// offered: connect has no way to show which port was picked, since the
// session's terminal reset wipes anything printed before the shell.
func parsePort(s string) (int, error) {
	// ParseUint rather than Atoi: Atoi accepts signs, so "+80" or "-0"
	// would pass as ports.
	p, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return 0, fmt.Errorf("%s out of range", s)
		}
		return 0, fmt.Errorf("%q is not a number", s)
	}
	if p == 0 || p > 65535 {
		return 0, fmt.Errorf("%d out of range", p)
	}
	return int(p), nil
}

// sshDialer is the part of *ssh.Client a forward needs; Dial opens a
// direct-tcpip channel that vibed connects to the address inside the sandbox.
type sshDialer interface {
	Dial(network, addr string) (net.Conn, error)
}

// portForwarder owns the local listeners for a set of forwards and every
// connection accepted on them.
type portForwarder struct {
	dialer    sshDialer
	listeners []net.Listener

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

// startForwards binds every forward before returning, so a port that is
// already taken fails the command up front instead of mid-session.
func startForwards(dialer sshDialer, specs []forwardSpec) (*portForwarder, error) {
	pf := &portForwarder{dialer: dialer, conns: make(map[net.Conn]struct{})}
	for _, spec := range specs {
		l, err := net.Listen("tcp", spec.localAddr())
		if err != nil {
			pf.Close() //nolint:errcheck
			return nil, fmt.Errorf("forward %s: %w", spec.localAddr(), err)
		}
		pf.listeners = append(pf.listeners, l)
		remote := spec.remoteAddr()
		go netpipe.AcceptLoop(l, func(c net.Conn) { pf.handle(c, remote) }) //nolint:errcheck // nil once closed
	}
	return pf, nil
}

// handle tunnels one accepted connection. Errors are not reported: the
// terminal belongs to the session, and a refused dial (nothing listening in
// the sandbox yet) shows up to the client as a closed connection, as with
// OpenSSH.
func (pf *portForwarder) handle(local net.Conn, remote string) {
	if !pf.track(local) {
		local.Close() //nolint:errcheck
		return
	}
	defer pf.untrack(local)

	tunnel, err := pf.dialer.Dial("tcp", remote)
	if err != nil {
		return
	}
	if !pf.track(tunnel) {
		tunnel.Close() //nolint:errcheck
		return
	}
	defer pf.untrack(tunnel)

	netpipe.Join(local, tunnel)
}

// track registers a connection for Close. It returns false once Close has
// run, so a connection accepted during shutdown is not left open.
func (pf *portForwarder) track(c net.Conn) bool {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	if pf.conns == nil {
		return false
	}
	pf.conns[c] = struct{}{}
	return true
}

func (pf *portForwarder) untrack(c net.Conn) {
	pf.mu.Lock()
	delete(pf.conns, c) // no-op on the nil map after Close
	pf.mu.Unlock()
	c.Close() //nolint:errcheck
}

// Close stops the listeners and closes every open forwarded connection. It
// does not wait for the handlers: one may be blocked in Dial on a stalled
// SSH connection, which only ends when the caller closes that connection.
// A handler that gets past Dial after Close finds track refusing and
// closes its tunnel itself.
func (pf *portForwarder) Close() error {
	var errs []error
	for _, l := range pf.listeners {
		if err := l.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	pf.mu.Lock()
	conns := pf.conns
	pf.conns = nil
	pf.mu.Unlock()
	for c := range conns {
		c.Close() //nolint:errcheck
	}
	return errors.Join(errs...)
}
