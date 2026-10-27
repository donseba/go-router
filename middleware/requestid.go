package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

const RequestIDHeader = "X-Request-ID"

// maxRequestIDLength bounds the incoming IDs RequestID accepts.
const maxRequestIDLength = 64

type requestIDContextKey struct{}

// RequestIDOptions configures RequestIDWithOptions.
type RequestIDOptions struct {
	// Header carries the ID in requests and responses. Default X-Request-ID.
	Header string

	// Generate returns a new ID for requests without a valid one. Default 32
	// random hex characters.
	Generate func() string

	// WithContext, when set, is called with the request context and the ID,
	// and its result becomes the request context. Use it to hand the ID to
	// other packages as well, for example a metrics or tracing library.
	WithContext func(ctx context.Context, id string) context.Context
}

// RequestID gives every request an ID: the one sent in X-Request-ID when it is
// valid (at most 64 letters, digits and -_.:), otherwise a new random one. The
// ID is stored in the request context (see GetRequestID) and set on the
// response's X-Request-ID header.
func RequestID(next http.Handler) http.Handler {
	return RequestIDWithOptions(RequestIDOptions{})(next)
}

// RequestIDWithOptions is RequestID with a configurable header, generator and
// context hook.
func RequestIDWithOptions(options RequestIDOptions) func(http.Handler) http.Handler {
	header := options.Header
	if header == "" {
		header = RequestIDHeader
	}

	// Canonical once, so reading and setting it costs no allocation per
	// request: X-Request-ID is X-Request-Id to net/http.
	header = http.CanonicalHeaderKey(header)

	generate := options.Generate
	if generate == nil {
		generate = newRequestID
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := r.Header.Get(header)
			if !ValidRequestID(requestID) {
				requestID = generate()
			}

			w.Header().Set(header, requestID)

			ctx := WithRequestID(r.Context(), requestID)
			if options.WithContext != nil {
				ctx = options.WithContext(ctx, requestID)
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetRequestID returns the request ID stored in ctx, or "".
func GetRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	if requestID, ok := ctx.Value(requestIDContextKey{}).(string); ok {
		return requestID
	}

	return ""
}

// WithRequestID returns ctx carrying requestID for GetRequestID.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

// ValidRequestID reports whether id is an acceptable incoming request ID: at
// most 64 letters, digits and -_.: characters, so that whatever a client sends
// cannot break a log line or grow it much.
func ValidRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLength {
		return false
	}

	for i := 0; i < len(id); i++ {
		switch c := id[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}

	return true
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}

	return hex.EncodeToString(b[:])
}
