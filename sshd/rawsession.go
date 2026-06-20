package sshd

import (
	"context"
	"io"

	charmssh "github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"

	"github.com/bernd/vibepit/session"
)

// rawSession adapts a raw gossh.Channel to the small surface the PTY/exec
// handlers need. It deliberately bypasses charmbracelet/ssh's emulated-PTY
// session, whose writer rewrites every \n to \r\n (see pty.go ptyWriter) and
// corrupts raw-mode TUI output. Writes here go straight to the SSH channel, so
// the session PTY's own termios is the sole authority on line endings.
//
// Embedding gossh.Channel provides Read, Write, Close, CloseWrite, Stderr, and
// SendRequest with no transformation; rawSession adds the session metadata
// (env, command, pty) and an Exit that mirrors charmssh's exit-status behavior.
type rawSession struct {
	gossh.Channel

	ctx    context.Context //nolint:containedctx // mirrors charmssh.Session.Context lifetime
	env    []string
	rawCmd string

	term  string
	winch chan charmssh.Window
}

// Environ returns a copy of the environment requested by the client via "env"
// requests. A copy keeps callers (which append to the result) from mutating the
// session's backing slice.
func (s *rawSession) Environ() []string {
	out := make([]string, len(s.env))
	copy(out, s.env)
	return out
}

// Context returns the connection-scoped context (canceled on disconnect).
func (s *rawSession) Context() context.Context { return s.ctx }

// RawCommand returns the command from an "exec" request, or "" for a shell.
func (s *rawSession) RawCommand() string { return s.rawCmd }

// Exit sends the SSH exit-status and closes the channel, matching the semantics
// the handlers previously relied on from charmssh.Session.Exit.
func (s *rawSession) Exit(code int) error {
	payload := struct{ Status uint32 }{Status: uint32(code)}             //nolint:gosec
	s.Channel.SendRequest("exit-status", false, gossh.Marshal(&payload)) //nolint:errcheck
	return s.Channel.Close()
}

// hasPty reports whether the client requested a PTY for this session.
func (s *rawSession) hasPty() bool { return s.winch != nil }

// Stderr returns the session's diagnostic stream. With a PTY the connected
// terminal is in raw mode, so vibed-generated messages written here (e.g. a
// "create session" error) must use CRLF — without it they stair-step. This
// mirrors charmbracelet/ssh's emulated-PTY Stderr, which cooked stderr only
// when a PTY was present. Without a PTY (exec) the client terminal is cooked
// and adds the CR itself, so stderr passes through raw.
func (s *rawSession) Stderr() io.Writer {
	if s.hasPty() {
		return crlfWriter{w: s.Channel.Stderr()}
	}
	return s.Channel.Stderr()
}

// crlfWriter wraps a writer so vibed-generated output (the session selector,
// PTY-session stderr) is CRLF-cooked for a raw-mode client. It delegates the
// transform to session.ToCRLF, the single source of truth shared with the
// reattach replay. Always reports len(p) consumed so callers see a full write.
type crlfWriter struct{ w io.Writer }

func (c crlfWriter) Write(p []byte) (int, error) {
	if _, err := c.w.Write(session.ToCRLF(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// SSH request payloads (RFC 4254 §6).
type ptyReqPayload struct {
	Term                         string
	Columns, Rows, WidthPx, HtPx uint32
	Modes                        string
}

type winChPayload struct {
	Columns, Rows, WidthPx, HtPx uint32
}

type envPayload struct{ Name, Value string }

type execPayload struct{ Command string }

// handleSessionChannel is registered as the "session" ChannelHandler, replacing
// charmssh's DefaultSessionHandler so output is never run through the emulated-
// PTY \n->\r\n writer. charmssh still owns TCP accept, the SSH handshake,
// public-key auth, idle timeout, and connection-level global requests; only the
// per-session channel I/O is handled here against the raw gossh.Channel.
func (s *Server) handleSessionChannel(_ *charmssh.Server, _ *gossh.ServerConn, newChan gossh.NewChannel, ctx charmssh.Context) {
	ch, reqs, err := newChan.Accept()
	if err != nil {
		return
	}

	sess := &rawSession{Channel: ch, ctx: ctx}
	var initialWin charmssh.Window
	dispatched := false

	for req := range reqs {
		switch req.Type {
		case "pty-req":
			if dispatched || sess.winch != nil {
				// At most one PTY per session. A later pty-req would replace
				// sess.winch and orphan the resize goroutine's channel (never
				// closed → leaked goroutine).
				replyReq(req, false)
				continue
			}
			var p ptyReqPayload
			if !unmarshalReq(req, &p) {
				continue
			}
			sess.term = p.Term
			initialWin = charmssh.Window{Width: int(p.Columns), Height: int(p.Rows)} //nolint:gosec
			// Buffered so a window-change never blocks the request loop; the
			// resize consumer drains it and only the latest size matters.
			sess.winch = make(chan charmssh.Window, 1)
			replyReq(req, true)

		case "window-change":
			var p winChPayload
			if err := gossh.Unmarshal(req.Payload, &p); err != nil || sess.winch == nil {
				replyReq(req, false)
				continue
			}
			win := charmssh.Window{Width: int(p.Columns), Height: int(p.Rows)} //nolint:gosec
			// Coalesce: drop any stale pending size, then enqueue the latest.
			select {
			case <-sess.winch:
			default:
			}
			select {
			case sess.winch <- win:
			default:
			}
			replyReq(req, true)

		case "env":
			if dispatched {
				// Environment must be set before shell/exec. Rejecting late env
				// avoids racing the dispatched handler goroutine's read of
				// sess.env (only writes before the go statement are ordered).
				replyReq(req, false)
				continue
			}
			var p envPayload
			if !unmarshalReq(req, &p) {
				continue
			}
			sess.env = append(sess.env, p.Name+"="+p.Value)
			replyReq(req, true)

		case "shell", "exec":
			if dispatched {
				replyReq(req, false)
				continue
			}
			if req.Type == "exec" {
				var p execPayload
				if !unmarshalReq(req, &p) {
					continue
				}
				sess.rawCmd = p.Command
			}
			dispatched = true
			replyReq(req, true)
			// Dispatch on PTY presence, mirroring the original
			// isPty ? handlePTYSession : handleExecSession logic. A shell
			// without a PTY falls through to handleExecSession, which reports
			// "no command specified" as before.
			if sess.hasPty() {
				go s.handlePTYSession(sess, initialWin.Width, initialWin.Height, sess.winch)
			} else {
				go s.handleExecSession(sess)
			}

		default:
			// signal, break, subsystem, xon-xoff, etc. — unsupported.
			replyReq(req, false)
		}
	}

	// The channel closed (session ended): stop the resize consumer.
	if sess.winch != nil {
		close(sess.winch)
	}
}

func replyReq(req *gossh.Request, ok bool) {
	if req.WantReply {
		req.Reply(ok, nil) //nolint:errcheck
	}
}

// unmarshalReq decodes an SSH request payload into dest, replying false to a
// decode failure. It returns true on success; on false the caller continues to
// the next request. window-change decodes inline because it pairs the decode
// with a sess.winch precondition.
func unmarshalReq(req *gossh.Request, dest any) bool {
	if err := gossh.Unmarshal(req.Payload, dest); err != nil {
		replyReq(req, false)
		return false
	}
	return true
}
