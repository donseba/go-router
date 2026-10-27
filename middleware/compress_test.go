package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

var page = strings.Repeat("<p>Hello, visitor.</p>\n", 200)

func serveRequest(handler http.Handler, r *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, r)

	return response
}

func TestAcceptsGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"":                      false,
		"gzip":                  true,
		"br, gzip;q=0.5":        true,
		"gzip;q=0":              false,
		"*":                     true,
		"*;q=0":                 false,
		"identity":              false,
		"deflate, GZIP":         true,
		"*, gzip;q=0":           false,
		"gzip;q=0.0, *;q=1.0":   false,
		"gzip ; q=1, identity":  true,
		"x-gzip, deflate, br":   false,
		"br;q=1.0, gzip;q=0.8 ": true,
	} {
		if got := AcceptsGzip(header); got != want {
			t.Errorf("AcceptsGzip(%q): want %v, got %v", header, want, got)
		}
	}
}

func contentHandler(contentType, body string, headers ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}

		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}

		_, _ = io.WriteString(w, body)
	})
}

func gzipRequest(encoding string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if encoding != "" {
		r.Header.Set("Accept-Encoding", encoding)
	}

	return r
}

// TestCompress checks that text goes out gzipped to clients that accept
// it, with Vary, and that images, small bodies, encoded bodies, partial
// responses and clients without gzip get the body as it is.
func TestCompress(t *testing.T) {
	t.Run("html", func(t *testing.T) {
		response := serveRequest(
			Compress()(contentHandler("text/html; charset=utf-8", page, "ETag", `"v1"`, "Content-Length", "4600")),
			gzipRequest("gzip, br"),
		)

		header := response.Header()
		if header.Get("Content-Encoding") != "gzip" || header.Get("Vary") != "Accept-Encoding" ||
			header.Get("ETag") != `W/"v1"` || header.Get("Content-Length") != "" {
			t.Fatalf("headers %v", header)
		}

		reader, err := gzip.NewReader(response.Body)
		if err != nil {
			t.Fatal(err)
		}

		body, _ := io.ReadAll(reader)
		if string(body) != page {
			t.Fatalf("body %q", body)
		}
	})

	t.Run("sniffed", func(t *testing.T) {
		response := serveRequest(Compress()(contentHandler("", "<!doctype html>"+page)), gzipRequest("gzip"))
		if response.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("headers %v", response.Header())
		}
	})

	t.Run("no gzip", func(t *testing.T) {
		response := serveRequest(Compress()(contentHandler("text/css", page)), gzipRequest("br"))
		if response.Header().Get("Content-Encoding") != "" || response.Body.String() != page ||
			response.Header().Get("Vary") != "Accept-Encoding" {
			t.Fatalf("headers %v", response.Header())
		}
	})

	t.Run("disabled", func(t *testing.T) {
		off := CompressOptions{Enabled: func(*http.Request) bool { return false }}

		response := serveRequest(Compress(off)(contentHandler("text/html", page)), gzipRequest("gzip"))
		if response.Header().Get("Content-Encoding") != "" || response.Body.String() != page {
			t.Fatalf("headers %v", response.Header())
		}
	})

	t.Run("own vary", func(t *testing.T) {
		response := serveRequest(Compress()(contentHandler("text/html", page, "Vary", "accept-encoding")), gzipRequest("gzip"))
		if values := response.Header().Values("Vary"); len(values) != 1 {
			t.Fatalf("vary %q", values)
		}
	})

	t.Run("options", func(t *testing.T) {
		options := CompressOptions{
			MinSize: 10,
			Level:   gzip.BestSpeed,
			Types:   func(contentType string) bool { return contentType == "application/x-custom" },
		}

		response := serveRequest(Compress(options)(contentHandler("application/x-custom", "0123456789abc")), gzipRequest("gzip"))
		if response.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("headers %v", response.Header())
		}

		response = serveRequest(Compress(options)(contentHandler("text/html", page)), gzipRequest("gzip"))
		if response.Header().Get("Content-Encoding") != "" {
			t.Fatalf("headers %v", response.Header())
		}
	})

	cases := map[string]http.Handler{
		"image":   contentHandler("image/png", page),
		"woff2":   contentHandler("font/woff2", page),
		"zip":     contentHandler("application/zip", page),
		"video":   contentHandler("video/mp4", page),
		"small":   contentHandler("text/html", "<p>Hello, visitor.</p>"),
		"short":   contentHandler("text/html", page, "Content-Length", "10"),
		"encoded": contentHandler("text/html", page, "Content-Encoding", "br"),
		"stream":  contentHandler("text/event-stream", page),
		"partial": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Range", "bytes 0-10/100")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, page)
		}),
	}

	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			response := serveRequest(Compress()(h), gzipRequest("gzip"))
			if response.Header().Get("Content-Encoding") == "gzip" || !strings.Contains(page, response.Body.String()) {
				t.Fatalf("compressed: %v", response.Header())
			}
		})
	}

	for name, header := range map[string]string{"Range": "bytes=0-10", "Upgrade": "websocket"} {
		t.Run(name, func(t *testing.T) {
			r := gzipRequest("gzip")
			r.Header.Set(name, header)

			response := serveRequest(Compress()(contentHandler("text/html", page)), r)
			if response.Header().Get("Content-Encoding") != "" {
				t.Fatalf("compressed: %v", response.Header())
			}
		})
	}
}

// TestCompressStatusAndFlush checks that the status survives compression
// and that a response flushed early goes out uncompressed.
func TestCompressStatusAndFlush(t *testing.T) {
	missing := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, page)
	})

	r := gzipRequest("gzip")

	response := serveRequest(Compress()(missing), r)
	if response.Code != http.StatusNotFound || response.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("%d %v", response.Code, response.Header())
	}

	for name, flush := range map[string]func(http.ResponseWriter){
		"Flusher":            func(w http.ResponseWriter) { w.(http.Flusher).Flush() },
		"ResponseController": func(w http.ResponseWriter) { _ = http.NewResponseController(w).Flush() },
	} {
		streamed := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<p>first</p>")
			flush(w)
			_, _ = io.WriteString(w, page)
		})

		response = serveRequest(Compress()(streamed), r)
		if response.Header().Get("Content-Encoding") != "" || response.Body.String() != "<p>first</p>"+page ||
			!response.Flushed {
			t.Fatalf("%s: %v %q", name, response.Header(), response.Body.String())
		}
	}

	empty := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	response = serveRequest(Compress()(empty), r)
	if response.Code != http.StatusNoContent || response.Header().Get("Content-Encoding") != "" {
		t.Fatalf("%d %v", response.Code, response.Header())
	}
}

// TestCompressPanicLeavesTheResponse checks that a panic writes nothing, so
// Recover outside can still answer 500.
func TestCompressPanicLeavesTheResponse(t *testing.T) {
	recovered := RecoverWithOptions(RecoverOptions{Logger: discardLogger()})
	handler := recovered(Compress()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<p>partial</p>")

		panic("broken")
	})))

	response := serveRequest(handler, gzipRequest("gzip"))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "partial") {
		t.Fatalf("%d %q", response.Code, response.Body.String())
	}
}

// TestETagCompressed checks the two together: the ETag of a page matches
// whether it went out compressed or not.
func TestETagCompressed(t *testing.T) {
	handler := Compress()(ETag(ETagOptions{CacheControl: func(*http.Request) string { return "no-cache" }})(
		contentHandler("text/html", page)))

	first := serveRequest(handler, gzipRequest("gzip"))
	if first.Header().Get("Content-Encoding") != "gzip" || first.Header().Get("ETag") == "" {
		t.Fatalf("%v", first.Header())
	}

	plain := httptest.NewRequest(http.MethodGet, "/", nil)
	plain.Header.Set("If-None-Match", first.Header().Get("ETag"))

	second := serveRequest(handler, plain)
	if second.Code != http.StatusNotModified {
		t.Fatalf("%d %v", second.Code, second.Header())
	}
}

// gzipped compresses body as Compress's writers do.
func gzipped(t *testing.T, body string) []byte {
	t.Helper()

	var out bytes.Buffer

	writer, err := gzip.NewWriterLevel(&out, gzip.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = io.WriteString(writer, body)
	_ = writer.Close()

	return out.Bytes()
}

// TestCompressOutput checks that a body comes out as gzip would compress
// it at once, however the handler writes it, and that reused writers carry
// nothing over from an earlier response.
func TestCompressOutput(t *testing.T) {
	bodies := []string{
		strings.Repeat("<p>A long page of the site.</p>\n", 3000),
		page,
		strings.Repeat("x", DefaultCompressMinSize),
	}

	chunked := func(body string, size int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")

			for start := 0; start < len(body); start += size {
				_, _ = w.Write([]byte(body[start:min(start+size, len(body))]))
			}
		})
	}

	handler := func(body string, size int) http.Handler {
		return Compress()(chunked(body, size))
	}

	for range 3 {
		for _, body := range bodies {
			want := gzipped(t, body)

			for _, size := range []int{len(body), 100, DefaultCompressMinSize - 1, 4096} {
				response := serveRequest(handler(body, size), gzipRequest("gzip"))

				if response.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(response.Body.Bytes(), want) {
					t.Fatalf("%d bytes in writes of %d: compressed output differs", len(body), size)
				}
			}
		}
	}
}

// TestGzipWritersSurviveCollections checks that idle writers outlive
// garbage collections, and that only as many as the free list holds are
// kept that way.
func TestGzipWritersSurviveCollections(t *testing.T) {
	writers := newGzipWriters(gzip.DefaultCompression)

	writers.put(writers.get())

	for range 3 {
		runtime.GC()
	}

	writers.put(writers.get())

	if made := writers.made.Load(); made != 1 {
		t.Fatalf("made %d writers, want 1", made)
	}

	size := cap(writers.free)
	if size < 1 || size > maxFreeWriters {
		t.Fatalf("free list of %d writers", size)
	}

	taken := make([]*gzip.Writer, size+2)
	for i := range taken {
		taken[i] = writers.get()
	}

	for _, writer := range taken {
		writers.put(writer)
	}

	if kept := len(writers.free); kept != size {
		t.Fatalf("free list holds %d writers, want %d", kept, size)
	}
}

// TestMediaType checks that mediaType reads Content-Type headers as
// mime.ParseMediaType does.
func TestMediaType(t *testing.T) {
	for _, header := range []string{
		"", "text/html", "text/html; charset=utf-8", "Text/HTML; Charset=UTF-8", " text/html ;charset=utf-8 ",
		"text/html;", "text/html; charset", "text/html; charset=", `text/html; charset="utf-8"`, "text/html; charset=utf-8; q=1",
		"text", "text/", "/html", "text/html/x", "text/ html", "text/(html)", "image/svg+xml", "application/vnd.api+json",
		"text/html; charset = utf-8", "text/html; foo", "téxt/html", "text/html; charset=ütf-8", "text/event-stream",
	} {
		wantMedia, _, err := mime.ParseMediaType(header)
		media, ok := mediaType(header)

		if ok != (err == nil) || ok && media != wantMedia {
			t.Errorf("mediaType(%q): want %q %v, got %q %v", header, wantMedia, err == nil, media, ok)
		}
	}
}

// TestVaries checks that varies finds Accept-Encoding among several Vary
// values and lists.
func TestVaries(t *testing.T) {
	for _, values := range [][]string{
		{"Accept-Encoding"},
		{"HX-Request", "HX-Target", "accept-encoding"},
		{"HX-Request, Accept-Encoding ,Cookie"},
		{"Cookie", " Accept-Encoding"},
	} {
		if !varies(http.Header{"Vary": values}) {
			t.Errorf("varies(%q): want true", values)
		}
	}

	for _, values := range [][]string{nil, {""}, {"HX-Request", "Accept-Language"}, {"Accept-Encodings, Encoding"}} {
		if varies(http.Header{"Vary": values}) {
			t.Errorf("varies(%q): want false", values)
		}
	}
}

// TestGzipWritersShared checks that every Compress of a level shares its
// writers, and that levels keep their own.
func TestGzipWritersShared(t *testing.T) {
	for _, level := range []int{gzip.HuffmanOnly, gzip.DefaultCompression, gzip.BestSpeed, gzip.BestCompression} {
		writers := sharedGzipWriters(level)
		if writers != sharedGzipWriters(level) || writers.level != level {
			t.Fatalf("level %d: writers are not shared", level)
		}
	}

	if sharedGzipWriters(gzip.BestSpeed) == sharedGzipWriters(gzip.DefaultCompression) {
		t.Fatal("two levels share writers")
	}

	// Two Compress of a level compress with the writers of the level.
	writers := sharedGzipWriters(gzip.BestSpeed)
	made := writers.made.Load()

	for range 2 {
		handler := Compress(CompressOptions{Level: gzip.BestSpeed})(contentHandler("text/html", page))
		serveRequest(handler, gzipRequest("gzip"))
	}

	if writers.made.Load() > made+1 {
		t.Fatalf("made %d writers for two Compress, want at most 1", writers.made.Load()-made)
	}
}

// TestCompressLevels checks that every gzip level is accepted, and that
// Compress panics on another.
func TestCompressLevels(t *testing.T) {
	for level := gzip.HuffmanOnly; level <= gzip.BestCompression; level++ {
		Compress(CompressOptions{Level: level})
	}

	for _, level := range []int{gzip.HuffmanOnly - 1, gzip.BestCompression + 1} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("level %d: no panic", level)
				}
			}()

			Compress(CompressOptions{Level: level})
		}()
	}
}
