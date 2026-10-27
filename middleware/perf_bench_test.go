package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// benchPage is a rendered page of about the size go-webthings' public page
// benchmark serves.
var benchPage = strings.Repeat("<li><a href=\"/en/page\">A page of the site</a></li>\n", 260)

// benchVary is the Vary header of a go-webthings page: the request headers
// go-partial's connector reads, and the language.
var benchVary = []string{
	"HX-Request",
	"HX-Target",
	"HX-Boosted",
	"HX-History-Restore-Request",
	"HX-Current-URL",
	"Accept-Language",
	"Cookie",
}

// discardWriter is a ResponseWriter that keeps no body and reuses its
// header map, so a benchmark counts the middleware's allocations only.
type discardWriter struct {
	header http.Header
	status int
	bytes  int
}

func newDiscardWriter() *discardWriter {
	return &discardWriter{header: make(http.Header, 16)}
}

func (d *discardWriter) reset() {
	clear(d.header)
	d.status = 0
	d.bytes = 0
}

func (d *discardWriter) Header() http.Header {
	return d.header
}

func (d *discardWriter) WriteHeader(status int) {
	if d.status == 0 {
		d.status = status
	}
}

func (d *discardWriter) Write(data []byte) (int, error) {
	d.bytes += len(data)

	return len(data), nil
}

func (d *discardWriter) WriteString(s string) (int, error) {
	d.bytes += len(s)

	return len(s), nil
}

// pageHandler answers like go-partial's Write: Vary, Content-Type, then the
// page as a string.
func pageHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		header := w.Header()
		header["Vary"] = append(header["Vary"], benchVary...)
		header.Set("Content-Type", "text/html; charset=utf-8")

		_, _ = io.WriteString(w, body)
	})
}

// serveBench serves request with handler b.N times.
func serveBench(b *testing.B, handler http.Handler, request *http.Request, want int) {
	b.Helper()

	writer := newDiscardWriter()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		writer.reset()
		handler.ServeHTTP(writer, request)

		if writer.status != want && (writer.status != 0 || want != http.StatusOK) {
			b.Fatalf("status %d, want %d", writer.status, want)
		}
	}
}

// BenchmarkETag serves a page through ETag, which holds and hashes it.
func BenchmarkETag(b *testing.B) {
	handler := ETag(ETagOptions{CacheControl: func(*http.Request) string { return "no-cache" }})(pageHandler(benchPage))

	serveBench(b, handler, httptest.NewRequest(http.MethodGet, "/en/", nil), http.StatusOK)
}

// BenchmarkETagNotModified answers a request for an unchanged page.
func BenchmarkETagNotModified(b *testing.B) {
	handler := ETag()(pageHandler(benchPage))

	request := httptest.NewRequest(http.MethodGet, "/en/", nil)
	request.Header.Set("If-None-Match", WeakETag([]byte(benchPage)))

	serveBench(b, handler, request, http.StatusNotModified)
}

// BenchmarkCompress gzips a page.
func BenchmarkCompress(b *testing.B) {
	handler := Compress()(pageHandler(benchPage))

	serveBench(b, handler, gzipRequest("gzip, deflate, br"), http.StatusOK)
}

// BenchmarkCompressSmall answers with a page too small to compress.
func BenchmarkCompressSmall(b *testing.B) {
	handler := Compress()(pageHandler("<p>Hello.</p>"))

	serveBench(b, handler, gzipRequest("gzip, deflate, br"), http.StatusOK)
}

// garbage keeps the allocations of BenchmarkCompressGCPressure alive long
// enough to count.
var garbage [][]byte

// BenchmarkCompressGCPressure gzips pages while every request also
// allocates garbage, as a rendered page does, so the garbage collector runs
// every few requests, as it does in go-webthings: one request at a time,
// and in parallel. The gzip writers must survive the collections: a writer
// that is lost costs about 800 KB to make again. writers/1000req counts
// those made.
func BenchmarkCompressGCPressure(b *testing.B) {
	setup := func() (http.Handler, *gzipWriters) {
		writers := newGzipWriters(gzip.DefaultCompression)
		options := CompressOptions{MinSize: DefaultCompressMinSize, Level: gzip.DefaultCompression, Types: CompressibleType}

		return compress(options, writers)(pageHandler(benchPage)), writers
	}

	// serve serves one request, then allocates about as much as rendering
	// a page does.
	serve := func(handler http.Handler, writer *discardWriter, request *http.Request, local [][]byte) [][]byte {
		writer.reset()
		handler.ServeHTTP(writer, request)

		return append(local[:0], make([]byte, 64<<10), make([]byte, 64<<10))
	}

	b.Run("serial", func(b *testing.B) {
		handler, writers := setup()
		writer := newDiscardWriter()
		request := gzipRequest("gzip")
		local := make([][]byte, 0, 2)

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			local = serve(handler, writer, request, local)
		}

		garbage = local
		b.ReportMetric(float64(writers.made.Load())*1000/float64(b.N), "writers/1000req")
	})

	b.Run("parallel", func(b *testing.B) {
		handler, writers := setup()

		b.ReportAllocs()
		b.ResetTimer()

		b.RunParallel(func(pb *testing.PB) {
			writer := newDiscardWriter()
			request := gzipRequest("gzip")
			local := make([][]byte, 0, 2)

			for pb.Next() {
				local = serve(handler, writer, request, local)
			}

			garbage = local
		})

		b.ReportMetric(float64(writers.made.Load())*1000/float64(b.N), "writers/1000req")
	})
}

// BenchmarkSecurityHeaders sets the default security headers.
func BenchmarkSecurityHeaders(b *testing.B) {
	handler := SecurityHeaders()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	serveBench(b, handler, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusOK)
}

// BenchmarkRequestID gives a request without an ID a new one.
func BenchmarkRequestID(b *testing.B) {
	handler := RequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	serveBench(b, handler, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusOK)
}

// BenchmarkRequestIDIncoming keeps the ID a request comes with.
func BenchmarkRequestIDIncoming(b *testing.B) {
	handler := RequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(RequestIDHeader, "0123456789abcdef0123456789abcdef")

	serveBench(b, handler, request, http.StatusOK)
}

// BenchmarkMediaTypes classifies a page's Content-Type as Compress and ETag
// do.
func BenchmarkMediaTypes(b *testing.B) {
	header := http.Header{"Content-Type": {"text/html; charset=utf-8"}}
	cacheable := CacheableTypes(DefaultETagTypes...)

	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if !CompressibleType("text/html; charset=utf-8") || !cacheable(http.StatusOK, header) {
			b.Fatal("not a page")
		}
	}
}

// BenchmarkStack serves a page through the middleware go-webthings puts in
// front of its public pages, outermost first.
func BenchmarkStack(b *testing.B) {
	handler := pageHandler(benchPage)

	handler = ETag(ETagOptions{CacheControl: func(*http.Request) string { return "no-cache" }})(handler)
	handler = Compress()(handler)
	handler = SecurityHeaders()(handler)
	handler = RecoverWithOptions(RecoverOptions{})(handler)
	handler = RequestID(handler)

	b.Run("plain", func(b *testing.B) {
		serveBench(b, handler, httptest.NewRequest(http.MethodGet, "/en/", nil), http.StatusOK)
	})

	b.Run("gzip", func(b *testing.B) {
		serveBench(b, handler, gzipRequest("gzip, deflate, br"), http.StatusOK)
	})
}

// BenchmarkCache serves a page through the output cache, with Compress and
// ETag inside it as the README advises: from the entry (hit, also answered
// 304 Not Modified), and rendered every time, as when the version changes
// on every request (miss).
func BenchmarkCache(b *testing.B) {
	var version uint64

	cache := NewCache(CacheOptions{
		Version: func(*http.Request) uint64 { return version },
	})

	handler := cache.Middleware(Compress()(ETag(ETagOptions{
		CacheControl: func(*http.Request) string { return "no-cache" },
	})(pageHandler(benchPage))))

	b.Run("hit", func(b *testing.B) {
		serveBench(b, handler, gzipRequest("gzip, deflate, br"), http.StatusOK)
	})

	b.Run("hit-plain", func(b *testing.B) {
		serveBench(b, handler, httptest.NewRequest(http.MethodGet, "/en/", nil), http.StatusOK)
	})

	b.Run("not-modified", func(b *testing.B) {
		request := gzipRequest("gzip, deflate, br")
		request.Header.Set("If-None-Match", WeakETag([]byte(benchPage)))

		serveBench(b, handler, request, http.StatusNotModified)
	})

	b.Run("miss", func(b *testing.B) {
		request := gzipRequest("gzip, deflate, br")
		writer := newDiscardWriter()

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			version++

			writer.reset()
			handler.ServeHTTP(writer, request)
		}
	})

	b.Run("uncached", func(b *testing.B) {
		uncached := Compress()(ETag(ETagOptions{
			CacheControl: func(*http.Request) string { return "no-cache" },
		})(pageHandler(benchPage)))

		serveBench(b, uncached, gzipRequest("gzip, deflate, br"), http.StatusOK)
	})
}

// BenchmarkCacheMixed serves a whole page and an HTMX navigation of the
// same address in turn, as mixed traffic does: the navigation varies on
// HX-Current-URL as well, so the two are kept under other Vary lists of
// one key. Both are hits after the first two requests; hits/op reports it.
func BenchmarkCacheMixed(b *testing.B) {
	pageVary := []string{"HX-Request", "HX-Target", "HX-Boosted", "HX-History-Restore-Request", "Accept-Language", "Cookie"}

	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header["Vary"] = append(header["Vary"], pageVary...)
		header.Set("Content-Type", "text/html; charset=utf-8")

		if r.Header.Get("HX-Request") != "" {
			header.Add("Vary", "HX-Current-URL")
		}

		_, _ = io.WriteString(w, benchPage)
	})

	full := gzipRequest("gzip, deflate, br")

	navigation := gzipRequest("gzip, deflate, br")
	navigation.Header.Set("HX-Request", "true")
	navigation.Header.Set("HX-Target", "content")
	navigation.Header.Set("HX-Current-URL", "http://example.com/en/work")

	requests := []*http.Request{full, navigation}

	b.Run("alternating", func(b *testing.B) {
		cache := NewCache()
		handler := cache.Middleware(Compress()(ETag()(page)))
		writer := newDiscardWriter()

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			writer.reset()
			handler.ServeHTTP(writer, requests[i%2])
		}

		b.ReportMetric(float64(cache.Stats().Hits)/float64(b.N), "hits/op")
	})

	b.Run("parallel", func(b *testing.B) {
		cache := NewCache()
		handler := cache.Middleware(Compress()(ETag()(page)))

		b.ReportAllocs()
		b.ResetTimer()

		b.RunParallel(func(pb *testing.PB) {
			writer := newDiscardWriter()

			for i := 0; pb.Next(); i++ {
				writer.reset()
				handler.ServeHTTP(writer, requests[i%2])
			}
		})

		b.ReportMetric(float64(cache.Stats().Hits)/float64(b.N), "hits/op")
	})
}
