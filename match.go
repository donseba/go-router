package router

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"slices"
	"strings"
	"sync"
)

// requestStateKey holds the *requestState of a request in its context.
type requestStateKey struct{}

// requestState is what the routers a request passes through know about it:
// the mounts it went through, the URL as the outermost router received it
// and the route that matched it. Routers put it in the request context when
// MatchedRoute needs it (TrackRoutes) or a request enters a mounted router.
type requestState struct {
	// root is the router the request entered, the outermost one.
	root *Router
	// links are the mounts the request went through, outermost first.
	links    []*mountLink
	linksBuf [4]*mountLink

	// url is the request URL as the outermost router received it, set
	// once the request enters a mounted router. Requests that never enter
	// one, most of them on a site, do not pay for the copy.
	url *url.URL

	// entry is the route that matched, depth the number of links it was
	// matched below.
	entry *routeEntry
	depth int

	// wrapped is a mounted router whose wrapper (Router() *Router) was
	// given the outer URL, wrappedURL the URL it matches with.
	wrapped    *Router
	wrappedURL *url.URL
}

// stateContext is the context of a request with its state: the state and
// the context value that holds it in a single allocation.
type stateContext struct {
	context.Context
	state requestState
}

// Value returns the request's state for requestStateKey, and asks the
// parent context for any other key.
func (c *stateContext) Value(key any) any {
	if _, ok := key.(requestStateKey); ok {
		return &c.state
	}

	return c.Context.Value(key)
}

// withRequestState returns req with a new state in its context, for a
// request that entered root: nil for a request served at a prefix
// (ServeAt) outside any router.
func withRequestState(req *http.Request, root *Router) (*http.Request, *requestState) {
	ctx := &stateContext{Context: req.Context()}
	ctx.state.root = root

	return req.WithContext(ctx), &ctx.state
}

// requestStateOf returns the state of a request, nil when no router put one
// in its context.
func requestStateOf(req *http.Request) *requestState {
	state, _ := req.Context().Value(requestStateKey{}).(*requestState)

	return state
}

// mounted reports whether the request entered a mounted router.
func (s *requestState) mounted() bool {
	return s.url != nil
}

// innermost is the router the request is in: the router of the last mount
// it went through, or the router it entered.
func (s *requestState) innermost() *Router {
	if len(s.links) > 0 {
		return s.links[len(s.links)-1].child
	}

	return s.root
}

func (s *requestState) push(link *mountLink) {
	if s.links == nil {
		s.links = s.linksBuf[:0]
	}

	s.links = append(s.links, link)
}

// relativePath returns a public path as the innermost router sees it: below
// the mount paths of the mounts the request went through. A path outside
// them is returned as it is.
func (s *requestState) relativePath(public string) string {
	outer := ""
	for _, link := range s.links {
		outer += link.prefix
	}

	if outer == "" || !pathHasPrefix(public, outer) {
		return public
	}

	return public[len(outer):]
}

// routeLabels returns the labels of the route of router that is serving the
// request, if any.
func (s *requestState) routeLabels(router *Router) []string {
	if s.entry == nil || s.entry.root != router || s.depth != len(s.links) {
		return nil
	}

	router.mu.RLock()
	defer router.mu.RUnlock()

	return slices.Clone(s.entry.labels)
}

// HasLabel reports whether the route has the label (RouteRegistration.Label).
func (info RouteInfo) HasLabel(label string) bool {
	return slices.Contains(info.Labels, label)
}

// MatchedRoute returns the route that served req, as the router the request
// entered has it: its public path and pattern, below the mount paths of the
// routers it is mounted on, its name and its labels. It is meant for UseAll
// middleware, after the request was served, such as an access log or
// metrics that label requests by route:
//
//	r.TrackRoutes()
//	r.UseAll(func(next http.Handler) http.Handler {
//		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
//			next.ServeHTTP(w, req)
//
//			route, ok := router.MatchedRoute(req)
//			class := "unmatched"
//			if ok {
//				class = route.Name
//			}
//			// ...
//		})
//	})
//
// A request that entered a mounted router, also one served at a prefix
// (ServeAt), but none of its routes, such as a 404 or 405 answer of the
// mounted router or a redirect or answer of its UseAll middleware, reports
// the mount (RouteInfo.Mount), with its labels: requests below a mount are
// the mount's, also those the mounted router does not know. Other mounted
// handlers are routes of their own, with Mount set as well. It reports
// false for the requests the router answered without a route or mount: its
// own 404 and 405 answers and NotFound and MethodNotAllowed handlers,
// trailing slash redirects, and requests UseAll middleware answered itself.
//
// The router records the route when it, or a router mounted on it, has
// TrackRoutes set, which costs two allocations per request; otherwise
// MatchedRoute reports false, except for requests of mounted routers. In
// route middleware and handlers req.Pattern gives the matched ServeMux
// pattern as well.
func MatchedRoute(req *http.Request) (RouteInfo, bool) {
	match, ok := matchedOf(req)
	if !ok {
		return RouteInfo{}, false
	}

	return match.info(), true
}

// MatchedLabel returns a label of the route that served req, as
// MatchedRoute reports it, without copying the route: the first of the
// route's labels that is one of labels, or its first label when no labels
// are given, and "" when it has none of them. ok is what MatchedRoute
// reports: false when no route served req. It is the cheap way to count
// requests by kind of route:
//
//	class, _ := router.MatchedLabel(req, "admin", "asset", "media")
//	if class == "" {
//		class = "public"
//	}
//
// The labels of the mounts a route is below come before its own, as in
// RouteInfo.Labels.
func MatchedLabel(req *http.Request, labels ...string) (label string, ok bool) {
	match, ok := matchedOf(req)
	if !ok {
		return "", false
	}

	return match.label(labels), true
}

// matchedOf returns the registration that served req, as MatchedRoute
// reports it.
func matchedOf(req *http.Request) (routeMatch, bool) {
	if req == nil {
		return routeMatch{}, false
	}

	state := requestStateOf(req)
	if state == nil {
		return routeMatch{}, false
	}

	depth := len(state.links)

	switch {
	case state.entry != nil && state.depth == depth:
		return routeMatch{entry: state.entry, moreLinks: state.links}, true
	case depth > 0:
		// The request entered a mount below the route, if any, but none of
		// the mounted router's routes.
		return routeMatch{mount: state.links[depth-1], moreLinks: state.links[:depth-1]}, true
	default:
		return routeMatch{}, false
	}
}

// Match returns the route a request with the given method and path would be
// served by, on the router's default host, as Walk lists it: routes of
// mounted routers below their mount path. A query string in path is
// ignored. It reports false when no route matches, and for a router's own
// 404 and 405 answers.
//
// A path the router redirects, the way the ServeMux redirects a path that
// is not clean ("/admin//users" to "/admin/users") or one without the
// trailing slash a route has ("/theme" for "/theme/{file...}"), and the way
// RedirectTrailingSlash removes one, is matched where the redirect leads,
// with RouteInfo.Redirect set to that path:
//
//	r.Get("/theme/{file...}", theme).Label("asset")
//	r.Match(http.MethodGet, "/theme") // GET /theme/{file...}, Redirect "/theme/"
//
// A mounted router is not a route of its own, its routes are; for a path
// below its mount path that none of them matches for the method, Match
// returns the mount, as Walk lists it, with RouteInfo.Mount set and the
// mount's labels:
//
//	r.Mount("/admin", admin).Label("admin")
//	r.Match(http.MethodGet, "/admin/users/7") // admin.users.show, /admin/users/{id}
//	r.Match(http.MethodGet, "/admin/nothing") // the mount, /admin/, labelled admin
//
// Use Owns for a path with any method, and MatchLabel when only the route's
// labels matter.
func (r *Router) Match(method string, path string) (RouteInfo, bool) {
	var match routeMatch
	if !r.matchPath(method, path, &match) {
		return RouteInfo{}, false
	}

	return match.info(), true
}

// MatchLabel returns a label of the route Match reports, without copying
// the route: the first of the route's labels that is one of labels, or its
// first label when no labels are given, and "" when it has none of them.
// ok is what Match reports. It is the cheap way to tell which part of an
// application a path belongs to:
//
//	class, ok := r.MatchLabel(http.MethodGet, path, "admin", "asset", "media")
func (r *Router) MatchLabel(method string, path string, labels ...string) (label string, ok bool) {
	var match routeMatch
	if !r.matchPath(method, path, &match) {
		return "", false
	}

	return match.label(labels), true
}

// matchPath finds the registration a request with method and path would be
// served by, on the default host, and records it in match.
func (r *Router) matchPath(method string, path string, match *routeMatch) bool {
	path, _, _ = strings.Cut(path, "?")

	redirect, ok := r.rootParent().match(method, url.URL{Path: path}, match)
	match.redirect = redirect

	return ok
}

// maxMatchRedirects is how many redirects Match follows before it gives up
// on a path, such as one the router and its ServeMux redirect back and
// forth.
const maxMatchRedirects = 4

// match finds the registration of root router r that serves a request with
// method and URL u on the default host, following the redirects of the
// router's trailing slash policy and of its ServeMux, and records it in
// match. redirect is
// the path, as r sees it, where the redirects lead, "" when the request is
// served as it is. It reports false when nothing serves the request but
// the router's 404 and 405 answers.
func (r *Router) match(method string, u url.URL, match *routeMatch) (redirect string, ok bool) {
	for range maxMatchRedirects + 1 {
		if target, redirected := r.trailingSlashTarget(method, &u); redirected {
			u = target
			redirect = target.Path

			continue
		}

		handler, pattern := r.muxHandler(method, &u)
		if isMuxRedirect(handler, pattern, method, &u) {
			target, redirected := muxRedirectTarget(method, &u)
			if !redirected {
				return "", false
			}

			u = target
			redirect = target.Path

			continue
		}

		entry, isEntry := handler.(*routeEntry)
		if !isEntry || entry.root != r {
			return "", false
		}

		match.entry = entry

		if entry.mount != nil {
			if below := entry.mount.matchBelow(method, u, match); below != "" {
				redirect = below
			}
		}

		return redirect, true
	}

	return "", false
}

// matchBelow records in match the route of the router mounted at link that
// serves a request with method and URL u, which the mount's route
// matched. It returns the path, as the router the mount is on sees it,
// where the mounted router's redirects lead, "" when it has none. A request
// that none of the mounted router's routes serves is the mount's: match
// keeps the mount's route.
func (link *mountLink) matchBelow(method string, u url.URL, match *routeMatch) string {
	inner := u
	if link.strip {
		stripped, below := stripURL(&u, link.path)
		if !below {
			return ""
		}

		inner = stripped
	}

	var (
		entry = match.entry
		mark  = match.linkCount()
	)

	match.push(link)

	innerRedirect, matched := link.child.match(method, inner, match)
	if !matched {
		match.truncate(mark)
		match.entry = entry

		return ""
	}

	if innerRedirect == "" {
		return ""
	}

	return link.prefix + innerRedirect
}

// matchProbe is a request to look a route up with. The ServeMux keeps a
// request it is given on the heap; Match reuses them instead.
type matchProbe struct {
	req http.Request
	url url.URL
}

var matchProbes = sync.Pool{
	New: func() any {
		return new(matchProbe)
	},
}

// muxHandler returns the handler and pattern the ServeMux of root router r
// has for a request with method and URL u on the default host.
func (r *Router) muxHandler(method string, u *url.URL) (handler http.Handler, pattern string) {
	// The ServeMux reads the method, host and URL of the request only, and
	// changes none of them, so the probe's other fields stay empty, the host
	// as well: the default host has none.
	probe := matchProbes.Get().(*matchProbe)
	probe.url = *u
	probe.req.Method = method
	probe.req.URL = &probe.url

	handler, pattern = r.selectMux("").Handler(&probe.req)

	matchProbes.Put(probe)

	return handler, pattern
}

// keepsTrailingSlash reports whether a route of root router r serves path,
// which ends in a slash, on host for method: whether its ServeMux, or that
// of a router mounted on it, redirects the path without the slash back to
// path, as it does for a route such as "/docs/", "/admin/{$}" or
// "/files/{name...}". RedirectTrailingSlash leaves such a path alone, since
// taking the slash off would redirect back and forth.
func (r *Router) keepsTrailingSlash(host string, method string, path string) bool {
	if path == "/" || path == "" {
		return false
	}

	probe := matchProbes.Get().(*matchProbe)
	probe.url = url.URL{Path: strings.TrimSuffix(path, "/")}
	probe.req.Method = method
	probe.req.Host = host
	probe.req.URL = &probe.url

	handler, pattern := r.selectMux(host).Handler(&probe.req)

	// The other probes have no host: put it back as they expect it.
	probe.req.Host = ""
	matchProbes.Put(probe)

	if reflect.TypeOf(handler) == muxRedirectType {
		return slashPattern(pattern)
	}

	// The mount path without its slash reaches the mounted router, which
	// may put the slash back itself.
	entry, isEntry := handler.(*routeEntry)
	if !isEntry || entry.mount == nil {
		return false
	}

	link := entry.mount

	inner := path
	if link.strip {
		inner = strings.TrimPrefix(path, link.path)
	}

	return link.child.keepsTrailingSlash(host, method, inner)
}

// trailingSlashTarget returns the URL root router r redirects a request
// with method and URL u on the default host to when RedirectTrailingSlash
// applies to it: the path without the trailing slash.
func (r *Router) trailingSlashTarget(method string, u *url.URL) (url.URL, bool) {
	if !r.redirectsTrailingSlash("", u.Path) || r.keepsTrailingSlash("", method, u.Path) {
		return url.URL{}, false
	}

	target := url.URL{Path: strings.TrimSuffix(u.Path, "/")}
	if u.RawPath != "" {
		target.RawPath = strings.TrimSuffix(u.RawPath, "/")
	}

	return target, true
}

// muxRedirectType is the type of the handler a ServeMux returns for a
// request it redirects (http.RedirectHandler).
var muxRedirectType = reflect.TypeOf(http.RedirectHandler("/", http.StatusTemporaryRedirect))

// isMuxRedirect reports whether a ServeMux redirects a request with method
// and URL u, for which it returned handler and pattern: with a redirect
// handler for a path that is not clean, or for the path without the
// trailing slash of a route that has one. A redirect handler registered on
// a ServeMux given to New is a route of its own.
func isMuxRedirect(handler http.Handler, pattern string, method string, u *url.URL) bool {
	if reflect.TypeOf(handler) != muxRedirectType {
		return false
	}

	if method != http.MethodConnect {
		escaped := u.EscapedPath()
		if cleanPath(escaped) != escaped {
			return true
		}
	}

	return slashPattern(pattern)
}

// slashPattern reports whether a pattern ends in a slash, {$} or a rest
// wildcard: whether the ServeMux redirects the path without the trailing
// slash to it.
func slashPattern(pattern string) bool {
	return strings.HasSuffix(pattern, "/") || strings.HasSuffix(pattern, "{$}") || strings.HasSuffix(pattern, "...}")
}

// muxRedirectTarget returns the URL a ServeMux redirects a request with
// method and URL u to: the clean path for a path that is not clean, and
// otherwise the path with a trailing slash. A clean path that the ServeMux
// redirects to the path with a slash as well is redirected once by the
// ServeMux; Match follows it in two steps, to the same place.
func muxRedirectTarget(method string, u *url.URL) (url.URL, bool) {
	escaped := u.EscapedPath()

	target := escaped
	if method != http.MethodConnect {
		target = cleanPath(escaped)
	}

	if target == escaped {
		if strings.HasSuffix(target, "/") {
			return url.URL{}, false
		}

		target += "/"
	}

	decoded, err := url.PathUnescape(target)
	if err != nil {
		return url.URL{}, false
	}

	redirected := url.URL{Path: decoded}
	if decoded != target {
		redirected.RawPath = target
	}

	return redirected, true
}

// cleanPath returns the canonical path for p, as the ServeMux cleans it:
// rooted, without . and .. elements and double slashes, with the trailing
// slash kept.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}

	if p[0] != '/' {
		p = "/" + p
	}

	cleaned := path.Clean(p)
	if p[len(p)-1] == '/' && cleaned != "/" {
		// Keep p when only its trailing slash differs, which saves an
		// allocation.
		if len(p) == len(cleaned)+1 && strings.HasPrefix(p, cleaned) {
			return p
		}

		cleaned += "/"
	}

	return cleaned
}

// routeMatch is the registration a request is, or would be, served by: a
// route, or the mount of a mounted router that has no route for it, below
// the mounts the request goes through. Match, Owns and MatchedRoute build
// the RouteInfo from it only when they report one; label lookups need no
// copy.
type routeMatch struct {
	// entry is the route, or the mount, of the innermost router. mount is
	// set instead for a router served at a prefix (ServeAt), none of whose
	// routes served the request.
	entry *routeEntry
	mount *mountLink

	// The mounts above the registration, outermost first: the first ones
	// in linkBuf, which keeps a Match free of allocations, and all of them
	// in moreLinks once there are more, or for the state of a request.
	linkBuf    [4]*mountLink
	linkBufLen int
	moreLinks  []*mountLink

	// redirect is the public path the request is redirected to before it
	// is served, "" when it is served as it is.
	redirect string
}

// mounts returns the mounts above the registration, outermost first.
func (m *routeMatch) mounts() []*mountLink {
	if m.moreLinks != nil {
		return m.moreLinks
	}

	return m.linkBuf[:m.linkBufLen]
}

// linkCount returns how many mounts are above the registration.
func (m *routeMatch) linkCount() int {
	return len(m.mounts())
}

// push adds a mount below the others.
func (m *routeMatch) push(link *mountLink) {
	if m.moreLinks == nil && m.linkBufLen < len(m.linkBuf) {
		m.linkBuf[m.linkBufLen] = link
		m.linkBufLen++

		return
	}

	if m.moreLinks == nil {
		m.moreLinks = slices.Clone(m.linkBuf[:m.linkBufLen])
	}

	m.moreLinks = append(m.moreLinks, link)
}

// truncate keeps the first n mounts.
func (m *routeMatch) truncate(n int) {
	if m.moreLinks != nil {
		m.moreLinks = m.moreLinks[:n]

		return
	}

	m.linkBufLen = n
}

// info returns the registration as the router the request entered has it.
func (m *routeMatch) info() RouteInfo {
	var routeInfo RouteInfo

	if m.entry != nil {
		routeInfo = m.entry.info()
	} else {
		routeInfo = m.mount.mountInfo()
	}

	links := m.mounts()
	for i := len(links) - 1; i >= 0; i-- {
		routeInfo = links[i].translateRoute(routeInfo)
	}

	routeInfo.Redirect = m.redirect

	return routeInfo
}

// label returns the first label of the registration that is one of wanted,
// or its first label when wanted is empty, "" when there is none. The
// labels of the mounts above it come first, as in RouteInfo.Labels.
func (m *routeMatch) label(wanted []string) string {
	for _, link := range m.mounts() {
		if label, found := link.label(wanted); found {
			return label
		}
	}

	if m.entry != nil {
		label, _ := m.entry.label(wanted)

		return label
	}

	label, _ := m.mount.mountLabel(wanted)

	return label
}

// pattern returns the public pattern of the registration, as Match reports
// it.
func (m *routeMatch) pattern() string {
	var pattern string

	if m.entry != nil {
		pattern = m.entry.pattern
	} else {
		pattern = m.mount.prefix + "/"
	}

	links := m.mounts()
	for i := len(links) - 1; i >= 0; i-- {
		pattern = links[i].prefix + pattern
	}

	return pattern
}

// firstLabel returns the first of labels that is one of wanted, or the
// first of labels when wanted is empty.
func firstLabel(labels []string, wanted []string) (string, bool) {
	for _, label := range labels {
		if len(wanted) == 0 || slices.Contains(wanted, label) {
			return label, true
		}
	}

	return "", false
}

// requestMethods are the methods Owns and the router's 405 answers try,
// next to the other methods routes have.
var requestMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
	http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace,
}

// Owns returns the registration that receives requests for path on the
// router's default host, with any method: the most specific route that
// path has, or, as Match reports it, the mount of a mounted router that has
// no route for path. It reports false when nothing would receive path. A
// query string in path is ignored. A path the router redirects is received
// by what the redirect leads to, with RouteInfo.Redirect set, as Match
// reports it.
//
// It answers which part of an application a path belongs to, such as a
// path below a mount, or a path a site must not give a page of its own:
//
//	r.Mount("/admin", admin).Label("admin")
//	r.Post("/login", login).Label("auth")
//	r.Handle("/", site)
//
//	r.Owns("/admin/anything") // the mount, labelled admin
//	r.Owns("/login")          // POST /login, not the site that GET reaches
//	r.Owns("/about")          // the site, "/"
//
// Of two routes, the more specific one is the one with a literal segment
// where the other has a wildcard, then the one with more segments, then
// the one that does not match the paths below it; of equally specific
// routes, the first of GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS,
// CONNECT, TRACE and other methods. Owns tries the methods the routes have
// one by one, so it costs about as much as a Match per method; Match is
// cheaper when the method is known. OwnsLabel gives the labels only.
func (r *Router) Owns(path string) (RouteInfo, bool) {
	var first, second routeMatch

	owner, ok := r.owns(path, &first, &second)
	if !ok {
		return RouteInfo{}, false
	}

	return owner.info(), true
}

// OwnsLabel returns a label of the registration Owns reports, without
// copying it: the first of its labels that is one of labels, or its first
// label when no labels are given, and "" when it has none of them. ok is
// what Owns reports.
func (r *Router) OwnsLabel(path string, labels ...string) (label string, ok bool) {
	var first, second routeMatch

	owner, ok := r.owns(path, &first, &second)
	if !ok {
		return "", false
	}

	return owner.label(labels), true
}

// owns finds the registration Owns reports, in first or second: the best
// match so far is kept in one while the next method is tried in the other.
func (r *Router) owns(path string, first *routeMatch, second *routeMatch) (*routeMatch, bool) {
	path, _, _ = strings.Cut(path, "?")

	var (
		root        = r.rootParent()
		current     = first
		best        *routeMatch
		bestPattern string
	)

	for _, method := range root.routeMethods() {
		*current = routeMatch{}

		redirect, ok := root.match(method, url.URL{Path: path}, current)
		if !ok {
			continue
		}

		pattern := current.pattern()
		if best != nil && !moreSpecific(pattern, bestPattern) {
			continue
		}

		current.redirect = redirect
		best = current
		bestPattern = pattern

		// Nothing is more specific than a path of literal segments.
		if literalPattern(pattern) {
			break
		}

		if current == first {
			current = second
		} else {
			current = first
		}
	}

	return best, best != nil
}

// routeMethods returns the methods Owns tries on root router r: those its
// routes have, in the order of requestMethods and then as registered, or
// GET when all of its routes take any method.
func (r *Router) routeMethods() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.methods) == 0 {
		return requestMethods[:1]
	}

	return r.methods
}

// addMethodLocked records the method of a route of root router r, for
// Owns. The list is replaced, never changed, so it can be read after the
// lock is released.
func (r *Router) addMethodLocked(method string) {
	if method == "" || method == "*" || slices.Contains(r.methods, method) {
		return
	}

	methods := append(slices.Clone(r.methods), method)
	slices.SortStableFunc(methods, func(a string, b string) int {
		return methodOrder(a) - methodOrder(b)
	})

	r.methods = methods
}

// methodOrder is the place of a method in requestMethods, after them for
// other methods.
func methodOrder(method string) int {
	if i := slices.Index(requestMethods, method); i >= 0 {
		return i
	}

	return len(requestMethods)
}

// patternShape returns the segments of a ServeMux pattern's path, joined
// by slashes, how many there are and whether it matches the paths below it
// as well. A rest wildcard is not a segment: "/v/{file...}" matches what
// "/v/" does. It does not allocate, as Owns compares patterns per method.
func patternShape(pattern string) (segments string, count int, subtree bool) {
	trimmed := strings.TrimPrefix(pattern, "/")

	if rest, exact := strings.CutSuffix(trimmed, "{$}"); exact {
		return rest, strings.Count(rest, "/") + 1, false
	}

	subtree = trimmed == "" || strings.HasSuffix(trimmed, "/") || strings.HasSuffix(trimmed, "...}")
	if subtree {
		if i := strings.LastIndexByte(trimmed, '/'); i >= 0 {
			trimmed = trimmed[:i]
		} else {
			trimmed = ""
		}
	}

	if trimmed == "" {
		return "", 0, subtree
	}

	return trimmed, strings.Count(trimmed, "/") + 1, subtree
}

// moreSpecific reports whether pattern a is more specific than pattern b,
// for a path both match: a literal segment beats a wildcard, more segments
// beat fewer, and a pattern without the paths below it beats one with them.
func moreSpecific(a string, b string) bool {
	aSegments, aCount, aSubtree := patternShape(a)
	bSegments, bCount, bSubtree := patternShape(b)

	for i := 0; i < aCount && i < bCount; i++ {
		var aSegment, bSegment string

		aSegment, aSegments, _ = strings.Cut(aSegments, "/")
		bSegment, bSegments, _ = strings.Cut(bSegments, "/")

		_, aWildcard := wildcardName(aSegment)
		_, bWildcard := wildcardName(bSegment)

		if aWildcard != bWildcard {
			return bWildcard
		}
	}

	if aCount != bCount {
		return aCount > bCount
	}

	return !aSubtree && bSubtree
}

// literalPattern reports whether a pattern matches a single path.
func literalPattern(pattern string) bool {
	segments, count, subtree := patternShape(pattern)
	if subtree {
		return false
	}

	for range count {
		var segment string

		segment, segments, _ = strings.Cut(segments, "/")
		if _, wildcard := wildcardName(segment); wildcard {
			return false
		}
	}

	return true
}
