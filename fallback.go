package router

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
)

type fallbackKind int

const (
	fallbackNotFound fallbackKind = iota
	fallbackMethodNotAllowed
)

// fallback is a NotFound or MethodNotAllowed handler, for the unmatched
// requests below prefix on host.
type fallback struct {
	kind    fallbackKind
	router  *Router // the router or group it was set on, for its middleware
	host    string
	prefix  string
	handler http.Handler
	// served is handler inside the middleware of router, set when the
	// router freezes.
	served http.Handler
}

// fallbackPattern is the ServeMux pattern the router registers to answer
// unmatched requests itself: it matches every path and method that no
// other pattern matches.
const fallbackPattern = "/"

// NotFound sets the handler for requests that no route matches: neither the
// path nor, for a 404, the path with another method. It answers only those,
// not routes that answer 404 themselves, and is not a route: it is not in
// Walk, it does not keep Use from being called, and a route of the router
// takes precedence over it. It runs inside the router's UseAll middleware
// and inside the Use middleware of the router or group it is set on, also
// middleware added after NotFound.
//
// Set on a group or host router, it answers the unmatched requests below
// the group's path or on its host; the most specific one answers:
//
//	r.NotFound(siteNotFound)
//	r.Group("/api", func(api *router.Router) {
//		api.NotFound(jsonNotFound) // /api/...
//	})
//
// A router mounted on another (Mount) answers its unmatched requests with
// its own NotFound handlers, or else with those of the routers it is
// mounted on, which see the request URL as the outer router received it.
// Those run without the Use middleware of their router again: the request
// passed it through the mount. The response is not replaced by
// HandleStatus handlers. NotFound panics
// after the first request. See MethodNotAllowed for 405 answers.
//
// A router that serves "/" itself, such as with r.Handle("/", site) or a
// router mounted at "/", leaves its unmatched requests to that handler.
func (r *Router) NotFound(handler http.Handler) {
	r.setFallback(fallbackNotFound, handler)
}

// MethodNotAllowed sets the handler for requests whose path has routes, but
// not for the request's method. It is set and applies like NotFound.
//
// The router sets the Allow header with those methods before the handler
// runs, so the handler can read them, and keeps it when the handler answers
// 405 Method Not Allowed. It removes the header when the handler answers
// with another status, such as a not found page for every unmatched
// request, unless the handler changed it:
//
//	r.MethodNotAllowed(notFoundPage) // a 404, without Allow
func (r *Router) MethodNotAllowed(handler http.Handler) {
	r.setFallback(fallbackMethodNotAllowed, handler)
}

// WithNotFound sets the router's NotFound handler.
func WithNotFound(handler http.Handler) Option {
	return func(r *Router) {
		r.NotFound(handler)
	}
}

// WithMethodNotAllowed sets the router's MethodNotAllowed handler.
func WithMethodNotAllowed(handler http.Handler) Option {
	return func(r *Router) {
		r.MethodNotAllowed(handler)
	}
}

func (r *Router) setFallback(kind fallbackKind, handler http.Handler) {
	if handler == nil {
		panic("router: nil NotFound or MethodNotAllowed handler")
	}

	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()
	rootRouter.ensureMutableLocked()

	set := fallback{
		kind:    kind,
		router:  r,
		host:    r.hostPattern,
		prefix:  routeNamePath(r.fullPattern("")),
		handler: handler,
	}

	for i, existing := range rootRouter.fallbacks {
		if existing.kind == kind && existing.host == set.host && existing.prefix == set.prefix {
			rootRouter.fallbacks[i] = set

			return
		}
	}

	rootRouter.fallbacks = append(rootRouter.fallbacks, set)
}

// installFallbacks registers the fallback pattern on the router's ServeMuxes
// when it or a router it is mounted on (inherited) has NotFound or
// MethodNotAllowed handlers, and passes that on to the routers mounted on
// it. It runs when the router freezes.
func (r *Router) installFallbacks(inherited bool) {
	r.mu.Lock()

	needed := inherited || len(r.fallbacks) > 0

	install := needed && !r.fallbacksInstalled
	if install {
		r.fallbacksInstalled = true
		r.installFallbacksLocked()
	}

	mounted := slices.Clone(r.mounted)
	r.mu.Unlock()

	for _, child := range mounted {
		child.installFallbacks(needed)
	}
}

func (r *Router) installFallbacksLocked() {
	for i := range r.fallbacks {
		r.fallbacks[i].served = wrapMiddleware(r.fallbacks[i].handler, r.fallbacks[i].router.middlewares)
	}

	r.installFallbackLocked("", r.mux)

	for host, mux := range r.hostMuxes {
		r.installFallbackLocked(host, mux)
	}
}

// installFallbackLocked registers the fallback pattern on the ServeMux of
// host, unless a route serves "/" there already.
func (r *Router) installFallbackLocked(host string, mux *http.ServeMux) {
	for _, method := range []string{"", "*"} {
		if _, exists := r.routeKeys[routeKey(RouteInfo{Host: host, Method: method, Pattern: fallbackPattern})]; exists {
			return
		}
	}

	methods := slices.Clone(requestMethods)

	for _, routeInfo := range r.routeList {
		if routeInfo.Method != "" && routeInfo.Method != "*" && !slices.Contains(methods, routeInfo.Method) {
			methods = append(methods, routeInfo.Method)
		}
	}

	defer func() {
		// A ServeMux given to New may serve "/" itself.
		if recovered := recover(); recovered != nil {
			slog.Default().Debug("router: fallback for unmatched requests not registered",
				slog.String("host", host),
				slog.Any("error", recovered),
			)
		}
	}()

	mux.Handle(fallbackPattern, &fallbackHandler{router: r, mux: mux, methods: methods})
}

// fallbackHandler answers the requests no route of a ServeMux matches, as
// the ServeMux would, unless a NotFound or MethodNotAllowed handler answers.
type fallbackHandler struct {
	router  *Router
	mux     *http.ServeMux
	methods []string
}

func (f *fallbackHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	kind := fallbackNotFound

	allowed := f.allowedMethods(req)
	if len(allowed) > 0 {
		kind = fallbackMethodNotAllowed
		w.Header().Set("Allow", strings.Join(allowed, ", "))
	}

	var (
		host       = normalizeRequestHost(req.Host)
		path       = req.URL.Path
		handler    = f.router.fallbackFor(kind, host, path, true)
		state      = requestStateOf(req)
		inMounted  = state != nil && state.innermost() == f.router
		restoreURL = inMounted && state.mounted()
	)

	// A mounted router without a handler of its own leaves the request to
	// the routers it is mounted on, the way the request came.
	// A request served at a prefix (ServeAt) from outside a router has no
	// router to leave it to.
	//
	// The request passed the middleware of the mount, or of the route that
	// called ServeAt, on its way in, so the handler of a router it is
	// mounted on runs without its router's Use middleware: that middleware
	// ran once already.
	if handler == nil && inMounted {
		for i := len(state.links) - 1; i >= 0 && handler == nil && state.links[i].parent != nil; i-- {
			link := state.links[i]
			path = link.prefix + path
			handler = link.parent.fallbackFor(kind, host, path, false)
		}
	}

	if handler == nil {
		if kind == fallbackMethodNotAllowed {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)

			return
		}

		http.NotFound(w, req)

		return
	}

	NoIntercept(w)

	if restoreURL {
		req = withRequestURL(req, state.url)
	}

	if kind == fallbackMethodNotAllowed {
		w = &allowWriter{ResponseWriter: w, allow: w.Header().Get("Allow")}
	}

	handler.ServeHTTP(w, req)
}

// allowWriter removes the Allow header the router set for a
// MethodNotAllowed handler when the handler answers with another status
// than 405 and left the header as it was.
type allowWriter struct {
	http.ResponseWriter
	allow   string
	written bool
}

func (w *allowWriter) WriteHeader(statusCode int) {
	// Informational answers come before the real one.
	if !w.written && statusCode >= 200 {
		w.written = true

		if statusCode != http.StatusMethodNotAllowed && w.Header().Get("Allow") == w.allow {
			w.Header().Del("Allow")
		}
	}

	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *allowWriter) Write(data []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}

	return w.ResponseWriter.Write(data)
}

func (w *allowWriter) Flush() {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}

	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *allowWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *allowWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// allowedMethods returns the methods that have a route for the request's
// path, sorted, as the Allow header of a 405 answer.
func (f *fallbackHandler) allowedMethods(req *http.Request) []string {
	var allowed []string

	for _, method := range f.methods {
		if method == req.Method {
			continue
		}

		probe := new(http.Request)
		*probe = *req
		probe.Method = method

		// A pattern with a method matched; a bare path is a redirect, or
		// the fallback pattern itself.
		if _, pattern := f.mux.Handler(probe); pattern != "" && pattern[0] != '/' {
			allowed = append(allowed, method)
		}
	}

	slices.Sort(allowed)

	return allowed
}

// fallbackFor returns the most specific handler of kind for an unmatched
// request on host at path, nil when there is none: inside the Use
// middleware of the router or group it was set on with wrapped, or else as
// it is. It is called after the router froze, when its fallbacks no longer
// change.
func (r *Router) fallbackFor(kind fallbackKind, host string, path string, wrapped bool) http.Handler {
	var (
		best    = -1
		handler http.Handler
	)

	for _, candidate := range r.fallbacks {
		if candidate.kind != kind || candidate.served == nil {
			continue
		}

		if !hostPatternMatches(candidate.host, host) || !pathHasPrefix(path, candidate.prefix) {
			continue
		}

		score := 2 * len(candidate.prefix)
		if candidate.host != "" {
			score++
		}

		if score > best {
			best = score
			handler = candidate.served

			if !wrapped {
				handler = candidate.handler
			}
		}
	}

	return handler
}
