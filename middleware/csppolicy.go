package middleware

import (
	"slices"
	"strings"
)

// CSPPolicy is one Content-Security-Policy: its directives in the order
// they were added, each with its sources, such as "script-src" with
// "'self'" and "'sha256-…'". The zero value is an empty policy, ready to
// use. Directive names are kept in lower case; sources are kept as given,
// without duplicates. A CSPPolicy is not safe for concurrent changes;
// Clone it to change a shared one.
//
//	policy := middleware.ParseCSP(middleware.StrictCSP)
//	policy.Add("script-src", "'sha256-…'")
//	policy.Set("frame-ancestors", "'none'")
//	header.Set("Content-Security-Policy", policy.String())
type CSPPolicy struct {
	directives []cspDirective
}

// cspDirective is one directive of a policy.
type cspDirective struct {
	name    string
	sources []string
}

// cspFallback lists, for the fetch directives that fall back to others
// when a policy lacks them, those others in the order browsers try them
// (CSP Level 3, "Get the effective directive"). Add starts a missing
// directive from the first of these the policy has.
var cspFallback = map[string][]string{
	"script-src":      {"default-src"},
	"script-src-elem": {"script-src", "default-src"},
	"script-src-attr": {"script-src", "default-src"},
	"style-src":       {"default-src"},
	"style-src-elem":  {"style-src", "default-src"},
	"style-src-attr":  {"style-src", "default-src"},
	"worker-src":      {"child-src", "script-src", "default-src"},
	"child-src":       {"default-src"},
	"frame-src":       {"child-src", "default-src"},
	"connect-src":     {"default-src"},
	"font-src":        {"default-src"},
	"img-src":         {"default-src"},
	"manifest-src":    {"default-src"},
	"media-src":       {"default-src"},
	"object-src":      {"default-src"},
}

// ParseCSP parses one serialized policy, such as the value of a
// Content-Security-Policy header: directives separated by ";", each a
// name followed by its sources, separated by white space. Names are case
// insensitive. As browsers do, it skips empty directives and names with
// other characters than letters, digits and "-", and keeps the first of
// directives with the same name. A header value that holds several
// policies separated by "," is a list; see SplitCSP.
func ParseCSP(policy string) CSPPolicy {
	var parsed CSPPolicy

	for _, part := range strings.Split(policy, ";") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}

		name := strings.ToLower(fields[0])
		if !validDirectiveName(name) || parsed.index(name) >= 0 {
			continue
		}

		parsed.directives = append(parsed.directives, cspDirective{
			name:    name,
			sources: appendSources(nil, fields[1:]),
		})
	}

	return parsed
}

// SplitCSP splits the values of a Content-Security-Policy header into the
// policies they hold: a header may be given several times, and one value
// may list several policies separated by ",". A browser enforces every
// one of them. Empty policies are left out.
func SplitCSP(values []string) []string {
	var policies []string

	for _, value := range values {
		for _, policy := range strings.Split(value, ",") {
			policy = strings.TrimSpace(policy)
			if policy != "" {
				policies = append(policies, policy)
			}
		}
	}

	return policies
}

// String returns the policy as a header value: the directives in order,
// separated by "; ", each name followed by its sources.
func (p CSPPolicy) String() string {
	var out strings.Builder

	for i, directive := range p.directives {
		if i > 0 {
			out.WriteString("; ")
		}

		out.WriteString(directive.name)

		for _, source := range directive.sources {
			out.WriteString(" ")
			out.WriteString(source)
		}
	}

	return out.String()
}

// IsZero reports whether the policy has no directives.
func (p CSPPolicy) IsZero() bool {
	return len(p.directives) == 0
}

// Sources returns a copy of the sources of the named directive, and
// whether the policy has it. It does not follow fallbacks: a policy
// with default-src but without script-src has no script-src.
func (p CSPPolicy) Sources(name string) ([]string, bool) {
	i := p.index(strings.ToLower(name))
	if i < 0 {
		return nil, false
	}

	return append([]string{}, p.directives[i].sources...), true
}

// Clone returns a copy of the policy that shares nothing with it.
func (p CSPPolicy) Clone() CSPPolicy {
	clone := CSPPolicy{directives: make([]cspDirective, len(p.directives))}

	for i, directive := range p.directives {
		clone.directives[i] = cspDirective{
			name:    directive.name,
			sources: append([]string(nil), directive.sources...),
		}
	}

	return clone
}

// Set sets the named directive to exactly sources, without duplicates,
// replacing its sources if the policy has it, in its place, and adding it
// at the end otherwise. A directive without sources, such as
// "upgrade-insecure-requests", is set with none. An invalid name is
// ignored.
func (p *CSPPolicy) Set(name string, sources ...string) {
	name = strings.ToLower(name)
	if !validDirectiveName(name) {
		return
	}

	values := appendSources(nil, sources)

	if i := p.index(name); i >= 0 {
		p.directives[i].sources = values

		return
	}

	p.directives = append(p.directives, cspDirective{name: name, sources: values})
}

// Add adds sources to the named directive, leaving out those it has, and
// so only ever allows more:
//
//   - A policy without the directive gets it at the end, starting from the
//     sources of the directive browsers fall back to, so adding a hash to
//     script-src of "default-src 'self'" gives "script-src 'self'
//     'sha256-…'", not a script-src that allows the hash alone. Without a
//     fallback in the policy the directive starts empty.
//   - 'none' is dropped when other sources join it, and adding 'none' to a
//     directive with other sources changes nothing.
//
// Keep in mind that browsers ignore 'unsafe-inline' in a directive that
// has a hash or a nonce, and that script-src-elem, when present, decides
// for <script> elements rather than script-src. An invalid name is
// ignored.
func (p *CSPPolicy) Add(name string, sources ...string) {
	name = strings.ToLower(name)
	if !validDirectiveName(name) {
		return
	}

	i := p.index(name)
	if i < 0 {
		var start []string

		for _, fallback := range cspFallback[name] {
			if j := p.index(fallback); j >= 0 {
				start = append([]string(nil), p.directives[j].sources...)

				break
			}
		}

		p.directives = append(p.directives, cspDirective{name: name, sources: start})
		i = len(p.directives) - 1
	}

	directive := &p.directives[i]
	added := appendSources(nil, sources)

	if hasOnlyNone(added) {
		if len(directive.sources) == 0 {
			directive.sources = added
		}

		return
	}

	added = withoutNone(added)
	if len(added) == 0 {
		return
	}

	if hasOnlyNone(directive.sources) {
		directive.sources = nil
	}

	directive.sources = appendSources(directive.sources, added)
}

// Delete removes the named directive.
func (p *CSPPolicy) Delete(name string) {
	i := p.index(strings.ToLower(name))
	if i < 0 {
		return
	}

	p.directives = append(p.directives[:i], p.directives[i+1:]...)
}

// Override sets every directive of other in p, as Set does: a directive
// of both gets the sources of other, in its place in p, and directives
// only other has are added at the end, in their order.
func (p *CSPPolicy) Override(other CSPPolicy) {
	for _, directive := range other.directives {
		p.Set(directive.name, directive.sources...)
	}
}

// index is the position of the directive with the lower-case name, or -1.
func (p CSPPolicy) index(name string) int {
	for i, directive := range p.directives {
		if directive.name == name {
			return i
		}
	}

	return -1
}

// validDirectiveName reports whether name is a directive name: one or
// more ASCII letters, digits and "-".
func validDirectiveName(name string) bool {
	if name == "" {
		return false
	}

	for _, c := range name {
		isLetter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		isDigit := c >= '0' && c <= '9'

		if !isLetter && !isDigit && c != '-' {
			return false
		}
	}

	return true
}

// appendSources appends the sources of add that are not empty, contain no
// white space, ";" or ",", and are not in sources yet.
func appendSources(sources, add []string) []string {
	for _, source := range add {
		if source == "" || strings.ContainsAny(source, " \t\n\f\r;,") {
			continue
		}

		if containsSource(sources, source) {
			continue
		}

		sources = append(sources, source)
	}

	return sources
}

// containsSource reports whether sources has source. Keywords in quotes,
// such as 'self', compare without regard to case; hashes, nonces and
// other sources compare exactly.
func containsSource(sources []string, source string) bool {
	for _, existing := range sources {
		if existing == source {
			return true
		}

		if isKeyword(existing) && isKeyword(source) && strings.EqualFold(existing, source) {
			return true
		}
	}

	return false
}

// isKeyword reports whether source is a quoted keyword such as 'self',
// rather than a quoted hash or nonce, whose base64 is case sensitive.
func isKeyword(source string) bool {
	if len(source) < 2 || source[0] != '\'' || source[len(source)-1] != '\'' {
		return false
	}

	lower := strings.ToLower(source)

	for _, prefix := range []string{"'sha256-", "'sha384-", "'sha512-", "'nonce-"} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}

	return true
}

// hasOnlyNone reports whether sources is 'none' alone.
func hasOnlyNone(sources []string) bool {
	return len(sources) == 1 && strings.EqualFold(sources[0], "'none'")
}

// withoutNone returns sources without 'none', in the same array.
func withoutNone(sources []string) []string {
	return slices.DeleteFunc(sources, func(source string) bool {
		return strings.EqualFold(source, "'none'")
	})
}
