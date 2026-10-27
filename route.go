package router

import (
	"net/http"
	"net/url"
	"slices"
)

// routeEntry is the handler a route registers on the ServeMux: the route's
// middleware chain around its handler. RouteRegistration.Use extends the
// chain until the first request freezes the router.
type routeEntry struct {
	handler     http.Handler
	middlewares []Middleware
	chain       http.Handler

	// What MatchedRoute and Match report: the route as its root router has
	// it. Name and labels change with As and Label until the first request.
	root         *Router
	method       string
	pattern      string
	host         string
	name         string
	labels       []string
	routeHandler http.Handler

	// mount is set for a mounted router, which is not a route of its own
	// for MatchedRoute: its routes are. isMount is set for every Mount.
	mount   *mountLink
	isMount bool
	// keepURL passes the request on as matched, for mounts, which match
	// below their path, rather than with the URL of the outer router.
	keepURL bool
}

func newRouteEntry(handler http.Handler, middlewares []Middleware) *routeEntry {
	entry := &routeEntry{
		handler:      handler,
		middlewares:  slices.Clone(middlewares),
		routeHandler: handler,
	}
	entry.build()

	return entry
}

func (e *routeEntry) build() {
	e.chain = wrapMiddleware(e.handler, e.middlewares)
}

// wrapMiddleware returns handler inside middlewares, the first of them
// outermost.
func wrapMiddleware(handler http.Handler, middlewares []Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}

	return handler
}

// ServeHTTP runs the middleware chain, with the request URL as the outermost
// router received it when the route belongs to a mounted router, and records
// the route as the request's matched route (MatchedRoute).
func (e *routeEntry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// The request's state belongs to the router the request is in, not to
	// a router that called this one's ServeHTTP from one of its handlers.
	if state := requestStateOf(req); state != nil && state.innermost() == e.root {
		if e.mount == nil {
			state.entry = e
			state.depth = len(state.links)
		}

		if state.mounted() && !e.keepURL {
			req = withRequestURL(req, state.url)
		}
	}

	e.chain.ServeHTTP(w, req)
}

// info returns the route as its root router has it.
func (e *routeEntry) info() RouteInfo {
	e.root.mu.RLock()
	defer e.root.mu.RUnlock()

	return RouteInfo{
		Method:      e.method,
		Path:        routeNamePath(e.pattern),
		Pattern:     e.pattern,
		Host:        e.host,
		Name:        e.name,
		Labels:      slices.Clone(e.labels),
		Handler:     e.routeHandler,
		Middlewares: slices.Clone(e.middlewares),
		Mount:       e.isMount,
	}
}

// label returns the first of the route's labels that is one of wanted, or
// its first label when wanted is empty.
func (e *routeEntry) label(wanted []string) (string, bool) {
	e.root.mu.RLock()
	defer e.root.mu.RUnlock()

	return firstLabel(e.labels, wanted)
}

// Label gives the route labels, such as "asset" or "admin", for whatever
// needs to tell routes apart by kind: metrics, access logs, checks of which
// paths are taken. They are in RouteInfo.Labels for Walk, Match and
// MatchedRoute:
//
//	r.ServeFiles("/assets/", assets).As("assets").Label("asset")
//	r.Mount("/admin", admin).Label("admin") // every route of admin
//
// Labels of a mounted router (Mount) are given to all of its routes as this
// router sees them. Label panics after the first request.
func (rr *RouteRegistration) Label(labels ...string) *RouteRegistration {
	rr.mustBeRegistered("Label")

	var mounted []RouteInfo

	if rr.mount != nil {
		child := rr.mount.child
		child.mu.RLock()
		mounted = slices.Clone(child.routeList)
		child.mu.RUnlock()
	}

	rootRouter := rr.router.rootParent()
	rootRouter.update(func() {
		rootRouter.ensureMutableLocked()

		rr.entry.labels = append(rr.entry.labels, labels...)
		rootRouter.changeRouteLocked(rr.key(), func(routeInfo *RouteInfo) {
			routeInfo.Labels = slices.Clone(rr.entry.labels)
		})

		if rr.mount == nil {
			return
		}

		rr.mount.labels = append(rr.mount.labels, labels...)

		for _, routeInfo := range mounted {
			translated := rr.mount.translateRoute(routeInfo)
			rootRouter.changeRouteLocked(routeKey(translated), func(current *RouteInfo) {
				current.Labels = translated.Labels
			})
		}
	})

	return rr
}

// Use adds middleware to this route only. Route middleware runs inside the
// middleware of the router and its groups, in the order given, and after
// middleware of earlier Use calls on the route:
//
//	r.Use(a)
//	r.Group("/admin", func(admin *router.Router) {
//		admin.Use(b)
//		admin.Get("/users", users).Use(c, d).As("admin.users")
//	})
//	// a, b, c, d, users
//
// It only runs for requests the route matched; on a Mount, that is every
// request of the mount, also those the mounted router answers without a
// route. NotFound and MethodNotAllowed handlers do not run inside route
// middleware, only inside that of the router or group they are set on. Use
// panics after the first request; TryUse returns an error instead.
func (rr *RouteRegistration) Use(middlewares ...Middleware) *RouteRegistration {
	rr.mustBeRegistered("Use")

	rootRouter := rr.router.rootParent()
	rootRouter.update(func() {
		rootRouter.ensureMutableLocked()

		rr.entry.middlewares = append(rr.entry.middlewares, middlewares...)
		rr.entry.build()
		rootRouter.changeRouteLocked(rr.key(), func(routeInfo *RouteInfo) {
			routeInfo.Middlewares = slices.Clone(rr.entry.middlewares)
		})
	})

	return rr
}

// mustBeRegistered panics when the route was not made by registering one,
// for method, the RouteRegistration method that needs a registered route.
func (rr *RouteRegistration) mustBeRegistered(method string) {
	if rr == nil || rr.entry == nil || rr.router == nil {
		panic("router: " + method + " on a route that was not registered")
	}
}

// key returns the route's routeKey.
func (rr *RouteRegistration) key() string {
	return routeKey(RouteInfo{Host: rr.router.hostPattern, Method: rr.method, Pattern: rr.path})
}

// changeRouteLocked changes the route of root router r with the given
// routeKey, if r has it, with change, and passes the change on to the
// routers r is mounted on.
func (r *Router) changeRouteLocked(key string, change func(*RouteInfo)) {
	i, exists := r.routeKeys[key]
	if !exists {
		return
	}

	routeInfo := r.routeList[i]
	change(&routeInfo)
	r.updateRouteLocked(routeInfo)
}

// recoverRouteError turns a panic into a RouteError in *err, for the Try
// variants of the registration methods that panic. It recovers only when it
// is deferred itself: defer recoverRouteError(&err).
func recoverRouteError(err *error) {
	if recovered := recover(); recovered != nil {
		*err = RouteError{Err: recovered}
	}
}

// TryLabel is Label returning an error instead of panicking.
func (rr *RouteRegistration) TryLabel(labels ...string) (err error) {
	defer recoverRouteError(&err)

	rr.Label(labels...)

	return nil
}

// TryUse is Use returning an error instead of panicking.
func (rr *RouteRegistration) TryUse(middlewares ...Middleware) (err error) {
	defer recoverRouteError(&err)

	rr.Use(middlewares...)

	return nil
}

func withRequestURL(req *http.Request, requestURL *url.URL) *http.Request {
	restoredRequest := new(http.Request)
	*restoredRequest = *req
	restoredRequest.URL = new(url.URL)
	*restoredRequest.URL = *requestURL

	return restoredRequest
}
