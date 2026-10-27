package middleware

import "net/http"

// SecurityHeadersOptions configures SecurityHeaders. Headers left empty are
// not set.
type SecurityHeadersOptions struct {
	ContentTypeOptions string // X-Content-Type-Options, such as "nosniff"
	FrameOptions       string // X-Frame-Options, such as "DENY"
	ReferrerPolicy     string // Referrer-Policy, such as "no-referrer"

	// ContentSecurityPolicy is merged into the Content-Security-Policy of
	// the response directive by directive, as CSP does: "frame-ancestors
	// 'none'" replaces the frame-ancestors of a policy set outside and
	// keeps its other directives, and the handler can still change single
	// directives and add sources (AddCSPSource).
	ContentSecurityPolicy string

	// XRobotsTag is the X-Robots-Tag header, such as "noindex, nofollow"
	// for pages search engines should leave alone, such as an admin.
	XRobotsTag string
}

// SecurityHeaders returns middleware that sets security headers on every
// response. Without options it sets X-Content-Type-Options: nosniff,
// X-Frame-Options: DENY and Referrer-Policy: no-referrer; options replace
// those defaults entirely. The headers replace values of the same name set
// before it runs, and the handler can still change them, except
// Content-Security-Policy, which is merged (see CSP).
func SecurityHeaders(options ...SecurityHeadersOptions) func(http.Handler) http.Handler {
	opts := SecurityHeadersOptions{
		ContentTypeOptions: "nosniff",
		FrameOptions:       "DENY",
		ReferrerPolicy:     "no-referrer",
	}
	if len(options) > 0 {
		opts = options[0]
	}

	headers := http.Header{}

	for name, value := range map[string]string{
		"X-Content-Type-Options": opts.ContentTypeOptions,
		"X-Frame-Options":        opts.FrameOptions,
		"Referrer-Policy":        opts.ReferrerPolicy,
		"X-Robots-Tag":           opts.XRobotsTag,
	} {
		if value != "" {
			headers[name] = []string{value}
		}
	}

	set := newHeaderSet(headers)

	var policy func(http.Handler) http.Handler
	if opts.ContentSecurityPolicy != "" {
		policy = CSP(opts.ContentSecurityPolicy)
	}

	return func(next http.Handler) http.Handler {
		if policy != nil {
			next = policy(next)
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			set.apply(w.Header())

			next.ServeHTTP(w, r)
		})
	}
}
