package middleware

import (
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

// DefaultCompressMinSize is the smallest body Compress compresses by
// default: below it, gzip's header and the work cost more than they save.
const DefaultCompressMinSize = 1024

// CompressOptions configures Compress.
type CompressOptions struct {
	// Enabled, when set, decides per request whether responses may be
	// compressed, for example from a site setting. Default always.
	Enabled func(*http.Request) bool

	// MinSize is the smallest body that is compressed. Default
	// DefaultCompressMinSize; a negative value compresses every body.
	MinSize int

	// Level is the gzip level, such as gzip.BestSpeed. Default
	// gzip.DefaultCompression.
	Level int

	// Types, when set, decides by Content-Type which responses are worth
	// compressing. Default CompressibleType.
	Types func(contentType string) bool
}

// Compress returns middleware that gzips responses for clients that accept
// it (Accept-Encoding with gzip or *, at a quality above zero). Only
// responses of a compressible type (CompressibleType: text, JSON, XML, SVG
// and the like, not images, video, archives, WOFF fonts or server-sent
// events) of at least MinSize bytes are compressed. HEAD, Range and Upgrade
// requests, and responses that are encoded already, partial (206 or with a
// Content-Range), 204 or 304, or flushed before MinSize bytes, go out as
// they are. Every response that could be compressed says Vary:
// Accept-Encoding, so caches keep both versions apart, and a strong ETag of
// a compressed response becomes weak, since the compressed bytes differ.
//
//	r.Use(middleware.Compress())
//	r.Use(middleware.Compress(middleware.CompressOptions{Enabled: settings.Compress}))
//
// The gzip writers are reused, and shared by every Compress of the process
// with the same Level: per level, up to min(GOMAXPROCS, 16) idle writers
// are kept, of about 0.8 MB each at gzip.BestSpeed and 1.05 to 1.2 MB at
// the other levels (0.3 MB at gzip.HuffmanOnly), with a sync.Pool for
// bursts beyond them.
//
// It panics on an invalid Level.
func Compress(options ...CompressOptions) func(http.Handler) http.Handler {
	opts := firstOption(options)

	if opts.MinSize == 0 {
		opts.MinSize = DefaultCompressMinSize
	}

	if opts.Level == 0 {
		opts.Level = gzip.DefaultCompression
	}

	if opts.Types == nil {
		opts.Types = CompressibleType
	}

	// Checked without making a writer, which costs a megabyte.
	if opts.Level < gzip.HuffmanOnly || opts.Level > gzip.BestCompression {
		panic("middleware: Compress: gzip: invalid compression level: " + strconv.Itoa(opts.Level))
	}

	return compress(opts, sharedGzipWriters(opts.Level))
}

// compress is Compress with its writers.
func compress(opts CompressOptions, writers *gzipWriters) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead || r.Header.Get("Upgrade") != "" || r.Header.Get("Range") != "" {
				next.ServeHTTP(w, r)

				return
			}

			if opts.Enabled != nil && !opts.Enabled(r) {
				next.ServeHTTP(w, r)

				return
			}

			writer := &compressWriter{
				ResponseWriter: w,
				accepts:        AcceptsGzip(r.Header.Get("Accept-Encoding")),
				options:        &opts,
				writers:        writers,
			}

			// A panic leaves the response as it is, for Recover to answer.
			completed := false

			defer func() {
				if completed {
					writer.close()
				} else {
					writer.abandon()
				}
			}()

			next.ServeHTTP(writer, r)

			completed = true
		})
	}
}

// maxFreeWriters bounds how many idle gzip writers Compress keeps where the
// garbage collector leaves them alone; see gzipWriters.
const maxFreeWriters = 16

// gzipWriters reuses the gzip writers of one compression level: first those
// on a free list, then those in a sync.Pool.
//
// A sync.Pool alone loses its writers: it drops what it holds over two
// garbage collections, and a writer put back on one processor is not found
// by a request on another before that. When pages allocate much, the
// collector runs every few requests, and from one request in ten to one in
// fifty made a new writer, of some 800 KB. The free list holds as many
// writers as requests can compress at once, one per processor (at most
// maxFreeWriters), and keeps their memory while they are idle; the
// sync.Pool takes the writers of bursts beyond it.
type gzipWriters struct {
	level int
	free  chan *gzip.Writer
	spare sync.Pool
	// made counts the writers made, for tests.
	made atomic.Int64
}

// writersByLevel are the gzip writers every Compress of the process shares,
// one set per compression level, from gzip.HuffmanOnly to
// gzip.BestCompression, made when a Compress first uses the level: the idle
// writers they keep are bounded per process, not per Compress.
var writersByLevel [gzip.BestCompression - gzip.HuffmanOnly + 1]struct {
	once    sync.Once
	writers *gzipWriters
}

// sharedGzipWriters returns the writers of a valid compression level.
func sharedGzipWriters(level int) *gzipWriters {
	shared := &writersByLevel[level-gzip.HuffmanOnly]

	shared.once.Do(func() {
		shared.writers = newGzipWriters(level)
	})

	return shared.writers
}

func newGzipWriters(level int) *gzipWriters {
	return &gzipWriters{
		level: level,
		free:  make(chan *gzip.Writer, min(runtime.GOMAXPROCS(0), maxFreeWriters)),
	}
}

// get returns an idle writer, or a new one when none is left.
func (p *gzipWriters) get() *gzip.Writer {
	select {
	case writer := <-p.free:
		return writer
	default:
	}

	if writer, ok := p.spare.Get().(*gzip.Writer); ok {
		return writer
	}

	p.made.Add(1)
	writer, _ := gzip.NewWriterLevel(nil, p.level)

	return writer
}

// put takes a writer back: on the free list while it has room, otherwise in
// the sync.Pool.
func (p *gzipWriters) put(writer *gzip.Writer) {
	writer.Reset(io.Discard)

	select {
	case p.free <- writer:
	default:
		p.spare.Put(writer)
	}
}

// AcceptsGzip reports whether an Accept-Encoding header allows gzip: named
// or as *, with a quality above zero. An explicit gzip decides over *.
func AcceptsGzip(header string) bool {
	accepted := false

	for header != "" {
		var part string
		part, header, _ = strings.Cut(header, ",")

		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		name = strings.TrimSpace(name)

		gzip := strings.EqualFold(name, "gzip")
		if !gzip && name != "*" {
			continue
		}

		quality := 1.0

		if value, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			parsed, err := strconv.ParseFloat(value, 64)
			if err == nil {
				quality = parsed
			}
		}

		if gzip {
			return quality > 0
		}

		accepted = quality > 0
	}

	return accepted
}

// CompressibleType reports whether a response of the content type is worth
// compressing: text (except server-sent events), JSON, JavaScript, XML,
// WebAssembly, SVG, icons, BMP and uncompressed fonts.
func CompressibleType(contentType string) bool {
	media, ok := mediaType(contentType)
	if !ok {
		return false
	}

	switch {
	case media == "text/event-stream":
		return false
	case strings.HasPrefix(media, "text/"):
		return true
	case strings.HasSuffix(media, "+json"), strings.HasSuffix(media, "+xml"):
		return true
	}

	switch media {
	case "application/json", "application/javascript", "application/ecmascript", "application/xml",
		"application/wasm", "image/svg+xml", "image/x-icon", "image/vnd.microsoft.icon", "image/bmp",
		"font/ttf", "font/otf", "application/vnd.ms-fontobject":
		return true
	default:
		return false
	}
}

// mediaType returns the media type of a Content-Type header in lower case,
// and whether the header is valid, as mime.ParseMediaType does. The usual
// forms, such as "text/html" and "text/html; charset=utf-8", are read
// without allocating; others are left to mime.ParseMediaType.
func mediaType(contentType string) (string, bool) {
	base, params, hasParams := strings.Cut(contentType, ";")
	media := strings.TrimSpace(base)

	if !simpleMediaType(media) || hasParams && !simpleCharset(params) {
		media, _, err := mime.ParseMediaType(contentType)

		return media, err == nil
	}

	// simpleMediaType lets ASCII through only: the upper case letters
	// unicode.IsUpper finds are A to Z.
	if strings.ContainsFunc(media, unicode.IsUpper) {
		media = strings.ToLower(media)
	}

	return media, true
}

// simpleMediaType reports whether media is a type and subtype of token
// characters, such as text/html.
func simpleMediaType(media string) bool {
	kind, subtype, ok := strings.Cut(media, "/")

	return ok && token(kind) && token(subtype)
}

// simpleCharset reports whether the parameters of a Content-Type are a
// charset alone, such as " charset=utf-8".
func simpleCharset(params string) bool {
	name, value, ok := strings.Cut(strings.TrimSpace(params), "=")

	return ok && strings.EqualFold(name, "charset") && token(value)
}

// token reports whether s is a non-empty token of RFC 2045: printable ASCII
// without spaces and the characters ()<>@,;:\"/[]?=.
func token(s string) bool {
	if s == "" {
		return false
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c >= 0x7f || strings.IndexByte(`()<>@,;:\"/[]?=`, c) >= 0 {
			return false
		}
	}

	return true
}

// compressWriter holds the start of a response until it knows whether to
// compress it: when the body reaches MinSize bytes, or the handler flushes
// or ends.
type compressWriter struct {
	http.ResponseWriter
	accepts bool
	options *CompressOptions
	writers *gzipWriters
	status  int
	// checkedType is the last Content-Type given to options.Types, and
	// compressible its answer.
	checkedType  string
	checked      bool
	compressible bool
	// buffer is the body until the decision; decided is set once it is made,
	// gzip is the compressor when the decision was to compress.
	buffer  []byte
	decided bool
	gzip    *gzip.Writer
}

func (c *compressWriter) WriteHeader(status int) {
	if c.decided || informational(status) || status == http.StatusSwitchingProtocols {
		c.ResponseWriter.WriteHeader(status)

		return
	}

	if c.status == 0 {
		c.status = status
	}
}

func (c *compressWriter) Write(data []byte) (int, error) {
	if c.decided {
		if c.gzip != nil {
			return c.gzip.Write(data)
		}

		return c.ResponseWriter.Write(data)
	}

	// The decision needs the type.
	detectContentType(c.Header(), data)

	if !c.accepts || !c.eligible() {
		c.decide(false)

		return c.ResponseWriter.Write(data)
	}

	if len(c.buffer)+len(data) < c.options.MinSize {
		c.buffer = append(c.buffer, data...)

		return len(data), nil
	}

	c.decide(true)

	// A body written at once is compressed as it is, without a copy.
	if len(c.buffer) > 0 {
		if _, err := c.gzip.Write(c.buffer); err != nil {
			return 0, err
		}

		c.buffer = nil
	}

	if _, err := c.gzip.Write(data); err != nil {
		return 0, err
	}

	return len(data), nil
}

// typeCompressible reports whether options.Types finds the response's
// Content-Type worth compressing, asking once per value.
func (c *compressWriter) typeCompressible() bool {
	contentType := c.Header().Get("Content-Type")

	if !c.checked || contentType != c.checkedType {
		c.checkedType = contentType
		c.checked = true
		c.compressible = c.options.Types(contentType)
	}

	return c.compressible
}

// eligible reports whether the response may be compressed, as far as its
// status and headers tell.
func (c *compressWriter) eligible() bool {
	header := c.Header()

	switch {
	case c.status == http.StatusNoContent, c.status == http.StatusNotModified, c.status == http.StatusPartialContent:
		return false
	case header.Get("Content-Encoding") != "", header.Get("Content-Range") != "":
		return false
	}

	if value := header.Get("Content-Length"); value != "" {
		if length, err := strconv.Atoi(value); err == nil && length < c.options.MinSize {
			return false
		}
	}

	return c.typeCompressible()
}

// decide writes the header, compressing from now on when compress is set.
func (c *compressWriter) decide(compress bool) {
	c.decided = true
	header := c.Header()

	if c.typeCompressible() && header.Get("Content-Encoding") == "" && !varies(header) {
		header.Add("Vary", "Accept-Encoding")
	}

	if compress {
		header.Del("Content-Length")
		header.Set("Content-Encoding", "gzip")

		// The compressed body differs byte for byte: a strong ETag of the
		// plain one would be wrong for it.
		if etag := header.Get("ETag"); strings.HasPrefix(etag, `"`) {
			header.Set("ETag", "W/"+etag)
		}

		c.gzip = c.writers.get()
		c.gzip.Reset(c.ResponseWriter)
	}

	if c.status != 0 {
		c.ResponseWriter.WriteHeader(c.status)
	}
}

// varies reports whether the Vary header names Accept-Encoding already.
func varies(header http.Header) bool {
	for _, value := range header.Values("Vary") {
		for value != "" {
			var name string
			name, value, _ = strings.Cut(value, ",")

			if strings.EqualFold(strings.TrimSpace(name), "Accept-Encoding") {
				return true
			}
		}
	}

	return false
}

func (c *compressWriter) Flush() {
	_ = c.FlushError()
}

// FlushError sends what was written so far, and allows
// http.ResponseController to flush through this writer. A response flushed
// before the decision goes out uncompressed, as it is probably streamed.
func (c *compressWriter) FlushError() error {
	if !c.decided {
		if err := c.flushBuffer(); err != nil {
			return err
		}
	}

	if c.gzip != nil {
		if err := c.gzip.Flush(); err != nil {
			return err
		}
	}

	return http.NewResponseController(c.ResponseWriter).Flush()
}

// Hijack hands the connection over, unless the response is being
// compressed already.
func (c *compressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if c.gzip != nil {
		return nil, nil, errors.New("middleware: Compress: cannot hijack a compressed response")
	}

	conn, buf, err := http.NewResponseController(c.ResponseWriter).Hijack()
	if err == nil {
		c.decided = true
		c.buffer = nil
	}

	return conn, buf, err
}

// flushBuffer decides against compression and writes the held body.
func (c *compressWriter) flushBuffer() error {
	c.decide(false)

	if len(c.buffer) == 0 {
		return nil
	}

	_, err := c.ResponseWriter.Write(c.buffer)
	c.buffer = nil

	return err
}

// close ends the response: a body that stayed small goes out as it is, a
// compressed one gets its gzip trailer.
func (c *compressWriter) close() {
	if !c.decided {
		_ = c.flushBuffer()
	}

	if c.gzip != nil {
		_ = c.gzip.Close()
		c.release()
	}
}

// abandon drops what is held after a panic: nothing more is written.
func (c *compressWriter) abandon() {
	c.buffer = nil

	if c.gzip != nil {
		c.release()
	}
}

func (c *compressWriter) release() {
	c.writers.put(c.gzip)
	c.gzip = nil
}

// Unwrap lets http.ResponseController and router.NoIntercept reach the
// original writer.
func (c *compressWriter) Unwrap() http.ResponseWriter {
	return c.ResponseWriter
}
