package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateFile_WrittenOnCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	m := testManager(50)
	m.SetStateFilePath(path)

	_, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var sessions []SessionInfo
	require.NoError(t, json.Unmarshal(data, &sessions))
	require.Len(t, sessions, 1)
	assert.Equal(t, "session-1", sessions[0].ID)
}

// TestStateFile_ConcurrentWritesNoStaleOverwrite verifies that the state
// file ends up reflecting the latest state after concurrent writes
// interleaved with state mutations. Previously writeStateFile snapshotted
// under m.mu, then *dropped* m.mu before writing and renaming the file.
// Two concurrent callers could each snapshot different states, race on
// the rename, and an older snapshot could win the rename — leaving stale
// content on disk even though the caller thought they had written the
// current state. The rename error was silently ignored.
//
// This test uses a barrier in the write-test hook to force a deterministic
// reproduction: the first writer parks after snapshotting 3 sessions, we
// create 2 more sessions (and let a fresh writer snapshot + rename the
// 5-session state), then we release the parked writer so its rename
// overwrites with the stale snapshot.
func TestStateFile_ConcurrentWritesNoStaleOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	m := testManager(50)
	m.SetStateFilePath(path)

	// Seed 3 sessions with the hook disabled so none of these Creates park.
	for range 3 {
		_, err := m.Create(80, 24, nil)
		require.NoError(t, err)
	}

	// Now install the parking hook: the next writer to enter it snapshots
	// 3 sessions, then blocks until we release it.
	parked := make(chan struct{})
	release := make(chan struct{})
	var first atomic.Bool
	prev := stateFileWriteTestHook
	stateFileWriteTestHook = func() {
		if !first.Swap(true) {
			close(parked)
			<-release
		}
	}
	defer func() { stateFileWriteTestHook = prev }()

	// Trigger a writer whose snapshot is 3 sessions. It parks inside the
	// hook after snapshotting, before writing/renaming.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.onSessionChanged()
	}()

	<-parked

	// With the stale-snapshot writer parked, add 2 more sessions. Each
	// Create also calls writeStateFile, which takes the non-parking path
	// (first is already true) and writes+renames a 5-session snapshot.
	for range 2 {
		_, err := m.Create(80, 24, nil)
		require.NoError(t, err)
	}

	// Let the parked writer go — it now writes+renames its stale
	// 3-session snapshot, which under the bug overwrites the current
	// 5-session state on disk.
	close(release)
	wg.Wait()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var sessions []SessionInfo
	require.NoError(t, json.Unmarshal(data, &sessions))
	assert.Len(t, sessions, 5,
		"final state file must reflect all 5 sessions; a stale writer "+
			"overwrote with an older snapshot")
}
