//go:build democast

package cmd

// Generates the asciinema demo recordings for the documentation site:
//
//	make docs-casts
//
// The startup cast replays the real status output with a scripted sandbox
// shell. The monitor cast runs the real monitor UI against an in-process
// control API, so the recording matches the current UI. It runs on a virtual
// clock, so regenerating an unchanged UI reproduces the cast exactly.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/stretchr/testify/require"
)

var castDir = flag.String("cast-dir", "", "directory to write the .cast files to")

const (
	castWidth   = 100
	castHeight  = 30
	castHome    = "/home/dev"
	castProject = castHome + "/src/myapp"
	castSession = "d3m9q2kfh4ps73c1vb2g"
)

// castEpoch is the virtual wall-clock time when the monitor opens.
var castEpoch = time.Date(2026, time.October, 17, 10, 41, 0, 0, time.UTC)

// cast collects asciicast v2 output events on a virtual clock.
type cast struct {
	now    time.Duration
	events [][3]any
	rng    *rand.Rand
}

func newCast() *cast {
	return &cast{rng: rand.New(rand.NewPCG(1, 2))}
}

func (c *cast) out(s string) {
	c.events = append(c.events, [3]any{float64(c.now.Milliseconds()) / 1000, "o", s})
}

// println writes like a shell on a terminal with onlcr.
func (c *cast) println(s string) {
	c.out(strings.ReplaceAll(s, "\n", "\r\n") + "\r\n")
}

func (c *cast) wait(d time.Duration) {
	c.now += d
}

// typeText types s with uneven human-like delays.
func (c *cast) typeText(s string) {
	for _, r := range s {
		c.wait(time.Duration(40+c.rng.IntN(90)) * time.Millisecond)
		c.out(string(r))
	}
}

// run types a command and presses enter.
func (c *cast) run(prompt, command string) {
	c.out(prompt)
	c.wait(600 * time.Millisecond)
	c.typeText(command)
	c.wait(400 * time.Millisecond)
	c.out("\r\n")
}

func (c *cast) status(verb, format string, args ...any) {
	c.out(strings.ReplaceAll(captureStdout(func() { tui.Status(verb, format, args...) }), "\n", "\r\n"))
}

func (c *cast) save(t *testing.T, name, title string) {
	t.Helper()
	path := filepath.Join(*castDir, name)
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	enc := json.NewEncoder(f)
	require.NoError(t, enc.Encode(map[string]any{
		"version": 2,
		"width":   castWidth,
		"height":  castHeight,
		"title":   title,
		"env":     map[string]string{"TERM": "xterm-256color", "SHELL": "/bin/bash"},
	}))
	for _, e := range c.events {
		require.NoError(t, enc.Encode(e))
	}
	t.Logf("wrote %s (%.1fs)", path, c.now.Seconds())
}

// captureStdout returns what fn prints, for helpers that only write to stdout.
func captureStdout(fn func()) string {
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	// Drain concurrently so output beyond the pipe buffer cannot block fn.
	out := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		out <- b
	}()
	orig := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = orig
	w.Close()
	return string(<-out)
}

func rgb(r, g, b int) string { return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b) }

// hostPrompt is a plain prompt so the host shell does not look like vibepit.
func hostPrompt() string {
	return rgb(120, 200, 120) + "~/src/myapp" + "\x1b[0m $ "
}

// sandboxPrompt mirrors PS1 in image/config/bashrc.
func sandboxPrompt() string {
	return rgb(0, 212, 255) + "code" + rgb(0, 153, 204) + "@" + rgb(139, 92, 246) + "vibes" +
		rgb(0, 153, 204) + ":" + rgb(249, 115, 22) + castProject + " " + rgb(0, 212, 255) + "❯" + "\x1b[0m "
}

func TestDemoCasts(t *testing.T) {
	if *castDir == "" {
		t.Skip("set -cast-dir to generate the demo casts")
	}
	t.Setenv("HOME", castHome)
	require.NoError(t, os.MkdirAll(*castDir, 0o755))

	t.Run("start", func(t *testing.T) { recordStartCast(t) })
	t.Run("monitor", func(t *testing.T) {
		c := recordMonitorCast(t)
		// A second recording catches anything that still depends on the wall
		// clock or scheduling, which would rewrite the cast on every
		// regeneration.
		require.Equal(t, c.events, recordMonitorCast(t).events, "monitor cast is not reproducible")
		c.save(t, "monitor.cast", "Watch and allow traffic with the monitor")
	})
}

func recordStartCast(t *testing.T) {
	c := newCast()
	c.wait(500 * time.Millisecond)
	c.run(hostPrompt(), "vibepit up")

	// The status lines are copies of the tui.Status calls in UpAction and
	// startSessionInfra. Only their styling follows the real code, so keep
	// the verbs and messages in sync by hand.
	c.wait(150 * time.Millisecond)
	c.out(strings.ReplaceAll(tui.RenderBanner(castWidth, castHeight)+"\n\n", "\n", "\r\n"))
	c.wait(500 * time.Millisecond)
	c.status("Verifying", "image signature: %s-uid-1000-gid-1000", defaultImagePrefix)
	c.wait(900 * time.Millisecond)
	c.status("Verifying", "image signature: %s", ctr.ProxyImage)
	c.wait(700 * time.Millisecond)
	c.status("Creating", "network %s%s", networkNamePrefix, castSession)
	c.wait(300 * time.Millisecond)
	c.status("Starting", "proxy container")
	c.wait(600 * time.Millisecond)
	c.status("Creating", "sandbox container in %s", castProject)
	c.wait(300 * time.Millisecond)
	c.status("Starting", "sandbox container")
	c.wait(500 * time.Millisecond)
	c.status("Awaiting", "sandbox startup")
	c.wait(900 * time.Millisecond)
	c.status("Ready", "session %s", castSession)
	c.out("\r\n")
	c.wait(500 * time.Millisecond)

	c.run(hostPrompt(), "vibepit connect")
	c.wait(400 * time.Millisecond)

	// Allowed by the pkg-node preset.
	c.run(sandboxPrompt(), "curl -sSI https://registry.npmjs.org | head -1")
	c.wait(500 * time.Millisecond)
	c.println("HTTP/2 200 ")
	c.wait(1200 * time.Millisecond)

	// Not on any allowlist.
	c.run(sandboxPrompt(), "curl -sSI https://webhook.site")
	c.wait(200 * time.Millisecond)
	c.println("curl: (56) CONNECT tunnel failed, response 403")
	c.wait(2 * time.Second)

	// Mirrors handleLastExit. The session keeps running for the monitor.
	c.run(sandboxPrompt(), "exit")
	c.wait(200 * time.Millisecond)
	c.println("You were the last connection.")
	c.out("Shut down the sandbox? [y/N] ")
	c.wait(1200 * time.Millisecond)
	c.typeText("n")
	c.wait(300 * time.Millisecond)
	c.out("\r\n")
	c.out(hostPrompt())
	c.wait(2500 * time.Millisecond)
	c.out("")

	// tutorials/first-sandbox.md uses the last frame as poster (npt:0:28).
	// Update it when the length changes.
	c.save(t, "start.cast", "Start a sandbox and connect")
}

// demoProxy fakes sandbox traffic through a real allowlist and log buffer.
type demoProxy struct {
	log       *proxy.LogBuffer
	allowlist *proxy.HTTPAllowlist
}

func (p *demoProxy) request(domain string, at time.Time) {
	e := proxy.LogEntry{Time: at, Domain: domain, Port: "443", Source: proxy.SourceProxy, Action: proxy.ActionAllow}
	if !p.allowlist.Allows(domain, "443") {
		e.Action, e.Reason, e.Code = proxy.ActionBlock, "domain not in allowlist", proxy.BlockNotAllowlisted
	}
	p.log.Add(e)
}

// virtualClock lets tuiDriver deliver the window's ticks itself.
type virtualClock struct{ now time.Time }

func (c *virtualClock) Now() time.Time { return c.now }
func (c *virtualClock) Tick() tea.Cmd  { return nil }

// tuiDriver runs a Bubble Tea model without tea.Program. Messages and
// commands run in order on one goroutine, and frames go to the cast as
// line updates, so the recording does not depend on scheduling.
type tuiDriver struct {
	c     *cast
	clock *virtualClock
	model tea.Model
	base  time.Duration
	lines []string
	quit  bool
}

func newTUIDriver(c *cast, w *tui.Window) *tuiDriver {
	clock := &virtualClock{now: castEpoch}
	w.SetClock(clock)
	return &tuiDriver{c: c, clock: clock, model: w, base: c.now}
}

// advance moves the clocks to offset d from the driver start.
func (d *tuiDriver) advance(at time.Duration) {
	d.clock.now = castEpoch.Add(at)
	d.c.now = d.base + at
}

func (d *tuiDriver) send(msg tea.Msg) {
	var cmd tea.Cmd
	d.model, cmd = d.model.Update(msg)
	d.run(cmd)
}

// run executes cmd synchronously. The model's commands are quick (control
// API calls against the in-process server), and ticks come from the driver.
func (d *tuiDriver) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			d.run(c)
		}
	case tea.QuitMsg:
		d.quit = true
	default:
		d.send(msg)
	}
}

// render writes the lines that changed since the last frame.
func (d *tuiDriver) render() {
	lines := strings.Split(d.model.View().Content, "\n")
	var b strings.Builder
	for i := range max(len(lines), len(d.lines)) {
		var line string
		if i < len(lines) {
			line = lines[i]
		}
		if i < len(d.lines) && d.lines[i] == line {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[m\x1b[K", i+1, line)
	}
	d.lines = lines
	if b.Len() > 0 {
		d.c.out(b.String())
	}
}

func recordMonitorCast(t *testing.T) *cast {
	allowlist, err := proxy.NewHTTPAllowlist(proxy.NewPresetRegistry().Expand([]string{"default", "pkg-node"}))
	require.NoError(t, err)
	dnsAllowlist, err := proxy.NewDNSAllowlist(nil)
	require.NoError(t, err)
	p := &demoProxy{log: proxy.NewLogBuffer(10000), allowlist: allowlist}

	// A few minutes of agent traffic before the monitor opens.
	var history []string
	for domain, count := range map[string]int{
		"api.anthropic.com":     38,
		"statsig.anthropic.com": 6,
		"github.com":            4,
		"registry.npmjs.org":    21,
		"sentry.io":             3,
		"webhook.site":          2,
	} {
		for range count {
			history = append(history, domain)
		}
	}
	slices.Sort(history)
	rng := rand.New(rand.NewPCG(3, 4))
	rng.Shuffle(len(history), func(i, j int) { history[i], history[j] = history[j], history[i] })
	at := castEpoch.Add(-5 * time.Second)
	times := make([]time.Time, len(history))
	for i := range slices.Backward(times) {
		at = at.Add(-time.Duration(1+rng.IntN(5)) * time.Second)
		times[i] = at
	}
	for i, domain := range history {
		p.request(domain, times[i])
	}

	srv := httptest.NewServer(proxy.NewControlAPI(p.log, map[string]any{}, allowlist, dnsAllowlist))
	defer srv.Close()
	client := &ControlClient{http: srv.Client(), baseURL: srv.URL}

	c := newCast()
	c.wait(500 * time.Millisecond)
	c.run(hostPrompt(), "vibepit monitor")
	c.wait(200 * time.Millisecond)

	session := &SessionInfo{SessionID: castSession, ProjectDir: castProject}
	screen := newMonitorScreen(session, client, func() tui.Screen { return nil })
	header := &tui.HeaderInfo{ProjectDir: castProject, SessionID: castSession}
	d := newTUIDriver(c, tui.NewWindow(header, screen))
	now := func() time.Time { return d.clock.now }

	key := func(code rune, text string) func() {
		return func() { d.send(tea.KeyPressMsg{Code: code, Text: text}) }
	}
	// Times are offsets from the monitor start, which is about 2.9s into the
	// cast. The docs use the details pane after the allow as poster
	// (npt:0:13 in tutorials/demo.md, how-to/allowlist-and-monitor.md and
	// tutorials/first-sandbox.md). Update them when retiming the script.
	script := []struct {
		after  time.Duration
		action func()
	}{
		{1200 * time.Millisecond, func() { p.request("api.anthropic.com", now()) }},
		{2400 * time.Millisecond, func() { p.request("cdn.jsdelivr.net", now()) }},
		{3000 * time.Millisecond, func() { p.request("cdn.jsdelivr.net", now()) }},
		{4200 * time.Millisecond, key('j', "j")},
		{5400 * time.Millisecond, key(tea.KeyEnter, "")},
		{8400 * time.Millisecond, key('a', "a")},
		{10400 * time.Millisecond, key(tea.KeyEnter, "")},
		{11000 * time.Millisecond, func() { p.request("cdn.jsdelivr.net", now()) }},
		{11600 * time.Millisecond, func() { p.request("api.anthropic.com", now()) }},
		{12000 * time.Millisecond, func() { p.request("cdn.jsdelivr.net", now()) }},
		{14500 * time.Millisecond, key(tea.KeyTab, "")},
		{15500 * time.Millisecond, func() { p.request("api.anthropic.com", now()) }},
		{16200 * time.Millisecond, func() { p.request("registry.npmjs.org", now()) }},
		{19500 * time.Millisecond, key('q', "q")},
	}

	// Bubble Tea switches to the alternate screen and hides the cursor.
	c.out("\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J")
	d.send(tea.WindowSizeMsg{Width: castWidth, Height: castHeight})
	d.run(d.model.Init())
	d.render()
	nextTick := tui.TickInterval
	for step := 0; !d.quit; {
		if step < len(script) && script[step].after < nextTick {
			d.advance(script[step].after)
			script[step].action()
			step++
		} else {
			d.advance(nextTick)
			d.send(tui.TickMsg{})
			nextTick += tui.TickInterval
		}
		d.render()
	}
	c.out("\x1b[?1049l\x1b[?25h")

	c.out(hostPrompt())
	c.wait(2 * time.Second)
	c.out("")
	return c
}
