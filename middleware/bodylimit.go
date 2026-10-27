package middleware

import (
	"net/http"
)

// BodyLimitOptions configures BodyLimit.
type BodyLimitOptions struct {
	// OnTooLarge, when set, answers requests whose Content-Length is over
	// the limit before the handler runs, for example with a page of the
	// application's own. Without it, such requests reach the handler, whose
	// reads of the body fail, as for bodies without a length.
	OnTooLarge http.Handler
}

// BodyLimit returns middleware that limits request bodies to n bytes, as
// http.MaxBytesHandler does: reading more fails with an *http.MaxBytesError,
// so form parsing and decoding fail, and the server closes the connection
// after the response.
//
//	r.Use(middleware.BodyLimit(1 << 20)) // 1 MiB
//	r.Post("/upload", upload).Use(middleware.BodyLimit(64<<20, middleware.BodyLimitOptions{
//		OnTooLarge: http.HandlerFunc(tooLarge),
//	}))
//
// A handler tells a body over the limit by the error:
//
//	var tooLarge *http.MaxBytesError
//	if errors.As(err, &tooLarge) { ... }
func BodyLimit(n int64, options ...BodyLimitOptions) func(http.Handler) http.Handler {
	opts := firstOption(options)

	return func(next http.Handler) http.Handler {
		limited := http.MaxBytesHandler(next, n)

		if opts.OnTooLarge == nil {
			return limited
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > n {
				opts.OnTooLarge.ServeHTTP(w, r)

				return
			}

			limited.ServeHTTP(w, r)
		})
	}
}
