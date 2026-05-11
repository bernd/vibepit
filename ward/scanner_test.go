package ward

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScannerScrollReset(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  scanResult
	}{
		{"ESC c (RIS)", []byte("\x1bc"), scanResult{ScrollReset: true}},
		{"CSI r (no params)", []byte("\x1b[r"), scanResult{ScrollReset: true}},
		{"CSI 1;24 r (parameterized)", []byte("\x1b[1;24r"), scanResult{}},
		{"plain text", []byte("hello world"), scanResult{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s escScanner
			got := s.Scan(tt.input)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestScannerBarErased(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  scanResult
	}{
		{"CSI J (no params)", []byte("\x1b[J"), scanResult{BarErased: true}},
		{"CSI 0J", []byte("\x1b[0J"), scanResult{BarErased: true}},
		{"CSI 2J (erase all)", []byte("\x1b[2J"), scanResult{BarErased: true}},
		{"CSI 3J (erase scrollback)", []byte("\x1b[3J"), scanResult{BarErased: true}},
		{"CSI 1J (erase above)", []byte("\x1b[1J"), scanResult{}},
		{"CSI ?J (private marker)", []byte("\x1b[?J"), scanResult{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s escScanner
			got := s.Scan(tt.input)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestScannerClearCommand(t *testing.T) {
	// clear typically emits CSI H CSI 2J
	var s escScanner
	got := s.Scan([]byte("\x1b[H\x1b[2J"))
	assert.True(t, got.BarErased, "clear output should set BarErased")
}

func TestScannerOSCDoesNotTrigger(t *testing.T) {
	// OSC sequences can contain bytes that look like CSI finals
	var s escScanner
	got := s.Scan([]byte("\x1b]0;title\x07"))
	assert.False(t, got.ScrollReset, "OSC should not set ScrollReset")
	assert.False(t, got.BarErased, "OSC should not set BarErased")
	assert.True(t, s.InGround(), "scanner should be in ground state after BEL-terminated OSC")
}

func TestScannerSplitAcrossReads(t *testing.T) {
	var s escScanner

	r1 := s.Scan([]byte("\x1b["))
	assert.False(t, r1.ScrollReset)
	assert.False(t, r1.BarErased)
	require.False(t, s.InGround(), "scanner should be in CSI state, not ground")

	r2 := s.Scan([]byte("2J"))
	assert.True(t, r2.BarErased, "completing CSI 2J should set BarErased")
	assert.True(t, s.InGround(), "scanner should be back in ground state")
}

func TestScannerEraseFollowedByIncompleteSequence(t *testing.T) {
	// CSI 2J followed by an incomplete OSC in one chunk. The scanner
	// should report BarErased but NOT be in ground state, so the
	// wrapper knows not to inject its repaint mid-sequence.
	var s escScanner
	got := s.Scan([]byte("\x1b[2J\x1b]0;title"))
	assert.True(t, got.BarErased, "CSI 2J should set BarErased")
	assert.False(t, s.InGround(), "incomplete OSC should leave scanner non-ground")
}

func TestScannerMultipleSequences(t *testing.T) {
	var s escScanner
	// ESC c followed by CSI 2J in one chunk
	got := s.Scan([]byte("\x1bc\x1b[2J"))
	assert.True(t, got.ScrollReset, "ESC c should set ScrollReset")
	assert.True(t, got.BarErased, "CSI 2J should set BarErased")
}

func TestScannerInGround(t *testing.T) {
	var s escScanner

	require.True(t, s.InGround(), "new scanner should be in ground state")

	s.Scan([]byte("\x1b"))
	assert.False(t, s.InGround(), "should not be in ground after bare ESC")

	s.Scan([]byte("["))
	assert.False(t, s.InGround(), "should not be in ground inside CSI")

	s.Scan([]byte("m"))
	assert.True(t, s.InGround(), "should be in ground after CSI m completes")
}

func TestScannerStringTerminatedByST(t *testing.T) {
	var s escScanner
	// DCS string terminated by ST (ESC \)
	got := s.Scan([]byte("\x1bPsome data\x1b\\"))
	assert.False(t, got.ScrollReset)
	assert.False(t, got.BarErased)
	assert.True(t, s.InGround(), "scanner should be in ground after ST")
}

func TestScannerRISInsideString(t *testing.T) {
	var s escScanner
	// ESC c (RIS) seen while inside an OSC string aborts the string
	// and resets the terminal — ward must detect the scroll reset.
	got := s.Scan([]byte("\x1b]0;title\x1bc"))
	assert.True(t, got.ScrollReset, "ESC c inside OSC should set ScrollReset")
	assert.True(t, s.InGround(), "scanner should be in ground after ESC c")
}

func TestScannerAltScreen(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  scanResult
	}{
		{"CSI ?1049h (enter alt screen)", []byte("\x1b[?1049h"), scanResult{ScrollReset: true}},
		{"CSI ?1049l (leave alt screen)", []byte("\x1b[?1049l"), scanResult{ScrollReset: true}},
		{"CSI ?1047h (alt buffer)", []byte("\x1b[?1047h"), scanResult{ScrollReset: true}},
		{"CSI ?1047l (alt buffer)", []byte("\x1b[?1047l"), scanResult{ScrollReset: true}},
		{"CSI ?47h (old alt screen)", []byte("\x1b[?47h"), scanResult{ScrollReset: true}},
		{"CSI ?47l (old alt screen)", []byte("\x1b[?47l"), scanResult{ScrollReset: true}},
		{"CSI ?25h (show cursor, not alt)", []byte("\x1b[?25h"), scanResult{}},
		{"CSI ?1000h (mouse, not alt)", []byte("\x1b[?1000h"), scanResult{}},
		{"CSI ?2004h (bracketed paste, not alt)", []byte("\x1b[?2004h"), scanResult{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s escScanner
			got := s.Scan(tt.input)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestScannerAltScreenSplitAcrossReads(t *testing.T) {
	var s escScanner

	r1 := s.Scan([]byte("\x1b[?10"))
	assert.False(t, r1.ScrollReset)
	assert.False(t, s.InGround())

	r2 := s.Scan([]byte("49h"))
	assert.True(t, r2.ScrollReset, "completing CSI ?1049h across reads should set ScrollReset")
	assert.True(t, s.InGround())
}
