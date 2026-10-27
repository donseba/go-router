package middleware

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"time"
)

// LastModified is middleware that answers If-Modified-Since with 304 Not
// Modified, without the body, when the handler's response has status 200
// and a Last-Modified that is not newer than the date the client names,
// compared to the second. It follows RFC 9110 (13.1.3 and 13.2.2): only GET
// and HEAD requests are answered, and a request with If-None-Match is
// passed on as it is, since an entity tag decides before a date does (see
// ETag). An If-Modified-Since that is not a valid HTTP date is ignored.
//
//	r.Use(middleware.Compress())
//	r.Use(middleware.ETag())
//	r.Use(middleware.LastModified)
//
// The handler still renders the response, which is then dropped. Put
// LastModified inside ETag, so a request answered by its date costs no hash
// (that 304 carries no ETag), and inside an output cache (NewCache), which
// answers both conditions from its entries itself and passes neither on to
// the handler.
//
// A date can only tell that nothing changed when every change makes it
// newer. A response whose Last-Modified can go back, such as a feed whose
// newest entry is taken off the site, would be answered 304 to a client
// that holds the version before the change: give it no Last-Modified, or a
// date that includes such changes.
func LastModified(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conditions := requestConditions(r)
		if conditions.ifModifiedSince.IsZero() {
			next.ServeHTTP(w, r)

			return
		}

		writer := &lastModifiedWriter{ResponseWriter: w, conditions: conditions}
		next.ServeHTTP(writer, r)
	})
}

// conditions are the conditional headers of a GET or HEAD request that a
// response is answered 304 Not Modified for (RFC 9110, 13.2.2):
// ifNoneMatch, or else ifModifiedSince, zero when the request has none, a
// date that is not valid, or If-None-Match.
type conditions struct {
	ifNoneMatch     string
	ifModifiedSince time.Time
}

// requestConditions returns the conditions of a request, none for other
// methods than GET and HEAD.
func requestConditions(r *http.Request) conditions {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return conditions{}
	}

	if match := r.Header.Get("If-None-Match"); match != "" {
		return conditions{ifNoneMatch: match}
	}

	since := r.Header.Get("If-Modified-Since")
	if since == "" {
		return conditions{}
	}

	date, err := http.ParseTime(since)
	if err != nil {
		return conditions{}
	}

	return conditions{ifModifiedSince: date}
}

// present reports whether there is a condition to answer.
func (c conditions) present() bool {
	return c.ifNoneMatch != "" || !c.ifModifiedSince.IsZero()
}

// notModified reports whether a response with status 200 and the given
// ETag and modification time (zero for none) is answered 304: by the
// ETag when the request has If-None-Match, otherwise by the date.
func (c conditions) notModified(etag string, modified time.Time) bool {
	if c.ifNoneMatch != "" {
		return etag != "" && ETagMatches(c.ifNoneMatch, etag)
	}

	if modified.IsZero() {
		return false
	}

	return c.notModifiedSince(modified)
}

// notModifiedSince reports whether modified, to the second, is not after
// If-Modified-Since.
func (c conditions) notModifiedSince(modified time.Time) bool {
	if c.ifModifiedSince.IsZero() {
		return false
	}

	return !modified.Truncate(time.Second).After(c.ifModifiedSince)
}

// lastModifiedTime returns the time of a Last-Modified header, zero when
// it has none or it is not a valid HTTP date.
func lastModifiedTime(header http.Header) time.Time {
	value := header.Get("Last-Modified")
	if value == "" {
		return time.Time{}
	}

	modified, err := http.ParseTime(value)
	if err != nil {
		return time.Time{}
	}

	return modified
}

// notModifiedHeaders removes the headers that describe a body from the
// headers of a 304 Not Modified response.
func notModifiedHeaders(header http.Header) {
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Encoding"} {
		header.Del(name)
	}
}

// lastModifiedWriter answers a request with If-Modified-Since 304 Not
// Modified when the response turns out not to be newer, and then drops its
// body.
type lastModifiedWriter struct {
	http.ResponseWriter
	conditions  conditions
	wroteHeader bool
	// notModified is set when the client is answered 304: the body is
	// dropped.
	notModified bool
}

func (c *lastModifiedWriter) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}

	if informational(status) {
		c.ResponseWriter.WriteHeader(status)

		return
	}

	c.wroteHeader = true
	header := c.Header()

	if status == http.StatusOK && c.conditions.notModified("", lastModifiedTime(header)) {
		notModifiedHeaders(header)
		c.notModified = true
		c.ResponseWriter.WriteHeader(http.StatusNotModified)

		return
	}

	c.ResponseWriter.WriteHeader(status)
}

func (c *lastModifiedWriter) Write(data []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}

	if c.notModified {
		return len(data), nil
	}

	return c.ResponseWriter.Write(data)
}

// WriteString is Write for a string, such as a rendered page, without
// first copying it into a byte slice.
func (c *lastModifiedWriter) WriteString(s string) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}

	if c.notModified {
		return len(s), nil
	}

	return io.WriteString(c.ResponseWriter, s)
}

func (c *lastModifiedWriter) Flush() {
	_ = c.FlushError()
}

// FlushError sends the status, and what was written so far unless the
// client is answered 304, and allows http.ResponseController to flush
// through this writer.
func (c *lastModifiedWriter) FlushError() error {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}

	if c.notModified {
		return nil
	}

	return http.NewResponseController(c.ResponseWriter).Flush()
}

// Hijack hands the connection over.
func (c *lastModifiedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buf, err := http.NewResponseController(c.ResponseWriter).Hijack()
	if err == nil {
		c.wroteHeader = true
	}

	return conn, buf, err
}

// Unwrap lets http.ResponseController and router.NoIntercept reach the
// original writer.
func (c *lastModifiedWriter) Unwrap() http.ResponseWriter {
	return c.ResponseWriter
}
