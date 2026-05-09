package session

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProcStat(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantPID  int
		wantPGID int
		wantSID  int
		wantOK   bool
	}{
		{
			name:     "simple command",
			line:     "12345 (bash) S 100 12345 12340 34816 12345 4194304 1000 0 0 0 10 5 0 0 20 0 1 0 1000 5000000 500 18446744073709551615 0 0 0 0 0 0 0 0 65536 0 0 0 17 0 0 0 0 0 0",
			wantPID:  12345,
			wantPGID: 12345,
			wantSID:  12340,
			wantOK:   true,
		},
		{
			name:     "comm with spaces",
			line:     "999 (tmux: server) S 1 999 999 0 -1 4194560 500 0 0 0 5 2 0 0 20 0 1 0 2000 3000000 300 18446744073709551615 0 0 0 0 0 0 0 0 65536 0 0 0 17 0 0 0 0 0 0",
			wantPID:  999,
			wantPGID: 999,
			wantSID:  999,
			wantOK:   true,
		},
		{
			name:     "comm with nested parens",
			line:     "42 (a (b) c) S 1 42 10 0 -1 4194560 100 0 0 0 1 1 0 0 20 0 1 0 500 1000000 100 18446744073709551615 0 0 0 0 0 0 0 0 65536 0 0 0 17 0 0 0 0 0 0",
			wantPID:  42,
			wantPGID: 42,
			wantSID:  10,
			wantOK:   true,
		},
		{
			name:     "orphaned shell from observed bug",
			line:     "186243 (/bin/sh) R 36859 186243 186242 0 -1 4194304 200 0 0 0 99 0 0 0 20 0 1 0 3000 2000000 150 18446744073709551615 0 0 0 0 0 0 0 0 65536 0 0 0 17 0 0 0 0 0 0",
			wantPID:  186243,
			wantPGID: 186243,
			wantSID:  186242,
			wantOK:   true,
		},
		{
			name:   "empty line",
			line:   "",
			wantOK: false,
		},
		{
			name:   "no closing paren",
			line:   "123 (bash S 1 123 123",
			wantOK: false,
		},
		{
			name:   "too few fields after comm",
			line:   "123 (bash) S 1",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pid, pgid, sid, ok := parseProcStat(tt.line)
			if !tt.wantOK {
				assert.False(t, ok)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tt.wantPID, pid)
			assert.Equal(t, tt.wantPGID, pgid)
			assert.Equal(t, tt.wantSID, sid)
		})
	}
}

func TestFindProcessesBySID_ExcludesSelfAndShell(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc filesystem")
	}

	self := os.Getpid()
	// findProcessesBySID with a SID matching our own process should still
	// exclude our PID when we pass it as shellPID or when it matches self.
	procs := findProcessesBySID(self, self)
	for _, p := range procs {
		assert.NotEqual(t, self, p.pid, "should exclude self")
	}
}
