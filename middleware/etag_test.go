package middleware

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// cachePolicy is go-webthings' policy for public pages: revalidate
// addresses with a query, keep others for maxAge seconds.
func cachePolicy(maxAge int) func(*http.Request) string {
	return func(r *http.Request) string {
		if maxAge <= 0 || r.URL.RawQuery != "" {
			return "no-cache"
		}

		return "public, max-age=" + strconv.Itoa(maxAge)
	}
}

func html(headers ...string) http.Handler {
	return contentHandler("text/html; charset=utf-8", page, headers...)
}

// TestETag checks the ETag, 304 and Cache-Control of pages.
func TestETag(t *testing.T) {
	revalidate := ETagOptions{CacheControl: cachePolicy(0)}
	keep := ETagOptions{CacheControl: cachePolicy(60)}

	first := serveRequest(ETag(revalidate)(html()), httptest.NewRequest(http.MethodGet, "/en/", nil))

	etag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || !strings.HasPrefix(etag, `W/"`) || first.Header().Get("Cache-Control") != "no-cache" ||
		first.Body.String() != page {
		t.Fatalf("%d %v", first.Code, first.Header())
	}

	again := httptest.NewRequest(http.MethodGet, "/en/", nil)
	again.Header.Set("If-None-Match", `"other", `+strings.TrimPrefix(etag, "W/"))

	second := serveRequest(ETag(revalidate)(html()), again)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 || second.Header().Get("ETag") != etag ||
		second.Header().Get("Content-Type") != "" {
		t.Fatalf("%d %v %q", second.Code, second.Header(), second.Body.String())
	}

	kept := serveRequest(ETag(keep)(html()), httptest.NewRequest(http.MethodGet, "/en/", nil))
	if got := kept.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Fatalf("cache control %q", got)
	}

	query := serveRequest(ETag(keep)(html()), httptest.NewRequest(http.MethodGet, "/en/?comment=sent", nil))
	if got := query.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache control with a query %q", got)
	}

	own := serveRequest(ETag(keep)(html("Cache-Control", "public, max-age=5")), httptest.NewRequest(http.MethodGet, "/en/", nil))
	if got := own.Header().Get("Cache-Control"); got != "public, max-age=5" || own.Header().Get("ETag") == "" {
		t.Fatalf("the handler's cache control was replaced: %v", own.Header())
	}

	none := serveRequest(ETag()(html()), httptest.NewRequest(http.MethodGet, "/en/", nil))
	if none.Header().Get("Cache-Control") != "" || none.Header().Get("ETag") == "" {
		t.Fatalf("without CacheControl: %v", none.Header())
	}

	for name, h := range map[string]http.Handler{
		"no-store": html("Cache-Control", "no-store"),
		"private":  html("Cache-Control", "private, no-store"),
		"cookie":   html("Set-Cookie", "a=b"),
		"image":    contentHandler("image/png", "\x89PNG"),
		"not found": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, page)
		}),
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/en/", nil)
			r.Header.Set("If-None-Match", "*")

			response := serveRequest(ETag(keep)(h), r)
			if response.Header().Get("ETag") != "" || response.Code == http.StatusNotModified ||
				strings.Contains(response.Header().Get("Cache-Control"), "max-age=60") {
				t.Fatalf("%d %v", response.Code, response.Header())
			}
		})
	}

	post := serveRequest(ETag(keep)(html()), httptest.NewRequest(http.MethodPost, "/en/", nil))
	if post.Header().Get("ETag") != "" {
		t.Fatalf("a post got an ETag: %v", post.Header())
	}

	// A custom Cacheable decides which responses get one.
	jsonToo := ETagOptions{Cacheable: func(status int, header http.Header) bool { return status == 0 || status == http.StatusOK }}

	api := serveRequest(ETag(jsonToo)(contentHandler("application/json", `{}`)), httptest.NewRequest(http.MethodGet, "/api", nil))
	if api.Header().Get("ETag") == "" {
		t.Fatalf("custom cacheable: %v", api.Header())
	}
}

// TestETagStreams checks that a flushed page goes out as it is written.
func TestETagStreams(t *testing.T) {
	streamed := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<p>first</p>")
		_ = http.NewResponseController(w).Flush()
		_, _ = io.WriteString(w, "<p>second</p>")
	})

	response := serveRequest(ETag()(streamed), httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Header().Get("ETag") != "" || response.Body.String() != "<p>first</p><p>second</p>" || !response.Flushed {
		t.Fatalf("%v %q", response.Header(), response.Body.String())
	}

	status := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	written := serveRequest(ETag()(status), httptest.NewRequest(http.MethodGet, "/", nil))
	if written.Code != http.StatusAccepted {
		t.Fatalf("status %d", written.Code)
	}
}

func TestETagMatches(t *testing.T) {
	for header, want := range map[string]bool{
		"":                 false,
		`"abc"`:            true,
		`W/"abc"`:          true,
		`"x", W/"abc"`:     true,
		`*`:                true,
		`"abcd"`:           false,
		`W/"x" , "y"`:      false,
		`  "abc"  , "y"  `: true,
	} {
		if got := ETagMatches(header, `W/"abc"`); got != want {
			t.Errorf("ETagMatches(%q): want %v, got %v", header, want, got)
		}
	}
}

// TestWeakETagFormat checks that the validator built in place is the one
// of base64.RawURLEncoding.EncodeToString, which it replaced.
func TestWeakETagFormat(t *testing.T) {
	for _, body := range []string{"", "x", page, strings.Repeat("\xff", 4096)} {
		sum := sha256.Sum256([]byte(body))
		want := `W/"` + base64.RawURLEncoding.EncodeToString(sum[:18]) + `"`

		if got := WeakETag([]byte(body)); got != want {
			t.Errorf("WeakETag(%d bytes): want %s, got %s", len(body), want, got)
		}
	}
}

// TestETagPooledBuffers checks that pages held in pooled buffers never
// carry bytes of an earlier response, written as strings or bytes, one
// after the other and at once.
func TestETagPooledBuffers(t *testing.T) {
	pages := []string{
		strings.Repeat("<p>A long page.</p>\n", 2000),
		"<p>short</p>",
		strings.Repeat("<p>Medium.</p>", 300),
		"",
	}

	asBytes := func(body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(body))
		})
	}

	check := func(t *testing.T, handler http.Handler, body string) {
		response := serveRequest(ETag()(handler), httptest.NewRequest(http.MethodGet, "/", nil))

		if response.Body.String() != body {
			t.Errorf("body of %d bytes: got %d bytes", len(body), response.Body.Len())
		}

		if got, want := response.Header().Get("ETag"), WeakETag([]byte(body)); got != want {
			t.Errorf("etag of %d bytes: want %s, got %s", len(body), want, got)
		}
	}

	for range 3 {
		for _, body := range pages {
			check(t, contentHandler("text/html", body), body)
			check(t, asBytes(body), body)
		}
	}

	var group sync.WaitGroup

	for i := range 32 {
		group.Add(1)

		go func() {
			defer group.Done()

			body := pages[i%len(pages)]
			check(t, contentHandler("text/html", body), body)
		}()
	}

	group.Wait()
}

// TestETagWriteStringSniffs checks that a string body without a
// Content-Type gets the type net/http would give it.
func TestETagWriteStringSniffs(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<!DOCTYPE html><html><body>"+page+"</body></html>")
	})

	response := serveRequest(ETag()(handler), httptest.NewRequest(http.MethodGet, "/", nil))
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" || response.Header().Get("ETag") == "" {
		t.Fatalf("%v", response.Header())
	}

	plain := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "plain text")
	})

	response = serveRequest(ETag()(plain), httptest.NewRequest(http.MethodGet, "/", nil))
	if got := response.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" || response.Header().Get("ETag") == "" ||
		response.Body.String() != "plain text" {
		t.Fatalf("%v %q", response.Header(), response.Body.String())
	}

	pagesOnly := ETagOptions{Types: []string{"text/html"}}

	response = serveRequest(ETag(pagesOnly)(plain), httptest.NewRequest(http.MethodGet, "/", nil))
	if got := response.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" || response.Header().Get("ETag") != "" ||
		response.Body.String() != "plain text" {
		t.Fatalf("%v %q", response.Header(), response.Body.String())
	}
}

// TestETagKeepsSmallBuffers checks that a buffer grown beyond
// maxPooledBody is not kept.
func TestETagKeepsSmallBuffers(t *testing.T) {
	large := getBody()
	large.Grow(maxPooledBody * 2)
	putBody(large)

	for range 8 {
		body := getBody()
		if body.Cap() > maxPooledBody || body.Len() != 0 {
			t.Fatalf("pooled buffer of cap %d and len %d", body.Cap(), body.Len())
		}

		defer putBody(body)
	}
}

// TestETagTypes checks the default media types, Types with its three forms
// of entries, and that Cacheable takes precedence over Types.
func TestETagTypes(t *testing.T) {
	tagged := func(options ETagOptions, contentType string, headers ...string) bool {
		response := serveRequest(
			ETag(options)(contentHandler(contentType, page, headers...)),
			httptest.NewRequest(http.MethodGet, "/", nil),
		)

		return response.Header().Get("ETag") != "" && response.Body.String() == page
	}

	for _, contentType := range []string{
		"text/html; charset=utf-8",
		"Text/HTML",
		"text/plain",
		"text/css",
		"text/javascript",
		"text/xml",
		"application/javascript",
		"application/json",
		"application/xml",
		"application/atom+xml; charset=utf-8",
		"application/rss+xml",
	} {
		if !tagged(ETagOptions{}, contentType) {
			t.Errorf("%s: no ETag by default", contentType)
		}
	}

	for _, contentType := range []string{"image/png", "application/octet-stream", "image/svg+xml", "text/event-stream; charset=utf-8", "invalid;;"} {
		if tagged(ETagOptions{}, contentType) {
			t.Errorf("%s: an ETag by default", contentType)
		}
	}

	// The other conditions hold for every type.
	for _, headers := range [][]string{
		{"Cache-Control", "private"},
		{"Set-Cookie", "a=b"},
		{"ETag", `"own"`},
	} {
		handler := ETag()(contentHandler("application/json", page, headers...))

		response := serveRequest(handler, httptest.NewRequest(http.MethodGet, "/", nil))
		if response.Header().Get("ETag") == WeakETag([]byte(page)) {
			t.Errorf("%v: an ETag", headers)
		}
	}

	custom := ETagOptions{Types: []string{" Application/JSON ", "text/*", "+xml"}}

	for contentType, want := range map[string]bool{
		"application/json":     true,
		"text/csv":             true,
		"text/html":            true,
		"image/svg+xml":        true,
		"application/atom+xml": true,
		"application/xml":      false,
		"application/ld+json":  false,
		"image/png":            false,
	} {
		if got := tagged(custom, contentType); got != want {
			t.Errorf("Types %v, %s: tagged %v, want %v", custom.Types, contentType, got, want)
		}
	}

	never := ETagOptions{
		Types:     []string{"application/json"},
		Cacheable: func(int, http.Header) bool { return false },
	}

	if tagged(never, "application/json") {
		t.Error("Types was used with Cacheable set")
	}

	for _, entry := range []string{"json", "text/", "*/*", "+", "application/json;q=1", ""} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Types %q did not panic", entry)
				}
			}()

			ETag(ETagOptions{Types: []string{entry}})
		}()
	}
}

// TestCacheableTypes checks the function on its own, as an application
// would call it from a Cacheable of its own.
func TestCacheableTypes(t *testing.T) {
	feeds := CacheableTypes("application/atom+xml", "application/rss+xml")
	header := http.Header{"Content-Type": {"application/rss+xml; charset=utf-8"}}

	if !feeds(0, header) || !feeds(http.StatusOK, header) {
		t.Fatal("a feed is not cacheable")
	}

	if feeds(http.StatusNotFound, header) {
		t.Fatal("a 404 is cacheable")
	}

	if feeds(http.StatusOK, http.Header{"Content-Type": {"text/html"}}) {
		t.Fatal("a page is cacheable as a feed")
	}
}
