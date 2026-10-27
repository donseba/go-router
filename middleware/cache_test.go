package middleware

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingHandler answers with body and the headers, and counts its calls.
type countingHandler struct {
	calls   atomic.Int64
	status  int
	body    string
	headers []string
}

func (h *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := h.calls.Add(1)

	for i := 0; i+1 < len(h.headers); i += 2 {
		w.Header().Add(h.headers[i], h.headers[i+1])
	}

	w.Header().Set("X-Render", fmt.Sprint(n))

	if h.status != 0 {
		w.WriteHeader(h.status)
	}

	_, _ = io.WriteString(w, h.body)
}

func pageRequest(method string, target string, headers ...string) *http.Request {
	r := httptest.NewRequest(method, target, nil)

	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Add(headers[i], headers[i+1])
	}

	return r
}

// TestCacheServesStoredResponse checks that a second request is answered
// with the stored status, headers and body, without the handler, and that
// headers set outside the cache stay those of each request.
func TestCacheServesStoredResponse(t *testing.T) {
	handler := &countingHandler{
		body:    page,
		headers: []string{"Content-Type", "text/html; charset=utf-8", "Cache-Control", "no-cache"},
	}
	cache := NewCache(CacheOptions{StatusHeader: "X-Cache"})

	var requests atomic.Int64

	// An outer middleware sets a header of its own on every request.
	stack := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", fmt.Sprint(requests.Add(1)))
		cache.Middleware(handler).ServeHTTP(w, r)
	})

	first := serveRequest(stack, pageRequest(http.MethodGet, "/en/"))
	second := serveRequest(stack, pageRequest(http.MethodGet, "/en/"))

	if handler.calls.Load() != 1 {
		t.Fatalf("handler called %d times, want 1", handler.calls.Load())
	}

	if first.Body.String() != page || second.Body.String() != page || second.Code != http.StatusOK {
		t.Fatalf("second response %d, %d bytes", second.Code, second.Body.Len())
	}

	header := second.Header()
	if header.Get("X-Render") != "1" || header.Get("Content-Type") != "text/html; charset=utf-8" ||
		header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("stored headers %v", header)
	}

	if header.Get("X-Request-ID") != "2" || header.Get("Content-Length") != fmt.Sprint(len(page)) {
		t.Fatalf("request ID %q, length %q", header.Get("X-Request-ID"), header.Get("Content-Length"))
	}

	if first.Header().Get("X-Cache") != "miss" || header.Get("X-Cache") != "hit" {
		t.Fatalf("X-Cache %q, %q", first.Header().Get("X-Cache"), header.Get("X-Cache"))
	}

	other := serveRequest(stack, pageRequest(http.MethodGet, "/en/?page=2"))
	if other.Header().Get("X-Render") != "2" {
		t.Fatal("another query was served from the entry of the page")
	}

	stats := cache.Stats()
	if stats.Hits != 1 || stats.Misses != 2 || stats.Entries != 2 || stats.Bytes <= int64(2*len(page)) {
		t.Fatalf("stats %+v", stats)
	}
}

// TestCacheKeys checks that the default key tells hosts apart, keeps the
// path a mount stripped, and that Key and Skip keep requests out.
func TestCacheKeys(t *testing.T) {
	handler := &countingHandler{body: "page"}
	cache := NewCache(CacheOptions{
		Key: func(r *http.Request) (string, bool) {
			if r.URL.Query().Has("q") {
				return "", false
			}

			return CacheKey(r)
		},
		Skip: func(r *http.Request) bool {
			_, err := r.Cookie("session")

			return err == nil
		},
	})

	served := cache.Middleware(handler)
	mounted := http.StripPrefix("/admin", served)

	serveRequest(served, pageRequest(http.MethodGet, "http://a.example/x"))
	serveRequest(served, pageRequest(http.MethodGet, "http://b.example/x"))
	serveRequest(mounted, pageRequest(http.MethodGet, "http://a.example/admin/x"))

	if handler.calls.Load() != 3 {
		t.Fatalf("handler called %d times, want 3: hosts and mounts share entries", handler.calls.Load())
	}

	for _, r := range []*http.Request{
		pageRequest(http.MethodGet, "http://a.example/x", "Cookie", "session=1"),
		pageRequest(http.MethodGet, "http://a.example/x", "Authorization", "Bearer x"),
		pageRequest(http.MethodGet, "http://a.example/x", "Range", "bytes=0-1"),
		pageRequest(http.MethodGet, "http://a.example/search?q=go"),
		pageRequest(http.MethodGet, "http://a.example/search?q=go"),
	} {
		serveRequest(served, r)
	}

	if calls, stats := handler.calls.Load(), cache.Stats(); calls != 8 || stats.Skips != 5 || stats.Entries != 3 {
		t.Fatalf("calls %d, stats %+v", calls, stats)
	}

	serveRequest(served, pageRequest(http.MethodPost, "http://a.example/x"))

	if calls, stats := handler.calls.Load(), cache.Stats(); calls != 9 || stats.Skips != 5 {
		t.Fatalf("a post: calls %d, stats %+v", calls, stats)
	}
}

// TestCacheVary checks that responses are kept apart by the request headers
// they vary on, Accept-Encoding by whether gzip is accepted, with Compress
// inside the cache storing the compressed body.
func TestCacheVary(t *testing.T) {
	handler := &countingHandler{body: page, headers: []string{"Content-Type", "text/html", "Vary", "HX-Request"}}
	cache := NewCache()
	served := cache.Middleware(Compress()(handler))

	render := func(headers ...string) *httptest.ResponseRecorder {
		return serveRequest(served, pageRequest(http.MethodGet, "/en/", headers...))
	}

	gzipped := render("Accept-Encoding", "gzip, deflate, br")
	render("Accept-Encoding", "br;q=1.0, gzip;q=0.8")
	plain := render()
	render("Accept-Encoding", "identity")
	partial := render("Accept-Encoding", "gzip", "HX-Request", "true")
	render("Accept-Encoding", "gzip", "HX-Request", "true")

	if calls := handler.calls.Load(); calls != 3 {
		t.Fatalf("handler called %d times, want 3: gzip, plain and htmx", calls)
	}

	cached := render("Accept-Encoding", "gzip")
	if cached.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(cached.Body.Bytes(), gzipped.Body.Bytes()) {
		t.Fatalf("gzip entry: %v", cached.Header())
	}

	reader, err := gzip.NewReader(cached.Body)
	if err != nil {
		t.Fatal(err)
	}

	if body, _ := io.ReadAll(reader); string(body) != page {
		t.Fatal("the stored gzip body is not the page")
	}

	if plain.Header().Get("Content-Encoding") != "" || plain.Body.String() != page {
		t.Fatal("a client without gzip got the compressed entry")
	}

	if partial.Header().Get("X-Render") != "3" {
		t.Fatal("an htmx request was served the full page")
	}

	varyCookie := &countingHandler{body: "page", headers: []string{"Vary", "Cookie"}}
	byTheme := NewCache(CacheOptions{
		VaryValue: func(r *http.Request, name string) (string, bool) {
			if name != "Cookie" {
				return "", false
			}

			theme, err := r.Cookie("theme")
			if err != nil {
				return "", true
			}

			return theme.Value, true
		},
	}).Middleware(varyCookie)

	serveRequest(byTheme, pageRequest(http.MethodGet, "/", "Cookie", "theme=dark; consent=1"))
	serveRequest(byTheme, pageRequest(http.MethodGet, "/", "Cookie", "theme=dark; consent=2"))
	serveRequest(byTheme, pageRequest(http.MethodGet, "/", "Cookie", "theme=light"))

	if calls := varyCookie.calls.Load(); calls != 2 {
		t.Fatalf("VaryValue: handler called %d times, want 2", calls)
	}
}

// TestCacheRefuses checks that responses that set a cookie, are private,
// no-store, vary on everything, have another status or are flushed or too
// large are sent but not stored.
func TestCacheRefuses(t *testing.T) {
	tests := map[string]http.Handler{
		"cookie":    &countingHandler{body: "page", headers: []string{"Set-Cookie", "a=b"}},
		"private":   &countingHandler{body: "page", headers: []string{"Cache-Control", "max-age=60, private"}},
		"no-store":  &countingHandler{body: "page", headers: []string{"Cache-Control", "no-store"}},
		"vary all":  &countingHandler{body: "page", headers: []string{"Vary", "*"}},
		"not found": &countingHandler{status: http.StatusNotFound, body: "missing"},
		"error":     &countingHandler{status: http.StatusInternalServerError, body: "error"},
		"redirect":  &countingHandler{status: http.StatusFound, body: ""},
		"too large": &countingHandler{body: strings.Repeat("x", 2048)},
		"flushed": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "start")
			_ = http.NewResponseController(w).Flush()
			_, _ = io.WriteString(w, " end")
		}),
	}

	for name, handler := range tests {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64

			counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				handler.ServeHTTP(w, r)
			})

			cache := NewCache(CacheOptions{MaxEntryBytes: 1024})
			served := cache.Middleware(counted)

			first := serveRequest(served, pageRequest(http.MethodGet, "/"))
			serveRequest(served, pageRequest(http.MethodGet, "/"))

			if calls.Load() != 2 || cache.Stats().Entries != 0 {
				t.Fatalf("calls %d, stats %+v", calls.Load(), cache.Stats())
			}

			if name == "too large" && first.Body.Len() != 2048 {
				t.Fatalf("too large: sent %d bytes", first.Body.Len())
			}

			if name == "flushed" && first.Body.String() != "start end" {
				t.Fatalf("flushed: sent %q", first.Body.String())
			}
		})
	}

	notFound := &countingHandler{status: http.StatusNotFound, body: "missing"}
	served := NewCache(CacheOptions{NotFound: true}).Middleware(notFound)

	serveRequest(served, pageRequest(http.MethodGet, "/gone"))

	response := serveRequest(served, pageRequest(http.MethodGet, "/gone"))
	if notFound.calls.Load() != 1 || response.Code != http.StatusNotFound || response.Body.String() != "missing" {
		t.Fatalf("NotFound: calls %d, status %d", notFound.calls.Load(), response.Code)
	}
}

// TestCacheVersionAndTTL checks that an entry is rendered again once its
// version changed or its TTL passed, and replaced.
func TestCacheVersionAndTTL(t *testing.T) {
	handler := &countingHandler{body: "page"}

	var version atomic.Uint64

	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	cache := NewCache(CacheOptions{
		Version: func(*http.Request) uint64 { return version.Load() },
	})
	cache.now = func() time.Time { return now }

	served := cache.Middleware(handler)
	get := func() string {
		return serveRequest(served, pageRequest(http.MethodGet, "/")).Header().Get("X-Render")
	}

	// The first answer is rendered, the second comes from the cache.
	first, second := get(), get()
	if first != "1" || second != "1" {
		t.Fatal("not stored")
	}

	version.Add(1)

	first, second = get(), get()
	if first != "2" || second != "2" {
		t.Fatal("a new version was not rendered again")
	}

	now = now.Add(DefaultCacheTTL - time.Second)

	if get() != "2" {
		t.Fatal("expired before the TTL")
	}

	now = now.Add(time.Second)

	first, second = get(), get()
	if first != "3" || second != "3" {
		t.Fatal("not rendered again after the TTL")
	}

	if stats := cache.Stats(); stats.Entries != 1 || stats.Misses != 3 || stats.Hits != 4 {
		t.Fatalf("stats %+v", stats)
	}

	forever := NewCache(CacheOptions{TTL: -1})
	forever.now = func() time.Time { return now }
	kept := forever.Middleware(handler)

	serveRequest(kept, pageRequest(http.MethodGet, "/"))
	now = now.Add(24 * time.Hour)

	if serveRequest(kept, pageRequest(http.MethodGet, "/")).Header().Get("X-Render") != "4" {
		t.Fatal("a negative TTL expired")
	}
}

// TestCacheHeadAndNotModified checks that HEAD is answered from a stored
// GET without a body, and that If-None-Match with the stored ETag is
// answered 304 without the handler, also for the request that renders.
func TestCacheHeadAndNotModified(t *testing.T) {
	handler := &countingHandler{body: page, headers: []string{"Content-Type", "text/html"}}
	cache := NewCache()
	served := cache.Middleware(ETag()(handler))
	etag := WeakETag([]byte(page))

	// A HEAD request finds nothing to answer from, and stores nothing.
	head := serveRequest(served, pageRequest(http.MethodHead, "/"))
	if handler.calls.Load() != 1 || cache.Stats().Entries != 0 || head.Code != http.StatusOK {
		t.Fatalf("HEAD miss: calls %d, stats %+v", handler.calls.Load(), cache.Stats())
	}

	// The request that renders the entry is answered 304 itself.
	first := serveRequest(served, pageRequest(http.MethodGet, "/", "If-None-Match", etag))
	if first.Code != http.StatusNotModified || first.Body.Len() != 0 || first.Header().Get("ETag") != etag {
		t.Fatalf("rendering request: %d, %v", first.Code, first.Header())
	}

	if first.Header().Get("Content-Type") != "" || cache.Stats().Entries != 1 {
		t.Fatalf("rendering request: %v, stats %+v", first.Header(), cache.Stats())
	}

	full := serveRequest(served, pageRequest(http.MethodGet, "/"))
	if full.Code != http.StatusOK || full.Body.String() != page || full.Header().Get("ETag") != etag {
		t.Fatalf("stored entry: %d, %v", full.Code, full.Header())
	}

	notModified := serveRequest(served, pageRequest(http.MethodGet, "/", "If-None-Match", `"other", `+etag))
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 ||
		notModified.Header().Get("Content-Length") != "" {
		t.Fatalf("304 from the entry: %d, %v", notModified.Code, notModified.Header())
	}

	changed := serveRequest(served, pageRequest(http.MethodGet, "/", "If-None-Match", `W/"old"`))
	if changed.Code != http.StatusOK || changed.Body.String() != page {
		t.Fatalf("changed page: %d", changed.Code)
	}

	head = serveRequest(served, pageRequest(http.MethodHead, "/"))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != fmt.Sprint(len(page)) {
		t.Fatalf("HEAD hit: %d, %v", head.Code, head.Header())
	}

	if handler.calls.Load() != 2 {
		t.Fatalf("handler called %d times, want 2", handler.calls.Load())
	}
}

// TestCacheLRU checks that the least recently used entries are evicted to
// stay within MaxBytes and MaxEntries.
func TestCacheLRU(t *testing.T) {
	handler := &countingHandler{body: strings.Repeat("x", 1000)}
	cache := NewCache(CacheOptions{MaxBytes: 4000})
	served := cache.Middleware(handler)

	get := func(path string) string {
		return serveRequest(served, pageRequest(http.MethodGet, path)).Header().Get("X-Render")
	}

	get("/a")
	get("/b")
	get("/c")

	// /a is used again, so /b is the least recently used.
	if get("/a") != "1" {
		t.Fatal("/a was not stored")
	}

	get("/d")

	stats := cache.Stats()
	if stats.Bytes > 4000 || stats.Entries != 3 || stats.Evictions != 1 {
		t.Fatalf("stats %+v", stats)
	}

	if get("/a") != "1" || get("/b") != "5" {
		t.Fatal("the wrong entry was evicted")
	}

	counted := NewCache(CacheOptions{MaxEntries: 2})
	limited := counted.Middleware(handler)

	for _, path := range []string{"/1", "/2", "/3"} {
		serveRequest(limited, pageRequest(http.MethodGet, path))
	}

	if stats := counted.Stats(); stats.Entries != 2 || stats.Evictions != 1 {
		t.Fatalf("MaxEntries: stats %+v", stats)
	}
}

// TestCacheRendersOnce checks that concurrent requests for a missing entry
// render it once and are all answered with it.
func TestCacheRendersOnce(t *testing.T) {
	var calls atomic.Int64

	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		<-release
		_, _ = io.WriteString(w, page)
	})

	cache := NewCache()
	served := cache.Middleware(handler)

	const requests = 32

	var (
		wg     sync.WaitGroup
		bodies = make([]string, requests)
	)

	for i := range requests {
		wg.Add(1)

		go func() {
			defer wg.Done()

			bodies[i] = serveRequest(served, pageRequest(http.MethodGet, "/")).Body.String()
		}()
	}

	// Let the requests arrive while the first one renders.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("rendered %d times, want once", calls.Load())
	}

	for i, body := range bodies {
		if body != page {
			t.Fatalf("request %d got %d bytes", i, len(body))
		}
	}

	if stats := cache.Stats(); stats.Hits != requests-1 || stats.Misses != 1 {
		t.Fatalf("stats %+v", stats)
	}
}

// TestCacheConcurrent serves many keys, versions and variants at once, for
// the race detector, and checks every response.
func TestCacheConcurrent(t *testing.T) {
	var version atomic.Uint64

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Add("Vary", "HX-Request")
		_, _ = io.WriteString(w, strings.Repeat(r.URL.Path+r.Header.Get("HX-Request")+"\n", 100))
	})

	cache := NewCache(CacheOptions{
		Version:  func(*http.Request) uint64 { return version.Load() / 50 },
		MaxBytes: 20_000,
	})
	served := cache.Middleware(Compress()(ETag()(handler)))

	var wg sync.WaitGroup

	for worker := range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := range 200 {
				version.Add(1)

				path := fmt.Sprintf("/%d", (worker+i)%7)
				htmx := fmt.Sprint(i%2 == 0)

				r := pageRequest(http.MethodGet, path, "HX-Request", htmx)
				if i%3 == 0 {
					r.Header.Set("Accept-Encoding", "gzip")
				}

				response := serveRequest(served, r)

				body := response.Body.Bytes()
				if response.Header().Get("Content-Encoding") == "gzip" {
					reader, err := gzip.NewReader(bytes.NewReader(body))
					if err != nil {
						t.Error(err)

						return
					}

					body, _ = io.ReadAll(reader)
				}

				if want := strings.Repeat(path+htmx+"\n", 100); string(body) != want {
					t.Errorf("%s %s: wrong body", path, htmx)

					return
				}
			}
		}()
	}

	wg.Wait()

	if stats := cache.Stats(); stats.Bytes > 20_000 || stats.Hits == 0 {
		t.Fatalf("stats %+v", stats)
	}
}

// TestCachePanic checks that a render that panics stores nothing and lets
// the next request render.
func TestCachePanic(t *testing.T) {
	var calls atomic.Int64

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			panic("render failed")
		}

		_, _ = io.WriteString(w, "page")
	})

	cache := NewCache()
	served := RecoverWithOptions(RecoverOptions{Logger: discardLogger()})(cache.Middleware(handler))

	serveRequest(served, pageRequest(http.MethodGet, "/"))

	if cache.Stats().Entries != 0 {
		t.Fatal("a panicked render was stored")
	}

	if body := serveRequest(served, pageRequest(http.MethodGet, "/")).Body.String(); body != "page" || calls.Load() != 2 {
		t.Fatalf("after a panic: %q, %d calls", body, calls.Load())
	}
}

// navigationHandler answers as a site with HTMX does: a whole page varies
// on HX-Request, a navigation on HX-Current-URL as well, as its body tells
// the page it comes from. A request with X-Preview gets a preview, which
// varies on Accept-Language too. Every response varies on X-Preview.
func navigationHandler(calls *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)

		header := w.Header()
		header.Set("Content-Type", "text/html")
		header.Add("Vary", "HX-Request, X-Preview")

		switch {
		case r.Header.Get("X-Preview") != "":
			header.Add("Vary", "Accept-Language")
			_, _ = io.WriteString(w, "preview "+r.URL.Path+" in "+r.Header.Get("Accept-Language"))
		case r.Header.Get("HX-Request") != "":
			header.Add("Vary", "HX-Current-URL")
			_, _ = io.WriteString(w, "navigation "+r.URL.Path+" from "+r.Header.Get("HX-Current-URL"))
		default:
			_, _ = io.WriteString(w, "page "+r.URL.Path)
		}
	})
}

// navigationRequest is a request of navigationHandler: a whole page when
// from is empty, otherwise an HTMX navigation from that address.
func navigationRequest(path string, from string) *http.Request {
	if from == "" {
		return pageRequest(http.MethodGet, path)
	}

	return pageRequest(http.MethodGet, path, "HX-Request", "true", "HX-Current-URL", from)
}

// navigationBody is the body navigationHandler answers navigationRequest
// with.
func navigationBody(path string, from string) string {
	if from == "" {
		return "page " + path
	}

	return "navigation " + path + " from " + from
}

// checkCache checks that the entries of the keys and sets and the LRU list
// are the same, that the bytes are their sizes, and that every key has its
// sets, at most MaxVarySets, with their union.
func checkCache(t *testing.T, c *ResponseCache) {
	t.Helper()

	c.mu.Lock()
	defer c.mu.Unlock()

	var (
		entries int
		bytes   int64
	)

	for key, stored := range c.keys {
		if len(stored.sets) == 0 || len(stored.sets) > c.options.MaxVarySets {
			t.Errorf("%q: %d sets", key, len(stored.sets))
		}

		if !slices.Equal(stored.union, unionNames(stored.sets)) {
			t.Errorf("%q: union %v", key, stored.union)
		}

		for _, set := range stored.sets {
			if len(set.variants) == 0 {
				t.Errorf("%q: a set %v without entries", key, set.vary)
			}

			for variant, entry := range set.variants {
				if entry.key != key || entry.set != set || entry.variant != variant {
					t.Errorf("%q: entry %q is filed wrongly", key, variant)
				}

				entries++
				bytes += entry.size
			}
		}
	}

	if entries != c.lru.Len() || bytes != c.bytes {
		t.Errorf("%d entries of %d bytes in the sets, %d of %d bytes counted", entries, bytes, c.lru.Len(), c.bytes)
	}

	for element := c.lru.Front(); element != nil; element = element.Next() {
		entry := element.Value.(*cacheEntry)

		stored := c.keys[entry.key]
		if stored == nil || !slices.Contains(stored.sets, entry.set) || entry.set.variants[entry.variant] != entry {
			t.Errorf("%q: entry %q in the LRU list is in no set", entry.key, entry.variant)
		}
	}
}

// TestCacheVarySets checks that a whole page and HTMX navigations of the
// same address, which vary on other request headers, are kept side by side
// and each served from the cache.
func TestCacheVarySets(t *testing.T) {
	var calls atomic.Int64

	cache := NewCache(CacheOptions{StatusHeader: "X-Cache"})
	served := cache.Middleware(navigationHandler(&calls))

	steps := []struct {
		from, status string
	}{
		{"", "miss"},
		{"/a", "miss"},
		{"", "hit"},
		{"/a", "hit"},
		{"/b", "miss"},
		{"", "hit"},
		{"/a", "hit"},
		{"/b", "hit"},
	}

	for i, step := range steps {
		response := serveRequest(served, navigationRequest("/work", step.from))

		if status := response.Header().Get("X-Cache"); status != step.status {
			t.Fatalf("step %d from %q: %s, want %s", i, step.from, status, step.status)
		}

		if body := response.Body.String(); body != navigationBody("/work", step.from) {
			t.Fatalf("step %d from %q: body %q", i, step.from, body)
		}
	}

	if calls.Load() != 3 {
		t.Fatalf("rendered %d times, want 3", calls.Load())
	}

	if stats := cache.Stats(); stats.Entries != 3 || stats.Evictions != 0 || stats.Hits != 5 {
		t.Fatalf("stats %+v", stats)
	}

	key, _ := CacheKey(navigationRequest("/work", ""))
	if sets := len(cache.keys[key].sets); sets != 2 {
		t.Fatalf("%d sets, want 2", sets)
	}

	checkCache(t, cache)
}

// TestCacheVarySetsBound checks that a key keeps entries for at most
// MaxVarySets Vary lists, evicting those of the list stored least
// recently, and that an entry evicted otherwise takes its set along once
// the set is empty.
func TestCacheVarySetsBound(t *testing.T) {
	var calls atomic.Int64

	cache := NewCache(CacheOptions{MaxVarySets: 2, StatusHeader: "X-Cache"})
	served := cache.Middleware(navigationHandler(&calls))

	page := navigationRequest("/work", "")
	navigation := navigationRequest("/work", "/a")
	preview := pageRequest(http.MethodGet, "/work", "X-Preview", "1", "Accept-Language", "nl")

	steps := []struct {
		name    string
		request *http.Request
		status  string
	}{
		{"page", page, "miss"},
		{"navigation", navigation, "miss"},
		{"page", page, "hit"},
		// A third list evicts the one stored least recently: the page's.
		{"preview", preview, "miss"},
		{"navigation", navigation, "hit"},
		{"page", page, "miss"},
		// Storing the page again evicted the navigation's list.
		{"preview", preview, "hit"},
		{"navigation", navigation, "miss"},
	}

	for i, step := range steps {
		response := serveRequest(served, step.request)
		if status := response.Header().Get("X-Cache"); status != step.status {
			t.Fatalf("step %d, %s: %s, want %s", i, step.name, status, step.status)
		}

		checkCache(t, cache)
	}

	if stats := cache.Stats(); stats.Entries != 2 || stats.Evictions != 3 {
		t.Fatalf("stats %+v", stats)
	}

	limited := NewCache(CacheOptions{MaxEntries: 1})
	served = limited.Middleware(navigationHandler(&calls))

	serveRequest(served, page)
	serveRequest(served, navigation)

	key, _ := CacheKey(page)
	if stored := limited.keys[key]; len(stored.sets) != 1 ||
		!slices.Equal(stored.union, []string{"Hx-Current-Url", "Hx-Request", "X-Preview"}) {
		t.Fatalf("after evicting the page: %d sets, union %v", len(stored.sets), stored.union)
	}

	checkCache(t, limited)
}

// TestCacheRendersOncePerVariant checks that concurrent whole pages and
// HTMX navigations of a missing address render once each: the requests of
// the variant that did not render first wait for a render of their own
// variant, not each render it.
func TestCacheRendersOncePerVariant(t *testing.T) {
	var calls atomic.Int64

	release := make(chan struct{})
	navigation := navigationHandler(&calls)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		navigation.ServeHTTP(w, r)
	})

	cache := NewCache()
	served := cache.Middleware(handler)

	const requests = 32

	var (
		wg     sync.WaitGroup
		failed atomic.Int64
	)

	for i := range requests {
		wg.Add(1)

		go func() {
			defer wg.Done()

			from := ""
			if i%2 == 1 {
				from = "/a"
			}

			body := serveRequest(served, navigationRequest("/work", from)).Body.String()
			if body != navigationBody("/work", from) {
				failed.Add(1)
			}
		}()
	}

	// Let the requests arrive while the first one renders.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if failed.Load() > 0 {
		t.Fatalf("%d requests got the wrong body", failed.Load())
	}

	if calls.Load() != 2 {
		t.Fatalf("rendered %d times, want twice", calls.Load())
	}

	if stats := cache.Stats(); stats.Hits != requests-2 || stats.Misses != 2 {
		t.Fatalf("stats %+v", stats)
	}

	checkCache(t, cache)
}

// TestCacheConcurrentVarySets serves whole pages, navigations and previews
// of a few addresses at once, with versions moving on and too little
// memory and too few sets for all of them, for the race detector, and
// checks every response and then the cache's bookkeeping.
func TestCacheConcurrentVarySets(t *testing.T) {
	var (
		version atomic.Uint64
		calls   atomic.Int64
	)

	cache := NewCache(CacheOptions{
		Version:     func(*http.Request) uint64 { return version.Load() / 40 },
		MaxBytes:    8_000,
		MaxVarySets: 2,
	})
	served := cache.Middleware(Compress()(ETag()(navigationHandler(&calls))))

	var wg sync.WaitGroup

	for worker := range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := range 300 {
				version.Add(1)

				path := fmt.Sprintf("/%d", (worker+i)%5)

				var (
					r    *http.Request
					want string
				)

				switch i % 4 {
				case 0:
					r, want = navigationRequest(path, ""), navigationBody(path, "")
				case 1:
					r, want = navigationRequest(path, "/a"), navigationBody(path, "/a")
				case 2:
					r, want = navigationRequest(path, "/b"), navigationBody(path, "/b")
				default:
					r = pageRequest(http.MethodGet, path, "X-Preview", "1", "Accept-Language", "nl")
					want = "preview " + path + " in nl"
				}

				if worker%2 == 0 {
					r.Header.Set("Accept-Encoding", "gzip")
				}

				response := serveRequest(served, r)

				body := response.Body.Bytes()
				if response.Header().Get("Content-Encoding") == "gzip" {
					reader, err := gzip.NewReader(bytes.NewReader(body))
					if err != nil {
						t.Error(err)

						return
					}

					body, _ = io.ReadAll(reader)
				}

				if string(body) != want {
					t.Errorf("%s: %q, want %q", path, body, want)

					return
				}
			}
		}()
	}

	wg.Wait()

	if stats := cache.Stats(); stats.Bytes > 8_000 || stats.Hits == 0 {
		t.Fatalf("stats %+v", stats)
	}

	checkCache(t, cache)
}
