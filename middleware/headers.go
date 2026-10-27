package middleware

import (
	"net/http"
)

// DefaultImmutable is a Cache-Control for versioned URLs, whose content
// changes with their version: kept for a year without asking again.
const DefaultImmutable = "public, max-age=31536000, immutable"

// NoStore keeps responses from being stored by browsers and caches, with
// Cache-Control: no-store, for pages with personal data, forms with a
// visitor's input and administration pages. A handler can still set a
// Cache-Control of its own.
func NoStore(next http.Handler) http.Handler {
	return CacheControl("no-store")(next)
}

// CacheControl returns middleware that sets the Cache-Control header of
// responses to value, before the handler runs, so the handler can still
// change it:
//
//	r.Get("/feed.xml", feed).Use(middleware.CacheControl("public, max-age=300"))
func CacheControl(value string) func(http.Handler) http.Handler {
	return Headers(http.Header{"Cache-Control": {value}})
}

// Headers returns middleware that sets the given response headers before the
// handler runs, replacing values of the same name, so the handler can still
// change them:
//
//	r.Use(middleware.Headers(http.Header{
//		"X-Robots-Tag":  {"noindex, nofollow"},
//		"Cache-Control": {"no-store"},
//	}))
func Headers(headers http.Header) func(http.Handler) http.Handler {
	set := newHeaderSet(headers)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			set.apply(w.Header())

			next.ServeHTTP(w, r)
		})
	}
}

// headerSet is a fixed set of response headers, set on every response with
// a single allocation.
type headerSet struct {
	// names are the canonical header names, and counts how many of values
	// belong to each, in order.
	names  []string
	counts []int
	values []string
}

// newHeaderSet prepares headers for apply.
func newHeaderSet(headers http.Header) headerSet {
	var set headerSet

	for name, values := range headers {
		set.names = append(set.names, http.CanonicalHeaderKey(name))
		set.counts = append(set.counts, len(values))
		set.values = append(set.values, values...)
	}

	return set
}

// apply sets the headers on header, replacing values of the same name. The
// values of all headers share one new array, so a response can change its
// own values but never those of another response; each slice ends at its
// own values, so an append to one does not overwrite the next.
func (set headerSet) apply(header http.Header) {
	if len(set.names) == 0 {
		return
	}

	values := make([]string, len(set.values))
	copy(values, set.values)

	start := 0

	for i, name := range set.names {
		end := start + set.counts[i]
		header[name] = values[start:end:end]
		start = end
	}
}
