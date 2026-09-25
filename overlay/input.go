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
	toContainer inputMode = iota
	draining              // T1 to T2: still the container's, watching for the barrier reply
	toPrompt
)

// InputMux is the only reader of stdin. Input goes to the container except
// while a prompt owns it, and it changes hands at exact bytes: at the
// barrier reply (T2) and under the caller's lock (T3).
type InputMux struct {
	src       io.Reader
	container io.Writer
	done      chan struct{}

	mu         sync.Mutex
	mode       inputMode
	held       int // leading bytes of barrierReply seen and not routed yet
	prompt     *bufPipe
	barrier    chan struct{} // closed at the barrier reply
	stripUntil time.Time     // until then, drop one barrier reply
	lastInput  time.Time     // the latest input other than reports
	appFocus   byte          // the latest focus report the container got: 'I', 'O' or 0
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

// NewInputMux routes src to container until a prompt takes the input.
func NewInputMux(src io.Reader, container io.Writer) *InputMux {
	return &InputMux{src: src, container: container, done: make(chan struct{})}
}

// Done is closed when stdin has ended.
func (m *InputMux) Done() <-chan struct{} { return m.done }

// Run reads stdin until it ends. It returns nil at EOF.
func (m *InputMux) Run() error {
	buf := make([]byte, 4096)
	for {
		n, err := m.src.Read(buf)
		if n > 0 && m.route(buf[:n]) {
			m.waitContainer()
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
// still belongs to the container.
func (m *InputMux) route(p []byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if only, _ := reports(p); !only {
		m.lastInput = now
	}
	if m.pos != nil && m.pos.active {
		p = m.scanPosition(p, now)
	}
	if m.mode == toContainer && !now.Before(m.stripUntil) {
		m.write(p)
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

// roomWaiter is a container input that queues writes instead of blocking.
type roomWaiter interface{ waitRoom() }

// waitContainer is the stdin pump's backpressure: it waits, outside the
// lock, until a queueing container input has room. The prompt's input
// never waits for the container.
func (m *InputMux) waitContainer() {
	if w, ok := m.container.(roomWaiter); ok {
		w.waitRoom()
	}
}

// reply handles a complete barrier reply.
func (m *InputMux) reply() {
	switch m.mode {
	case draining:
		// T2: the terminal answers in order, so every reply it owed the
		// app has already gone to the container.
		m.mode = toPrompt
		close(m.barrier)
	case toPrompt:
		// The prompt's queries are filtered out, so this answers a DSR 5n
		// the app sent before the cut. The first CSI 0n went to the
		// barrier; the bytes are the same, so the app still gets one.
		_, _ = m.container.Write([]byte(barrierReply))
	default:
		// The late reply to a barrier query that timed out.
		m.stripUntil = time.Time{}
	}
}

// write sends p to the current owner of the input.
func (m *InputMux) write(p []byte) {
	if len(p) == 0 {
		return
	}
	_, focus := reports(p)
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
	_, _ = m.container.Write(p)
}

func (m *InputMux) flushHeldLocked() {
	if m.held > 0 {
		m.write([]byte(barrierReply[:m.held]))
		m.held = 0
	}
}

// Drain starts T1. Input stays with the container until the barrier
// reply, which AwaitBarrier waits for. Call it before the barrier query
// goes out.
func (m *InputMux) Drain() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mode = draining
	m.held = 0
	m.stripUntil = time.Time{}
	m.prompt = newBufPipe(promptInputMax)
	m.barrier = make(chan struct{})
}

// AwaitBarrier is T2: it waits for the barrier reply and returns the
// prompt's input. It fails with ErrBarrierTimeout after timeout.
func (m *InputMux) AwaitBarrier(ctx context.Context, timeout time.Duration) (io.Reader, error) {
	m.mu.Lock()
	barrier, prompt := m.barrier, m.prompt
	m.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var err error
	select {
	case <-barrier:
		return prompt, nil
	case <-timer.C:
		err = ErrBarrierTimeout
	case <-ctx.Done():
		err = ctx.Err()
	case <-m.done:
		err = ErrClosed
	}
	select {
	case <-barrier:
		// The reply won the race.
		return prompt, nil
	default:
		return nil, err
	}
}

// AwaitSilence is T2 for a terminal that doesn't answer the barrier
// query: input moves to the prompt after quiet without input, or after
// max, so typing that never pauses can't hold the prompt back. Mouse and
// focus reports don't count as input: the terminal sends them while the
// mouse moves. A reply or key in flight may land on the wrong side.
func (m *InputMux) AwaitSilence(ctx context.Context, quiet, max time.Duration) (io.Reader, error) {
	deadline := time.Now().Add(max)
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

// Release is T3: it runs leave while no input can be routed, then hands
// input back to the container. When the barrier reply is still pending,
// strip drops one that arrives within that time, so it can't reach the
// app. When the app takes focus reports (focusReports), the latest one the
// prompt got goes to the app, unless the app already had that focus.
func (m *InputMux) Release(strip time.Duration, focusReports bool, leave func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	leave()
	pending := m.mode == draining
	m.flushHeldLocked()
	m.mode = toContainer
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
func (m *InputMux) ExpectPosition() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pos = &posWait{active: true, reply: make(chan struct{})}
}

// AwaitPosition waits for the reply ExpectPosition watches for and returns
// the 1-based cursor position. It fails with errPositionTimeout after
// timeout; a reply that arrives within strip after that is dropped, so it
// can't reach the app.
func (m *InputMux) AwaitPosition(ctx context.Context, timeout, strip time.Duration) (row, col int, err error) {
	m.mu.Lock()
	w := m.pos
	m.mu.Unlock()
	if w == nil {
		return 0, 0, errPositionTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.reply:
	case <-timer.C:
		err = errPositionTimeout
	case <-ctx.Done():
		err = ctx.Err()
	case <-m.done:
		err = ErrClosed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-w.reply:
		// The reply won the race.
		return w.row, w.col, nil
	default:
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
func (m *InputMux) scanPosition(p []byte, now time.Time) []byte {
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

// matchPosition matches b against CSI row ; col R.
func matchPosition(b []byte) (state, row, col int) {
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
func reports(p []byte) (only bool, focus byte) {
	if bytes.IndexByte(p, 0x1b) < 0 {
		return len(p) == 0, 0
	}
	only = true
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
func reportAt(p []byte) (n int, focus byte) {
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
