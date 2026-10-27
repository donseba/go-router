package middleware

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// sniffLen is how much of a body http.DetectContentType reads.
const sniffLen = 512

// maxPooledBody is the largest buffer ETag keeps for later responses: a
// buffer that grew for a bigger one goes to the garbage collector, so one
// large response does not keep its memory for good.
const maxPooledBody = 256 << 10

// bodies are the buffers ETag holds responses in, reused across requests.
var bodies = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

// getBody returns an empty buffer from the pool.
func getBody() *bytes.Buffer {
	return bodies.Get().(*bytes.Buffer)
}

// putBody empties a buffer and gives it back to the pool, unless it grew
// beyond maxPooledBody.
func putBody(body *bytes.Buffer) {
	if body.Cap() > maxPooledBody {
		return
	}

	body.Reset()
	bodies.Put(body)
}

// DefaultETagTypes are the media types of the responses ETag gives an ETag
// by default: text formats that are the same for every visitor until their
// content changes, such as pages, feeds, JSON, scripts and styles. Changing
// the slice changes the default of ETag middleware made afterwards.
var DefaultETagTypes = []string{
	"text/html",
	"text/plain",
	"text/css",
	"text/javascript",
	"text/xml",
	"application/javascript",
	"application/json",
	"application/xml",
	"application/atom+xml",
	"application/rss+xml",
}

// ETagOptions configures ETag.
type ETagOptions struct {
	// Types are the media types of the responses that get an ETag, on the
	// other conditions of CacheableTypes. An entry is a media type such as
	// "application/json", all subtypes of a type such as "text/*", or a
	// structured syntax suffix such as "+xml", which takes
	// "application/atom+xml" and "image/svg+xml". Default DefaultETagTypes.
	// Types is not used when Cacheable is set.
	Types []string

	// Cacheable decides by its status and headers, once the handler starts
	// the body, whether a response gets an ETag: it is then held until it
	// is complete. Default CacheableTypes of Types.
	Cacheable func(status int, header http.Header) bool

	// CacheControl, when set, returns the Cache-Control header for
	// responses that get an ETag and have no Cache-Control of their own,
	// such as "no-cache", or "public, max-age=60" for a request without a
	// query.
	CacheControl func(*http.Request) string
}

// ETag returns middleware that gives complete responses to GET and HEAD
// requests a weak ETag, a hash of their body, and answers requests that name
// it in If-None-Match with 304 Not Modified, without the body. It suits
// pages rendered per request, which are the same for every visitor until
// their content changes: the client still asks every time, but an unchanged
// page costs no transfer.
//
//	r.Use(middleware.ETag(middleware.ETagOptions{
//		CacheControl: func(*http.Request) string { return "no-cache" },
//	}))
//
// Responses that are not cacheable (see CacheableTypes and Types) go
// through as they are written, and so do responses that are flushed: they
// are streamed. The ETag is weak, so it stays valid when Compress, outside
// ETag, compresses the response. ETag answers If-None-Match only; put
// LastModified inside it for If-Modified-Since.
//
// ETag panics when an entry of Types is not a media type, a type with "/*"
// or a suffix starting with "+".
func ETag(options ...ETagOptions) func(http.Handler) http.Handler {
	opts := firstOption(options)

	if opts.Cacheable == nil {
		types := opts.Types
		if len(types) == 0 {
			types = DefaultETagTypes
		}

		opts.Cacheable = CacheableTypes(types...)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				next.ServeHTTP(w, r)

				return
			}

			writer := &etagWriter{ResponseWriter: w, cacheable: opts.Cacheable}
			defer writer.release()

			next.ServeHTTP(writer, r)

			writer.finish(r, opts.CacheControl)
		})
	}
}

// CacheableTypes returns a Cacheable function for ETagOptions that reports
// whether a response is complete and every visitor may see it alike:
// status 200, without an ETag, Content-Encoding or Set-Cookie of its own,
// not private or no-store, and a Content-Type of one of types.
// An entry of types is a media type such as "application/json", all
// subtypes of a type such as "text/*", or a structured syntax suffix such
// as "+xml"; case does not matter.
//
// CacheableTypes panics when an entry is none of these.
func CacheableTypes(types ...string) func(status int, header http.Header) bool {
	matcher := newMediaTypes(types)

	return func(status int, header http.Header) bool {
		if !sharedResponse(status, header) {
			return false
		}

		media, ok := mediaType(header.Get("Content-Type"))

		return ok && matcher.match(media)
	}
}

// sharedResponse reports whether a response, whatever its type, is one
// every visitor may get alike: status 200 (or not written yet), without an
// ETag, Content-Encoding or Set-Cookie of its own, and not private or
// no-store.
func sharedResponse(status int, header http.Header) bool {
	if status != 0 && status != http.StatusOK {
		return false
	}

	if header.Get("ETag") != "" || header.Get("Set-Cookie") != "" || header.Get("Content-Encoding") != "" {
		return false
	}

	return !privateOrNoStore(header)
}

// mediaTypes matches media types against the entries of CacheableTypes:
// whole types, types of which every subtype matches (kinds, such as
// "text/"), and structured syntax suffixes (such as "+xml").
type mediaTypes struct {
	exact    []string
	kinds    []string
	suffixes []string
}

// newMediaTypes sorts the entries of CacheableTypes into a mediaTypes, in
// lower case. It panics on an entry that is none of the three forms.
func newMediaTypes(types []string) mediaTypes {
	var matcher mediaTypes

	for _, entry := range types {
		value := strings.ToLower(strings.TrimSpace(entry))
		kind := strings.TrimSuffix(value, "/*")

		switch {
		case strings.Contains(kind, "*"):
			panic(invalidMediaType(entry))
		case strings.HasPrefix(value, "+") && token(value[1:]):
			matcher.suffixes = append(matcher.suffixes, value)
		case kind != value && token(kind):
			matcher.kinds = append(matcher.kinds, kind+"/")
		case simpleMediaType(value):
			matcher.exact = append(matcher.exact, value)
		default:
			panic(invalidMediaType(entry))
		}
	}

	return matcher
}

// invalidMediaType is the panic message of newMediaTypes for an entry.
func invalidMediaType(entry string) string {
	return fmt.Sprintf("middleware: invalid media type %q: want type/subtype, type/* or +suffix", entry)
}

// match reports whether media, in lower case and without parameters,
// matches one of the entries.
func (m mediaTypes) match(media string) bool {
	if slices.Contains(m.exact, media) {
		return true
	}

	for _, kind := range m.kinds {
		if strings.HasPrefix(media, kind) {
			return true
		}
	}

	for _, suffix := range m.suffixes {
		if strings.HasSuffix(media, suffix) {
			return true
		}
	}

	return false
}

// privateOrNoStore reports whether a response's Cache-Control keeps it
// from shared caches: private or no-store.
func privateOrNoStore(header http.Header) bool {
	for _, value := range header.Values("Cache-Control") {
		for _, directive := range strings.Split(value, ",") {
			name, _, _ := strings.Cut(strings.TrimSpace(directive), "=")
			if strings.EqualFold(name, "no-store") || strings.EqualFold(name, "private") {
				return true
			}
		}
	}

	return false
}

// WeakETag returns a weak validator of a body: weak, because the body may
// go out compressed or not, which changes its bytes but not what it shows.
func WeakETag(body []byte) string {
	sum := sha256.Sum256(body)

	// W/" + 18 bytes in unpadded base64 (24 characters) + ", built in place
	// so the validator costs one allocation.
	var tag [3 + 24 + 1]byte

	copy(tag[:], `W/"`)
	base64.RawURLEncoding.Encode(tag[3:27], sum[:18])
	tag[27] = '"'

	return string(tag[:])
}

// ETagMatches reports whether an If-None-Match header names etag, comparing
// weakly (RFC 9110, 13.1.2): W/"x" and "x" match, and so does *.
func ETagMatches(header string, etag string) bool {
	if header == "" {
		return false
	}

	want := strings.TrimPrefix(etag, "W/")

	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == want {
			return true
		}
	}

	return false
}

// etagWriter holds a response that can be cached until it is complete, so
// its ETag can be computed; others go through as they are written.
type etagWriter struct {
	http.ResponseWriter
	cacheable func(int, http.Header) bool
	status    int
	// decided is set once the response is known to be cacheable
	// (buffering) or not.
	decided   bool
	buffering bool
	// body holds the response while buffering: a buffer of the pool, given
	// back by release.
	body *bytes.Buffer
}

func (c *etagWriter) WriteHeader(status int) {
	if c.decided {
		if !c.buffering {
			c.ResponseWriter.WriteHeader(status)
		}

		return
	}

	if status < http.StatusOK {
		c.ResponseWriter.WriteHeader(status)

		return
	}

	if c.status == 0 {
		c.status = status
	}
}

func (c *etagWriter) Write(data []byte) (int, error) {
	if !c.decided {
		detectContentType(c.Header(), data)
		c.decide()
	}

	if c.buffering {
		return c.body.Write(data)
	}

	return c.ResponseWriter.Write(data)
}

// WriteString is Write for a string, such as a rendered page, which it
// holds without first copying it into a byte slice.
func (c *etagWriter) WriteString(s string) (int, error) {
	if !c.decided {
		detectContentTypeString(c.Header(), s)
		c.decide()
	}

	if c.buffering {
		return c.body.WriteString(s)
	}

	return io.WriteString(c.ResponseWriter, s)
}

// finish sends the response once the handler is done. A response that went
// through as it was written gets the status the handler set without writing
// a body. A held response gets its ETag, and the Cache-Control of
// cacheControl when it has none, and is answered 304 Not Modified when the
// request's If-None-Match names the ETag.
func (c *etagWriter) finish(r *http.Request, cacheControl func(*http.Request) string) {
	if !c.buffering {
		if !c.decided && c.status != 0 {
			c.ResponseWriter.WriteHeader(c.status)
		}

		return
	}

	header := c.Header()
	etag := WeakETag(c.body.Bytes())
	header.Set("ETag", etag)

	if cacheControl != nil && header.Get("Cache-Control") == "" {
		if value := cacheControl(r); value != "" {
			header.Set("Cache-Control", value)
		}
	}

	if ETagMatches(r.Header.Get("If-None-Match"), etag) {
		notModifiedHeaders(header)
		c.ResponseWriter.WriteHeader(http.StatusNotModified)

		return
	}

	c.ResponseWriter.WriteHeader(http.StatusOK)
	_, _ = c.ResponseWriter.Write(c.body.Bytes())
}

// release gives the buffer back to the pool once the response is out.
// Nothing is buffered after it.
func (c *etagWriter) release() {
	c.buffering = false

	if c.body != nil {
		putBody(c.body)
		c.body = nil
	}
}

// detectContentType sets the Content-Type of a response from the start of
// its body, as net/http would, unless the handler set one, for writers that
// need the type before the body goes out.
func detectContentType(header http.Header, data []byte) {
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", http.DetectContentType(data))
	}
}

// detectContentTypeString is detectContentType for a body written as a
// string. Only its first bytes tell the content type, and they are only
// copied when the handler set none.
func detectContentTypeString(header http.Header, s string) {
	if header.Get("Content-Type") == "" {
		detectContentType(header, []byte(s[:min(len(s), sniffLen)]))
	}
}

// decide looks at the status and headers once the body starts.
func (c *etagWriter) decide() {
	c.decided = true
	header := c.Header()

	c.buffering = c.cacheable(c.status, header)
	if c.buffering {
		c.body = getBody()

		return
	}

	if c.status != 0 {
		c.ResponseWriter.WriteHeader(c.status)
	}
}

// stream gives up the ETag: the response goes out as it comes.
func (c *etagWriter) stream() error {
	if !c.decided {
		c.decided = true

		if c.status != 0 {
			c.ResponseWriter.WriteHeader(c.status)
		}

		return nil
	}

	if !c.buffering {
		return nil
	}

	c.buffering = false

	if c.status != 0 {
		c.ResponseWriter.WriteHeader(c.status)
	}

	_, err := c.ResponseWriter.Write(c.body.Bytes())
	c.release()

	return err
}

func (c *etagWriter) Flush() {
	_ = c.FlushError()
}

// FlushError gives up the ETag and allows http.ResponseController to flush
// through this writer.
func (c *etagWriter) FlushError() error {
	if err := c.stream(); err != nil {
		return err
	}

	return http.NewResponseController(c.ResponseWriter).Flush()
}

// Hijack gives up the ETag and hands the connection over.
func (c *etagWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buf, err := http.NewResponseController(c.ResponseWriter).Hijack()
	if err == nil {
		c.decided = true
		c.release()
	}

	return conn, buf, err
}

// Unwrap lets http.ResponseController and router.NoIntercept reach the
// original writer.
func (c *etagWriter) Unwrap() http.ResponseWriter {
	return c.ResponseWriter
}
