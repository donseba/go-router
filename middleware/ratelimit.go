package middleware

import (
	"net/http"
	"sync"
	"time"
)

// RateLimitOptions configures RateLimit.
type RateLimitOptions struct {
	// Limit is how many requests a client may make per Window. Zero or less
	// turns the limit off.
	Limit int

	// Window is the sliding window requests are counted in. Zero or less
	// turns the limit off.
	Window time.Duration

	// Key returns who a request is counted for. Default ClientIP.
	Key func(*http.Request) string

	// Skip, when set, leaves the requests it reports true for uncounted and
	// unlimited, such as safe methods (SafeMethod) when only posts are
	// limited.
	Skip func(*http.Request) bool

	// OnLimit answers requests over the limit. Default 429 Too Many
	// Requests.
	OnLimit http.Handler
}

// RateLimit returns middleware that allows each client (Key, by default
// ClientIP) at most Limit requests per Window, and answers the others with
// OnLimit:
//
//	r.Post("/comments", post).Use(middleware.RateLimit(middleware.RateLimitOptions{
//		Limit:  5,
//		Window: time.Minute,
//	}))
//
// Requests are counted in memory, per middleware and per process: an
// application on several servers allows Limit requests per server, and a
// restart forgets them. Behind proxies, use RealIPWithOptions so the client
// IP is the client's, not the proxy's.
func RateLimit(options RateLimitOptions) func(http.Handler) http.Handler {
	limiter := NewLimiter(options.Limit, options.Window)

	key := options.Key
	if key == nil {
		key = ClientIP
	}

	onLimit := options.OnLimit
	if onLimit == nil {
		onLimit = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		})
	}

	return func(next http.Handler) http.Handler {
		if limiter == nil {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if options.Skip != nil && options.Skip(r) {
				next.ServeHTTP(w, r)

				return
			}

			if !limiter.Allow(key(r), time.Now()) {
				onLimit.ServeHTTP(w, r)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// SafeMethod reports whether the request's method is GET, HEAD, OPTIONS or
// TRACE, which do not change anything, for RateLimitOptions.Skip.
func SafeMethod(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

// Limiter allows each key at most a number of events per sliding window,
// counted in memory. It is safe for concurrent use. A nil *Limiter allows
// everything.
type Limiter struct {
	limit  int
	window time.Duration

	mu     sync.Mutex
	counts map[string][]time.Time
	swept  time.Time
}

// NewLimiter returns a limiter of limit events per window for each key, for
// limits RateLimit does not fit, such as counting only some requests of a
// handler. It returns nil, which allows everything, when limit or window is
// zero or less.
func NewLimiter(limit int, window time.Duration) *Limiter {
	if limit <= 0 || window <= 0 {
		return nil
	}

	return &Limiter{limit: limit, window: window, counts: map[string][]time.Time{}}
}

// Allow counts an event of key at now and reports whether it is within the
// limit. Events over the limit are not counted.
func (l *Limiter) Allow(key string, now time.Time) bool {
	if l == nil {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Forget keys whose events are all older than the window, so the map
	// does not grow with every client ever seen.
	if now.Sub(l.swept) > l.window {
		for seen, times := range l.counts {
			if len(times) == 0 || now.Sub(times[len(times)-1]) >= l.window {
				delete(l.counts, seen)
			}
		}

		l.swept = now
	}

	recent := l.counts[key][:0]
	for _, at := range l.counts[key] {
		if now.Sub(at) < l.window {
			recent = append(recent, at)
		}
	}

	allowed := len(recent) < l.limit
	if allowed {
		recent = append(recent, now)
	}

	l.counts[key] = recent

	return allowed
}
