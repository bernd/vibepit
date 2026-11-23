package netproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStaticAddressSubjectToDefaultCIDRs(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer up.Close()
	_, port, err := net.SplitHostPort(up.Listener.Addr().String())
	require.NoError(t, err)
	authority := net.JoinHostPort("service.test", port)
	p, err := newInspectionProxy([]inspectionOrigin{{
		authority: authority, rules: []requestRule{{}},
	}}, nil, upstreamConfig{staticHosts: map[string]netip.Addr{"service.test": netip.MustParseAddr("127.0.0.1")}})
	require.NoError(t, err)
	defer p.closeIdleConnections()
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://"+authority+"/", nil))
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Zero(t, calls.Load())
}

type resolveFunc func(context.Context, string) (Resolution, error)

func (f resolveFunc) ResolveIPv4(ctx context.Context, host string) (Resolution, error) {
	return f(ctx, host)
}

func TestReconnectRechecksTheWholeAnswer(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			var upstreamCalls, lookups atomic.Int32
			up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls.Add(1)
				w.Header().Set("Connection", "close")
				w.WriteHeader(http.StatusNoContent)
			}))
			upCert, upRoots := testCertificate(t)
			if scheme == "https" {
				up.TLS = &tls.Config{Certificates: []tls.Certificate{upCert}}
				up.StartTLS()
			} else {
				up.Start()
			}
			t.Cleanup(up.Close)
			_, port, err := net.SplitHostPort(up.Listener.Addr().String())
			require.NoError(t, err)
			authority := net.JoinHostPort("origin.test", port)
			leaf, roots := testCertificate(t)
			cfg := upstreamConfig{
				resolver: resolveFunc(func(ctx context.Context, host string) (Resolution, error) {
					if host != "origin.test" {
						return Resolution{}, fmt.Errorf("unexpected host %s", host)
					}
					answer := Resolution{Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
					if lookups.Add(1) > 1 {
						answer.Addresses = append(answer.Addresses, netip.MustParseAddr("10.0.0.8"))
					}
					return answer, nil
				}),
				allowCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
			}
			p, err := newInspectionProxy([]inspectionOrigin{{authority: authority, certificate: leaf, rules: []requestRule{{paths: []string{"/**"}}}}}, upRoots, cfg)
			require.NoError(t, err)
			defer p.closeIdleConnections()
			server := httptest.NewServer(p)
			defer server.Close()
			proxyURL, err := url.Parse(server.URL)
			require.NoError(t, err)
			tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}}
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
			for i, want := range []int{http.StatusNoContent, http.StatusForbidden} {
				var reused bool
				trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}
				req, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), trace), "GET", scheme+"://"+authority+"/", nil)
				require.NoError(t, err)
				resp, err := client.Do(req)
				require.NoError(t, err)
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				require.NoError(t, err)
				require.Equal(t, want, resp.StatusCode)
				if i == 1 {
					require.Contains(t, string(body), "blocked IP address 10.0.0.8")
					if scheme == "https" {
						require.True(t, reused, "same inspected client connection")
					}
				}
			}
			require.EqualValues(t, 2, lookups.Load())
			require.EqualValues(t, 1, upstreamCalls.Load())
		})
	}
}

func TestAliasRulesAtEachEntryPoint(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		rule                           requestRule
		port                           string
		wantPlain, wantTLS, wantTunnel int
	}{
		{"host deny", requestRule{deny: true}, "same", 403, 403, 403},
		{"path deny", requestRule{deny: true, paths: []string{"/private"}}, "same", 204, 204, 403},
		{"other port", requestRule{deny: true}, "other", 204, 204, 200},
		{"unused alias", requestRule{}, "same", 204, 204, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, form := range []string{"plain", "inspected", "tunnel"} {
				t.Run(form, func(t *testing.T) {
					var calls atomic.Int32
					up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						w.WriteHeader(http.StatusNoContent)
					}))
					upCert, upRoots := testCertificate(t)
					if form == "inspected" {
						up.TLS = &tls.Config{Certificates: []tls.Certificate{upCert}}
						up.StartTLS()
					} else {
						up.Start()
					}
					defer up.Close()
					port := netip.MustParseAddrPort(up.Listener.Addr().String()).Port()
					target := net.JoinHostPort("origin.test", strconv.Itoa(int(port)))
					aliasPort := port
					if tc.port == "other" {
						aliasPort = 1
					}
					leaf, roots := testCertificate(t)
					origin := inspectionOrigin{authority: target, rules: []requestRule{{}}}
					if form == "inspected" {
						origin.certificate = leaf
						origin.rules = []requestRule{{paths: []string{"/**"}}}
					}
					cfg := upstreamConfig{resolver: resolveFunc(func(context.Context, string) (Resolution, error) {
						return Resolution{Aliases: []string{"middle.test", "Alias.TEST."}, Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}, nil
					}), allowCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}
					p, err := newInspectionProxy([]inspectionOrigin{origin, {authority: net.JoinHostPort("alias.test", strconv.Itoa(int(aliasPort))), rules: []requestRule{tc.rule}}}, upRoots, cfg)
					require.NoError(t, err)
					defer p.closeIdleConnections()
					s := httptest.NewServer(p)
					defer s.Close()
					if form == "tunnel" {
						c, err := net.DialTimeout("tcp", s.Listener.Addr().String(), time.Second)
						require.NoError(t, err)
						defer c.Close()
						require.NoError(t, c.SetDeadline(time.Now().Add(time.Second)))
						_, err = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
						require.NoError(t, err)
						resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
						require.NoError(t, err)
						require.Equal(t, tc.wantTunnel, resp.StatusCode)
						if resp.StatusCode == 403 {
							body, err := io.ReadAll(resp.Body)
							require.NoError(t, err)
							require.Contains(t, string(body), "alias.test")
							resp.Body.Close()
						}
						return
					}
					scheme, want := "http", tc.wantPlain
					if form == "inspected" {
						scheme, want = "https", tc.wantTLS
					}
					proxyURL, err := url.Parse(s.URL)
					require.NoError(t, err)
					tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}}
					defer tr.CloseIdleConnections()
					client := http.Client{Transport: tr, Timeout: 3 * time.Second}
					resp, err := client.Get(scheme + "://" + target + "/private")
					require.NoError(t, err)
					resp.Body.Close()
					require.Equal(t, want, resp.StatusCode)
					if want == 403 {
						require.Zero(t, calls.Load())
					} else {
						require.EqualValues(t, 1, calls.Load())
					}
				})
			}
		})
	}
}

func TestDialSourcesAndCIDRs(t *testing.T) {
	loop := netip.MustParseAddr("127.0.0.1")
	unused := resolveFunc(func(context.Context, string) (Resolution, error) {
		t.Fatal("unexpected DNS lookup")
		return Resolution{}, nil
	})
	cfg := upstreamConfig{resolver: unused, staticHosts: map[string]netip.Addr{"STATIC.test.": loop}, gateway: loop,
		blockCIDRs: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, allowCIDRs: []netip.Prefix{netip.MustParsePrefix("192.0.2.20/32")}}
	d, err := newUpstreamDialer(cfg)
	require.NoError(t, err)
	for _, tc := range []struct {
		name       string
		hostPort   bool
		addr, code string
	}{
		{"static.test", false, "", "blocked_cidr"}, {"127.0.0.1", false, "", "blocked_cidr"},
		{"192.0.2.22", false, "", "blocked_cidr"}, {"192.0.2.20", false, "192.0.2.20", ""},
		{"gateway.test", true, loop.String(), ""}, {"203.0.113.1", false, "203.0.113.1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.candidates(t.Context(), tc.name, "443", tc.hostPort, false)
			if tc.code != "" {
				var block *dialRefusal
				require.ErrorAs(t, err, &block)
				require.Equal(t, tc.code, block.code)
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []netip.Addr{netip.MustParseAddr(tc.addr)}, got)
		})
	}
}

func TestInvalidUpstreamConfiguration(t *testing.T) {
	ip := netip.MustParseAddr("192.0.2.2")
	for _, cfg := range []upstreamConfig{
		{staticHosts: map[string]netip.Addr{"v6.test": netip.IPv6Loopback()}},
		{staticHosts: map[string]netip.Addr{"api.test": ip, "API.TEST.": ip}},
		{gateway: netip.IPv6Loopback()}, {blockCIDRs: []netip.Prefix{{}}},
	} {
		_, err := newUpstreamDialer(cfg)
		require.Error(t, err)
	}
}

func TestCheckedAddressesRemainPinned(t *testing.T) {
	answer := []netip.Addr{netip.MustParseAddr("192.0.2.10")}
	d, err := newUpstreamDialer(upstreamConfig{resolver: resolveFunc(func(context.Context, string) (Resolution, error) {
		return Resolution{Addresses: answer}, nil
	})})
	require.NoError(t, err)
	got, err := d.candidates(t.Context(), "origin.test", "443", false, false)
	require.NoError(t, err)
	answer[0] = netip.MustParseAddr("10.0.0.1")
	require.Equal(t, netip.MustParseAddr("192.0.2.10"), got[0])
}

func TestLookupFailureRefusesConnect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer Resolution
		err    error
	}{
		{"resolver error", Resolution{}, errors.New("DNS unavailable")},
		{"empty answer", Resolution{}, nil},
		{"IPv6 answer", Resolution{Addresses: []netip.Addr{netip.IPv6Loopback()}}, nil},
		{"invalid alias", Resolution{Aliases: []string{"bad..name"}, Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.2")}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := upstreamConfig{resolver: resolveFunc(func(context.Context, string) (Resolution, error) { return tc.answer, tc.err })}
			p, err := newInspectionProxy([]inspectionOrigin{{authority: "origin.test:443", rules: []requestRule{{}}}}, nil, cfg)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			p.ServeHTTP(w, httptest.NewRequest(http.MethodConnect, "origin.test:443", nil))
			require.Equal(t, http.StatusForbidden, w.Code)
		})
	}
}

func TestAbandonedRequestStopsResolution(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	resolver := resolveFunc(func(ctx context.Context, _ string) (Resolution, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return Resolution{}, ctx.Err()
	})
	p, err := newInspectionProxy([]inspectionOrigin{{authority: "origin.test:80", rules: []requestRule{{}}}}, nil, upstreamConfig{resolver: resolver})
	require.NoError(t, err)
	defer p.closeIdleConnections()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://origin.test/", nil)
	done := make(chan struct{})
	go func() {
		p.ServeHTTP(httptest.NewRecorder(), r)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("resolver did not start")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("resolver outlived the abandoned request")
	}
	<-done
}

func TestLookupTimeoutIsARefusal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d, err := newUpstreamDialer(upstreamConfig{resolver: resolveFunc(func(ctx context.Context, _ string) (Resolution, error) {
			<-ctx.Done()
			return Resolution{}, ctx.Err()
		})})
		require.NoError(t, err)
		_, err = d.dial(t.Context(), "origin.test", "443", false, false)
		var refused *dialRefusal
		require.ErrorAs(t, err, &refused)
		require.Equal(t, "resolution_failed", refused.code)
	})
}

func TestDialTriesSecondAddressAfterFirstTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ips := []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		var attempts []string
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		got, err := dialAddresses(ctx, ips, "443", func(attempt context.Context, network, endpoint string) (net.Conn, error) {
			require.Equal(t, "tcp4", network)
			attempts = append(attempts, endpoint)
			if len(attempts) == 1 {
				<-attempt.Done()
				return nil, attempt.Err()
			}
			require.NoError(t, attempt.Err())
			return client, nil
		})
		require.NoError(t, err)
		require.Same(t, client, got)
		require.Equal(t, []string{"192.0.2.1:443", "192.0.2.2:443"}, attempts)
	})
}
