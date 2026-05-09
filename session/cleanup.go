package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// parseProcStat extracts PID, PGID, and SID from a /proc/<pid>/stat line.
// Field 2 (comm) is parenthesized and may contain spaces or nested parens,
// so parsing starts after the last ')'.
func parseProcStat(line string) (int, int, int, bool) {
	closeParen := strings.LastIndex(line, ")")
	if closeParen < 0 || closeParen+2 >= len(line) {
		return 0, 0, 0, false
	}

	pidStr, _, found := strings.Cut(line, " ")
	if !found {
		return 0, 0, 0, false
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, 0, 0, false
	}

	// Fields after comm: state ppid pgid sid ...
	// Indices:            0     1    2    3
	fields := strings.Fields(line[closeParen+2:])
	if len(fields) < 4 {
		return 0, 0, 0, false
	}

	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, 0, false
	}
	sid, err := strconv.Atoi(fields[3])
	if err != nil {
		return 0, 0, 0, false
	}

	return pid, pgid, sid, true
}

type procInfo struct {
	pid  int
	pgid int
}

// findProcessesBySID scans /proc for processes whose SID matches sid,
// excluding shellPID (already reaped) and the current process.
func findProcessesBySID(sid, shellPID int) []procInfo {
	self := os.Getpid()
	entries, err := filepath.Glob("/proc/[0-9]*/stat")
	if err != nil {
		return nil
	}

	var result []procInfo
	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		pid, pgid, procSID, ok := parseProcStat(string(data))
		if !ok || procSID != sid || pid == shellPID || pid == self {
			continue
		}
		result = append(result, procInfo{pid: pid, pgid: pgid})
	}
	return result
}

// cleanupDescendants kills processes that share the PTY session's SID.
// Uses PGID-based group kills first (SIGTERM), then a bounded loop of
// individual SIGKILL for survivors.
func cleanupDescendants(shellPID, sid int) {
	procs := findProcessesBySID(sid, shellPID)
	if len(procs) == 0 {
		return
	}

	// SIGTERM by process group for efficient cleanup.
	killed := make(map[int]bool)
	for _, p := range procs {
		if p.pgid == 0 || killed[p.pgid] {
			continue
		}
		killed[p.pgid] = true
		syscall.Kill(-p.pgid, syscall.SIGTERM) //nolint:errcheck
	}

	// Bounded loop: re-scan and SIGKILL survivors.
	for range 5 {
		time.Sleep(1 * time.Second)
		procs = findProcessesBySID(sid, shellPID)
		if len(procs) == 0 {
			return
		}
		for _, p := range procs {
			syscall.Kill(p.pid, syscall.SIGKILL) //nolint:errcheck
		}
	}
}
