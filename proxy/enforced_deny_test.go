package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnforcedDenyFiltering(t *testing.T) {
	rules, err := NewDenyRules([]string{"example.com:443", "host.vibepit:*"}, []string{"example.com", "host.vibepit"})
	require.NoError(t, err)
	al, _ := NewHTTPAllowlist([]string{"example.com:443"})
	log := NewLogBuffer(100)
	p := NewHTTPProxy(NewPolicy(al, nil, rules), NewCIDRBlocker(nil, nil), log, "127.0.0.1:1")
	p.SetHostVibepit("127.0.0.1", []int{80})
	for _, target := range []struct{ host, port string }{{"example.com", "443"}, {"host.vibepit", "80"}} {
		r := p.checkRequest(context.Background(), target.host, target.port)
		require.Equal(t, BlockDenied, r.code)
		require.Equal(t, "domain in denylist", r.reason)
	}
	for _, method := range []string{"GET", "CONNECT"} {
		target := "http://host.vibepit:80/"
		if method == "CONNECT" {
			target = "host.vibepit:80"
		}
		req := httptest.NewRequest(method, target, nil)
		req.Host = "host.vibepit:80"
		w := httptest.NewRecorder()
		p.Handler().ServeHTTP(w, req)
		require.Equal(t, 403, w.Code)
	}
	da, _ := NewDNSAllowlist([]string{"example.com"})
	s := NewDNSServer(NewPolicy(nil, da, rules), NewCIDRBlocker(nil, nil), log, "127.0.0.1:1")
	s.SetProxyIP(net.ParseIP("127.0.0.1"))
	addr, stop := s.ListenAndServeTest()
	defer stop()
	for _, host := range []string{"example.com.", "host.vibepit."} {
		m := new(dns.Msg)
		m.SetQuestion(host, dns.TypeA)
		r, _, err := new(dns.Client).Exchange(m, addr)
		require.NoError(t, err)
		require.Equal(t, dns.RcodeNameError, r.Rcode)
	}
	for _, e := range log.Entries() {
		require.Equal(t, BlockDenied, e.Code)
		require.False(t, e.Allowable())
	}
}

func TestEnforcedDenyAPI(t *testing.T) {
	al, _ := NewHTTPAllowlist([]string{"a.example.com:443"})
	da, _ := NewDNSAllowlist(nil)
	log := NewLogBuffer(100)
	log.Add(LogEntry{Domain: "a.example.com", Port: "443", Source: SourceProxy, Action: ActionAllow})
	rules, _ := NewDenyRules(nil, nil)
	api := NewControlAPI(log, nil, NewPolicy(al, da, rules))
	for _, path := range []string{"/deny-http", "/deny-dns"} {
		good := "good.example.com"
		if path == "/deny-http" {
			good += ":443"
		}
		for _, body := range []string{`{`, `{"entries":[]}`, `{"entries":["` + good + `","bad entry"]}`, `{"entries":["` + good + `"]} {}`} {
			w := httptest.NewRecorder()
			api.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
			require.Equal(t, 400, w.Code)
		}
	}
	require.False(t, rules.Denied(Target{Source: SourceDNS, Host: "good.example.com"}))
	require.False(t, rules.Denied(Target{Source: SourceProxy, Host: "good.example.com", Port: "443"}))
	for _, tc := range []struct{ path, body string }{{"/deny-http", `{"entries":["*.example.com:443","other.com:80"]}`}, {"/deny-dns", `{"entries":["*.example.com","other.com"]}`}} {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
		require.Equal(t, 200, w.Code)
	}
	w := httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest("GET", "/check?source=proxy&target=a.example.com:443", nil))
	require.JSONEq(t, `{"allowed":false,"denied":true}`, w.Body.String())
	w = httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest("GET", "/hosts", nil))
	require.Contains(t, w.Body.String(), `"denied":true`)
	require.Contains(t, w.Body.String(), `"allowlisted":false`)
	require.Contains(t, w.Body.String(), `"last_action":"allow"`)
}

func TestEnforcedDenyHolds(t *testing.T) {
	f := newHoldsFixture(t, time.Second, true)
	rules := f.rules
	keys := []HostKey{{Source: SourceProxy, Domain: "a.example.com", Port: "443"}, {Source: SourceProxy, Domain: "b.example.com", Port: "443"}, {Source: SourceProxy, Domain: "other.com", Port: "443"}}
	channels := make([]<-chan HoldOutcome, 3)
	for i, k := range keys {
		channels[i] = f.wait(context.Background(), k)
		f.awaitWaiters(t, k, 1)
	}
	require.NoError(t, rules.AddHTTP([]string{"*.example.com:443"}))
	f.holds.EndDenied()
	for i := range 2 {
		require.Equal(t, HoldDenied, recvOutcome(t, channels[i], time.Second))
	}
	assertStillWaiting(t, channels[2])
	require.NoError(t, f.allowlist.Add([]string{"a.example.com:443"}))
	f.holds.EndAllowed()
	require.Equal(t, HoldNone, waitOutcome(f.holds.Wait(context.Background(), keys[0])))
}

func TestDeniedBetweenFilterAndHoldAdmission(t *testing.T) {
	f := newHoldsFixture(t, time.Second, true)
	rules := f.rules
	p := NewHTTPProxy(NewPolicy(f.allowlist, nil, rules), NewCIDRBlocker(nil, nil), f.log, "127.0.0.1:1")
	p.SetHolds(f.holds)
	entered, release := make(chan struct{}), make(chan struct{})
	decide := f.holds.decide
	var stall sync.Once
	f.holds.decide = func(t Target) Decision {
		stall.Do(func() { close(entered); <-release })
		return decide(t)
	}
	done := make(chan filterResult, 1)
	go func() { done <- p.checkRequest(context.Background(), "a.example.com", "443") }()
	<-entered
	// The target is now both allowed and denied: the deny must win.
	require.NoError(t, f.allowlist.Add([]string{"a.example.com:443"}))
	require.NoError(t, rules.AddHTTP([]string{"*.example.com:443"}))
	close(release)
	require.Equal(t, BlockDenied, (<-done).code)
	require.Empty(t, f.holds.Open())
	require.Equal(t, BlockDenied, f.log.Entries()[0].Code)
}

func TestDeniedConcurrentAllowRelease(t *testing.T) {
	f := newHoldsFixture(t, time.Second, true)
	rules := f.rules
	p := NewHTTPProxy(NewPolicy(f.allowlist, nil, rules), NewCIDRBlocker(nil, nil), f.log, "127.0.0.1:1")
	p.SetHolds(f.holds)
	key := HostKey{Source: SourceProxy, Domain: "a.example.com", Port: "443"}
	done := make(chan filterResult, 1)
	go func() { done <- p.checkRequest(context.Background(), key.Domain, key.Port) }()
	f.awaitWaiters(t, key, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	decide := f.holds.decide
	var stall sync.Once
	f.holds.decide = func(t Target) Decision {
		stall.Do(func() { close(entered); <-release })
		return decide(t)
	}
	released := make(chan struct{})
	go func() { f.holds.EndAllowed(); close(released) }()
	<-entered
	// The target is now both allowed and denied: the deny must win.
	require.NoError(t, f.allowlist.Add([]string{"a.example.com:443"}))
	require.NoError(t, rules.AddHTTP([]string{"*.example.com:443"}))
	close(release)
	<-released
	f.holds.EndDenied()
	require.Equal(t, BlockDenied, (<-done).code)
	require.Empty(t, f.holds.Open())
	entries := f.log.Entries()
	require.Equal(t, HoldDenied, entries[0].Outcome)
	require.Equal(t, BlockDenied, entries[1].Code)
}

func TestEnforcedDenyZeroPaddedPortCannotReachUpstream(t *testing.T) {
	for _, wildcardAllow := range []bool{false, true} {
		for _, runtime := range []bool{false, true} {
			t.Run(fmt.Sprintf("wildcardAllow=%v/runtime=%v", wildcardAllow, runtime), func(t *testing.T) {
				var reached atomic.Int32
				backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
				backend.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					if state == http.StateNew {
						reached.Add(1)
					}
				}
				backend.Start()
				defer backend.Close()
				u, err := url.Parse(backend.URL)
				require.NoError(t, err)
				port := u.Port()
				portNumber, err := strconv.Atoi(port)
				require.NoError(t, err)
				entries := []string(nil)
				if wildcardAllow {
					entries = []string{"host.vibepit:*"}
				}
				al, err := NewHTTPAllowlist(entries)
				require.NoError(t, err)
				log := NewLogBuffer(100)
				deny := []string{"host.vibepit:" + port}
				initial := deny
				if runtime {
					initial = nil
				}
				rules, err := NewDenyRules(initial, nil)
				require.NoError(t, err)
				p := NewHTTPProxy(NewPolicy(al, nil, rules), NewCIDRBlocker(nil, nil), log, DefaultUpstreamDNS)
				p.SetHostVibepit(u.Host, []int{portNumber})
				if runtime {
					require.NoError(t, rules.AddHTTP(deny))
				}
				server := httptest.NewServer(p.Handler())
				defer server.Close()
				for _, method := range []string{"GET", "CONNECT"} {
					conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
					require.NoError(t, err)
					t.Cleanup(func() { _ = conn.Close() })
					require.NoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
					target := "host.vibepit:0" + port
					requestTarget := target
					if method == "GET" {
						requestTarget = "http://" + target + "/"
					}
					_, err = fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", method, requestTarget, target)
					require.NoError(t, err)
					resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
					if method == "CONNECT" {
						// RejectConnect closes HTTP/1 connections without a response.
						require.ErrorIs(t, err, io.ErrUnexpectedEOF)
					} else {
						require.NoError(t, err)
						resp.Body.Close()
						assert.Equal(t, 403, resp.StatusCode, method)
					}
					conn.Close()
				}
				require.Zero(t, reached.Load())
				require.Len(t, log.Entries(), 2)
				for _, entry := range log.Entries() {
					require.Equal(t, BlockDenied, entry.Code)
				}
			})
		}
	}
}

func TestAllowOfEnforcedDeniedTargetRejected(t *testing.T) {
	al, _ := NewHTTPAllowlist(nil)
	da, _ := NewDNSAllowlist(nil)
	rules, _ := NewDenyRules([]string{"evil.com:443"}, []string{"evil.com"})
	api := NewControlAPI(NewLogBuffer(10), nil, NewPolicy(al, da, rules))
	for _, tc := range []struct {
		path, body string
		code       int
	}{
		{"/allow-http", `{"entries":["evil.com:443"]}`, 409},
		{"/allow-http", `{"entries":["ok.com:443","EVIL.com:443"]}`, 409},
		{"/allow-dns", `{"entries":["evil.com"]}`, 409},
		{"/allow-http", `{"entries":["*.evil.com:443"]}`, 200},
		{"/allow-http", `{"entries":["evil.com:80"]}`, 200},
	} {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
		assert.Equal(t, tc.code, w.Code, tc.body)
	}
	assert.False(t, al.Allows("ok.com", "443"), "a rejected batch adds nothing")
}

// A user's deny ends the hold as a block even when an allow of the same
// target lands before the request is decided.
func TestHoldDeniedStaysBlockedDespiteRacingAllow(t *testing.T) {
	f := newHoldsFixture(t, time.Second, true)
	rules := f.rules
	p := NewHTTPProxy(NewPolicy(f.allowlist, nil, rules), NewCIDRBlocker(nil, nil), f.log, "127.0.0.1:1")
	p.SetHolds(f.holds)
	key := HostKey{Source: SourceProxy, Domain: "a.example.com", Port: "443"}
	done := make(chan filterResult, 1)
	go func() { done <- p.checkRequest(context.Background(), key.Domain, key.Port) }()
	f.awaitWaiters(t, key, 1)
	require.NoError(t, f.allowlist.Add([]string{"a.example.com:443"}))
	f.holds.End(key.Target(), HoldDenied)
	r := <-done
	require.Equal(t, ActionBlock, r.action)
	require.Equal(t, BlockNotAllowlisted, r.code)
}

// A fully qualified name resolves to the same host, so a trailing dot must
// not slip past a deny, even where a broad allow pattern matches it.
func TestEnforcedDenyTrailingDot(t *testing.T) {
	rules, _ := NewDenyRules([]string{"api.example.com:443"}, []string{"api.example.com"})
	al, _ := NewHTTPAllowlist([]string{"api.**:443"})
	p := NewHTTPProxy(NewPolicy(al, nil, rules), NewCIDRBlocker(nil, nil), NewLogBuffer(10), "127.0.0.1:1")
	for _, hostport := range []string{"api.example.com.:443", "API.example.com..:443"} {
		host, port, ok := canonicalTarget(hostport, "443")
		require.True(t, ok, hostport)
		assert.Equal(t, BlockDenied, p.checkRequest(context.Background(), host, port).code, hostport)
		req := httptest.NewRequest("CONNECT", hostport, nil)
		req.Host = hostport
		w := httptest.NewRecorder()
		p.Handler().ServeHTTP(w, req)
		assert.NotEqual(t, 200, w.Code, hostport)
	}
	assert.True(t, rules.Denied(Target{Source: SourceProxy, Host: "api.example.com.", Port: "443"}))
	assert.True(t, rules.Denied(Target{Source: SourceDNS, Host: "api.example.com."}))
}

func TestAllowRejectionNamesEveryDeniedEntry(t *testing.T) {
	al, _ := NewHTTPAllowlist(nil)
	da, _ := NewDNSAllowlist(nil)
	rules, _ := NewDenyRules([]string{"a.com:443", "b.com:443"}, nil)
	api := NewControlAPI(NewLogBuffer(10), nil, NewPolicy(al, da, rules))
	w := httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest("POST", "/allow-http", strings.NewReader(`{"entries":["good.com:443","a.com:443","b.com:443"]}`)))
	require.Equal(t, 409, w.Code)
	var body struct{ Error string }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Contains(t, body.Error, `"a.com:443", "b.com:443"`)
	assert.Contains(t, body.Error, "nothing was allowed")
	assert.False(t, al.Allows("good.com", "443"))
}

func TestAllowValidatesBeforeDenyCheckAndErrorsAreJSON(t *testing.T) {
	al, _ := NewHTTPAllowlist(nil)
	da, _ := NewDNSAllowlist(nil)
	rules, _ := NewDenyRules([]string{"evil.com:443", "évil.com:443"}, nil)
	api := NewControlAPI(NewLogBuffer(10), nil, NewPolicy(al, da, rules))
	for _, tc := range []struct {
		body string
		code int
		want string
	}{
		{`{"entries":["bad entry","evil.com:443"]}`, 400, "bad entry"},
		{`{"entries":["évil.com:443"]}`, 409, "évil.com:443"},
		{`{`, 400, "invalid JSON"},
	} {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest("POST", "/allow-http", strings.NewReader(tc.body)))
		assert.Equal(t, tc.code, w.Code, tc.body)
		var body struct{ Error string }
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
		assert.Contains(t, body.Error, tc.want)
	}
}

// Only the first question is checked, so a query with more is refused
// rather than forwarded with an unchecked denied name.
func TestDNSRefusesMultipleQuestions(t *testing.T) {
	rules, _ := NewDenyRules(nil, []string{"telemetry.example.com"})
	da, _ := NewDNSAllowlist([]string{"allowed.example.com"})
	s := NewDNSServer(NewPolicy(nil, da, rules), NewCIDRBlocker(nil, nil), NewLogBuffer(10), "127.0.0.1:1")
	addr, stop := s.ListenAndServeTest()
	defer stop()
	m := new(dns.Msg)
	m.SetQuestion("allowed.example.com.", dns.TypeA)
	m.Question = append(m.Question, dns.Question{Name: "telemetry.example.com.", Qtype: dns.TypeA, Qclass: dns.ClassINET})
	r, _, err := new(dns.Client).Exchange(m, addr)
	require.NoError(t, err)
	require.Equal(t, dns.RcodeFormatError, r.Rcode)
}

// Without deny rules nothing would enforce a deny, so the endpoints refuse
// instead of reporting rules as added.
func TestControlAPIWithoutRulesRefusesDenies(t *testing.T) {
	al, _ := NewHTTPAllowlist(nil)
	da, _ := NewDNSAllowlist(nil)
	api := NewControlAPI(NewLogBuffer(10), nil, NewPolicy(al, da, nil))
	for _, tc := range []struct{ path, body string }{{"/deny-http", `{"entries":["a.com:443"]}`}, {"/deny-dns", `{"entries":["a.com"]}`}} {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
		assert.Equal(t, 404, w.Code, tc.path)
	}
}

// A deny arriving while an allow of the same target is between its deny
// check and its add waits for it, so the allow can't be reported after
// the deny refused it; an allow after the deny is refused.
func TestAllowAndDenySerialize(t *testing.T) {
	al, _ := NewHTTPAllowlist(nil)
	da, _ := NewDNSAllowlist(nil)
	rules, _ := NewDenyRules(nil, nil)
	api := NewControlAPI(NewLogBuffer(10), nil, NewPolicy(al, da, rules))
	post := func(path, entry string) int {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"entries":["`+entry+`"]}`)))
		return w.Code
	}
	denied := make(chan int, 1)
	testHookAllowChecked = func() {
		testHookAllowChecked = nil
		go func() { denied <- post("/deny-http", "evil.com:443") }()
		select {
		case <-denied:
			require.FailNow(t, "the deny ran while the allow held the rule lock")
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Cleanup(func() { testHookAllowChecked = nil })

	assert.Equal(t, 200, post("/allow-http", "evil.com:443"))
	assert.Equal(t, 200, <-denied)
	assert.Equal(t, 409, post("/allow-http", "evil.com:443"))
	assert.True(t, rules.Denied(Target{Source: SourceProxy, Host: "evil.com", Port: "443"}))
}
