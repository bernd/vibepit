package netproxy

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthorizationUsesUpstreamPath(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		rules      []requestRule
		want       bool
	}{
		{"encoded separator can leave allow", "/packages/a%2Fb", []requestRule{{paths: []string{"/packages/*"}}}, false},
		{"encoded separator stays in prefix", "/packages/a%2Fb", []requestRule{{paths: []string{"/packages/**"}}}, true},
		{"deny sees decoded separator", "/A%2fb", []requestRule{{}, {deny: true, paths: []string{"/a/b"}}}, false},
		{"when sees both paths", "/packages/a%2Fb", []requestRule{{when: `request.path == "/packages/a/b"`}}, false},
		{"deny-only when sees both paths", "/packages/a%5Cb", []requestRule{{}, {deny: true, when: `request.path == "/packages/a/b"`}}, false},
		{"invalid path even with host allow", "/out/%2e%2e/in", []requestRule{{}, {deny: true, paths: []string{"/private"}}}, false},
		{"deny trailing slash", "/private/", []requestRule{{}, {deny: true, paths: []string{"/private"}}}, false},
		{"single segment excludes root", "/", []requestRule{{paths: []string{"/*"}}}, false},
		{"root recursive", "/", []requestRule{{paths: []string{"/**"}}}, true},
		{"recursive includes parent", "/packages", []requestRule{{paths: []string{"/packages/**"}}}, true},
		{"empty expansion matches nothing", "/public", []requestRule{{paths: []string{}}}, false},
		{"allow case", "/PUBLIC", []requestRule{{paths: []string{"/public"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := compileRules(tc.rules)
			require.NoError(t, err)
			u, err := url.ParseRequestURI(tc.path)
			require.NoError(t, err)
			require.Equal(t, tc.want, policy.allows(u))
		})
	}
}

func TestInvalidPolicyFailsToLoad(t *testing.T) {
	for _, rule := range []requestRule{
		{when: "request.path"}, {when: "request.missing == 1"},
		{paths: []string{"relative"}}, {paths: []string{"/a**b"}},
	} {
		_, err := compileRules([]requestRule{rule})
		require.Error(t, err)
	}
}
