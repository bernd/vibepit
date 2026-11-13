package sshd

import (
	"net"
	"strconv"
	"strings"

	"github.com/bernd/vibepit/tui"
	"github.com/charmbracelet/colorprofile"
)

// ForwardsRequestType is a session channel request that "connect --forward"
// sends before "shell" to list its port forwards. vibed shows them as a
// banner in a session it creates for that shell. It is informational: the
// forwards work without it, and an older vibed rejects it.
const ForwardsRequestType = "forwards@vibepit"

// maxBannerForwards bounds the banner a client can make vibed print.
const maxBannerForwards = 32

// ForwardsRequest is the ForwardsRequestType payload: host listen addresses
// and their sandbox destinations as host:port, index-aligned. Fields are
// exported for ssh.Marshal/Unmarshal.
type ForwardsRequest struct {
	Local  []string
	Remote []string
}

func (r ForwardsRequest) valid() bool {
	if len(r.Local) == 0 || len(r.Local) != len(r.Remote) || len(r.Local) > maxBannerForwards {
		return false
	}
	for i := range r.Local {
		// SplitHostPort does not check the characters of either part, so
		// the banner's text is limited to printable ASCII separately: an
		// escape sequence (a raw C1 CSI byte is enough) could otherwise
		// query the emulator or redraw the screen.
		if !printableASCII(r.Local[i]) || !printableASCII(r.Remote[i]) {
			return false
		}
		if _, port, err := net.SplitHostPort(r.Local[i]); err != nil || !validPort(port) {
			return false
		}
		host, port, err := net.SplitHostPort(r.Remote[i])
		if err != nil || !validPort(port) || !ForwardHostAllowed(host) {
			return false
		}
	}
	return true
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// sandboxTarget describes a forward's sandbox destination for the banner.
// "localhost" is the client's default and covers both loopback families,
// so it reads as just "port N"; a host the user named, like [::1], stays.
func sandboxTarget(remote string) string {
	host, port, err := net.SplitHostPort(remote)
	if err == nil && strings.EqualFold(host, "localhost") {
		return "port " + port
	}
	return remote
}

func validPort(s string) bool {
	p, err := strconv.ParseUint(s, 10, 16)
	return err == nil && p > 0
}

// banner renders the forwards for session.CreateWithBanner as status lines
// in the style of the vibepit CLI's own output, then a blank line to set
// them apart from the prompt. Colors are downsampled to the profile env
// (the SSH session's environment) describes, as for the session selector:
// the emulator keeps them as written and replays them to the client.
func (r ForwardsRequest) banner(env []string) []byte {
	if len(r.Local) == 0 {
		return nil
	}
	locals := make([]string, len(r.Local))
	width := 0
	for i, local := range r.Local {
		if strings.HasPrefix(local, ":") {
			local = "*" + local // all interfaces
		}
		locals[i] = local
		width = max(width, len(local)) // valid() allows only ASCII
	}
	var b strings.Builder
	for i, local := range locals {
		// Pad so the arrows line up.
		b.WriteString(tui.StatusLine("Forwarding", "%-*s → sandbox %s", width, local, sandboxTarget(r.Remote[i])) + "\r\n")
	}
	b.WriteString("\r\n")
	var out strings.Builder
	w := &colorprofile.Writer{Forward: &out, Profile: colorprofile.Env(env)}
	w.WriteString(b.String()) //nolint:errcheck // strings.Builder never fails
	return []byte(out.String())
}
