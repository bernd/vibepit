//go:build linux

package session

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readSIDFromProc(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	_, _, sid, ok := parseProcStat(string(data))
	if !ok {
		return 0, fmt.Errorf("failed to parse /proc/%d/stat", pid)
	}
	return sid, nil
}

func TestCleanupDescendants_KillsOrphanedGrandchild(t *testing.T) {
	// Start a shell under pty.StartWithSize which calls setsid, giving
	// it an isolated SID (== its PID). The shell spawns a background
	// grandchild that sleeps, then exits. cleanupDescendants should
	// kill the orphaned grandchild.

	// The grandchild writes its PID to a temp file so we can verify it.
	pidFile := t.TempDir() + "/grandchild.pid"

	// trap '' HUP prevents SIGHUP from being delivered to background jobs when
	// the PTY session leader exits, allowing the grandchild to survive the shell exit.
	cmd := exec.Command("/bin/sh", "-c",
		fmt.Sprintf("trap '' HUP\n/bin/sh -c 'echo $$ > %s; sleep 300' &\nexit 0", pidFile))

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	require.NoError(t, err)
	defer ptmx.Close()

	shellPID := cmd.Process.Pid
	sid := shellPID // pty.StartWithSize sets Setsid, so SID == PID.

	// Verify the fixture SID differs from the test runner's SID.
	testSID, err := readSIDFromProc(os.Getpid())
	require.NoError(t, err)
	require.NotEqual(t, testSID, sid,
		"fixture SID must differ from test runner SID to avoid killing go test")

	// Wait for the shell to exit (it runs "exit 0" after spawning the bg job).
	err = cmd.Wait()
	require.NoError(t, err)

	// Wait for grandchild PID file to appear.
	var grandchildPID int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return false
		}
		grandchildPID = pid
		return true
	}, 5*time.Second, 50*time.Millisecond, "grandchild PID file never appeared")

	// Verify grandchild is alive.
	err = syscallKillCheck(grandchildPID)
	require.NoError(t, err, "grandchild should be alive before cleanup")

	// Run cleanup.
	cleanupDescendants(shellPID, sid)

	// Verify grandchild is dead.
	err = syscallKillCheck(grandchildPID)
	assert.Error(t, err, "grandchild should be dead after cleanup")
}

// syscallKillCheck sends signal 0 to check if a process exists.
func syscallKillCheck(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.Signal(0))
}
