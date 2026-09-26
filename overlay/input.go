package overlay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	// barrierQuery is DSR 5n. Terminals answer it with barrierReply, in
	// order with their other replies, so the reply marks the byte where
	// every reply owed to the app has arrived.
	barrierQuery = "\x1b[5n"
	barrierReply = "\x1b[0n"
	// positionQuery is DSR 6n. Terminals answer it with CSI row ; col R.
	positionQuery = "\x1b[6n"
	// promptInputMax bounds what the prompt's input buffers.
	promptInputMax = 64 << 10
)

type inputMode uint8

const (
	toSession inputMode = iota
	draining            // cut to handoff: still the session's, watching for the barrier reply
	toPrompt
)

// inputMux is the only reader of stdin. Input goes to the session except
// while a prompt owns it, and it changes hands at exact bytes: at the
// barrier reply (the handoff) and under the caller's lock (the leave).
type inputMux struct {
	src     io.Reader
	session io.Writer
	done    chan struct{}

	mu         sync.Mutex
	mode       inputMode
	held       int // leading bytes of barrierReply seen and not routed yet
	prompt     *bufPipe
	barrier    chan struct{} // closed at the barrier reply
	stripUntil time.Time     // until then, drop one barrier reply
	lastInput  time.Time     // the latest input other than reports
	appFocus   byte          // the latest focus report the session got: 'I', 'O' or 0
	// promptFocus is the latest focus report the current prompt got,
	// which Release passes on to the app.
	promptFocus byte
	pos         *posWait // the latest cursor position query
}

// posWait watches stdin for the reply to a cursor position query.
type posWait struct {
	active   bool          // stdin is scanned for the reply
	held     []byte        // a possible reply prefix
	reply    chan struct{} // closed at the reply
	row, col int
	until    time.Time // after a timeout: drop a late reply until then
}

// errPositionTimeout means the terminal didn't answer the cursor position
// query in time.
var errPositionTimeout = errors.New("overlay: terminal did not answer the cursor position query")

// newInputMux routes src to session until a prompt takes the input.
func newInputMux(src io.Reader, session io.Writer) *inputMux {
	return &inputMux{src: src, session: session, done: make(chan struct{})}
}

// Done is closed when stdin has ended.
func (m *inputMux) Done() <-chan struct{} { return m.done }

// Run reads stdin until it ends. It returns nil at EOF.
func (m *inputMux) Run() error {
	buf := make([]byte, 4096)
	for {
		n, err := m.src.Read(buf)
		if n > 0 && m.route(buf[:n]) {
			m.waitSession()
		}
		if err != nil {
			m.mu.Lock()
			if m.pos != nil && m.pos.active {
				m.write(m.pos.held)
				m.pos.active = false
			}
			m.flushHeldLocked()
			if m.prompt != nil {
				_ = m.prompt.Close()
			}
			close(m.done)
			m.mu.Unlock()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// route sends p to the owner of the input. It reports whether the input
// still belongs to the session.
func (m *inputMux) route(p []byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	only, focus := reports(p)
	if !only {
		m.lastInput = now
	}
	if m.pos != nil && m.pos.active {
		p = m.scanPosition(p, now)
		_, focus = reports(p)
	}
	if m.mode == toSession && !now.Before(m.stripUntil) {
		m.send(p, focus)
		return true
	}
	var plain []byte
	for _, b := range p {
		if b == barrierReply[m.held] {
			m.held++
			if m.held == len(barrierReply) {
				m.held = 0
				m.write(plain)
				plain = plain[:0]
				m.reply()
			}
			continue
		}
		if m.held > 0 {
			// Not the reply after all: the held prefix is ordinary input.
			plain = append(plain, barrierReply[:m.held]...)
			m.held = 0
		}
		if b == barrierReply[0] {
			m.held = 1
			continue
		}
		plain = append(plain, b)
	}
	// Only the barrier reply is worth waiting for across reads. Anywhere
	// else a held ESC would delay the Escape key.
	if m.mode != draining && m.held > 0 {
		plain = append(plain, barrierReply[:m.held]...)
		m.held = 0
	}
	m.write(plain)
	return m.mode != toPrompt
}

// roomWaiter is a session input that queues writes instead of blocking.
type roomWaiter interface{ waitRoom() }

// waitSession is the stdin pump's backpressure: it waits, outside the
// lock, until a queueing session input has room. The prompt's input
// never waits for the session.
func (m *inputMux) waitSession() {
	if w, ok := m.session.(roomWaiter); ok {
		w.waitRoom()
	}
}

// reply handles a complete barrier reply.
func (m *inputMux) reply() {
	switch m.mode {
	case draining:
		// The handoff: the terminal answers in order, so every reply it owed
		// the app has already gone to the session.
		m.mode = toPrompt
		close(m.barrier)
	case toPrompt:
		// The prompt's queries are filtered out, so this answers a DSR 5n
		// the app sent before the cut. The first CSI 0n went to the
		// barrier; the bytes are the same, so the app still gets one.
		_, _ = m.session.Write([]byte(barrierReply))
	default:
		// The late reply to a barrier query that timed out.
		m.stripUntil = time.Time{}
	}
}

// write sends p to the current owner of the input.
func (m *inputMux) write(p []byte) {
	_, focus := reports(p)
	m.send(p, focus)
}

// send is write with p's last focus report already known.
func (m *inputMux) send(p []byte, focus byte) {
	if len(p) == 0 {
		return
	}
	if m.mode == toPrompt {
		if focus != 0 {
			m.promptFocus = focus
		}
		_, _ = m.prompt.Write(p)
		return
	}
	if focus != 0 {
		m.appFocus = focus
	}
	_, _ = m.session.Write(p)
}

func (m *inputMux) flushHeldLocked() {
	if m.held > 0 {
		m.write([]byte(barrierReply[:m.held]))
		m.held = 0
	}
}

// Drain is input's side of the cut. Input stays with the session until the
// barrier reply, which AwaitBarrier waits for. Call it before the barrier
// query goes out.
func (m *inputMux) Drain() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mode = draining
	m.held = 0
	m.stripUntil = time.Time{}
	m.prompt = newBufPipe(promptInputMax)
	m.barrier = make(chan struct{})
}

// AwaitBarrier is the handoff: it waits for the barrier reply and returns the
// prompt's input. It fails with ErrBarrierTimeout after timeout.
func (m *inputMux) AwaitBarrier(ctx context.Context, timeout time.Duration) (io.Reader, error) {
	m.mu.Lock()
	barrier, prompt := m.barrier, m.prompt
	m.mu.Unlock()
	if err := m.await(ctx, barrier, timeout, ErrBarrierTimeout); err != nil {
		return nil, err
	}
	return prompt, nil
}

// await waits until reply is closed, or fails with timeoutErr after
// timeout, with ctx, or at the end of stdin. A reply that won the race
// against the failure still counts.
func (m *inputMux) await(ctx context.Context, reply <-chan struct{}, timeout time.Duration, timeoutErr error) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var err error
	select {
	case <-reply:
		return nil
	case <-timer.C:
		err = timeoutErr
	case <-ctx.Done():
		err = ctx.Err()
	case <-m.done:
		err = ErrClosed
	}
	if isClosed(reply) {
		return nil
	}
	return err
}

// AwaitSilence is the handoff for a terminal that doesn't answer the barrier
// query: input moves to the prompt after quiet without input, or after
// limit, so typing that never pauses can't hold the prompt back. Mouse and
// focus reports don't count as input: the terminal sends them while the
// mouse moves. A reply or key in flight may land on the wrong side.
func (m *inputMux) AwaitSilence(ctx context.Context, quiet, limit time.Duration) (io.Reader, error) {
	deadline := time.Now().Add(limit)
	for {
		m.mu.Lock()
		wait := min(quiet-time.Since(m.lastInput), time.Until(deadline))
		if wait <= 0 {
			m.flushHeldLocked()
			m.mode = toPrompt
			p := m.prompt
			m.mu.Unlock()
			return p, nil
		}
		m.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-m.done:
			timer.Stop()
			return nil, ErrClosed
		}
	}
}

// Release is input's side of the leave: it runs leave while no input can be
// routed, then hands input back to the session. When the barrier reply is
// still pending, strip drops one that arrives within that time, so it can't
// reach the app. When the app takes focus reports (focusReports), the latest
// one the prompt got goes to the app, unless the app already had that focus.
func (m *inputMux) Release(strip time.Duration, focusReports bool, leave func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	leave()
	pending := m.mode == draining
	m.flushHeldLocked()
	m.mode = toSession
	if focusReports && m.promptFocus != 0 && m.promptFocus != m.appFocus {
		m.write([]byte{0x1b, '[', m.promptFocus})
	}
	m.promptFocus = 0
	if m.prompt != nil {
		_ = m.prompt.Close()
		m.prompt = nil
	}
	if strip > 0 && pending {
		m.stripUntil = time.Now().Add(strip)
	}
}

// ExpectPosition starts watching stdin for the reply to a cursor position
// query. Call it before the query goes out.
func (m *inputMux) ExpectPosition() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pos = &posWait{active: true, reply: make(chan struct{})}
}

// AwaitPosition waits for the reply ExpectPosition watches for and returns
// the 1-based cursor position. It fails with errPositionTimeout after
// timeout; a reply that arrives within strip after that is dropped, so it
// can't reach the app.
func (m *inputMux) AwaitPosition(ctx context.Context, timeout, strip time.Duration) (int, int, error) {
	m.mu.Lock()
	w := m.pos
	m.mu.Unlock()
	if w == nil {
		return 0, 0, errPositionTimeout
	}
	err := m.await(ctx, w.reply, timeout, errPositionTimeout)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Checked again under the lock: the reply may have come since.
	if isClosed(w.reply) {
		return w.row, w.col, nil
	}
	if w.active {
		m.write(w.held)
		w.held = nil
		if errors.Is(err, errPositionTimeout) && strip > 0 {
			w.until = time.Now().Add(strip)
		} else {
			w.active = false
		}
	}
	return 0, 0, err
}

// scanPosition removes the cursor position reply from p and stops
// watching after it. A possible reply prefix waits across reads only until
// AwaitPosition gives up. A late reply must arrive in one read, so a held
// ESC can't delay the Escape key.
func (m *inputMux) scanPosition(p []byte, now time.Time) []byte {
	w := m.pos
	late := !w.until.IsZero()
	if late && !now.Before(w.until) {
		w.active = false
		return p
	}
	var out []byte
	for i, b := range p {
		w.held = append(w.held, b)
		switch st, row, col := matchPosition(w.held); st {
		case posPrefix:
		case posComplete:
			w.active = false
			if !late {
				w.row, w.col = row, col
				close(w.reply)
			}
			return append(out, p[i+1:]...)
		default:
			// Not the reply: the held prefix is ordinary input, and b may
			// start the next one.
			out = append(out, w.held[:len(w.held)-1]...)
			w.held = w.held[:0]
			if b == 0x1b {
				w.held = append(w.held, b)
			} else {
				out = append(out, b)
			}
		}
	}
	if late {
		out = append(out, w.held...)
		w.held = w.held[:0]
	}
	return out
}

const (
	posNone = iota
	posPrefix
	posComplete
)

// maxPositionDigits bounds each number in a position reply.
const maxPositionDigits = 5

// matchPosition matches b against CSI row ; col R and returns the match
// state, row and col.
func matchPosition(b []byte) (int, int, int) {
	if b[0] != 0x1b {
		return posNone, 0, 0
	}
	if len(b) == 1 {
		return posPrefix, 0, 0
	}
	if b[1] != '[' {
		return posNone, 0, 0
	}
	nums := [2]int{}
	digits, n := 0, 0
	for _, c := range b[2:] {
		switch {
		case c >= '0' && c <= '9' && digits < maxPositionDigits:
			nums[n] = nums[n]*10 + int(c-'0')
			digits++
		case c == ';' && n == 0 && digits > 0:
			n, digits = 1, 0
		case c == 'R' && n == 1 && digits > 0:
			return posComplete, nums[0], nums[1]
		default:
			return posNone, 0, 0
		}
	}
	return posPrefix, 0, 0
}

// reports scans p for mouse and focus reports, which the terminal sends
// without a key being typed. only tells whether p holds nothing else;
// focus is the final byte of the last focus report, 'I' or 'O', or 0.
// Anything unrecognized, a report split across reads included, counts as
// input.
func reports(p []byte) (bool, byte) {
	if bytes.IndexByte(p, 0x1b) < 0 {
		return len(p) == 0, 0
	}
	only, focus := true, byte(0)
	for i := 0; i < len(p); {
		n, f := reportAt(p[i:])
		if n == 0 {
			only = false
			i++
			continue
		}
		if f != 0 {
			focus = f
		}
		i += n
	}
	return only, focus
}

// reportAt returns the length of the mouse or focus report at the start of
// p, or 0, and the final byte of a focus report.
func reportAt(p []byte) (int, byte) {
	if len(p) < 3 || p[0] != 0x1b || p[1] != '[' {
		return 0, 0
	}
	switch c := p[2]; c {
	case 'I', 'O':
		return 3, c
	case 'M':
		// X10: three bytes from 32 up.
		if len(p) >= 6 && p[3] >= 32 && p[4] >= 32 && p[5] >= 32 {
			return 6, 0
		}
	case '<':
		// SGR (1006): CSI < b ; x ; y M, or m for a release.
		if k := 3 + mouseParams(p[3:]); k > 3 && k < len(p) && (p[k] == 'M' || p[k] == 'm') {
			return k + 1, 0
		}
	default:
		// urxvt (1015): CSI b ; x ; y M.
		if k := 2 + mouseParams(p[2:]); k > 2 && k < len(p) && p[k] == 'M' {
			return k + 1, 0
		}
	}
	return 0, 0
}

// mouseParams returns the length of the three semicolon-separated numbers
// at the start of p, or 0.
func mouseParams(p []byte) int {
	count, digits := 1, 0
	for i, c := range p {
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == ';' && digits > 0 && count < 3:
			count, digits = count+1, 0
		default:
			if count == 3 && digits > 0 {
				return i
			}
			return 0
		}
	}
	return 0
}
