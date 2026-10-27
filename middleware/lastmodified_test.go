package middleware

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// modifiedAt is the Last-Modified of the dated handlers of these tests.
var modifiedAt = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// httpDate formats a time as an HTTP date.
func httpDate(t time.Time) string {
	return t.UTC().Format(http.TimeFormat)
}

// datedHandler answers with body and a Last-Modified of modified, and
// records the conditional headers of the requests it gets.
type datedHandler struct {
	body     string
	modified time.Time
	status   int
	calls    int
	// seen are the If-None-Match and If-Modified-Since headers of the
	// requests, the latest last.
	seen []string
}

func (h *datedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.calls++
	h.seen = append(h.seen, r.Header.Get("If-None-Match")+"|"+r.Header.Get("If-Modified-Since"))

	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")

	if !h.modified.IsZero() {
		w.Header().Set("Last-Modified", httpDate(h.modified))
	}

	if h.status != 0 {
		w.WriteHeader(h.status)
	}

	_, _ = io.WriteString(w, h.body)
}

// conditionsSeen reports whether the handler got any conditional header.
func (h *datedHandler) conditionsSeen() bool {
	for _, seen := range h.seen {
		if seen != "|" {
			return true
		}
	}

	return false
}

// TestLastModified checks that If-Modified-Since is answered 304 when the
// response is not newer, to the second, and only for GET and HEAD
// requests without If-None-Match, with status 200.
func TestLastModified(t *testing.T) {
	handler := &datedHandler{body: page, modified: modifiedAt}
	served := LastModified(handler)

	full := serveRequest(served, pageRequest(http.MethodGet, "/feed.atom"))
	if full.Code != http.StatusOK || full.Body.String() != page || full.Header().Get("Last-Modified") != httpDate(modifiedAt) {
		t.Fatalf("without a condition: %d %v", full.Code, full.Header())
	}

	for name, test := range map[string]struct {
		method  string
		headers []string
		want    int
	}{
		"same date":     {http.MethodGet, []string{"If-Modified-Since", httpDate(modifiedAt)}, http.StatusNotModified},
		"later date":    {http.MethodGet, []string{"If-Modified-Since", httpDate(modifiedAt.Add(time.Hour))}, http.StatusNotModified},
		"head":          {http.MethodHead, []string{"If-Modified-Since", httpDate(modifiedAt)}, http.StatusNotModified},
		"older date":    {http.MethodGet, []string{"If-Modified-Since", httpDate(modifiedAt.Add(-time.Second))}, http.StatusOK},
		"invalid date":  {http.MethodGet, []string{"If-Modified-Since", "yesterday"}, http.StatusOK},
		"post":          {http.MethodPost, []string{"If-Modified-Since", httpDate(modifiedAt)}, http.StatusOK},
		"if-none-match": {http.MethodGet, []string{"If-None-Match", `"other"`, "If-Modified-Since", httpDate(modifiedAt)}, http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			response := serveRequest(served, pageRequest(test.method, "/feed.atom", test.headers...))
			if response.Code != test.want {
				t.Fatalf("status %d, want %d", response.Code, test.want)
			}

			if test.want != http.StatusNotModified {
				return
			}

			header := response.Header()
			if response.Body.Len() != 0 || header.Get("Content-Type") != "" ||
				header.Get("Last-Modified") != httpDate(modifiedAt) {
				t.Fatalf("304 with %d bytes, %v", response.Body.Len(), header)
			}
		})
	}

	// Other responses than 200, and those without a valid Last-Modified,
	// are not answered by the date.
	since := []string{"If-Modified-Since", httpDate(modifiedAt)}

	missing := &datedHandler{body: page, modified: modifiedAt, status: http.StatusNotFound}

	response := serveRequest(LastModified(missing), pageRequest(http.MethodGet, "/", since...))
	if response.Code != http.StatusNotFound || response.Body.String() != page {
		t.Fatalf("404: %d", response.Code)
	}

	undated := &datedHandler{body: page}

	response = serveRequest(LastModified(undated), pageRequest(http.MethodGet, "/", since...))
	if response.Code != http.StatusOK || response.Body.String() != page {
		t.Fatalf("without Last-Modified: %d", response.Code)
	}

	invalid := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Last-Modified", "today")
		_, _ = io.WriteString(w, page)
	})

	response = serveRequest(LastModified(invalid), pageRequest(http.MethodGet, "/", since...))
	if response.Code != http.StatusOK {
		t.Fatalf("invalid Last-Modified: %d", response.Code)
	}
}

// TestLastModifiedWrites checks the 304 of a response written with
// WriteString, written after a flush, and with an explicit status.
func TestLastModifiedWrites(t *testing.T) {
	since := []string{"If-Modified-Since", httpDate(modifiedAt)}

	for name, write := range map[string]func(http.ResponseWriter){
		"write string": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, page)
		},
		"write header": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(page))
		},
		"flush first": func(w http.ResponseWriter) {
			_ = http.NewResponseController(w).Flush()
			_, _ = io.WriteString(w, page)
		},
	} {
		t.Run(name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Last-Modified", httpDate(modifiedAt))
				write(w)
			})

			response := serveRequest(LastModified(handler), pageRequest(http.MethodGet, "/", since...))
			if response.Code != http.StatusNotModified || response.Body.Len() != 0 {
				t.Fatalf("%d with %d bytes", response.Code, response.Body.Len())
			}
		})
	}
}

// TestLastModifiedStreamsAndHijacks checks that a response that is not
// answered 304 can be flushed and hijacked through LastModified.
func TestLastModifiedStreamsAndHijacks(t *testing.T) {
	server := httptest.NewServer(LastModified(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hijack" {
			conn, buf, err := http.NewResponseController(w).Hijack()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)

				return
			}

			defer func() {
				_ = conn.Close()
			}()

			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 8\r\nConnection: close\r\n\r\nhijacked")
			_ = buf.Flush()

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: first\n\n")

		if err := http.NewResponseController(w).Flush(); err != nil {
			_, _ = fmt.Fprint(w, "flush failed: "+err.Error())
		}
	})))
	defer server.Close()

	get := func(path string) string {
		request, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		request.Header.Set("If-Modified-Since", httpDate(modifiedAt))

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			_ = response.Body.Close()
		}()

		body, _ := io.ReadAll(bufio.NewReader(response.Body))

		return string(body)
	}

	if body := get("/stream"); body != "data: first\n\n" {
		t.Fatalf("stream %q", body)
	}

	if body := get("/hijack"); body != "hijacked" {
		t.Fatalf("hijack %q", body)
	}
}

// TestLastModifiedWithETag checks LastModified inside ETag and Compress:
// If-None-Match decides when the request has it, the date otherwise.
func TestLastModifiedWithETag(t *testing.T) {
	handler := &datedHandler{body: page, modified: modifiedAt}
	served := Compress()(ETag()(LastModified(handler)))
	etag := WeakETag([]byte(page))
	since := httpDate(modifiedAt)

	full := serveRequest(served, pageRequest(http.MethodGet, "/", "Accept-Encoding", "gzip"))
	if full.Code != http.StatusOK || full.Header().Get("ETag") != etag || full.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("full response: %d %v", full.Code, full.Header())
	}

	reader, err := gzip.NewReader(full.Body)
	if err != nil {
		t.Fatal(err)
	}

	if body, _ := io.ReadAll(reader); string(body) != page {
		t.Fatal("compressed body")
	}

	byDate := serveRequest(served, pageRequest(http.MethodGet, "/", "Accept-Encoding", "gzip", "If-Modified-Since", since))
	if byDate.Code != http.StatusNotModified || byDate.Body.Len() != 0 || byDate.Header().Get("Content-Encoding") != "" {
		t.Fatalf("by date: %d %v", byDate.Code, byDate.Header())
	}

	// An entity tag that does not match wins over a date that would.
	changed := serveRequest(served, pageRequest(http.MethodGet, "/", "If-None-Match", `W/"old"`, "If-Modified-Since", since))
	if changed.Code != http.StatusOK || changed.Body.String() != page {
		t.Fatalf("If-None-Match first: %d", changed.Code)
	}

	byTag := serveRequest(
		served,
		pageRequest(http.MethodGet, "/", "If-None-Match", etag, "If-Modified-Since", httpDate(modifiedAt.Add(-time.Hour))),
	)
	if byTag.Code != http.StatusNotModified {
		t.Fatalf("by tag: %d", byTag.Code)
	}
}

// TestCacheIfModifiedSince checks that the cache answers If-Modified-Since
// from the stored Last-Modified and from the response it renders, after
// If-None-Match, and passes neither condition on to the handler.
func TestCacheIfModifiedSince(t *testing.T) {
	handler := &datedHandler{body: page, modified: modifiedAt}
	cache := NewCache(CacheOptions{StatusHeader: "X-Cache"})
	served := cache.Middleware(ETag()(LastModified(handler)))
	since := httpDate(modifiedAt)
	etag := WeakETag([]byte(page))

	// The request that renders the entry is answered by the date itself.
	first := serveRequest(served, pageRequest(http.MethodGet, "/feed.atom", "If-Modified-Since", since))
	if first.Code != http.StatusNotModified || first.Body.Len() != 0 || first.Header().Get("X-Cache") != "miss" {
		t.Fatalf("rendering request: %d %v", first.Code, first.Header())
	}

	if cache.Stats().Entries != 1 || handler.conditionsSeen() {
		t.Fatalf("stats %+v, handler saw %q", cache.Stats(), handler.seen)
	}

	for name, test := range map[string]struct {
		method  string
		headers []string
		want    int
	}{
		"same date":    {http.MethodGet, []string{"If-Modified-Since", since}, http.StatusNotModified},
		"later date":   {http.MethodGet, []string{"If-Modified-Since", httpDate(modifiedAt.Add(time.Minute))}, http.StatusNotModified},
		"head":         {http.MethodHead, []string{"If-Modified-Since", since}, http.StatusNotModified},
		"older date":   {http.MethodGet, []string{"If-Modified-Since", httpDate(modifiedAt.Add(-time.Second))}, http.StatusOK},
		"invalid date": {http.MethodGet, []string{"If-Modified-Since", "never"}, http.StatusOK},
		"tag first":    {http.MethodGet, []string{"If-None-Match", `W/"old"`, "If-Modified-Since", since}, http.StatusOK},
		"tag":          {http.MethodGet, []string{"If-None-Match", etag, "If-Modified-Since", httpDate(modifiedAt.Add(-time.Hour))}, http.StatusNotModified},
	} {
		t.Run(name, func(t *testing.T) {
			response := serveRequest(served, pageRequest(test.method, "/feed.atom", test.headers...))
			if response.Code != test.want || response.Header().Get("X-Cache") != "hit" {
				t.Fatalf("status %d, want %d, %v", response.Code, test.want, response.Header())
			}

			if test.want == http.StatusNotModified &&
				(response.Body.Len() != 0 || response.Header().Get("Content-Length") != "") {
				t.Fatalf("304 with %d bytes, %v", response.Body.Len(), response.Header())
			}
		})
	}

	if handler.calls != 1 {
		t.Fatalf("handler called %d times, want 1", handler.calls)
	}

	// A browser sends both validators: the tag decides on the render too,
	// here of an empty cache.
	empty := NewCache(CacheOptions{StatusHeader: "X-Cache"}).Middleware(ETag()(LastModified(handler)))

	both := serveRequest(
		empty,
		pageRequest(http.MethodGet, "/feed.atom", "If-None-Match", `W/"old"`, "If-Modified-Since", since),
	)
	if both.Code != http.StatusOK || both.Body.String() != page || handler.conditionsSeen() {
		t.Fatalf("rendering request with both: %d, handler saw %q", both.Code, handler.seen)
	}
}

// TestCacheIfModifiedSinceVersion checks that an entry stale by version
// answers no condition from its Last-Modified: when a post is taken off a
// feed, the version changes, and the request renders the feed again.
func TestCacheIfModifiedSinceVersion(t *testing.T) {
	var version uint64

	handler := &datedHandler{body: "<feed>with the post</feed>", modified: modifiedAt}
	cache := NewCache(CacheOptions{
		Version:      func(*http.Request) uint64 { return version },
		StatusHeader: "X-Cache",
	})
	served := cache.Middleware(ETag()(LastModified(handler)))

	first := serveRequest(served, pageRequest(http.MethodGet, "/feed.atom"))
	if first.Code != http.StatusOK || first.Header().Get("Last-Modified") != httpDate(modifiedAt) {
		t.Fatalf("first: %d %v", first.Code, first.Header())
	}

	oldETag := first.Header().Get("ETag")
	since := first.Header().Get("Last-Modified")

	if response := serveRequest(
		served,
		pageRequest(http.MethodGet, "/feed.atom", "If-Modified-Since", since),
	); response.Code != http.StatusNotModified {
		t.Fatalf("before the change: %d", response.Code)
	}

	// The post is unpublished: the version changes and the feed changes
	// with it, its date that of the change.
	version++
	handler.body = "<feed></feed>"
	handler.modified = modifiedAt.Add(time.Hour)

	stale := serveRequest(served, pageRequest(http.MethodGet, "/feed.atom", "If-Modified-Since", since))
	if stale.Code != http.StatusOK || stale.Body.String() != "<feed></feed>" || stale.Header().Get("X-Cache") != "miss" {
		t.Fatalf("after the change: %d %q %v", stale.Code, stale.Body.String(), stale.Header())
	}

	if stale.Header().Get("Last-Modified") != httpDate(modifiedAt.Add(time.Hour)) {
		t.Fatalf("Last-Modified %q", stale.Header().Get("Last-Modified"))
	}

	// A browser that holds the old feed sends both validators. Its tag
	// decides, also when the date of the new feed went back.
	version++
	handler.body = "<feed>other</feed>"
	handler.modified = modifiedAt.Add(-time.Hour)

	both := serveRequest(served, pageRequest(http.MethodGet, "/feed.atom", "If-None-Match", oldETag, "If-Modified-Since", since))
	if both.Code != http.StatusOK || both.Body.String() != "<feed>other</feed>" {
		t.Fatalf("both validators after the change: %d %q", both.Code, both.Body.String())
	}

	hit := serveRequest(served, pageRequest(http.MethodGet, "/feed.atom", "If-None-Match", oldETag, "If-Modified-Since", since))
	if hit.Code != http.StatusOK || hit.Header().Get("X-Cache") != "hit" {
		t.Fatalf("both validators from the entry: %d %v", hit.Code, hit.Header())
	}

	if handler.conditionsSeen() || handler.calls != 3 {
		t.Fatalf("handler called %d times, saw %q", handler.calls, handler.seen)
	}
}

// TestCacheNotModifiedOnlyFor200 checks that a stored 404 is not answered
// 304, whatever its validators.
func TestCacheNotModifiedOnlyFor200(t *testing.T) {
	handler := &datedHandler{body: "gone", modified: modifiedAt, status: http.StatusNotFound}
	served := NewCache(CacheOptions{NotFound: true}).Middleware(handler)

	serveRequest(served, pageRequest(http.MethodGet, "/"))

	response := serveRequest(served, pageRequest(http.MethodGet, "/", "If-Modified-Since", httpDate(modifiedAt)))
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "gone") || handler.calls != 1 {
		t.Fatalf("%d %q, %d calls", response.Code, response.Body.String(), handler.calls)
	}
}
