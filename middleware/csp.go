package middleware

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"slices"
)

// StrictCSP is a Content-Security-Policy that allows only what the site
// itself serves: no inline scripts or styles, no eval, no plugins, and
// forms, <base> and framing limited to the site. Pages allow the inline
// scripts they need by hash, with AddCSPSource. Images may also be data:
// URLs.
const StrictCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'"

// cspHeader is the name of the Content-Security-Policy header.
const cspHeader = "Content-Security-Policy"

// CSP returns middleware that merges policy into the Content-Security-Policy
// of every response, directive by directive, and lets handlers add sources
// to it with AddCSPSource until the response is written:
//
//	r.Use(middleware.CSP(middleware.StrictCSP))
//
//	func page(w http.ResponseWriter, r *http.Request) {
//		middleware.AddCSPSource(w, "script-src", importMap.ScriptHash())
//		...
//	}
//
// The policy of a response is made in this order; a later step wins over
// an earlier one for the directives it names, and keeps the others:
//
//  1. The policy the response has when the middleware runs, set by an
//     outer middleware, such as another CSP or SecurityHeaders.
//  2. policy: its directives replace those of the same name, such as
//     "frame-ancestors 'none'" replacing only frame-ancestors.
//  3. What the handler, or a middleware inside this one, leaves in the
//     header when the response is written: its directives replace those
//     of the same name. Setting "frame-ancestors *" changes only
//     frame-ancestors; it does not drop the rest. A handler that deletes
//     the header sends no policy.
//  4. The sources of AddCSPSource, added to the directives as they then
//     stand (see CSPPolicy.Add).
//
// So the innermost middleware and the handler have the last word: put a
// site-wide policy outside, and the policy of a group of routes, such as
// "frame-ancestors 'none'" for an admin, inside. The middleware sets the
// merged policy of steps 1 and 2 in the header before the handler runs,
// so the handler can read it, and merges steps 3 and 4 when the status is
// written, or after the handler returns without writing anything.
//
// Only the first policy of the header is merged; further policies, which
// browsers enforce as well (see SplitCSP), stay as they are. An empty
// policy only lets handlers add sources to a policy set elsewhere.
//
// Put CSP inside an output cache (NewCache): the cache stores the headers
// set inside it, so the policy, with the hashes of the page, is stored and
// served with the page.
func CSP(policy string) func(http.Handler) http.Handler {
	parsed := ParseCSP(policy)
	serialized := parsed.String()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := w.Header()

			// Without a policy yet, the merged policy is policy itself.
			var base []string
			if len(header[cspHeader]) == 0 && serialized != "" {
				base = []string{serialized}
			} else {
				base = mergePolicy(header[cspHeader], parsed)
			}

			if len(base) > 0 {
				header[cspHeader] = base
			}

			writer := &cspWriter{
				ResponseWriter: w,
				base:           slices.Clone(base),
			}

			next.ServeHTTP(writer, r)

			writer.apply()
		})
	}
}

// AddCSPSource adds sources, such as the hash of an inline script
// ("'sha256-…'"), to a directive of the response's Content-Security-Policy,
// as CSPPolicy.Add does: a missing directive starts from the one it falls
// back to, so the policy only ever allows more. Call it before the
// response is written.
//
// It changes the policy in the header at once, and under CSP adds the
// sources again when the response is written, so they also survive a
// handler that sets the header itself afterwards. A response without a
// policy stays without one: a directive with only these sources would
// forbid everything else.
func AddCSPSource(w http.ResponseWriter, directive string, sources ...string) {
	if writer := findCSPWriter(w); writer != nil && !writer.applied {
		writer.additions = append(writer.additions, cspAddition{
			directive: directive,
			sources:   slices.Clone(sources),
		})
	}

	header := w.Header()

	policies := SplitCSP(header[cspHeader])
	if len(policies) == 0 {
		return
	}

	policy := ParseCSP(policies[0])
	policy.Add(directive, sources...)

	header[cspHeader] = append([]string{policy.String()}, policies[1:]...)
}

// mergePolicy returns the header values with policy merged into the first
// policy of values, as CSP does before the handler runs; nil when there is
// no policy at all.
func mergePolicy(values []string, policy CSPPolicy) []string {
	policies := SplitCSP(values)

	var first CSPPolicy
	if len(policies) > 0 {
		first = ParseCSP(policies[0])
		policies = policies[1:]
	}

	first.Override(policy)

	if first.IsZero() {
		return policies
	}

	return append([]string{first.String()}, policies...)
}

// findCSPWriter returns the innermost cspWriter w is or wraps, through
// Unwrap, or nil.
func findCSPWriter(w http.ResponseWriter) *cspWriter {
	for w != nil {
		if writer, ok := w.(*cspWriter); ok {
			return writer
		}

		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}

		w = unwrapper.Unwrap()
	}

	return nil
}

// cspAddition is one call of AddCSPSource.
type cspAddition struct {
	directive string
	sources   []string
}

// cspWriter merges the policy the handler leaves in the header, and the
// sources of AddCSPSource, into the policy of CSP when the response is
// written.
type cspWriter struct {
	http.ResponseWriter
	// base are the header values CSP set before the handler ran.
	base      []string
	additions []cspAddition
	// applied is set once the policy is final.
	applied bool
}

// apply makes the policy final, once; see CSP.
func (c *cspWriter) apply() {
	if c.applied {
		return
	}

	c.applied = true
	header := c.Header()
	values := header[cspHeader]
	unchanged := slices.Equal(values, c.base)

	if unchanged && len(c.additions) == 0 {
		return
	}

	policies := SplitCSP(values)
	if len(policies) == 0 {
		return
	}

	var policy CSPPolicy

	if unchanged {
		policy = ParseCSP(policies[0])
	} else {
		if base := SplitCSP(c.base); len(base) > 0 {
			policy = ParseCSP(base[0])
		}

		policy.Override(ParseCSP(policies[0]))
	}

	for _, addition := range c.additions {
		policy.Add(addition.directive, addition.sources...)
	}

	header[cspHeader] = append([]string{policy.String()}, policies[1:]...)
}

func (c *cspWriter) WriteHeader(status int) {
	if !informational(status) {
		c.apply()
	}

	c.ResponseWriter.WriteHeader(status)
}

func (c *cspWriter) Write(data []byte) (int, error) {
	c.apply()

	return c.ResponseWriter.Write(data)
}

// WriteString is Write for a string, such as a rendered page, without
// first copying it into a byte slice.
func (c *cspWriter) WriteString(s string) (int, error) {
	c.apply()

	return io.WriteString(c.ResponseWriter, s)
}

func (c *cspWriter) Flush() {
	_ = c.FlushError()
}

// FlushError sends the headers, with the final policy, and what was
// written so far, and allows http.ResponseController to flush through
// this writer.
func (c *cspWriter) FlushError() error {
	c.apply()

	return http.NewResponseController(c.ResponseWriter).Flush()
}

// Hijack hands the connection over; no policy is sent.
func (c *cspWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buf, err := http.NewResponseController(c.ResponseWriter).Hijack()
	if err == nil {
		c.applied = true
	}

	return conn, buf, err
}

// Unwrap lets http.ResponseController and router.NoIntercept reach the
// original writer.
func (c *cspWriter) Unwrap() http.ResponseWriter {
	return c.ResponseWriter
}
