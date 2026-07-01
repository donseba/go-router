package middleware

import "net/http"

type SecurityHeadersOptions struct {
	ContentTypeOptions    string
	FrameOptions          string
	ReferrerPolicy        string
	ContentSecurityPolicy string
}

func SecurityHeaders(options ...SecurityHeadersOptions) func(http.Handler) http.Handler {
	opts := SecurityHeadersOptions{
		ContentTypeOptions: "nosniff",
		FrameOptions:       "DENY",
		ReferrerPolicy:     "no-referrer",
	}
	if len(options) > 0 {
		opts = options[0]
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if opts.ContentTypeOptions != "" {
				w.Header().Set("X-Content-Type-Options", opts.ContentTypeOptions)
			}
			if opts.FrameOptions != "" {
				w.Header().Set("X-Frame-Options", opts.FrameOptions)
			}
			if opts.ReferrerPolicy != "" {
				w.Header().Set("Referrer-Policy", opts.ReferrerPolicy)
			}
			if opts.ContentSecurityPolicy != "" {
				w.Header().Set("Content-Security-Policy", opts.ContentSecurityPolicy)
			}

			next.ServeHTTP(w, r)
		})
	}
}
