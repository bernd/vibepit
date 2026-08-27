package ward

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// winsizeRewriter filters XTWINOPS window-size reports on their way from
// the real terminal to the child PTY.
//
// Ward shrinks the child's PTY by one row to reserve space for its bar, but
// applications such as tmux ask the terminal directly for its size
// (CSI 18 t / CSI 14 t) and trust the reply over TIOCGWINSZ. The real
// terminal answers with the full height, so tmux would grow back onto the
// protected row. Rewriting the reports keeps the child's view of the
// terminal consistent with the PTY size ward hands it.
//
// Rewritten sequences:
//   - CSI 8 ; rows ; cols t   (cells)  -> rows replaced by rows-1
//   - CSI 9 ; rows ; cols t   (screen)  -> rows replaced by rows-1
//   - CSI 4 ; height ; width t (pixels) -> height scaled by (rows-1)/rows
//   - CSI 48 ; rows ; cols ; height ; width t (in-band resize, mode 2048)
//     -> rows and pixel height adjusted as above
//
// Only this report family is covered. A child could still probe the real
// height with a cursor position report (CSI 6 n) after moving the cursor
// past the scroll region; no known application does, so that is out of
// scope.
type winsizeRewriter struct {
	w      io.Writer
	rows   *atomic.Int32 // full terminal height, as seen by ward
	parser *ansi.Parser

	mu      sync.Mutex
	pending [maxPending]byte // partial CSI that may still become a size report
	npend   int
	flush   *time.Timer // releases pending bytes if no continuation arrives
}

// winsizeFlushDelay bounds how long a size-report prefix is held back.
// Terminal reports arrive in one burst, so a prefix that is not completed
// quickly is user input (e.g. Esc followed by '[').
const winsizeFlushDelay = 50 * time.Millisecond

// maxPending caps how much of an unfinished sequence is held back. A size
// report is at most ESC [ 48 ; n ; n ; n ; n t with five-digit values;
// anything longer is passed through verbatim.
const maxPending = 32

func newWinsizeRewriter(w io.Writer, rows *atomic.Int32) *winsizeRewriter {
	p := ansi.NewParser()
	p.SetDataSize(0) // only CSI params are needed; no OSC/DCS payloads
	return &winsizeRewriter{w: w, rows: rows, parser: p}
}

// Write implements io.Writer. It always reports len(p) bytes consumed.
// Bytes that might be the prefix of a size report are held back until the
// sequence completes, is ruled out, or the flush delay expires. A lone
// trailing ESC is never held back: terminals emit reports atomically, so
// a split right after ESC is far less likely than a user pressing Esc.
func (r *winsizeRewriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.npend == 0 && bytes.IndexByte(p, asciiESC) < 0 {
		_, err := r.w.Write(p)
		return len(p), err
	}

	if r.flush != nil {
		r.flush.Stop()
	}

	data := p
	if r.npend > 0 {
		data = append(r.pending[:r.npend:r.npend], p...)
		r.npend = 0
	}

	for len(data) > 0 {
		idx := bytes.IndexByte(data, asciiESC)
		if idx < 0 {
			idx = len(data)
		}
		if idx > 0 {
			if _, err := r.w.Write(data[:idx]); err != nil {
				return 0, err
			}
			data = data[idx:]
			continue
		}

		n, repl, incomplete := r.matchSizeReport(data)
		switch {
		case incomplete && len(data) > 1 && len(data) <= maxPending:
			r.npend = copy(r.pending[:], data)
			data = nil
		case n > 0:
			if _, err := r.w.Write(repl); err != nil {
				return 0, err
			}
			data = data[n:]
		default:
			if _, err := r.w.Write(data[:1]); err != nil {
				return 0, err
			}
			data = data[1:]
		}
	}

	if r.npend > 0 {
		if r.flush == nil {
			r.flush = time.AfterFunc(winsizeFlushDelay, r.flushPending)
		} else {
			r.flush.Reset(winsizeFlushDelay)
		}
	}
	return len(p), nil
}

// flushPending writes held-back bytes verbatim once the flush delay expires.
func (r *winsizeRewriter) flushPending() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.npend == 0 {
		return
	}
	n := r.npend
	r.npend = 0
	r.w.Write(r.pending[:n]) //nolint:errcheck
}

// matchSizeReport inspects data, which must start with ESC. It returns the
// number of bytes consumed and their replacement when a complete size
// report is present, incomplete=true when data is a proper prefix of one,
// and (0, nil, false) when data cannot be a size report.
func (r *winsizeRewriter) matchSizeReport(data []byte) (int, []byte, bool) {
	_, _, n, state := ansi.DecodeSequence(data, ansi.NormalState, r.parser)
	if state != ansi.NormalState {
		return 0, nil, true
	}
	cmd := ansi.Cmd(r.parser.Command())
	if cmd.Final() != 't' || cmd.Prefix() != 0 || cmd.Intermediate() != 0 {
		return 0, nil, false
	}

	params := r.parser.Params()
	if len(params) == 0 {
		return 0, nil, false
	}
	ps := make([]int, len(params))
	for i, p := range params {
		// Sub-parameters or missing values never occur in a size report.
		if p.HasMore() || p.Param(-1) < 0 {
			return 0, nil, false
		}
		ps[i] = p.Param(-1)
	}

	// ps[1] is the row count (or pixel height for kind 4);
	// ps[3] is the pixel height for in-band resize reports.
	kind := ps[0]
	rows := int(r.rows.Load())
	switch {
	case rows <= 1:
		return 0, nil, false
	case (kind == 8 || kind == 9) && len(ps) == 3:
		ps[1] = shrinkRows(ps[1])
	case kind == 4 && len(ps) == 3:
		ps[1] = ps[1] * (rows - 1) / rows
	case kind == 48 && len(ps) == 5:
		ps[1] = shrinkRows(ps[1])
		ps[3] = ps[3] * (rows - 1) / rows
	default:
		return 0, nil, false
	}
	return n, []byte(ansi.WindowOp(kind, ps[1:]...)), false
}

func shrinkRows(v int) int {
	if v > 1 {
		return v - 1
	}
	return v
}
