package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicyDecide(t *testing.T) {
	httpAL, err := NewHTTPAllowlist([]string{"*.example.com:443"})
	require.NoError(t, err)
	dnsAL, err := NewDNSAllowlist([]string{"**.example.com"})
	require.NoError(t, err)
	rules, err := NewDenyRules([]string{"bad.example.com:443"}, []string{"bad.example.com"})
	require.NoError(t, err)
	p := NewPolicy(httpAL, dnsAL, rules)
	proxyT := func(host, port string) Target { return Target{Source: SourceProxy, Host: host, Port: port} }
	dnsT := func(host string) Target { return Target{Source: SourceDNS, Host: host} }
	for _, tc := range []struct {
		name   string
		target Target
		want   Decision
	}{
		{"allowed", proxyT("good.example.com", "443"), Allowed},
		{"deny wins over allow", proxyT("bad.example.com", "443"), Denied},
		{"other port", proxyT("good.example.com", "80"), NotAllowlisted},
		{"dns allowed", dnsT("a.b.example.com"), Allowed},
		{"dns deny wins", dnsT("bad.example.com"), Denied},
		{"sources are separate", dnsT("other.org"), NotAllowlisted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, p.Decide(tc.target))
			assert.Equal(t, tc.want == Denied, p.Denied(tc.target))
		})
	}

	// Missing parts are empty: nothing allowed, nothing denied.
	empty := NewPolicy(nil, nil, nil)
	assert.Equal(t, NotAllowlisted, empty.Decide(proxyT("good.example.com", "443")))
	assert.Equal(t, NotAllowlisted, empty.Decide(dnsT("a.example.com")))
	assert.False(t, empty.HasHTTPDenies())
	assert.Error(t, empty.AddAllowHTTP([]string{"a.com:443"}))
	assert.Nil(t, empty.Rules())

	alias, denied := p.DeniedAlias(SourceProxy, []string{"cdn.net", "bad.example.com"}, "443")
	assert.True(t, denied)
	assert.Equal(t, "bad.example.com", alias)
	_, denied = p.DeniedAlias(SourceProxy, []string{"bad.example.com"}, "80")
	assert.False(t, denied)
}
