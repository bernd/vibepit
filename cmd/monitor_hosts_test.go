package cmd

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bernd/vibepit/proxy"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var hostsT0 = time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)

func mkHost(domain, port string, src proxy.Source, last time.Time, allowed, blocked int, lastAction proxy.Action) proxy.HostStats {
	h := proxy.HostStats{
		HostKey:     proxy.HostKey{Source: src, Domain: domain, Port: port},
		Allowed:     allowed,
		Blocked:     blocked,
		FirstSeen:   last.Add(-time.Minute),
		FirstAction: lastAction,
		LastSeen:    last,
		LastAction:  lastAction,
	}
	if blocked > 0 {
		h.LastBlockReason = "domain not in allowlist"
		h.LastBlockCode = proxy.BlockNotAllowlisted
	}
	return h
}

func hostsFixture() []proxy.HostStats {
	return []proxy.HostStats{
		mkHost("b.com", "443", proxy.SourceProxy, hostsT0.Add(1*time.Second), 0, 5, proxy.ActionBlock),
		mkHost("a.com", "443", proxy.SourceProxy, hostsT0.Add(4*time.Second), 2, 0, proxy.ActionAllow),
		mkHost("c.com", "", proxy.SourceDNS, hostsT0.Add(2*time.Second), 0, 5, proxy.ActionBlock),
		mkHost("a.com", "", proxy.SourceDNS, hostsT0.Add(3*time.Second), 1, 0, proxy.ActionAllow),
	}
}

func viewDomains(v *hostsView) []string {
	var out []string
	for _, h := range v.hosts {
		out = append(out, allowValue(h.HostKey)+"/"+string(h.Source))
	}
	return out
}

func selectDomain(t *testing.T, v *hostsView, domain string, src proxy.Source) {
	t.Helper()
	for i, h := range v.hosts {
		if h.Domain == domain && h.Source == src {
			v.cursor.Pos, v.selecting = i, true
			return
		}
	}
	t.Fatalf("host %s/%s not found", domain, src)
}

func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func TestHostsViewSort(t *testing.T) {
	tests := []struct {
		sort hostSort
		want []string
	}{
		{sortNewest, []string{"a.com:443/proxy", "a.com/dns", "c.com/dns", "b.com:443/proxy"}},
		// b.com and c.com tie on count 5; c.com is newer.
		{sortCount, []string{"c.com/dns", "b.com:443/proxy", "a.com:443/proxy", "a.com/dns"}},
		{sortHost, []string{"a.com/dns", "a.com:443/proxy", "b.com:443/proxy", "c.com/dns"}},
	}

	for _, tt := range tests {
		t.Run(tt.sort.String(), func(t *testing.T) {
			v := &hostsView{sort: tt.sort}
			v.setHosts(hostsFixture())
			assert.Equal(t, tt.want, viewDomains(v))
		})
	}
}

// The proxy returns hosts in map order; ties must not reorder rows per poll.
func TestHostsViewSortTiesUseHostKey(t *testing.T) {
	tied := []proxy.HostStats{
		mkHost("c.com", "443", proxy.SourceProxy, hostsT0, 0, 1, proxy.ActionBlock),
		mkHost("a.com", "443", proxy.SourceProxy, hostsT0, 0, 1, proxy.ActionBlock),
		mkHost("b.com", "", proxy.SourceDNS, hostsT0, 0, 1, proxy.ActionBlock),
	}
	want := []string{"a.com:443/proxy", "b.com/dns", "c.com:443/proxy"}
	for _, sort := range []hostSort{sortNewest, sortCount} {
		t.Run(sort.String(), func(t *testing.T) {
			for _, order := range [][]int{{0, 1, 2}, {2, 1, 0}, {1, 2, 0}} {
				var hosts []proxy.HostStats
				for _, i := range order {
					hosts = append(hosts, tied[i])
				}
				v := &hostsView{sort: sort}
				v.setHosts(slices.Clone(hosts))
				assert.Equal(t, want, viewDomains(v), "input order %v", order)
			}
		})
	}
}

func TestHostsViewSelection(t *testing.T) {
	t.Run("cycleSort cycles and keeps selection", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(hostsFixture())
		selectDomain(t, v, "b.com", proxy.SourceProxy)

		for _, want := range []hostSort{sortHost, sortCount, sortNewest} {
			v.cycleSort()
			assert.Equal(t, want, v.sort)
			sel, ok := v.selected()
			require.True(t, ok)
			assert.Equal(t, "b.com", sel.Domain)
		}
	})

	t.Run("s key cycles sort", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(hostsFixture())
		assert.True(t, v.handleKey(keyPress("s")))
		assert.Equal(t, sortHost, v.sort)
	})

	t.Run("selection follows host across re-sort", func(t *testing.T) {
		v := &hostsView{}
		hosts := hostsFixture()
		v.setHosts(slices.Clone(hosts))
		selectDomain(t, v, "c.com", proxy.SourceDNS)
		require.Equal(t, 2, v.cursor.Pos)

		hosts[2].LastSeen = hostsT0.Add(time.Minute) // c.com becomes the newest
		v.setHosts(slices.Clone(hosts))

		sel, ok := v.selected()
		require.True(t, ok)
		assert.Equal(t, hosts[2].HostKey, sel.HostKey)
		assert.Equal(t, 0, v.cursor.Pos)
	})

	t.Run("selected host removed ends the selection", func(t *testing.T) {
		v := &hostsView{}
		hosts := hostsFixture()
		v.setHosts(slices.Clone(hosts))
		selectDomain(t, v, "b.com", proxy.SourceProxy)
		v.handleKey(keyPress("enter"))

		v.setHosts(slices.Clone(hosts[1:]))
		_, ok := v.selected()
		assert.False(t, ok, "the selection must not move to a neighbouring host")
		assert.Nil(t, v.expanded)

		v.setHosts(nil)
		assert.NotPanics(t, func() { v.handleKey(keyPress("j")) })
		assert.NotPanics(t, func() { v.handleKey(keyPress("enter")) })
	})

	t.Run("enter toggles expansion; expansion follows host", func(t *testing.T) {
		v := &hostsView{}
		hosts := hostsFixture()
		v.setHosts(slices.Clone(hosts))
		selectDomain(t, v, "a.com", proxy.SourceProxy)

		assert.True(t, v.handleKey(keyPress("enter")))
		require.NotNil(t, v.expanded)
		assert.Equal(t, hosts[1].HostKey, *v.expanded)

		hosts[1].LastSeen = hostsT0
		v.setHosts(slices.Clone(hosts))
		require.NotNil(t, v.expanded)
		assert.Equal(t, hosts[1].HostKey, *v.expanded)

		assert.True(t, v.handleKey(keyPress("space")))
		assert.Nil(t, v.expanded)
	})
}

func TestHostsViewRender(t *testing.T) {
	now := hostsT0.Add(time.Minute)

	t.Run("render header and statuses", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(hostsFixture()[:2])
		out := renderPlain(v, allowState{}, 120, 20, now)

		for _, want := range []string{"last seen ↓", "host", "source", "count", "status",
			"2 hosts", "● 1 allowed", "● 1 blocked", "blocked", "allowed", "b.com", "proxy"} {
			assert.Contains(t, out, want)
		}

		v.cycleSort()
		assert.Contains(t, renderPlain(v, allowState{}, 120, 20, now), "host ↑")
		v.cycleSort()
		assert.Contains(t, renderPlain(v, allowState{}, 120, 20, now), "↓ count")
	})

	t.Run("count label stays in place when sorted", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(hostsFixture())
		countAt := func() int {
			header := lineContaining(renderPlain(v, allowState{}, 120, 20, now), "last seen")
			return ansi.StringWidth(header[:strings.Index(header, "count")])
		}
		unsorted := countAt()
		v.sort = sortCount
		assert.Equal(t, unsorted, countAt())
	})

	t.Run("sort arrow is highlighted", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(hostsFixture())
		for _, tt := range []struct {
			sort  hostSort
			arrow string
		}{
			{sortNewest, "↓"},
			{sortHost, "↑"},
			{sortCount, "↓"},
		} {
			v.sort = tt.sort
			header := v.renderColumnHeader(120, v.columns(120))
			assert.Contains(t, header, sortArrowStyle.Render(tt.arrow), tt.sort.String())
		}
	})

	t.Run("render user allow statuses", func(t *testing.T) {
		v := &hostsView{}
		hosts := hostsFixture()
		hosts[0].Allowlisted = true
		hosts[2].Allowlisted = true
		v.setHosts(slices.Clone(hosts))
		allows := allowState{
			hosts[0].HostKey: statusTemp,
			hosts[2].HostKey: statusSaved,
		}
		out := renderPlain(v, allows, 120, 20, now)
		assert.Contains(t, out, "allowed*")
		assert.Contains(t, out, "saved")
		assert.Contains(t, out, "● 4 allowed")
	})

	t.Run("render details", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(hostsFixture())
		selectDomain(t, v, "c.com", proxy.SourceDNS)
		v.handleKey(keyPress("enter"))
		out := renderPlain(v, allowState{}, 120, 30, now)

		for _, want := range []string{"status:", "host:", "port:", "source:", "DNS query", "first seen:",
			"last seen:", "count:", "5 total", "0 allowed", "5 blocked", "reason:", "domain not in allowlist"} {
			assert.Contains(t, out, want)
		}
		portLine := lineContaining(out, "port:")
		assert.Contains(t, portLine, "-")

		selectDomain(t, v, "a.com", proxy.SourceProxy)
		v.handleKey(keyPress("enter")) // moves expansion from c.com to a.com:443
		assert.True(t, v.isExpandedHost(v.hosts[v.cursor.Pos].HostKey))
		out = renderPlain(v, allowState{}, 120, 30, now)
		assert.Contains(t, out, "HTTP proxy")
		assert.Contains(t, lineContaining(out, "port:"), "443")
		assert.NotContains(t, out, "reason:")
	})

	t.Run("saved note", func(t *testing.T) {
		v := &hostsView{}
		hosts := hostsFixture()
		hosts[0].Allowlisted = true
		v.setHosts(slices.Clone(hosts))
		selectDomain(t, v, "b.com", proxy.SourceProxy)
		v.handleKey(keyPress("enter"))

		out := renderPlain(v, allowState{hosts[0].HostKey: statusSaved}, 120, 30, now)
		assert.Contains(t, out, "(saved to .vibepit/network.yaml)")

		out = renderPlain(v, allowState{hosts[0].HostKey: statusTemp}, 120, 30, now)
		assert.Contains(t, out, "(this session only)")
	})

	t.Run("inline hint", func(t *testing.T) {
		v := &hostsView{}
		hosts := hostsFixture()
		v.setHosts(slices.Clone(hosts))
		selectDomain(t, v, "b.com", proxy.SourceProxy)
		line := lineContaining(renderPlain(v, allowState{}, 120, 20, now), "b.com")
		assert.Contains(t, line, "a allow")
		assert.Contains(t, line, "A allow+save")

		hosts[0].LastBlockCode = proxy.BlockCIDR
		v.setHosts(slices.Clone(hosts))
		line = lineContaining(renderPlain(v, allowState{}, 120, 20, now), "b.com")
		assert.NotContains(t, line, "a allow")
		assert.NotContains(t, line, "allow+save")

		selectDomain(t, v, "c.com", proxy.SourceDNS)
		line = lineContaining(renderPlain(v, allowState{}, 120, 20, now), "b.com")
		assert.NotContains(t, line, "allow")
	})

	t.Run("host takes priority over the inline hint", func(t *testing.T) {
		v := &hostsView{}
		long := "sdmntprnorthcentralus.oaiusercontent.com"
		v.setHosts([]proxy.HostStats{mkHost(long, "443", proxy.SourceProxy, hostsT0, 0, 8, proxy.ActionBlock)})

		line := lineContaining(renderPlain(v, allowState{}, 80, 20, now), "sdmntpr")
		assert.Contains(t, line, "sdmntprnorthcentralus.oaiuserco")
		assert.NotContains(t, line, "allow+save", "hint is dropped when it does not fit")
		assert.Contains(t, line, "blocked")
		assert.LessOrEqual(t, ansi.StringWidth(line), 80)
	})

	t.Run("count column", func(t *testing.T) {
		long := "sdmntprnorthcentralus.oaiusercontent.com"
		tests := []struct {
			name      string
			domain    string
			width     int
			wantCount bool
			wantHost  string
		}{
			{"shown when wide", long, 120, true, long},
			{"shown when narrow hosts fit", "a.com", 60, true, "a.com"},
			{"dropped so the host fits", long, 80, false, long},
			{"kept while sorting by count", long, 80, true, "sdmntprnorthcentralus"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				v := &hostsView{}
				if strings.HasPrefix(tt.name, "kept while sorting") {
					v.sort = sortCount
				}
				v.setHosts([]proxy.HostStats{mkHost(tt.domain, "443", proxy.SourceProxy, hostsT0, 0, 8, proxy.ActionBlock)})
				out := renderPlain(v, allowState{}, tt.width, 20, now)
				header := lineContaining(out, "last seen")
				line := lineContaining(out, tt.wantHost)
				assert.Contains(t, line, tt.wantHost)
				assert.Contains(t, line, "blocked")
				assert.LessOrEqual(t, ansi.StringWidth(line), tt.width)
				if tt.wantCount {
					assert.Contains(t, header, "count")
					assert.Contains(t, line, "8  blocked")
				} else {
					assert.NotContains(t, header, "count")
					assert.NotContains(t, line, " 8 ")
				}
			})
		}
	})

	t.Run("lines fit narrow width", func(t *testing.T) {
		v := &hostsView{}
		hosts := append(hostsFixture(),
			mkHost("a-very-long-subdomain.example.org", "443", proxy.SourceProxy, hostsT0, 0, 1, proxy.ActionBlock))
		v.setHosts(slices.Clone(hosts))
		selectDomain(t, v, "a-very-long-subdomain.example.org", proxy.SourceProxy)
		v.handleKey(keyPress("enter"))

		out := renderPlain(v, allowState{}, 40, 20, now)
		for _, line := range strings.Split(out, "\n") {
			assert.LessOrEqual(t, ansi.StringWidth(line), 40, "line too wide: %q", line)
		}
		assert.Equal(t, 19, strings.Count(out, "\n"))
	})

	t.Run("render pads to height", func(t *testing.T) {
		v := &hostsView{}
		out := renderPlain(v, allowState{}, 80, 10, now)
		assert.Equal(t, 9, strings.Count(out, "\n"))
		assert.Contains(t, out, "0 hosts")
	})

	t.Run("expanded details taller than viewport keep row visible", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(hostsFixture())
		v.cursor.Pos, v.selecting = 3, true
		v.handleKey(keyPress("enter"))

		out := renderPlain(v, allowState{}, 120, 6, now)
		assert.Contains(t, out, "b.com")
		assert.Equal(t, 5, strings.Count(out, "\n"))
	})

	t.Run("rows never exceed the width", func(t *testing.T) {
		// Host column truncated (no count) and host filling the space next
		// to the count: both used to leave rows one cell too wide. Below 43
		// the minimum host column does not fit and render truncates.
		for width := 43; width <= 60; width++ {
			v := &hostsView{}
			h := mkHost("abcdefghijklmnopqrst.com", "443", proxy.SourceProxy, hostsT0, 0, 1, proxy.ActionBlock)
			h.Allowlisted = true
			v.setHosts([]proxy.HostStats{h})
			out := ansi.Strip(renderHostRow(v.hosts[0], allowState{h.HostKey: statusTemp}, false, width, v.columns(width), now))
			assert.LessOrEqual(t, ansi.StringWidth(out), width, "width %d: %q", width, out)
			assert.True(t, strings.HasSuffix(out, "allowed*"), "width %d: %q", width, out)
		}
	})

	t.Run("host column measures display width", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts([]proxy.HostStats{mkHost("bücherbücher.de", "", proxy.SourceDNS, hostsT0, 1, 0, proxy.ActionAllow)})
		assert.Equal(t, ansi.StringWidth("bücherbücher.de")+2, v.columns(120).hostW)
	})

	t.Run("page keys step by the rows next to the details", func(t *testing.T) {
		var hosts []proxy.HostStats
		for i := range 40 {
			hosts = append(hosts, mkHost(fmt.Sprintf("h%02d.com", i), "443", proxy.SourceProxy,
				hostsT0.Add(time.Duration(-i)*time.Second), 1, 0, proxy.ActionAllow))
		}
		v := &hostsView{}
		v.setHosts(slices.Clone(hosts))
		v.setHeight(20)
		v.handleKey(keyPress("j"))
		v.handleKey(keyPress("enter"))
		out := renderPlain(v, allowState{}, 120, 20, now)
		visibleRows := strings.Count(out, ".com ")
		require.Less(t, visibleRows, 18, "details take up part of the list")

		v.handleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
		assert.Equal(t, visibleRows, v.cursor.Pos)
		out = renderPlain(v, allowState{}, 120, 20, now)
		assert.Contains(t, out, fmt.Sprintf("h%02d.com", v.cursor.Pos))
	})

	t.Run("scrolls to keep selection visible", func(t *testing.T) {
		var hosts []proxy.HostStats
		for i := range 30 {
			hosts = append(hosts, mkHost(strings.Repeat("x", i+1)+".com", "443", proxy.SourceProxy,
				hostsT0.Add(time.Duration(-i)*time.Second), 0, 1, proxy.ActionBlock))
		}
		v := &hostsView{}
		v.setHosts(slices.Clone(hosts))
		v.handleKey(keyPress("G"))

		out := renderPlain(v, allowState{}, 120, 10, now)
		assert.Contains(t, out, strings.Repeat("x", 30)+".com")
		assert.NotContains(t, out, " x.com")
	})
}

func TestHostsViewFooterKeys(t *testing.T) {
	hosts := hostsFixture()

	t.Run("blocked allowable", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(slices.Clone(hosts))
		selectDomain(t, v, "b.com", proxy.SourceProxy)
		descs := footerKeyDescs(v.footerKeys(allowState{}))
		assert.Contains(t, descs, "allow")
		assert.Contains(t, descs, "allow+save")
		assert.Contains(t, descs, "details")
		assert.Contains(t, descs, "sort: newest")
	})

	t.Run("expanded shows collapse", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(slices.Clone(hosts))
		v.handleKey(keyPress("j"))
		v.handleKey(keyPress("enter"))
		assert.Contains(t, footerKeyDescs(v.footerKeys(allowState{})), "collapse")
	})

	t.Run("temp allowed shows save", func(t *testing.T) {
		v := &hostsView{}
		allowed := slices.Clone(hosts)
		allowed[0].Allowlisted = true
		v.setHosts(allowed)
		selectDomain(t, v, "b.com", proxy.SourceProxy)
		descs := footerKeyDescs(v.footerKeys(allowState{hosts[0].HostKey: statusTemp}))
		assert.Contains(t, descs, "save")
		assert.NotContains(t, descs, "allow+save")
	})

	t.Run("CIDR block has no allow keys", func(t *testing.T) {
		cidr := slices.Clone(hosts)
		cidr[0].LastBlockCode = proxy.BlockCIDR
		v := &hostsView{}
		v.setHosts(cidr)
		selectDomain(t, v, "b.com", proxy.SourceProxy)
		descs := footerKeyDescs(v.footerKeys(allowState{}))
		assert.NotContains(t, descs, "allow")
		assert.NotContains(t, descs, "allow+save")
	})
}

func TestHostsViewSelectionExpired(t *testing.T) {
	t.Run("selection on the expanded row stays", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(hostsFixture())
		selectDomain(t, v, "b.com", proxy.SourceProxy)
		v.handleKey(keyPress("enter"))

		v.selectionExpired()
		_, ok := v.selected()
		assert.True(t, ok)
		assert.NotNil(t, v.expanded)
	})

	t.Run("selection moved off the expanded row ends", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(hostsFixture())
		selectDomain(t, v, "b.com", proxy.SourceProxy)
		v.handleKey(keyPress("enter"))
		selectDomain(t, v, "a.com", proxy.SourceProxy)

		v.selectionExpired()
		_, ok := v.selected()
		assert.False(t, ok)
		assert.Nil(t, v.expanded)
	})
}

func lineContaining(out, needle string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

func renderPlain(v *hostsView, allows allowState, width, height int, now time.Time) string {
	return ansi.Strip(v.render(allows, width, height, now))
}

func TestHostsViewSelectionLifecycle(t *testing.T) {
	older := func(n int) []proxy.HostStats {
		var hosts []proxy.HostStats
		for i := range n {
			hosts = append(hosts, mkHost(fmt.Sprintf("old%d.com", i), "443", proxy.SourceProxy,
				hostsT0.Add(time.Duration(-i)*time.Second), 0, 1, proxy.ActionBlock))
		}
		return hosts
	}
	withNewer := func(hosts []proxy.HostStats, n int) []proxy.HostStats {
		for i := range n {
			hosts = append(hosts, mkHost(fmt.Sprintf("new%d.com", i), "443", proxy.SourceProxy,
				hostsT0.Add(time.Duration(i+1)*time.Second), 0, 1, proxy.ActionBlock))
		}
		return hosts
	}

	t.Run("no selection by default", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(older(5))
		_, ok := v.selected()
		assert.False(t, ok)
		assert.NotContains(t, renderPlain(v, allowState{}, 120, 10, hostsT0), "➔")
		v.handleKey(keyPress("enter"))
		assert.Nil(t, v.expanded)
	})

	t.Run("new hosts appear on top without a selection", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(older(5))
		v.setHosts(withNewer(older(5), 10))
		out := renderPlain(v, allowState{}, 120, 7, hostsT0)
		assert.Contains(t, out, "new9.com")
		assert.NotContains(t, out, "➔")
	})

	tests := []struct {
		key     string
		wantPos int
	}{
		{"j", 0}, {"k", 0}, {"down", 0}, {"up", 0}, {"g", 0}, {"G", 4},
	}
	for _, tt := range tests {
		t.Run("first "+tt.key+" selects", func(t *testing.T) {
			v := &hostsView{}
			v.setHosts(older(5))
			msg := keyPress(tt.key)
			switch tt.key {
			case "down":
				msg = tea.KeyPressMsg{Code: tea.KeyDown}
			case "up":
				msg = tea.KeyPressMsg{Code: tea.KeyUp}
			}
			assert.True(t, v.handleKey(msg))
			sel, ok := v.selected()
			require.True(t, ok)
			assert.Equal(t, v.hosts[tt.wantPos].HostKey, sel.HostKey)
		})
	}

	t.Run("first page down selects the next page", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(older(30))
		v.setHeight(12)
		v.handleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
		assert.Equal(t, 10, v.cursor.Pos)
	})

	t.Run("selection stays on its host", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(older(5))
		v.handleKey(keyPress("j"))
		v.setHosts(withNewer(older(5), 10))

		sel, ok := v.selected()
		require.True(t, ok)
		assert.Equal(t, "old0.com", sel.Domain)
		assert.Contains(t, lineContaining(renderPlain(v, allowState{}, 120, 7, hostsT0), "➔"), "old0.com")
	})

	t.Run("expiry ends the selection and returns to the top", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(older(30))
		v.handleKey(keyPress("G"))
		renderPlain(v, allowState{}, 120, 10, hostsT0)
		require.Positive(t, v.cursor.Offset)

		v.selectionExpired()
		_, ok := v.selected()
		assert.False(t, ok)
		assert.Contains(t, renderPlain(v, allowState{}, 120, 10, hostsT0), "old0.com")
	})

	t.Run("expiry keeps an expanded row", func(t *testing.T) {
		v := &hostsView{}
		v.setHosts(older(5))
		v.handleKey(keyPress("j"))
		v.handleKey(keyPress("enter"))

		v.selectionExpired()
		_, ok := v.selected()
		assert.True(t, ok)

		v.handleKey(keyPress("enter"))
		v.selectionExpired()
		_, ok = v.selected()
		assert.False(t, ok)
	})
}

func TestFormatCount(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{12345, "12,345"},
		{1234567, "1,234,567"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, formatCount(tt.n))
		})
	}
}

func TestHostsViewRenderLargeCounts(t *testing.T) {
	v := &hostsView{}
	v.setHosts([]proxy.HostStats{
		mkHost("big.com", "443", proxy.SourceProxy, hostsT0, 1234000, 567, proxy.ActionAllow),
		mkHost("small.com", "443", proxy.SourceProxy, hostsT0.Add(-time.Second), 3, 0, proxy.ActionAllow),
	})
	v.handleKey(keyPress("j"))
	v.handleKey(keyPress("enter"))
	out := renderPlain(v, allowState{}, 120, 20, hostsT0)

	assert.Contains(t, lineContaining(out, "big.com"), " 1,234,567  allowed")
	assert.Contains(t, lineContaining(out, "small.com"), "        3  allowed", "column widens for the largest count")
	assert.Contains(t, out, "1,234,567 total")
	assert.Contains(t, out, "1,234,000 allowed")
	for _, line := range strings.Split(out, "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(line), 120)
	}
}

func TestHostLabel(t *testing.T) {
	tests := []struct {
		name string
		key  proxy.HostKey
		want string
	}{
		{"default https port omitted", proxy.HostKey{Source: proxy.SourceProxy, Domain: "a.com", Port: "443"}, "a.com"},
		{"other port shown", proxy.HostKey{Source: proxy.SourceProxy, Domain: "a.com", Port: "8080"}, "a.com:8080"},
		{"http port shown", proxy.HostKey{Source: proxy.SourceProxy, Domain: "a.com", Port: "80"}, "a.com:80"},
		{"dns has no port", proxy.HostKey{Source: proxy.SourceDNS, Domain: "a.com"}, "a.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hostLabel(tt.key))
		})
	}
}
