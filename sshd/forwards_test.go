package sshd

import (
	"strings"
	"testing"
	"time"

	"github.com/bernd/vibepit/session"
	"github.com/bernd/vibepit/tui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

func TestForwardsRequestValid(t *testing.T) {
	many := make([]string, maxBannerForwards+1)
	for i := range many {
		many[i] = "127.0.0.1:3000"
	}
	tests := []struct {
		name string
		req  ForwardsRequest
		want bool
	}{
		{"one", ForwardsRequest{[]string{"127.0.0.1:3000"}, []string{"localhost:3000"}}, true},
		{"all interfaces", ForwardsRequest{[]string{":8080"}, []string{"localhost:3000"}}, true},
		{"ipv6", ForwardsRequest{[]string{"[::1]:8080"}, []string{"[::1]:3000"}}, true},
		{"empty", ForwardsRequest{}, false},
		{"length mismatch", ForwardsRequest{[]string{"127.0.0.1:1", "127.0.0.1:2"}, []string{"localhost:1"}}, false},
		{"not host:port", ForwardsRequest{[]string{"3000"}, []string{"localhost:3000"}}, false},
		{"control characters", ForwardsRequest{[]string{"\x1b[2J:3000"}, []string{"localhost:3000"}}, false},
		{"escape without bracket", ForwardsRequest{[]string{"\x1bZ:3000"}, []string{"localhost:3000"}}, false},
		{"raw C1 CSI in port", ForwardsRequest{[]string{"127.0.0.1:3000"}, []string{"localhost:\x9b6n"}}, false},
		{"non-numeric port", ForwardsRequest{[]string{"127.0.0.1:http"}, []string{"localhost:3000"}}, false},
		{"port zero", ForwardsRequest{[]string{"127.0.0.1:3000"}, []string{"localhost:0"}}, false},
		{"non-loopback remote", ForwardsRequest{[]string{"127.0.0.1:3000"}, []string{"10.0.0.1:3000"}}, false},
		{"too many", ForwardsRequest{many, many}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.req.valid())
		})
	}
}

func TestForwardsRequestBanner(t *testing.T) {
	req := ForwardsRequest{
		Local:  []string{"127.0.0.1:3000", "127.0.0.1:10000", ":8080"},
		Remote: []string{"localhost:3000", "localhost:10000", "[::1]:80"},
	}
	// Host addresses are padded so the arrows line up.
	assert.Equal(t,
		tui.StatusLine("Forwarding", "127.0.0.1:3000  → sandbox port 3000")+"\r\n"+
			tui.StatusLine("Forwarding", "127.0.0.1:10000 → sandbox port 10000")+"\r\n"+
			tui.StatusLine("Forwarding", "*:8080          → sandbox [::1]:80")+"\r\n"+
			"\r\n", // blank line before the prompt
		string(req.banner([]string{"TERM=xterm-256color", "COLORTERM=truecolor"})))
	assert.Empty(t, ForwardsRequest{}.banner(nil))

	// A terminal without truecolor gets the color downsampled, and one
	// without color support gets none.
	assert.NotContains(t, string(req.banner([]string{"TERM=xterm-256color"})), "38;2;")
	assert.NotContains(t, string(req.banner([]string{"TERM=dumb"})), "\x1b[")
}

// TestForwardsBannerViaSSH checks that a forwards request sent before the
// shell shows up above the new session's first output, and that one sent
// after the shell is rejected.
func TestForwardsBannerViaSSH(t *testing.T) {
	mgr := session.NewManager(50)
	mgr.Command = []string{"/bin/sh", "-c", "printf PROMPT; sleep 0.3"}
	client := dialTestServer(t, mgr)

	sess, err := client.NewSession()
	require.NoError(t, err)
	var out syncBuf
	sess.Stdout = &out

	payload := gossh.Marshal(&ForwardsRequest{Local: []string{"127.0.0.1:3000"}, Remote: []string{"localhost:3000"}})
	ok, err := sess.SendRequest(ForwardsRequestType, true, payload)
	require.NoError(t, err)
	require.True(t, ok, "forwards request before shell should be accepted")

	require.NoError(t, sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{}))
	require.NoError(t, sess.Shell())

	ok, err = sess.SendRequest(ForwardsRequestType, true, payload)
	require.NoError(t, err)
	assert.False(t, ok, "forwards request after shell should be rejected")

	done := make(chan struct{})
	go func() { sess.Wait(); close(done) }() //nolint:errcheck
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not exit in time")
	}

	got := out.String()
	// The replay re-renders styles, so match the unstyled part.
	banner := strings.Index(got, "127.0.0.1:3000 → sandbox port 3000")
	prompt := strings.Index(got, "PROMPT")
	require.GreaterOrEqual(t, banner, 0, "banner missing from output: %q", got)
	require.Greater(t, prompt, banner, "banner should come before the shell's output")
}
