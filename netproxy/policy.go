package netproxy

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

type requestRule struct {
	deny  bool
	paths []string // nil omits the constraint. An empty expansion matches nothing.
	when  string
}

type pathRequest struct {
	Path string `expr:"path"`
}

type conditionEnv struct {
	Request pathRequest `expr:"request"`
}

type compiledRule struct {
	deny  bool
	paths []*regexp.Regexp
	when  *vm.Program
}

type requestPolicy []compiledRule

func (r compiledRule) isRequestRule() bool { return r.paths != nil || r.when != nil }

// Alias policy uses the strongest deny on the authority. Request rules only
// constrain a blind tunnel, where the proxy cannot check the inner authority.
func (p requestPolicy) aliasDeny() aliasDeny {
	result := noAliasDeny
	for _, r := range p {
		if !r.deny {
			continue
		}
		if !r.isRequestRule() {
			return denyHostAlias
		}
		result = denyTunnelAlias
	}
	return result
}

func (p requestPolicy) admitsHost() bool {
	allowed := false
	for _, r := range p {
		if r.deny && !r.isRequestRule() {
			return false
		}
		if !r.deny && (r.paths == nil || len(r.paths) > 0) {
			allowed = true
		}
	}
	return allowed
}

func (p requestPolicy) requiresInspection() bool {
	for _, r := range p {
		if r.isRequestRule() {
			return true
		}
	}
	return false
}

func compileRules(rules []requestRule) (requestPolicy, error) {
	var policy requestPolicy
	for _, rule := range rules {
		cr := compiledRule{deny: rule.deny}
		if rule.paths != nil {
			cr.paths = make([]*regexp.Regexp, 0, len(rule.paths))
		}
		for _, pattern := range rule.paths {
			re, err := compilePath(pattern, rule.deny)
			if err != nil {
				return nil, err
			}
			cr.paths = append(cr.paths, re)
		}
		if rule.when != "" {
			prog, err := expr.Compile(rule.when, expr.Env(conditionEnv{}), expr.AsBool(), expr.WarnOnAny(), expr.DisableAllBuiltins())
			if err != nil {
				return nil, fmt.Errorf("when: %w", err)
			}
			cr.when = prog
		}
		policy = append(policy, cr)
	}
	return policy, nil
}

func compilePath(pattern string, deny bool) (*regexp.Regexp, error) {
	if !strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "//") || strings.ContainsAny(pattern, "{}\x00") {
		return nil, fmt.Errorf("invalid expanded path pattern %q", pattern)
	}
	var re strings.Builder
	if deny {
		re.WriteString("(?i)")
	}
	re.WriteByte('^')
	for _, segment := range strings.Split(strings.Trim(pattern, "/"), "/") {
		switch {
		case segment == "*":
			re.WriteString("/[^/]+")
		case segment == "**":
			re.WriteString("(/[^/]+)*")
		case strings.Contains(segment, "**") || segment == "." || segment == "..":
			return nil, fmt.Errorf("invalid segment in path pattern %q", pattern)
		case segment != "":
			re.WriteByte('/')
			re.WriteString(strings.Join(quotePathFragments(segment), "[^/]*"))
		}
	}
	re.WriteString("/?$")
	return regexp.Compile(re.String())
}

func quotePathFragments(segment string) []string {
	parts := strings.Split(segment, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return parts
}

var protectSeparators = strings.NewReplacer("%2f", "%252f", "%2F", "%252F", "%5c", "%255c", "%5C", "%255C")

// Routers differ on whether an encoded separator splits a segment. Construct
// both policy inputs without changing the URL that will be forwarded.
func pathInterpretations(u *url.URL) ([]string, bool) {
	raw := u.EscapedPath()
	if raw == "" {
		raw = "/"
	}
	protected, err := url.PathUnescape(protectSeparators.Replace(raw))
	if err != nil {
		return nil, false
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return nil, false
	}
	paths := []string{protected}
	if split := strings.ReplaceAll(decoded, "\\", "/"); split != protected {
		paths = append(paths, split)
	}
	for i, path := range paths {
		if !strings.HasPrefix(path, "/") || strings.Contains(path, "//") || strings.ContainsRune(path, 0) {
			return nil, false
		}
		for part := range strings.SplitSeq(path, "/") {
			if part == "." || part == ".." {
				return nil, false
			}
		}
		if path != "/" {
			paths[i] = strings.TrimSuffix(path, "/")
		}
	}
	return paths, true
}

func (r compiledRule) matches(path string) bool {
	matched := r.paths == nil
	for _, re := range r.paths {
		if re.MatchString(path) {
			matched = true
			break
		}
	}
	if !matched || r.when == nil {
		return matched
	}
	value, err := expr.Run(r.when, conditionEnv{Request: pathRequest{Path: path}})
	if err != nil {
		return r.deny
	}
	return value == true
}

func (p requestPolicy) allows(u *url.URL) bool {
	paths, ok := pathInterpretations(u)
	if !ok {
		return false
	}
	allowed := false
	for _, rule := range p {
		allMatch := true
		for _, path := range paths {
			if rule.matches(path) {
				if rule.deny {
					return false
				}
			} else {
				allMatch = false
			}
		}
		if !rule.deny && allMatch {
			allowed = true
		}
	}
	return allowed
}
