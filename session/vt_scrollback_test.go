package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vt "github.com/unixshells/vt-go"
)

// TestVT_ED2PushesToScrollback characterizes the emulator's behavior when an
// application issues ED 2 (ESC[2J — "clear entire screen"). We rely on the
// cleared content going into scrollback so that reattach replay still shows
// output that was visible on screen before a `clear`.
func TestVT_ED2PushesToScrollback(t *testing.T) {
	e := vt.NewSafeEmulator(10, 5)
	defer e.Close() //nolint:errcheck

	// Fill rows with distinguishable content.
	_, err := e.Write([]byte("row1\r\nrow2\r\nrow3\r\n"))
	require.NoError(t, err)

	before := e.ScrollbackLen()

	// ESC[2J — erase entire screen.
	_, err = e.Write([]byte("\x1b[2J"))
	require.NoError(t, err)

	after := e.ScrollbackLen()

	assert.Greater(t, after, before,
		"ED 2 should push cleared visible content into scrollback; "+
			"scrollback len went from %d to %d", before, after)
}

// TestVT_AltScreenDoesNotPolluteScrollback characterizes the emulator's
// behavior when an application enters and leaves alt-screen mode (vim, less,
// htop). Lines written inside alt-screen must not appear in the main-screen
// scrollback; the old custom scrollback buffer had an explicit pause flag
// for this reason. With the VT emulator as the sole source of truth, we
// depend on the library to enforce this itself.
func TestVT_AltScreenDoesNotPolluteScrollback(t *testing.T) {
	e := vt.NewSafeEmulator(10, 5)
	defer e.Close() //nolint:errcheck

	// Seed the main screen and capture scrollback len.
	_, err := e.Write([]byte("main1\r\nmain2\r\n"))
	require.NoError(t, err)
	before := e.ScrollbackLen()

	// Enter alt-screen, write a screen of content, leave alt-screen.
	_, err = e.Write([]byte("\x1b[?1049h"))
	require.NoError(t, err)
	for range 20 {
		_, err = e.Write([]byte("altline\r\n"))
		require.NoError(t, err)
	}
	_, err = e.Write([]byte("\x1b[?1049l"))
	require.NoError(t, err)

	after := e.ScrollbackLen()
	assert.Equal(t, before, after,
		"alt-screen output must not enter main-screen scrollback; "+
			"scrollback len changed from %d to %d", before, after)
}
