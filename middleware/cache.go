package middleware

import (
	"bufio"
	"bytes"
	"container/list"
	"io"
	"maps"
	"net"
	"net/http"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultCacheTTL is how long the output cache (NewCache) keeps a
	// response by default.
	DefaultCacheTTL = time.Minute

	// DefaultCacheMaxBytes is how much memory the output cache uses for
	// responses by default: 32 MiB.
	DefaultCacheMaxBytes = 32 << 20

	// DefaultCacheMaxEntryBytes is the largest response body the output
	// cache stores by default: 1 MiB.
	DefaultCacheMaxEntryBytes = 1 << 20

	// DefaultCacheMaxVarySets is how many Vary lists the output cache keeps
	// entries for under one key by default.
	DefaultCacheMaxVarySets = 4
)

// cacheEntryOverhead is what an entry counts for beyond its body, key and
// headers: the entry itself and its places in the maps and the LRU list.
const cacheEntryOverhead = 256

// cacheFlightWaits is how many renders of others a request waits for before
// it renders its own response: see ResponseCache.serve.
const cacheFlightWaits = 3

// CacheOptions configures NewCache.
type CacheOptions struct {
	// Key returns the key a request's response is cached under, and false
	// for a request that is not cached. GET and HEAD requests share keys.
	// Default CacheKey: the scheme, host, path and query.
	Key func(*http.Request) (string, bool)

	// Version returns the version of the content a request would see now,
	// such as a counter the application raises on every change. An entry
	// stored at another version is stale and rendered again. Default: the
	// version is always 0, and entries only expire.
	Version func(*http.Request) uint64

	// Skip, when set, keeps the requests it reports true for away from the
	// cache: they are neither served from it nor stored, such as requests
	// of signed-in visitors.
	Skip func(*http.Request) bool

	// TTL is how long an entry is used. Default DefaultCacheTTL; a negative
	// TTL keeps entries until their version changes or they are evicted.
	TTL time.Duration

	// MaxBytes bounds the memory of all entries: the least recently used
	// are evicted beyond it. Default DefaultCacheMaxBytes.
	MaxBytes int64

	// MaxEntryBytes is the largest response body stored: a larger one goes
	// out as it is written, uncached. Default DefaultCacheMaxEntryBytes.
	MaxEntryBytes int64

	// MaxEntries, when above zero, bounds the number of entries too.
	MaxEntries int

	// MaxVarySets is how many different Vary lists the responses of one
	// key may name, each with entries of its own, such as a whole page and
	// an HTMX navigation of the same address that vary on other request
	// headers. Beyond it the entries of the list stored least recently are
	// evicted. Default DefaultCacheMaxVarySets.
	MaxVarySets int

	// VaryValue, when set, returns the value of a request header a
	// response varies on (Vary) as the variant of an entry is told by,
	// such as only the cookies that change a page out of the Cookie
	// header, and false for the default: the header's values, and of
	// Accept-Encoding whether gzip is accepted.
	VaryValue func(r *http.Request, name string) (string, bool)

	// NotFound stores responses with status 404 Not Found as well as those
	// with 200 OK.
	NotFound bool

	// StatusHeader, when set, names a response header, such as X-Cache,
	// that says how the cache answered: hit, miss or skip. For development.
	StatusHeader string
}

// CacheStats are the counts of a ResponseCache.
type CacheStats struct {
	// Hits are requests answered from the cache, Misses those it rendered
	// (and stored, when the response could be), Skips the GET and HEAD
	// requests Skip or Key kept from it.
	Hits   uint64
	Misses uint64
	Skips  uint64

	// Evictions are entries removed to stay within MaxBytes, MaxEntries or
	// MaxVarySets.
	Evictions uint64

	// Entries and Bytes are what the cache holds now.
	Entries int
	Bytes   int64
}

// ResponseCache keeps complete responses in memory and serves them again
// without calling the handler: see NewCache. Its methods are safe for
// concurrent use.
type ResponseCache struct {
	options CacheOptions
	now     func() time.Time

	hits      atomic.Uint64
	misses    atomic.Uint64
	skips     atomic.Uint64
	evictions atomic.Uint64

	mu sync.Mutex
	// keys holds the entries by key, lru the same entries, the most
	// recently used first, bytes their size.
	keys  map[string]*cacheKey
	lru   *list.List
	bytes int64
	// flights are the renders in progress, by key and variant.
	flights map[string]chan struct{}
}

// cacheKey holds the entries of one key, by the request headers their
// responses vary on (Vary): a set for every Vary list the responses named,
// the one stored most recently first, at most MaxVarySets.
type cacheKey struct {
	sets []*cacheVarySet
	// union are the names of all sets, sorted and each once: their values
	// tell the flights of the requests no set has a fresh entry for apart.
	union []string
}

// cacheVarySet holds the entries of a key whose responses vary on the same
// request headers: one per variant, as the values of those headers tell
// them apart.
type cacheVarySet struct {
	vary     []string
	variants map[string]*cacheEntry
}

// cacheEntry is a stored response, of the set of its key that holds it.
type cacheEntry struct {
	key     string
	set     *cacheVarySet
	variant string
	status  int
	// header holds the headers the response set inside the cache, deleted
	// those it removed; length is the Content-Length of body.
	header  http.Header
	deleted []string
	length  []string
	body    []byte
	// etag and modified are the ETag and Last-Modified the response set
	// inside the cache, which conditional requests are answered by; zero
	// for none.
	etag     string
	modified time.Time
	version  uint64
	expires  time.Time
	size     int64
	element  *list.Element
}

// NewCache returns an output cache, whose Middleware keeps complete
// responses to GET requests in memory and serves them again, to GET and
// HEAD requests, without calling the handler. Stats reports how it does.
//
// A response is stored when its status is 200 OK (or 404 Not Found, with
// NotFound), it is complete and not flushed, its body is at most
// MaxEntryBytes, and it sets no cookie, says neither private nor no-store
// in Cache-Control, and does not say Vary: *. Requests with Authorization,
// Range or Upgrade headers, and those Skip reports or Key refuses, are not
// cached.
//
// Entries are kept by Key and by the values of the request headers the
// response names in Vary, Accept-Encoding by whether gzip is accepted. The
// responses of a key may name up to MaxVarySets different Vary lists, such
// as a whole page and an HTMX navigation: a request is answered by the
// entry of the list stored most recently that has one for it. An entry is
// stale once TTL has passed or Version returns another version than it was
// stored at. Many requests for a missing or stale entry at once render it
// once: the others wait and are served from the entry. The least recently
// used entries are evicted beyond MaxBytes.
//
// Stored are the status, the body and the headers set inside the cache;
// headers set outside it, before it runs, such as a request ID, stay those
// of each request.
//
// Conditional GET and HEAD requests are answered 304 Not Modified by the
// cache itself, from a fresh entry or from the response it renders, which
// is at the current version either way (RFC 9110, 13.2.2): a request with
// If-None-Match when it names the stored ETag, otherwise a request with
// If-Modified-Since when the stored Last-Modified is not newer, to the
// second. The cache renders the whole response to store it, so it passes
// neither header on to the handler. An entry stale by version or TTL
// answers no condition: the request renders the response again.
//
// Put Compress, ETag and LastModified inside the cache, so the compressed
// body and the ETag are stored with the entry, and LastModified answers the
// requests the cache skips:
//
//	cache := middleware.NewCache(middleware.CacheOptions{
//		Version: content.Version,
//		Skip:    signedIn,
//	})
//	r.Use(cache.Middleware)
//	r.Use(middleware.Compress())
//	r.Use(middleware.ETag())
//	r.Use(middleware.LastModified)
func NewCache(options ...CacheOptions) *ResponseCache {
	opts := firstOption(options)

	if opts.Key == nil {
		opts.Key = CacheKey
	}

	if opts.TTL == 0 {
		opts.TTL = DefaultCacheTTL
	}

	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultCacheMaxBytes
	}

	if opts.MaxEntryBytes <= 0 {
		opts.MaxEntryBytes = DefaultCacheMaxEntryBytes
	}

	if opts.MaxVarySets <= 0 {
		opts.MaxVarySets = DefaultCacheMaxVarySets
	}

	return &ResponseCache{
		options: opts,
		now:     time.Now,
		keys:    make(map[string]*cacheKey),
		lru:     list.New(),
		flights: make(map[string]chan struct{}),
	}
}

// CacheKey is the default key of NewCache: the scheme, host (see Scheme and
// Host), path and query a request arrived with, as the client sent them,
// also when a mount stripped a prefix from the request's URL.
func CacheKey(r *http.Request) (string, bool) {
	target := r.RequestURI
	if target == "" {
		target = r.URL.RequestURI()
	}

	return Scheme(r) + "://" + Host(r) + target, true
}

// Middleware serves requests from the cache, and stores the responses of
// the handler when they can be.
func (c *ResponseCache) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.serve(w, r, next)
	})
}

// Stats returns the counts of the cache.
func (c *ResponseCache) Stats() CacheStats {
	c.mu.Lock()
	entries := c.lru.Len()
	bytes := c.bytes
	c.mu.Unlock()

	return CacheStats{
		Hits:      c.hits.Load(),
		Misses:    c.misses.Load(),
		Skips:     c.skips.Load(),
		Evictions: c.evictions.Load(),
		Entries:   entries,
		Bytes:     bytes,
	}
}

func (c *ResponseCache) serve(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		next.ServeHTTP(w, r)

		return
	}

	key, ok := c.cacheable(r)
	if !ok {
		c.skips.Add(1)
		c.mark(w, "skip")
		next.ServeHTTP(w, r)

		return
	}

	var version uint64
	if c.options.Version != nil {
		version = c.options.Version(r)
	}

	// A HEAD request has no body to store: without an entry it is answered
	// as it is.
	if r.Method == http.MethodHead {
		if entry := c.lookup(r, key, version); entry != nil {
			c.serveEntry(w, r, entry)

			return
		}

		c.misses.Add(1)
		c.mark(w, "miss")
		next.ServeHTTP(w, r)

		return
	}

	// A request without an entry joins the flight that renders it, and
	// waits unless it leads it. The response rendered may be of another
	// variant: then the request looks again, and may wait for the flight
	// of its own variant now, as when the key's first response told which
	// headers its responses vary on. When it would wait for the same
	// flight again, the response could not be stored: it renders its own.
	var waited string

	for range cacheFlightWaits {
		entry, flight := c.lookupOrJoin(r, key, version, waited)
		if entry != nil {
			c.serveEntry(w, r, entry)

			return
		}

		if flight.leader {
			c.lead(flight, func() {
				c.render(w, r, next, key, version)
			})

			return
		}

		if flight.done == nil {
			c.render(w, r, next, key, version)

			return
		}

		select {
		case <-flight.done:
		case <-r.Context().Done():
			return
		}

		waited = flight.key
	}

	if entry := c.lookup(r, key, version); entry != nil {
		c.serveEntry(w, r, entry)

		return
	}

	c.render(w, r, next, key, version)
}

// cacheFlight is a render in progress that requests for the same variant
// wait for: key tells it apart, done is closed when it lands, and leader is
// set for the request that renders it.
type cacheFlight struct {
	key    string
	done   chan struct{}
	leader bool
}

// lead runs render, which renders the response of a flight that the
// requests waiting for it are served from, and lands the flight, also when
// the render panics.
func (c *ResponseCache) lead(flight cacheFlight, render func()) {
	defer c.land(flight)

	render()
}

// cacheable returns the key of a request that may be cached.
func (c *ResponseCache) cacheable(r *http.Request) (string, bool) {
	header := r.Header

	if header.Get("Authorization") != "" || header.Get("Range") != "" || header.Get("Upgrade") != "" {
		return "", false
	}

	if c.options.Skip != nil && c.options.Skip(r) {
		return "", false
	}

	return c.options.Key(r)
}

// mark sets the StatusHeader, if any.
func (c *ResponseCache) mark(w http.ResponseWriter, status string) {
	if c.options.StatusHeader != "" {
		w.Header().Set(c.options.StatusHeader, status)
	}
}

// lookup returns the fresh entry for a request, or nil.
func (c *ResponseCache) lookup(r *http.Request, key string, version uint64) *cacheEntry {
	now := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, _ := c.findLocked(r, key, version, now)

	return entry
}

// lookupOrJoin returns the fresh entry for a request, or else joins the
// flight that renders it, at once, so no flight lands in between: as its
// leader when it has none. It joins no flight when that is the flight
// waited, which the request waited for already: the flight's key is set,
// but not its done channel.
func (c *ResponseCache) lookupOrJoin(r *http.Request, key string, version uint64, waited string) (*cacheEntry, cacheFlight) {
	now := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, flight := c.findLocked(r, key, version, now)
	if entry != nil {
		return entry, cacheFlight{}
	}

	if flight == waited {
		return nil, cacheFlight{key: flight}
	}

	if done, ok := c.flights[flight]; ok {
		return nil, cacheFlight{key: flight, done: done}
	}

	done := make(chan struct{})
	c.flights[flight] = done

	return nil, cacheFlight{key: flight, done: done, leader: true}
}

// findLocked returns the fresh entry for a request, or nil and the key of
// the flight that renders it: the key, and the values of the request
// headers the key's responses vary on, of all its sets, once those are
// known.
//
// The entry is that of the set stored most recently that has one for the
// request. When that entry is stale, the request is not served from an
// older set: the entry stays until the render replaces it, so the requests
// that find it meanwhile join the same flight.
func (c *ResponseCache) findLocked(r *http.Request, key string, version uint64, now time.Time) (*cacheEntry, string) {
	stored := c.keys[key]
	if stored == nil {
		return nil, key + "\x00"
	}

	// The variant is built on the stack: a map lookup with string(variant)
	// does not allocate.
	var buffer [256]byte

	for _, set := range stored.sets {
		variant := c.appendVariant(buffer[:0], r, set.vary)

		entry := set.variants[string(variant)]
		if entry == nil {
			continue
		}

		if entry.version != version || (!entry.expires.IsZero() && !now.Before(entry.expires)) {
			break
		}

		c.lru.MoveToFront(entry.element)

		return entry, ""
	}

	return nil, key + "\x00" + string(c.appendVariant(buffer[:0], r, stored.union))
}

// land ends a flight, also when its render panicked, so the requests
// waiting for it go on.
func (c *ResponseCache) land(flight cacheFlight) {
	c.mu.Lock()
	delete(c.flights, flight.key)
	c.mu.Unlock()

	close(flight.done)
}

// serveEntry answers a request from an entry.
func (c *ResponseCache) serveEntry(w http.ResponseWriter, r *http.Request, entry *cacheEntry) {
	c.hits.Add(1)
	c.mark(w, "hit")

	// The stored values are shared, not copied: their slices are full, so
	// a header added later gets a new array.
	header := w.Header()
	maps.Copy(header, entry.header)

	for _, name := range entry.deleted {
		header.Del(name)
	}

	if entry.status == http.StatusOK && requestConditions(r).notModified(entry.etag, entry.modified) {
		notModifiedHeaders(header)
		w.WriteHeader(http.StatusNotModified)

		return
	}

	header["Content-Length"] = entry.length
	w.WriteHeader(entry.status)

	if r.Method != http.MethodHead {
		_, _ = w.Write(entry.body)
	}
}

// render calls the handler, sends its response, and stores it when it can
// be.
func (c *ResponseCache) render(w http.ResponseWriter, r *http.Request, next http.Handler, key string, version uint64) {
	c.misses.Add(1)
	c.mark(w, "miss")

	writer := &cacheWriter{
		ResponseWriter: w,
		cache:          c,
		before:         w.Header().Clone(),
		conditions:     requestConditions(r),
	}
	defer writer.release()

	// The handler renders the whole response, to be stored; the cache
	// answers If-None-Match and If-Modified-Since itself.
	downstream := r
	if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
		downstream = r.Clone(r.Context())
		downstream.Header.Del("If-None-Match")
		downstream.Header.Del("If-Modified-Since")
	}

	next.ServeHTTP(writer, downstream)

	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}

	if writer.recording {
		c.store(r, key, version, writer)
	}
}

// storable reports whether a response may be stored, as far as its status
// and headers tell.
func (c *ResponseCache) storable(status int, header http.Header) bool {
	switch {
	case status == http.StatusOK:
	case status == http.StatusNotFound && c.options.NotFound:
	default:
		return false
	}

	if len(header["Set-Cookie"]) > 0 || header.Get("Content-Range") != "" {
		return false
	}

	return !privateOrNoStore(header)
}

// store keeps the response a writer recorded.
func (c *ResponseCache) store(r *http.Request, key string, version uint64, writer *cacheWriter) {
	body := bytes.Clone(writer.body.Bytes())
	variant := string(c.appendVariant(nil, r, writer.vary))

	entry := &cacheEntry{
		key:      key,
		variant:  variant,
		status:   writer.status,
		header:   writer.stored,
		deleted:  writer.deleted,
		length:   []string{strconv.Itoa(len(body))},
		body:     body,
		etag:     writer.stored.Get("ETag"),
		modified: lastModifiedTime(writer.stored),
		version:  version,
	}

	if c.options.TTL > 0 {
		entry.expires = c.now().Add(c.options.TTL)
	}

	entry.size = int64(cacheEntryOverhead + len(body) + len(key) + len(variant) + headerSize(entry.header))
	if entry.size > c.options.MaxBytes {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	stored := c.keys[key]
	if stored == nil {
		stored = &cacheKey{}
		c.keys[key] = stored
	}

	set := c.setLocked(stored, writer.vary)

	if old := set.variants[variant]; old != nil {
		c.unlinkLocked(old)
	}

	entry.set = set
	set.variants[variant] = entry
	entry.element = c.lru.PushFront(entry)
	c.bytes += entry.size

	c.evictLocked()
}

// setLocked returns the set of a key for responses that vary on vary, made
// when the key has none, and puts it first, as the one stored most
// recently. A new set beyond MaxVarySets evicts the entries of the set
// stored least recently.
func (c *ResponseCache) setLocked(stored *cacheKey, vary []string) *cacheVarySet {
	for i, set := range stored.sets {
		if !slices.Equal(set.vary, vary) {
			continue
		}

		copy(stored.sets[1:i+1], stored.sets[:i])
		stored.sets[0] = set

		return set
	}

	set := &cacheVarySet{vary: vary, variants: make(map[string]*cacheEntry)}
	stored.sets = slices.Insert(stored.sets, 0, set)

	for len(stored.sets) > c.options.MaxVarySets {
		oldest := stored.sets[len(stored.sets)-1]
		stored.sets = stored.sets[:len(stored.sets)-1]

		for _, entry := range oldest.variants {
			c.unlinkLocked(entry)
			c.evictions.Add(1)
		}
	}

	stored.union = unionNames(stored.sets)

	return set
}

// evictLocked removes the least recently used entries beyond MaxBytes and
// MaxEntries.
func (c *ResponseCache) evictLocked() {
	for c.bytes > c.options.MaxBytes || (c.options.MaxEntries > 0 && c.lru.Len() > c.options.MaxEntries) {
		oldest := c.lru.Back()
		if oldest == nil {
			return
		}

		c.removeLocked(oldest.Value.(*cacheEntry))
		c.evictions.Add(1)
	}
}

// removeLocked removes an entry, its set once that has no entries left, and
// its key once that has no sets left.
func (c *ResponseCache) removeLocked(entry *cacheEntry) {
	c.unlinkLocked(entry)

	set := entry.set
	if set.variants[entry.variant] != entry {
		return
	}

	delete(set.variants, entry.variant)

	if len(set.variants) > 0 {
		return
	}

	stored := c.keys[entry.key]
	if stored == nil {
		return
	}

	i := slices.Index(stored.sets, set)
	if i < 0 {
		return
	}

	stored.sets = slices.Delete(stored.sets, i, i+1)

	if len(stored.sets) == 0 {
		delete(c.keys, entry.key)

		return
	}

	stored.union = unionNames(stored.sets)
}

// unlinkLocked takes an entry out of the LRU list and the bytes, for
// removeLocked, or for store when it replaces the entry in its key.
func (c *ResponseCache) unlinkLocked(entry *cacheEntry) {
	c.lru.Remove(entry.element)
	c.bytes -= entry.size
}

// appendVariant appends the values of the request headers named in vary,
// by which the responses of a key differ, to buffer: those VaryValue
// returns, or else of Accept-Encoding only whether gzip is accepted, since
// Compress decides by that alone, and of others their values.
func (c *ResponseCache) appendVariant(buffer []byte, r *http.Request, vary []string) []byte {
	for _, name := range vary {
		buffer = append(buffer, name...)
		buffer = append(buffer, '=')

		if c.options.VaryValue != nil {
			if value, ok := c.options.VaryValue(r, name); ok {
				buffer = append(buffer, value...)
				buffer = append(buffer, '\n')

				continue
			}
		}

		if name == "Accept-Encoding" {
			if AcceptsGzip(r.Header.Get(name)) {
				buffer = append(buffer, "gzip"...)
			}
		} else {
			for i, value := range r.Header.Values(name) {
				if i > 0 {
					buffer = append(buffer, ", "...)
				}

				buffer = append(buffer, value...)
			}
		}

		buffer = append(buffer, '\n')
	}

	return buffer
}

// varyNames returns the canonical names of the request headers a response
// varies on, sorted and each once, and false for Vary: *, which no request
// can be matched with.
func varyNames(header http.Header) ([]string, bool) {
	var names []string

	for _, value := range header.Values("Vary") {
		for _, name := range strings.Split(value, ",") {
			name = strings.TrimSpace(name)

			switch name {
			case "*":
				return nil, false
			case "":
				continue
			}

			names = append(names, textproto.CanonicalMIMEHeaderKey(name))
		}
	}

	slices.Sort(names)

	return slices.Compact(names), true
}

// unionNames returns the names of the request headers the sets vary on,
// sorted and each once.
func unionNames(sets []*cacheVarySet) []string {
	var names []string

	for _, set := range sets {
		names = append(names, set.vary...)
	}

	slices.Sort(names)

	return slices.Compact(names)
}

// changedHeaders returns the headers of after that differ from before, as
// a copy, and the names of the headers before has and after has not: what
// a handler set and removed.
func changedHeaders(before http.Header, after http.Header) (http.Header, []string) {
	changed := make(http.Header)

	for name, values := range after {
		if !slices.Equal(before[name], values) {
			changed[name] = values
		}
	}

	var deleted []string

	for name := range before {
		if _, ok := after[name]; !ok {
			deleted = append(deleted, name)
		}
	}

	return changed.Clone(), deleted
}

// headerSize is about how many bytes the names and values of a header take.
func headerSize(header http.Header) int {
	size := 0

	for name, values := range header {
		size += len(name)

		for _, value := range values {
			size += len(value)
		}
	}

	return size
}

// cacheWriter sends a response as the handler writes it and records it to
// be stored, as long as it can be.
type cacheWriter struct {
	http.ResponseWriter
	cache *ResponseCache
	// before is the response header as it was when the handler started,
	// conditions those of the request, which the cache answers.
	before      http.Header
	conditions  conditions
	status      int
	wroteHeader bool
	// notModified is set when the client is answered 304 Not Modified: the
	// body is recorded but not sent.
	notModified bool
	// recording is set while the response may be stored: stored are the
	// headers it set, deleted those it removed, vary the request headers
	// it varies on, body what was written so far, a buffer of the pool.
	recording bool
	stored    http.Header
	deleted   []string
	vary      []string
	body      *bytes.Buffer
}

func (c *cacheWriter) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}

	if informational(status) {
		c.ResponseWriter.WriteHeader(status)

		return
	}

	c.wroteHeader = true
	c.status = status
	header := c.Header()

	if c.cache.storable(status, header) {
		c.vary, c.recording = varyNames(header)
	}

	if c.recording {
		c.stored, c.deleted = changedHeaders(c.before, header)
		c.body = getBody()
	}

	if status == http.StatusOK && c.conditions.present() {
		if c.conditions.notModified(header.Get("ETag"), lastModifiedTime(header)) {
			notModifiedHeaders(header)
			c.notModified = true
			c.ResponseWriter.WriteHeader(http.StatusNotModified)

			return
		}
	}

	c.ResponseWriter.WriteHeader(status)
}

func (c *cacheWriter) Write(data []byte) (int, error) {
	if !c.wroteHeader {
		detectContentType(c.Header(), data)
		c.WriteHeader(http.StatusOK)
	}

	if c.keepsRecording(len(data)) {
		c.body.Write(data)
	}

	if c.notModified {
		return len(data), nil
	}

	return c.ResponseWriter.Write(data)
}

// WriteString is Write for a string, such as a rendered page, without
// first copying it into a byte slice.
func (c *cacheWriter) WriteString(s string) (int, error) {
	if !c.wroteHeader {
		detectContentTypeString(c.Header(), s)
		c.WriteHeader(http.StatusOK)
	}

	if c.keepsRecording(len(s)) {
		c.body.WriteString(s)
	}

	if c.notModified {
		return len(s), nil
	}

	return io.WriteString(c.ResponseWriter, s)
}

// keepsRecording reports whether n more bytes of body are recorded: while
// the response may be stored and its body stays within MaxEntryBytes. It
// gives up storing the response when the body grows beyond.
func (c *cacheWriter) keepsRecording(n int) bool {
	if !c.recording {
		return false
	}

	if int64(c.body.Len()+n) > c.cache.options.MaxEntryBytes {
		c.stopRecording()

		return false
	}

	return true
}

// stopRecording gives up storing the response.
func (c *cacheWriter) stopRecording() {
	c.recording = false
	c.release()
}

// release gives the body buffer back to the pool.
func (c *cacheWriter) release() {
	if c.body != nil {
		putBody(c.body)
		c.body = nil
	}
}

func (c *cacheWriter) Flush() {
	_ = c.FlushError()
}

// FlushError sends what was written so far, and allows
// http.ResponseController to flush through this writer. A flushed response
// is streamed, and not stored.
func (c *cacheWriter) FlushError() error {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}

	c.stopRecording()

	if c.notModified {
		return nil
	}

	return http.NewResponseController(c.ResponseWriter).Flush()
}

// Hijack hands the connection over; the response is not stored.
func (c *cacheWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buf, err := http.NewResponseController(c.ResponseWriter).Hijack()
	if err == nil {
		c.wroteHeader = true
		c.stopRecording()
	}

	return conn, buf, err
}

// Unwrap lets http.ResponseController and router.NoIntercept reach the
// original writer.
func (c *cacheWriter) Unwrap() http.ResponseWriter {
	return c.ResponseWriter
}
