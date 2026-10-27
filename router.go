package router

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
)

var (
	DefaultRedirectTrailingSlash = false
	DefaultRedirectStatusCode    = http.StatusTemporaryRedirect // or http.StatusMovedPermanently
	DefaultUseOpenapiDocs        = false
	OpenApiVersion               = "3.0.1"
)

type (
	RouteError struct {
		Err any
	}

	Router struct {
		mux                   *http.ServeMux
		basePath              string
		hostPattern           string
		redirectTrailingSlash bool
		openapiDocs           bool
		middlewares           []Middleware
		docs                  []Docs
		parent                *Router // Reference to the parent router

		// around is the middleware of UseAll and served the router's
		// requests through it, nil without UseAll.
		around       []Middleware
		aroundServed http.Handler

		handleStatus map[int]http.HandlerFunc
		hostMuxes    map[string]*http.ServeMux
		patternMap   map[string]string
		routes       map[string]RouteInfo
		routeList    []RouteInfo
		routeKeys    map[string]int // index in routeList by routeKey
		methods      []string       // the methods of the routes, for Owns
		redirects    []redirectTrailingSlashPolicy
		mounted      []*Router
		mountedOn    []*mountLink  // where this root router is mounted
		outbox       []mountChange // changes for the routers it is mounted on
		hasRoutes    bool
		frozen       bool
		freezeOnce   sync.Once

		// fallbacks are the NotFound and MethodNotAllowed handlers, installed
		// on the ServeMuxes when the router freezes.
		fallbacks          []fallback
		fallbacksInstalled bool

		// trackRoutes is set by TrackRoutes; tracks when the router froze
		// with it set on itself or on a router mounted on it: requests then
		// record their matched route (MatchedRoute).
		trackRoutes bool
		tracks      bool
		// wrapped is set when the router froze mounted through a wrapper
		// (see Mount), whose requests ServeHTTP then looks for.
		wrapped bool
		// slashRoutes is set when the router froze with a route its
		// ServeMux redirects the path without a trailing slash to, such as
		// "/users/": as a mounted router, it then corrects those redirects
		// (see redirectMounted).
		slashRoutes bool

		// servedAt is the mount of a request a router returned by For gives
		// public paths for (ServeAt).
		servedAt *mountLink

		once    sync.Once
		mu      sync.RWMutex
		openapi *OpenAPI
	}

	RouteInfo struct {
		Method string
		// Path is the route's path for reverse routing: the registered
		// pattern without {$}, so "/admin/{$}" and "/admin/" are "/admin/".
		Path string
		// Pattern is the registered ServeMux pattern, such as "/admin/{$}".
		Pattern     string
		Host        string
		Name        string
		Handler     http.Handler
		Middlewares []Middleware
		// Labels are the labels of the route and of the mounts it is below
		// (RouteRegistration.Label).
		Labels []string
		// Mount reports that the registration is a mount (Mount, ServeAt)
		// rather than a route. Match and MatchedRoute report the mount of a
		// mounted router for the requests none of its routes matches.
		Mount bool
		// Redirect is set by Match and Owns for a path the
		// router redirects before the route serves it: the path the
		// redirects lead to, such as "/theme/" for "/theme" with a route
		// "/theme/{file...}", or "/admin/users" for "/admin//users". It is
		// "" for a path served as it is, and in Walk and MatchedRoute.
		Redirect string
	}

	WalkFunc func(RouteInfo) error

	redirectTrailingSlashPolicy struct {
		hostPattern string
		pathPrefix  string
		redirect    bool
	}

	RouteRegistration struct {
		method string
		path   string
		name   string
		router *Router
		entry  *routeEntry
		mount  *mountLink // a mounted router
	}

	Docs struct {
		Tags        []string              // Tags for the operation
		Summary     string                // Short summary of the operation
		Description string                // Operation description
		Parameters  []Parameter           // Parameters for the operation
		RequestBody *RequestBody          // Request body for the operation
		Responses   map[string]Response   // Expected responses
		Security    []map[string][]string // Security requirements

		In  map[string]DocIn
		Out map[string]DocOut
	}

	DocOut struct {
		ApplicationType string
		Description     string
		Object          any
	}

	DocIn struct {
		Object   any
		Required bool
	}

	Middleware func(http.Handler) http.Handler
)

func (e RouteError) Error() string {
	return fmt.Sprintf("%v", e.Err)
}

func New(ht *http.ServeMux, title string, version string) *Router {
	return &Router{
		mux:                   ht,
		redirectTrailingSlash: DefaultRedirectTrailingSlash,
		openapiDocs:           DefaultUseOpenapiDocs,
		openapi: &OpenAPI{
			Openapi: OpenApiVersion,
			Info: Info{
				Title:   title,
				Version: version,
			},
			Servers: []Server{},
			Paths:   make(map[string]PathItem),
			Components: Components{
				Schemas: make(map[string]Schema),
			},
		},
		handleStatus: make(map[int]http.HandlerFunc),
		hostMuxes:    make(map[string]*http.ServeMux),
		patternMap:   make(map[string]string),
		routes:       make(map[string]RouteInfo),
		routeKeys:    make(map[string]int),
	}
}

// AddServerEndpoint adds a server, its URL and description, to the OpenAPI
// document of the router. A group, host or With router adds it to the
// document of the router it belongs to.
func (r *Router) AddServerEndpoint(url string, description string) {
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()
	rootRouter.ensureMutableLocked()

	rootRouter.openapi.Servers = append(rootRouter.openapi.Servers, Server{
		URL:         url,
		Description: description,
	})
}

func (r *Router) Get(pattern string, handler http.HandlerFunc, doc ...Docs) *RouteRegistration {
	return r.handle(http.MethodGet, pattern, handler, doc...)
}

func (r *Router) TryGet(pattern string, handler http.HandlerFunc, doc ...Docs) (*RouteRegistration, error) {
	return r.tryHandle(http.MethodGet, pattern, handler, doc...)
}

func (r *Router) Head(pattern string, handler http.HandlerFunc, doc ...Docs) *RouteRegistration {
	return r.handle(http.MethodHead, pattern, handler, doc...)
}

func (r *Router) TryHead(pattern string, handler http.HandlerFunc, doc ...Docs) (*RouteRegistration, error) {
	return r.tryHandle(http.MethodHead, pattern, handler, doc...)
}

func (r *Router) Post(pattern string, handler http.HandlerFunc, doc ...Docs) *RouteRegistration {
	return r.handle(http.MethodPost, pattern, handler, doc...)
}

func (r *Router) TryPost(pattern string, handler http.HandlerFunc, doc ...Docs) (*RouteRegistration, error) {
	return r.tryHandle(http.MethodPost, pattern, handler, doc...)
}

func (r *Router) Put(pattern string, handler http.HandlerFunc, doc ...Docs) *RouteRegistration {
	return r.handle(http.MethodPut, pattern, handler, doc...)
}

func (r *Router) TryPut(pattern string, handler http.HandlerFunc, doc ...Docs) (*RouteRegistration, error) {
	return r.tryHandle(http.MethodPut, pattern, handler, doc...)
}

func (r *Router) Patch(pattern string, handler http.HandlerFunc, doc ...Docs) *RouteRegistration {
	return r.handle(http.MethodPatch, pattern, handler, doc...)
}

func (r *Router) TryPatch(pattern string, handler http.HandlerFunc, doc ...Docs) (*RouteRegistration, error) {
	return r.tryHandle(http.MethodPatch, pattern, handler, doc...)
}

func (r *Router) Delete(pattern string, handler http.HandlerFunc, doc ...Docs) *RouteRegistration {
	return r.handle(http.MethodDelete, pattern, handler, doc...)
}

func (r *Router) TryDelete(pattern string, handler http.HandlerFunc, doc ...Docs) (*RouteRegistration, error) {
	return r.tryHandle(http.MethodDelete, pattern, handler, doc...)
}

func (r *Router) Options(pattern string, handler http.HandlerFunc, doc ...Docs) *RouteRegistration {
	return r.handle(http.MethodOptions, pattern, handler, doc...)
}

func (r *Router) TryOptions(pattern string, handler http.HandlerFunc, doc ...Docs) (*RouteRegistration, error) {
	return r.tryHandle(http.MethodOptions, pattern, handler, doc...)
}

func (r *Router) Group(basePath string, fn func(*Router)) {
	subRouter := &Router{
		basePath:              r.basePath + basePath,
		hostPattern:           r.hostPattern,
		redirectTrailingSlash: r.redirectTrailingSlash,
		middlewares:           append([]Middleware{}, r.middlewares...),
		docs:                  append([]Docs{}, r.docs...),
		parent:                r,
		openapiDocs:           r.openapiDocs,
		handleStatus:          r.handleStatus,
	}

	fn(subRouter)
}

func (r *Router) With(fn func(*Router)) {
	subRouter := &Router{
		basePath:              r.basePath,
		hostPattern:           r.hostPattern,
		redirectTrailingSlash: r.redirectTrailingSlash,
		middlewares:           append([]Middleware{}, r.middlewares...),
		docs:                  append([]Docs{}, r.docs...),
		parent:                r,
		openapiDocs:           r.openapiDocs,
		handleStatus:          r.handleStatus,
	}

	fn(subRouter)
}

func (r *Router) Host(hostPattern string, fn func(*Router)) {
	hostPattern = normalizeHostPattern(hostPattern)
	rootRouter := r.rootParent()

	rootRouter.mu.Lock()
	func() {
		defer rootRouter.mu.Unlock()

		rootRouter.ensureMutableLocked()
		rootRouter.hostMuxLocked(hostPattern)
	}()

	rootRouter.flush()

	subRouter := &Router{
		basePath:              r.basePath,
		hostPattern:           hostPattern,
		redirectTrailingSlash: r.redirectTrailingSlash,
		middlewares:           append([]Middleware{}, r.middlewares...),
		docs:                  append([]Docs{}, r.docs...),
		parent:                r,
		openapiDocs:           r.openapiDocs,
		handleStatus:          r.handleStatus,
	}

	fn(subRouter)
}

func (r *Router) Subdomain(subdomain string, domain string, fn func(*Router)) {
	if strings.TrimSpace(subdomain) == "*" {
		r.Host("*."+domain, fn)
		return
	}

	r.Host(subdomain+"."+domain, fn)
}

func (r *Router) WildcardSubdomain(domain string, fn func(*Router)) {
	r.Host("*."+domain, fn)
}

func (r *Router) RedirectTrailingSlash(redirect bool) {
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()
	rootRouter.ensureMutableLocked()

	r.redirectTrailingSlash = redirect
	if r.parent != nil || r.basePath != "" || r.hostPattern != "" {
		rootRouter.redirects = append(rootRouter.redirects, redirectTrailingSlashPolicy{
			hostPattern: r.hostPattern,
			pathPrefix:  routeNamePath(r.fullPattern("")),
			redirect:    redirect,
		})
	}
}

func (r *Router) UseOpenapiDocs(use bool) {
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()
	rootRouter.ensureMutableLocked()

	r.openapiDocs = use
}

func (r *Router) UseDocs(doc Docs) {
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()
	rootRouter.ensureMutableLocked()

	r.docs = append(r.docs, doc)
}

// HandleStatus registers a handler for responses with the given status, such
// as the 404 and 405 answers of the router and the 404s of handlers: the
// response is dropped and handler answers instead. A handler keeps its own
// response with NoIntercept. Status handlers run inside UseAll middleware,
// but not inside the middleware of Use (see Use for what that covers).
func (r *Router) HandleStatus(httpStatus int, handler http.HandlerFunc) {
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()
	rootRouter.ensureMutableLocked()

	r.handleStatus[httpStatus] = handler
}

// Use adds middleware to the routes registered on this router (and its groups)
// after the call. Middleware is applied when a route is registered, so Use
// must come before the routes: it panics when routes were already registered
// on this router or on one of its groups, since those would silently miss the
// middleware. Use Group or With for routes that need different middleware,
// or Use on a single route (RouteRegistration.Use).
//
// Use middleware runs around the handlers registered after it on this
// router and its groups:
//
//   - its routes, and every request of a Mount, which is a route as well,
//     so also the requests a mounted router answers without a route of
//     its own, such as its 404 answers;
//   - the NotFound and MethodNotAllowed handlers set on this router or one
//     of its groups, also when they were set before Use (the router builds
//     their chain when it freezes).
//
// It does not run for what the router answers without a handler: its plain
// 404 and 405 answers when no NotFound or MethodNotAllowed handler applies,
// trailing slash redirects and the redirects of the ServeMux (such as
// unclean paths), nor for HandleStatus handlers and requests that UseAll
// middleware answers itself. UseAll, or wrapping the router itself, as in
// http.ListenAndServe(addr, middleware.RequestID(r)), covers every request.
func (r *Router) Use(middleware Middleware) {
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()
	rootRouter.ensureMutableLocked()

	if r.hasRoutes {
		panic("router: Use after routes were registered; register middleware before routes, or use Group or With")
	}

	r.middlewares = append(r.middlewares, middleware)
}

// UseAll adds middleware around the whole router: unlike Use, it runs for
// every request the router serves, before a route is matched, so also for
// trailing slash redirects and other redirects, the router's plain 404 and
// 405 answers and HandleStatus handlers, next to routes and NotFound and
// MethodNotAllowed handlers.
// It is the same as wrapping the router, r.UseAll(a, b) serves as a(b(r)),
// and suits request IDs, logging, recovery and real IP middleware:
//
//	r.UseAll(middleware.RequestID, middleware.Logger(), middleware.Recover)
//
// UseAll middleware runs before the route is matched: req.PathValue is
// empty and Use middleware has not run yet. A router mounted on another runs
// its UseAll middleware for the requests that reach it, with the request URL
// as the outer router received it, like its routes.
//
// UseAll can be called after routes were registered, but only on the router
// itself, not on a group, With or host router, since it applies to all of
// the router's requests. It panics on those and after the first request.
func (r *Router) UseAll(middlewares ...Middleware) {
	if r.parent != nil {
		panic("router: UseAll on a group, With or host router; call it on the router itself")
	}

	r.update(func() {
		r.ensureMutableLocked()

		r.around = append(r.around, middlewares...)
		r.aroundServed = wrapMiddleware(http.HandlerFunc(r.serveAround), r.around)
	})
}

// TrackRoutes has the router record which route serves each request, for
// MatchedRoute and MatchedLabel, in UseAll middleware and in the routes'
// own middleware and handlers. It costs two allocations per request, the
// request with its state in the context, so it is off by default. A router mounted on another with TrackRoutes has the router it is
// mounted on record its requests too. TrackRoutes panics after the first
// request.
func (r *Router) TrackRoutes() {
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()
	rootRouter.ensureMutableLocked()

	rootRouter.trackRoutes = true
}

// markRoutesLocked records that r and the routers it belongs to have routes,
// so that a later Use, which would not apply to them, panics.
func (r *Router) markRoutesLocked() {
	for router := r; router != nil; router = router.parent {
		router.hasRoutes = true
	}
}

// Handle serves handler at pattern, a ServeMux pattern that may start with a
// method, such as "/feed.xml", "/theme/" or "GET /files/{path...}", and
// returns the route, so it can be named, labelled and get route middleware.
// The handler sees the full request URL, also in a mounted router; see Mount
// for a handler that wants the path below a prefix.
func (r *Router) Handle(pattern string, handler http.Handler) *RouteRegistration {
	method := ""

	// A method comes first, followed by a space: "GET /feed.xml".
	if before, after, found := strings.Cut(pattern, " "); found && !strings.Contains(before, "/") {
		method = before
		pattern = strings.TrimLeft(after, " \t")
	}

	return r.registerRoute(method, r.fullPattern(pattern), handler)
}

// TryHandle is Handle returning an error instead of panicking, such as for
// a pattern registered twice. The route is nil when err is set.
func (r *Router) TryHandle(pattern string, handler http.Handler) (routeRegistration *RouteRegistration, err error) {
	defer recoverRouteError(&err)

	return r.Handle(pattern, handler), nil
}

// HandleFunc is Handle for a handler function.
func (r *Router) HandleFunc(pattern string, handler http.HandlerFunc) *RouteRegistration {
	return r.Handle(pattern, handler)
}

// TryHandleFunc is HandleFunc returning an error instead of panicking.
func (r *Router) TryHandleFunc(pattern string, handler http.HandlerFunc) (*RouteRegistration, error) {
	return r.TryHandle(pattern, handler)
}

// TryMount is Mount returning an error instead of panicking, such as for a
// mount path taken by another mount or route of the same pattern, a router
// mounted on itself, or route names of the mounted router that this router
// has already. The mount is nil when err is set; otherwise it can be
// labelled and get middleware as Mount's:
//
//	mount, err := r.TryMount("/admin", admin, router.AbsolutePaths())
//	if err != nil {
//		return err
//	}
//
//	if err := mount.TryLabel("admin"); err != nil {
//		return err
//	}
func (r *Router) TryMount(
	pattern string,
	handler http.Handler,
	options ...MountOption,
) (routeRegistration *RouteRegistration, err error) {
	defer recoverRouteError(&err)

	return r.Mount(pattern, handler, options...), nil
}

// ServeFiles serves the files of fs with GET below pattern, and returns the
// route, so it can be named and get route middleware. The route's pattern
// ends in a rest wildcard, {file...} unless pattern ends in one of its own,
// which gives the file's path when the route is reversed:
//
//	r.ServeFiles("/assets/", http.FS(assets)).As("assets")
//	r.RoutePathArgs("assets", "file", "css/site.css") // "/assets/css/site.css"
//
//	r.ServeFiles("/static/{path...}", http.Dir("static")).As("static")
//	r.RoutePathArgs("static", "path", "logo.svg") // "/static/logo.svg"
//
// "/assets", "/assets/" and "/assets/{file...}" are the same route. The file
// server gets the wildcard's value as its path, so ServeFiles works the same
// in groups, hosts and mounted routers.
func (r *Router) ServeFiles(pattern string, fs http.FileSystem) *RouteRegistration {
	pattern, wildcard := filesPattern(r.fullPattern(pattern))
	fileServer := http.FileServer(fs)

	return r.registerRoute(http.MethodGet, pattern, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fileServer.ServeHTTP(w, withRequestPath(req, "/"+req.PathValue(wildcard)))
	}))
}

// ServeFile serves the file at filepath with GET at pattern, and returns the
// route, so it can be named and get route middleware.
func (r *Router) ServeFile(pattern string, filepath string) *RouteRegistration {
	return r.registerRoute(
		http.MethodGet,
		r.fullPattern(pattern),
		http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			http.ServeFile(w, req, filepath)
		}),
	)
}

// filesPattern returns the pattern of ServeFiles and the name of its rest
// wildcard: pattern when it ends in one, and otherwise pattern with a
// trailing slash and {file...}.
func filesPattern(pattern string) (string, string) {
	if strings.HasSuffix(pattern, "...}") {
		open := strings.LastIndexByte(pattern, '{')

		return pattern, pattern[open+1 : len(pattern)-len("...}")]
	}

	if !strings.HasSuffix(pattern, "/") {
		pattern += "/"
	}

	return pattern + "{file...}", "file"
}

// withRequestPath returns a shallow copy of req with path as its URL path.
func withRequestPath(req *http.Request, path string) *http.Request {
	pathRequest := withRequestURL(req, req.URL)
	pathRequest.URL.Path = path
	pathRequest.URL.RawPath = ""

	return pathRequest
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// just before serving add all the option handlers based on the openapi paths
	if r.openapiDocs {
		r.once.Do(func() {
			for p := range r.openapi.Paths {
				r.registerOptionsHandler(p)
			}
		})
	}

	r.freeze()

	// Only routers that need the request's state look it up: the serving
	// hot path stays free of it.
	var state *requestState
	if r.tracks || r.wrapped {
		state = requestStateOf(req)
	}

	if state != nil && state.wrapped == r {
		r.serveWrapped(w, req, state)

		return
	}

	if r.tracks && (state == nil || state.innermost() != r) {
		req, state = withRequestState(req, r)
	}

	if r.aroundServed != nil {
		r.serveThroughAround(w, req, state)

		return
	}

	r.serve(w, req)
}

// serveWrapped serves a request a wrapper of this mounted router (see
// Mount) passed on: matched with the URL below the mount path, while UseAll
// middleware sees the URL as the outer router received it, as the wrapper
// did.
func (r *Router) serveWrapped(w http.ResponseWriter, req *http.Request, state *requestState) {
	inner := state.wrappedURL
	state.wrapped = nil
	state.wrappedURL = nil

	if r.aroundServed == nil {
		r.serve(w, withRequestURL(req, inner))

		return
	}

	ctx := context.WithValue(req.Context(), aroundContextKey{}, aroundRequest{router: r, url: inner})
	r.aroundServed.ServeHTTP(w, req.WithContext(ctx))
}

// aroundContextKey holds the URL a mounted router matches a request with,
// while its UseAll middleware sees the request URL of the outer router.
type aroundContextKey struct{}

type aroundRequest struct {
	router *Router
	url    *url.URL
}

// serveThroughAround serves req through the UseAll middleware. A mounted
// router's middleware gets the request URL as the outer router received it,
// as its routes do; serveAround matches with the URL below the mount.
func (r *Router) serveThroughAround(w http.ResponseWriter, req *http.Request, state *requestState) {
	if state == nil {
		state = requestStateOf(req)
	}

	if state == nil || !state.mounted() || state.innermost() != r {
		r.aroundServed.ServeHTTP(w, req)

		return
	}

	ctx := context.WithValue(req.Context(), aroundContextKey{}, aroundRequest{router: r, url: req.URL})
	r.aroundServed.ServeHTTP(w, withRequestURL(req.WithContext(ctx), state.url))
}

// serveAround is the handler inside the UseAll middleware.
func (r *Router) serveAround(w http.ResponseWriter, req *http.Request) {
	if around, ok := req.Context().Value(aroundContextKey{}).(aroundRequest); ok && around.router == r {
		req = withRequestURL(req, around.url)
	}

	r.serve(w, req)
}

// serve routes a request: trailing slash redirects, the ServeMux of the
// request's host and the HandleStatus handlers.
func (r *Router) serve(w http.ResponseWriter, req *http.Request) {
	if r.redirectsTrailingSlash(req.Host, req.URL.Path) && !r.keepsTrailingSlash(req.Host, req.Method, req.URL.Path) {
		target := req.URL.Path

		// A mounted router redirects to the public path.
		if state := requestStateOf(req); state != nil && state.mounted() && state.innermost() == r {
			target = state.url.Path
		}

		http.Redirect(w, req, strings.TrimSuffix(target, "/"), DefaultRedirectStatusCode)

		return
	}

	mux := r.selectMux(req.Host)

	// A mounted router serves its requests on its root router.
	if root := r.rootParent(); root.slashRoutes && root.redirectMounted(w, req, mux) {
		return
	}

	if len(r.handleStatus) == 0 {
		mux.ServeHTTP(w, req)
		return
	}

	interceptor := &routingStatusInterceptWriter{
		ResponseWriter: &excludeHeaderWriter{
			ResponseWriter:  w,
			excludedHeaders: []string{HeaderFlagDoNotIntercept},
		},
		statusHandlers: r.handleStatus,
	}

	mux.ServeHTTP(interceptor, req)

	if interceptor.intercepted {
		switch {
		case interceptor.statusCode == http.StatusMethodNotAllowed:
			// Set the Allow header
			pattern := req.URL.Path
			allowedMethods := r.getMethodsForPattern(pattern)
			if len(allowedMethods) > 0 {
				interceptor.ResponseWriter.Header().Set("Allow", strings.Join(allowedMethods, ", "))
			}

			r.handleStatus[http.StatusMethodNotAllowed].ServeHTTP(interceptor.ResponseWriter, req)
		default:
			if v, ok := r.handleStatus[interceptor.statusCode]; ok {
				v.ServeHTTP(interceptor.ResponseWriter, req)
			}
		}
	}
}

// redirectMounted answers a request of a mounted router that its ServeMux
// redirects, such as "/users" to "/users/" below the mount path /admin,
// with the redirect to the public path, "/admin/users/": the ServeMux only
// knows the path below the mount path. It reports whether it answered.
func (r *Router) redirectMounted(w http.ResponseWriter, req *http.Request, mux *http.ServeMux) bool {
	if strings.HasSuffix(req.URL.Path, "/") {
		return false
	}

	state := requestStateOf(req)
	if state == nil || !state.mounted() || state.innermost() != r || !state.links[len(state.links)-1].strip {
		return false
	}

	handler, pattern := mux.Handler(req)
	if !isMuxRedirect(handler, pattern, req.Method, req.URL) {
		return false
	}

	// The public path ends in the path below the mount path.
	prefix, below := strings.CutSuffix(state.url.EscapedPath(), req.URL.EscapedPath())
	if !below {
		return false
	}

	redirect := &redirectRecorder{header: make(http.Header)}
	handler.ServeHTTP(redirect, req)

	http.Redirect(w, req, prefix+redirect.header.Get("Location"), redirect.status)

	return true
}

// redirectRecorder keeps the status and Location of a ServeMux redirect.
type redirectRecorder struct {
	header http.Header
	status int
}

func (w *redirectRecorder) Header() http.Header {
	return w.header
}

func (w *redirectRecorder) Write(data []byte) (int, error) {
	return len(data), nil
}

func (w *redirectRecorder) WriteHeader(status int) {
	w.status = status
}

// hasSlashRouteLocked reports whether root router r has a route below the
// root whose pattern ends in a slash, {$} or a rest wildcard: one its
// ServeMux redirects the path without the trailing slash to.
func (r *Router) hasSlashRouteLocked() bool {
	for _, routeInfo := range r.routeList {
		if slashPattern(routeInfo.Pattern) && strings.Count(routeInfo.Pattern, "/") > 1 {
			return true
		}
	}

	return false
}

func (r *Router) handle(method, pattern string, handler http.HandlerFunc, docs ...Docs) *RouteRegistration {
	pattern = r.fullPattern(pattern)

	routeRegistration := r.registerRoute(method, pattern, http.Handler(handler))
	if r.openapiDocs {
		r.registerDocs(method, pattern, r.mergeDocs(docs)...)
	}

	return routeRegistration
}

func (r *Router) tryHandle(method, pattern string, handler http.HandlerFunc, docs ...Docs) (routeRegistration *RouteRegistration, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			routeRegistration = nil
			err = RouteError{Err: recovered}
		}
	}()

	return r.handle(method, pattern, handler, docs...), nil
}

func (r *Router) mergeDocs(routeDocs []Docs) []Docs {
	if len(r.docs) == 0 {
		return routeDocs
	}

	merged := Docs{}
	for _, doc := range r.docs {
		merged = mergeDoc(merged, doc)
	}

	if len(routeDocs) > 0 {
		merged = mergeDoc(merged, routeDocs[0])
	}

	return []Docs{merged}
}

func mergeDoc(base Docs, override Docs) Docs {
	base.Tags = append(base.Tags, override.Tags...)
	base.Parameters = append(base.Parameters, override.Parameters...)
	base.Security = append(base.Security, override.Security...)

	if override.Summary != "" {
		base.Summary = override.Summary
	}
	if override.Description != "" {
		base.Description = override.Description
	}
	if override.RequestBody != nil {
		base.RequestBody = override.RequestBody
	}
	if override.Responses != nil {
		if base.Responses == nil {
			base.Responses = make(map[string]Response)
		}
		for status, response := range override.Responses {
			base.Responses[status] = response
		}
	}
	if override.In != nil {
		if base.In == nil {
			base.In = make(map[string]DocIn)
		}
		for contentType, docIn := range override.In {
			base.In[contentType] = docIn
		}
	}
	if override.Out != nil {
		if base.Out == nil {
			base.Out = make(map[string]DocOut)
		}
		for status, docOut := range override.Out {
			base.Out[status] = docOut
		}
	}

	return base
}

// registerRoute registers handler for method (none for "") at pattern, a
// full pattern, as a route.
func (r *Router) registerRoute(method, pattern string, handler http.Handler) *RouteRegistration {
	var (
		muxPattern = pattern
		rootRouter = r.rootParent()
		entry      = newRouteEntry(handler, r.middlewares)
	)

	if method != "" {
		muxPattern = method + " " + pattern
	}

	entry.root = rootRouter
	entry.method = method
	entry.pattern = pattern
	entry.host = r.hostPattern

	rootRouter.update(func() {
		rootRouter.registerRouteInfoLocked(r.hostPattern, method, pattern, handler, r.middlewares, func(mux *http.ServeMux) {
			mux.Handle(muxPattern, entry)
		})
		r.markRoutesLocked()
	})

	return &RouteRegistration{
		method: method,
		path:   pattern,
		router: r,
		entry:  entry,
	}
}

// As names the route, for reverse routing (RoutePath, RoutePathArgs, the
// route template function and the like), and returns it, so calls chain:
// r.Get("/users/{id}", show).As("users.show").Use(auth). A name can only be
// given to one route; As panics on a name in use, and after the first
// request. TryAs returns an error instead.
func (rr *RouteRegistration) As(name string) *RouteRegistration {
	rr.name = name
	rr.syncNamedRoute()

	return rr
}

// TryAs is As returning an error instead of panicking, such as for a name
// another route has.
func (rr *RouteRegistration) TryAs(name string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = RouteError{Err: recovered}
		}
	}()

	rr.As(name)
	return nil
}

func (rr *RouteRegistration) syncNamedRoute() {
	if rr == nil || rr.router == nil || rr.name == "" {
		return
	}

	rootRouter := rr.router.rootParent()
	rootRouter.update(func() {
		rootRouter.ensureMutableLocked()

		if rr.entry != nil {
			rr.entry.name = rr.name
		}

		rootRouter.nameRouteLocked(RouteInfo{
			Method:  rr.method,
			Path:    routeNamePath(rr.path),
			Pattern: rr.path,
			Host:    rr.router.hostPattern,
			Name:    rr.name,
		})
	})
}

// nameRouteLocked names the route of routeInfo, which has the route's
// method, host and pattern. A name can only be given to one route.
func (r *Router) nameRouteLocked(routeInfo RouteInfo) {
	key := routeKey(routeInfo)
	if existing, exists := r.routes[routeInfo.Name]; exists && routeKey(existing) != key {
		panic(fmt.Sprintf("router: duplicate route name %q", routeInfo.Name))
	}

	r.routes[routeInfo.Name] = RouteInfo{
		Method:  routeInfo.Method,
		Path:    routeInfo.Path,
		Pattern: routeInfo.Pattern,
		Host:    routeInfo.Host,
		Name:    routeInfo.Name,
	}

	if i, exists := r.routeKeys[key]; exists {
		r.routeList[i].Name = routeInfo.Name
	}

	r.recordLocked(mountChange{kind: routeNamed, route: r.routes[routeInfo.Name]})
}

func (r *Router) Routes() map[string]RouteInfo {
	rootRouter := r.rootParent()
	rootRouter.mu.RLock()
	defer rootRouter.mu.RUnlock()

	routes := make(map[string]RouteInfo, len(rootRouter.routes))
	for name, routeInfo := range rootRouter.routes {
		routes[name] = routeInfo
	}

	return routes
}

func (r *Router) Walk(fn WalkFunc) error {
	rootRouter := r.rootParent()
	rootRouter.mu.RLock()
	routes := append([]RouteInfo{}, rootRouter.routeList...)
	rootRouter.mu.RUnlock()

	for _, route := range routes {
		if err := fn(route); err != nil {
			return err
		}
	}

	return nil
}

// RoutePath returns the path pattern of the named route, such as
// "/users/{id}", or "" for an unknown route. A router mounted on another
// returns the path the route is served at, below the mount path.
func (r *Router) RoutePath(name string) string {
	routeInfo, _ := r.namedRoute(name)

	return routeInfo.Path
}

// RouteHost returns the host pattern of the named route, or "".
func (r *Router) RouteHost(name string) string {
	routeInfo, _ := r.namedRoute(name)

	return routeInfo.Host
}

func (r *Router) RouteTable() string {
	var buffer bytes.Buffer
	_ = r.WriteRouteTable(&buffer)
	return buffer.String()
}

func (r *Router) WriteRouteTable(w io.Writer) error {
	_, err := fmt.Fprintln(w, "HOST\tMETHOD\tPATH\tNAME\tMIDDLEWARES\tHANDLER")
	if err != nil {
		return err
	}

	return r.Walk(func(route RouteInfo) error {
		_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%T\n",
			route.Host,
			route.Method,
			route.Path,
			route.Name,
			len(route.Middlewares),
			route.Handler,
		)
		return err
	})
}

// FuncMap returns template functions for named routes:
//
//   - routePath name: the route's pattern, such as "/users/{id}"
//   - route name [key value]... [params map] [query url.Values]: the route's
//     path with its parameters filled in, and a query string; an unknown
//     route, a missing or an unknown parameter fails the template. See
//     RoutePathArgs
//   - hasRoute name: whether the route exists (HasRoute), for links to
//     routes that are optional
//   - routeOr name fallback [args]...: route, or fallback for an unknown
//     route, without an error or a warning; parameter errors of a route that
//     exists still fail the template
//   - routePathWithParams name params: RoutePathWithParams
//   - routeHost name: RouteHost
//   - routeURL name params scheme: RouteURL
//   - isActiveRoute current name [args]...: whether current is the route's
//     path or below it (IsActiveRoute); isActiveRouteExact current name
//     [args]...: whether it is the route's path (IsActiveRouteExact)
//   - isActiveRouteContains current substr: whether current's path contains
//     substr
//
// current is a *url.URL, a url.URL, an *http.Request or a path or URL string.
func (r *Router) FuncMap() map[string]any {
	return map[string]any{
		"routePath": func(name string) string {
			return r.RoutePath(name)
		},
		"routePathWithParams": func(name string, params map[string]any) string {
			return r.RoutePathWithParams(name, params)
		},
		"routeHost": func(name string) string {
			return r.RouteHost(name)
		},
		"routeURL": func(name string, params map[string]any, scheme string) string {
			return r.RouteURL(name, params, scheme)
		},
		"route":    r.RoutePathArgs,
		"hasRoute": r.HasRoute,
		"routeOr": func(name string, fallback string, args ...any) (string, error) {
			if !r.HasRoute(name) {
				return fallback, nil
			}

			return r.RoutePathArgs(name, args...)
		},
		"isActiveRoute":      r.IsActiveRoute,
		"isActiveRouteExact": r.IsActiveRouteExact,
		"isActiveRouteContains": func(current any, substr string) (bool, error) {
			currentURL, err := toCurrentURL(current)
			if err != nil || currentURL == nil {
				return false, err
			}

			return strings.Contains(currentURL.Path, substr), nil
		},
	}
}

func (r *Router) fullPattern(pattern string) string {
	if r.basePath != "" {
		pattern = r.basePath + pattern
	}

	if pattern == "" {
		return "/"
	}
	if pattern[0] != '/' {
		return "/" + pattern
	}

	return pattern
}

func (r *Router) targetMuxLocked(hostPattern string) *http.ServeMux {
	if hostPattern == "" {
		return r.mux
	}

	return r.hostMuxLocked(hostPattern)
}

// hostMuxLocked returns the ServeMux of a host pattern, made on first use.
func (r *Router) hostMuxLocked(hostPattern string) *http.ServeMux {
	if mux, exists := r.hostMuxes[hostPattern]; exists {
		return mux
	}

	mux := http.NewServeMux()
	r.hostMuxes[hostPattern] = mux
	r.recordLocked(mountChange{kind: hostAdded, host: hostPattern})

	return mux
}

func (r *Router) registerRouteInfoLocked(hostPattern string, method string, pattern string, handler http.Handler, middlewares []Middleware, register func(*http.ServeMux)) {
	r.ensureMutableLocked()

	routeInfo := RouteInfo{
		Method:      method,
		Path:        routeNamePath(pattern),
		Pattern:     pattern,
		Host:        hostPattern,
		Handler:     handler,
		Middlewares: append([]Middleware{}, middlewares...),
		// Mount registers with the method "*", which no route has.
		Mount: method == "*",
	}

	r.addRouteLocked(routeInfo, "duplicate route", func() {
		register(r.targetMuxLocked(hostPattern))
	})
}

// addRouteLocked adds a route to the route list, after register, if any,
// registered its handler. A route is its host, method and pattern, and can
// only be added once.
func (r *Router) addRouteLocked(routeInfo RouteInfo, duplicate string, register func()) {
	key := routeKey(routeInfo)
	if _, exists := r.routeKeys[key]; exists {
		panic(fmt.Sprintf("router: %s %s %s %s", duplicate, routeInfo.Host, routeInfo.Method, routeInfo.Pattern))
	}

	if register != nil {
		register()
	}

	r.routeKeys[key] = len(r.routeList)
	r.routeList = append(r.routeList, routeInfo)
	r.addMethodLocked(routeInfo.Method)
	r.recordLocked(mountChange{kind: routeAdded, route: routeInfo})
}

func mergePathItem(base PathItem, mounted PathItem) PathItem {
	if mounted.Get != nil {
		base.Get = mounted.Get
	}
	if mounted.Head != nil {
		base.Head = mounted.Head
	}
	if mounted.Post != nil {
		base.Post = mounted.Post
	}
	if mounted.Put != nil {
		base.Put = mounted.Put
	}
	if mounted.Delete != nil {
		base.Delete = mounted.Delete
	}
	if mounted.Patch != nil {
		base.Patch = mounted.Patch
	}
	if mounted.Options != nil {
		base.Options = mounted.Options
	}

	return base
}

// redirectsTrailingSlash reports whether the router redirects path, on
// host, to the path without its trailing slash (RedirectTrailingSlash).
func (r *Router) redirectsTrailingSlash(host string, path string) bool {
	if path == "/" || path == "" || path[len(path)-1] != '/' {
		return false
	}

	rootRouter := r.rootParent()
	host = normalizeRequestHost(host)

	rootRouter.mu.RLock()
	defer rootRouter.mu.RUnlock()

	var (
		bestLen  = -1
		redirect = rootRouter.redirectTrailingSlash
	)

	for _, policy := range rootRouter.redirects {
		if !hostPatternMatches(policy.hostPattern, host) {
			continue
		}

		prefix := policy.pathPrefix
		if prefix == "" {
			prefix = "/"
		}

		if !pathHasPrefix(path, prefix) {
			continue
		}

		if len(prefix) > bestLen {
			bestLen = len(prefix)
			redirect = policy.redirect
		}
	}

	return redirect
}

func (r *Router) selectMux(host string) *http.ServeMux {
	rootRouter := r.rootParent()

	rootRouter.mu.RLock()
	if len(rootRouter.hostMuxes) == 0 {
		rootRouter.mu.RUnlock()
		return rootRouter.mux
	}
	rootRouter.mu.RUnlock()

	host = normalizeRequestHost(host)

	rootRouter.mu.RLock()
	defer rootRouter.mu.RUnlock()

	if mux, exists := rootRouter.hostMuxes[host]; exists {
		return mux
	}

	var (
		bestSuffix string
		bestMux    *http.ServeMux
	)

	for hostPattern, mux := range rootRouter.hostMuxes {
		suffix, wildcard := strings.CutPrefix(hostPattern, "*.")
		if !wildcard {
			continue
		}

		if hostMatchesWildcard(host, suffix) && len(suffix) > len(bestSuffix) {
			bestSuffix = suffix
			bestMux = mux
		}
	}

	if bestMux != nil {
		return bestMux
	}

	return rootRouter.mux
}

func hostPatternMatches(hostPattern string, host string) bool {
	if hostPattern == "" {
		return true
	}
	if hostPattern == host {
		return true
	}

	suffix, wildcard := strings.CutPrefix(hostPattern, "*.")
	return wildcard && hostMatchesWildcard(host, suffix)
}

func hostMatchesWildcard(host string, suffix string) bool {
	return host != suffix && strings.HasSuffix(host, "."+suffix)
}

func normalizeHostPattern(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if parsed, err := url.Parse(host); err == nil && parsed.Host != "" {
		host = parsed.Host
	}

	if strings.HasPrefix(host, "*.") {
		return "*." + normalizeRequestHost(strings.TrimPrefix(host, "*."))
	}

	return normalizeRequestHost(host)
}

func normalizeRequestHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return ""
	}

	if strings.LastIndexByte(host, ':') >= 0 {
		if parsedHost, _, err := net.SplitHostPort(host); err == nil {
			host = parsedHost
		}
	}

	return strings.TrimSuffix(host, ".")
}

func pathHasPrefix(path string, prefix string) bool {
	if prefix == "/" {
		return true
	}

	return path == prefix || strings.HasPrefix(path, strings.TrimSuffix(prefix, "/")+"/")
}

func firstNonEmpty(value string, fallback string) string {
	if value != "" {
		return value
	}

	return fallback
}

// routeKey identifies a route by its host, method and registered pattern:
// "/users" and "/users/", or "/admin/" and "/admin/{$}", are different routes.
func routeKey(routeInfo RouteInfo) string {
	return routeInfo.Host + "\x00" + routeInfo.Method + "\x00" + routeInfo.Pattern
}

func appendMissingTags(tags []Tag, mountedTags []Tag) []Tag {
	for _, mountedTag := range mountedTags {
		if slices.ContainsFunc(tags, func(tag Tag) bool {
			return tag.Name == mountedTag.Name
		}) {
			continue
		}

		tags = append(tags, mountedTag)
	}

	return tags
}

// routeNamePath is the path of a pattern for reverse routing: the pattern
// without {$}, keeping its trailing slash, so "/admin/{$}" and "/admin/"
// are "/admin/" and "/{$}" is "/".
func routeNamePath(pattern string) string {
	pattern = strings.ReplaceAll(pattern, "{$}", "")
	if pattern == "" {
		return "/"
	}

	return pattern
}

func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case float64:
		return fmt.Sprintf("%v", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func (r *Router) registerDocs(method, pattern string, docs ...Docs) {
	if len(docs) == 0 {
		return
	}

	var (
		stripPattern = strings.ReplaceAll(pattern, "{$}", "") //strip {$} from the pattern for the docs
		doc          = &docs[0]
	)

	rootRouter := r.rootParent()
	rootRouter.update(func() {
		rootRouter.ensureMutableLocked()
		r.registerDocsLocked(rootRouter, method, pattern, stripPattern, doc)
	})
}

func (r *Router) registerDocsLocked(rootRouter *Router, method, pattern, stripPattern string, doc *Docs) {
	rootRouter.patternMap[stripPattern] = pattern

	// Get or create the path item of the pattern, kept by its stripped form.
	pathItem, exists := rootRouter.openapi.Paths[stripPattern]
	if !exists {
		pathItem = PathItem{}
	}

	op := &Operation{
		Tags:        doc.Tags,
		Summary:     doc.Summary,
		Description: doc.Description,
		OperationID: fmt.Sprintf("%s%s", method, r.OperationID(stripPattern)),
		Parameters:  doc.Parameters,
		RequestBody: doc.RequestBody,
		Responses:   doc.Responses,
		Security:    doc.Security,
	}

	// handle doc out
	componentSchema, routeResponse := r.handleDocOut(doc.Out, rootRouter.openapi.Components.Schemas)
	if componentSchema != nil {
		for na, cs := range componentSchema {
			if _, ex := rootRouter.openapi.Components.Schemas[na]; ex {
				continue
			}
			rootRouter.openapi.Components.Schemas[na] = cs
		}
	}

	if routeResponse != nil {
		op.Responses = routeResponse
	}

	// handle doc in
	componentSchema, requestBody := r.handleDocIn(doc.In, rootRouter.openapi.Components.Schemas)
	if componentSchema != nil {
		for na, cs := range componentSchema {
			if _, ex := rootRouter.openapi.Components.Schemas[na]; ex {
				continue
			}
			rootRouter.openapi.Components.Schemas[na] = cs
		}
	}

	if requestBody != nil {
		op.RequestBody = requestBody
	}

	rootRouter.openapi.Paths[stripPattern] = pathItem.SetMethod(method, op)
	rootRouter.recordDocsLocked(stripPattern)
}

func (r *Router) rootParent() *Router {
	if r.parent == nil {
		return r
	}
	return r.parent.rootParent()
}

// freeze stops registration on the router and on the routers mounted on it,
// whose routes were copied into this one when they were mounted, and
// installs the handlers for unmatched requests (NotFound).
func (r *Router) freeze() {
	rootRouter := r.rootParent()
	rootRouter.freezeOnce.Do(func() {
		rootRouter.mu.Lock()
		rootRouter.frozen = true
		mounted := slices.Clone(rootRouter.mounted)
		rootRouter.mu.Unlock()

		tracks := rootRouter.trackRoutes

		for _, mountedRouter := range mounted {
			mountedRouter.freeze()
			tracks = tracks || mountedRouter.tracks
		}

		rootRouter.installFallbacks(false)

		rootRouter.mu.Lock()
		rootRouter.tracks = tracks
		rootRouter.slashRoutes = rootRouter.hasSlashRouteLocked()

		for _, link := range rootRouter.mountedOn {
			rootRouter.wrapped = rootRouter.wrapped || link.wrapper
		}

		rootRouter.mu.Unlock()
	})
}

func (r *Router) ensureMutableLocked() {
	if r.frozen {
		panic("router: registration after first request")
	}
}

func addIfMissing[T comparable](slice []T, element T, prepend bool) []T {
	if slices.Contains(slice, element) {
		return slice
	}
	if prepend {
		return append([]T{element}, slice...)
	}
	return append(slice, element)
}

func (r *Router) getMethodsForPattern(pattern string) []string {
	rootRouter := r.rootParent()
	rootRouter.mu.RLock()
	defer rootRouter.mu.RUnlock()

	var methods []string
	if routeInfo, exists := rootRouter.openapi.Paths[pattern]; exists {
		if routeInfo.Get != nil {
			methods = addIfMissing(methods, http.MethodGet, false)
		}
		if routeInfo.Head != nil {
			methods = addIfMissing(methods, http.MethodHead, false)
		}
		if routeInfo.Post != nil {
			methods = addIfMissing(methods, http.MethodPost, false)
		}
		if routeInfo.Put != nil {
			methods = addIfMissing(methods, http.MethodPut, false)
		}
		if routeInfo.Delete != nil {
			methods = addIfMissing(methods, http.MethodDelete, false)
		}
		if routeInfo.Patch != nil {
			methods = addIfMissing(methods, http.MethodPatch, false)
		}
		if routeInfo.Options != nil {
			methods = addIfMissing(methods, http.MethodOptions, false)
		}
	}
	return methods
}

func (r *Router) registerOptionsHandler(strippedPattern string) {
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()

	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Default().Warn("router: automatic OPTIONS handler not registered",
				slog.String("path", strippedPattern),
				slog.Any("error", recovered),
			)
		}
	}()

	// Get the original pattern
	pattern, exists := rootRouter.patternMap[strippedPattern]
	if !exists {
		pattern = strippedPattern // Fallback to strippedPattern if mapping is missing
	}

	// Get methods for the pattern
	routeInfo, exists := rootRouter.openapi.Paths[pattern]
	if !exists || len(routeInfo.Methods()) == 0 || routeInfo.Options != nil {
		return
	}

	// An explicitly registered OPTIONS route wins over the automatic one.
	if _, explicit := rootRouter.routeKeys[routeKey(RouteInfo{Method: http.MethodOptions, Pattern: pattern})]; explicit {
		return
	}

	slog.Default().Debug("router: registering OPTIONS handler", slog.String("path", pattern))

	// Create the OPTIONS handler with the Allow header
	methods := addIfMissing(routeInfo.Methods(), http.MethodOptions, true)
	optionsHandler := func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Allow", strings.Join(methods, ", "))
		w.WriteHeader(http.StatusNoContent)
	}

	// Register the handler
	rootRouter.mux.HandleFunc("OPTIONS "+pattern, optionsHandler)
}

func (r *Router) OperationID(s string) string {
	if s == "" || s == "/" {
		s = "root"
	}

	parts := strings.Split(s, "/")
	for i, part := range parts {
		if part == "" {
			continue
		}
		part = strings.TrimRight(strings.TrimLeft(part, "{"), "}")
		parts[i] = strings.Title(part)
	}
	return strings.Join(parts, "")
}

func (r *Router) handleDocOut(do map[string]DocOut, schemas map[string]Schema) (map[string]Schema, map[string]Response) {
	var (
		componentSchemas map[string]Schema
		routeResponse    map[string]Response
	)

	if do == nil {
		return nil, nil
	}

	for responseCode, docOut := range do {
		var schema *Schema
		if docOut.Object != nil {
			var newComponentSchemas map[string]Schema
			schema, newComponentSchemas = schemaForObject(docOut.Object, schemas)
			if len(newComponentSchemas) > 0 {
				if componentSchemas == nil {
					componentSchemas = make(map[string]Schema)
				}
				for name, schema := range newComponentSchemas {
					componentSchemas[name] = schema
				}
			}
		} else {
			schema = nil
		}

		if routeResponse == nil {
			routeResponse = make(map[string]Response)
		}

		mediaType := MediaType{}
		if schema != nil {
			mediaType.Schema = schema
		}

		routeResponse[responseCode] = Response{
			Description: docOut.Description,
			Content: map[string]MediaType{
				docOut.ApplicationType: mediaType,
			},
		}
	}

	return componentSchemas, routeResponse
}

func (r *Router) handleDocIn(do map[string]DocIn, schemas map[string]Schema) (map[string]Schema, *RequestBody) {
	var (
		componentSchemas map[string]Schema
		requestBody      *RequestBody
	)

	if do == nil {
		return nil, nil
	}

	for contentType, docIn := range do {
		schema, newComponentSchemas := schemaForObject(docIn.Object, schemas)
		if len(newComponentSchemas) > 0 {
			if componentSchemas == nil {
				componentSchemas = make(map[string]Schema)
			}
			for name, schema := range newComponentSchemas {
				componentSchemas[name] = schema
			}
		}

		if requestBody == nil {
			requestBody = &RequestBody{
				Content: make(map[string]MediaType),
			}
		}
		requestBody.Required = requestBody.Required || docIn.Required

		requestBody.Content[contentType] = MediaType{
			Schema: schema,
		}
	}

	return componentSchemas, requestBody
}

func schemaForObject(object any, existingSchemas map[string]Schema) (*Schema, map[string]Schema) {
	if object == nil {
		return nil, nil
	}

	components := make(map[string]Schema)
	schema := schemaForType(reflect.TypeOf(object), existingSchemas, components)
	return &schema, components
}

func schemaForType(t reflect.Type, existingSchemas map[string]Schema, components map[string]Schema) Schema {
	if t == nil {
		return Schema{}
	}

	nullable := false
	for t.Kind() == reflect.Ptr {
		nullable = true
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.Bool:
		return withNullable(Schema{Type: "boolean"}, nullable)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return withNullable(Schema{Type: "integer", Format: integerFormat(t.Kind())}, nullable)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return withNullable(Schema{Type: "integer", Format: integerFormat(t.Kind())}, nullable)
	case reflect.Float32:
		return withNullable(Schema{Type: "number", Format: "float"}, nullable)
	case reflect.Float64:
		return withNullable(Schema{Type: "number", Format: "double"}, nullable)
	case reflect.String:
		return withNullable(Schema{Type: "string"}, nullable)
	case reflect.Slice, reflect.Array:
		itemSchema := schemaForType(t.Elem(), existingSchemas, components)
		return withNullable(Schema{Type: "array", Items: &itemSchema}, nullable)
	case reflect.Map:
		valueSchema := schemaForType(t.Elem(), existingSchemas, components)
		return withNullable(Schema{Type: "object", AdditionalProperties: &valueSchema}, nullable)
	case reflect.Struct:
		if t.PkgPath() == "time" && t.Name() == "Time" {
			return withNullable(Schema{Type: "string", Format: "date-time"}, nullable)
		}

		if t.Name() != "" {
			if _, exists := existingSchemas[t.Name()]; !exists {
				if _, building := components[t.Name()]; !building {
					components[t.Name()] = Schema{Type: "object"}
					components[t.Name()] = schemaForStruct(t, existingSchemas, components)
				}
			}
			return withNullable(Schema{Ref: fmt.Sprintf("#/components/schemas/%s", t.Name())}, nullable)
		}

		return withNullable(schemaForStruct(t, existingSchemas, components), nullable)
	default:
		return withNullable(Schema{Type: "string"}, nullable)
	}
}

func schemaForStruct(t reflect.Type, existingSchemas map[string]Schema, components map[string]Schema) Schema {
	schema := Schema{
		Type:       "object",
		Properties: make(map[string]Schema),
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}

		fieldName, omitEmpty, ok := schemaFieldName(field)
		if !ok {
			continue
		}

		fieldSchema := schemaForType(field.Type, existingSchemas, components)
		if field.Anonymous {
			fieldType := field.Type
			for fieldType.Kind() == reflect.Ptr {
				fieldType = fieldType.Elem()
			}
			if fieldType.Kind() == reflect.Struct && !(fieldType.PkgPath() == "time" && fieldType.Name() == "Time") {
				fieldSchema = schemaForStruct(fieldType, existingSchemas, components)
			}
		}
		applySchemaFieldTags(&fieldSchema, field)
		if field.Anonymous && fieldName == field.Name && fieldSchema.Ref == "" && fieldSchema.Type == "object" {
			for name, property := range fieldSchema.Properties {
				schema.Properties[name] = property
			}
			schema.Required = append(schema.Required, fieldSchema.Required...)
			continue
		}
		schema.Properties[fieldName] = fieldSchema

		if isRequiredField(field, omitEmpty) {
			schema.Required = append(schema.Required, fieldName)
		}
	}

	if len(schema.Properties) == 0 {
		schema.Properties = nil
	}

	return schema
}

func schemaFieldName(field reflect.StructField) (string, bool, bool) {
	jsonTag := field.Tag.Get("json")
	if jsonTag == "-" {
		return "", false, false
	}

	if jsonTag == "" {
		return field.Name, false, true
	}

	parts := strings.Split(jsonTag, ",")
	name := parts[0]
	if name == "" {
		name = field.Name
	}

	return name, slices.Contains(parts[1:], "omitempty"), true
}

func isRequiredField(field reflect.StructField, omitEmpty bool) bool {
	validateTag := field.Tag.Get("validate")
	bindingTag := field.Tag.Get("binding")
	return strings.Contains(validateTag, "required") || strings.Contains(bindingTag, "required") || field.Tag.Get("required") == "true" && !omitEmpty
}

func applySchemaFieldTags(schema *Schema, field reflect.StructField) {
	if format := field.Tag.Get("format"); format != "" {
		schema.Format = format
	}
	if enumTag := field.Tag.Get("enum"); enumTag != "" {
		values := strings.Split(enumTag, "|")
		schema.Enum = make([]any, 0, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value != "" {
				schema.Enum = append(schema.Enum, value)
			}
		}
	}
}

func integerFormat(kind reflect.Kind) string {
	switch kind {
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return "int32"
	case reflect.Int64, reflect.Uint64:
		return "int64"
	default:
		return ""
	}
}

func withNullable(schema Schema, nullable bool) Schema {
	schema.Nullable = nullable
	return schema
}

// OpenAPI returns the root documentation tree
func (r *Router) OpenAPI() *OpenAPI {
	rootRouter := r.rootParent()
	return rootRouter.openapi
}
