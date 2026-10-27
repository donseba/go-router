package router

import (
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// A router mounted on another is part of it. The routes, route names, hosts
// and OpenAPI paths it registers, before and after Mount, are taken over by
// the router it is mounted on, below the mount path, until the first request
// freezes both. Its handlers and middleware see the request URL as the outer
// router received it, and it resolves route names through the outermost
// router it is mounted on, so reverse routing gives the same public paths.

type (
	// mountLink is a place a root router is mounted at. It is shared by the
	// router it is mounted on and the mounted router, and serves the
	// requests of the mount.
	mountLink struct {
		parent  *Router      // the root router it is mounted on
		child   *Router      // the mounted root router
		host    string       // the host pattern of the mount
		path    string       // the mount path without its trailing slash, "" at the root
		prefix  string       // what the mounted router's paths get: path, or "" (AbsolutePaths)
		pattern string       // the mount's ServeMux pattern: path + "/"
		handler http.Handler // the parent's handler for the mount, its route
		target  http.Handler // what serves the mount: the router or its wrapper
		wrapper bool         // target wraps the router (Router() *Router)
		strip   bool         // the mounted router matches below the mount path

		// namePrefix comes before the mounted router's route names, labels
		// are given to all of its routes (NamePrefix, RouteRegistration.Label).
		namePrefix string
		labels     []string

		// dynamic is set for a mount of a single request (ServeAt). Its path
		// is the public path the router is served at, prefix that path as
		// the router it is served from sees it, and root the public path of
		// its root route ("/" or "/{$}"), the path as ServeAt was given it.
		dynamic bool
		root    string
	}

	// MountOption configures Mount.
	MountOption func(*mountOptions)

	mountOptions struct {
		namePrefix string
		absolute   bool
	}

	mountChangeKind int

	// mountChange is a registration on a mounted router, for the routers it
	// is mounted on to take over.
	mountChange struct {
		kind     mountChangeKind
		route    RouteInfo         // routeAdded, routeUpdated, routeNamed
		path     string            // docsChanged: the OpenAPI path
		pattern  string            // docsChanged: its ServeMux pattern
		pathItem PathItem          // docsChanged
		schemas  map[string]Schema // docsChanged: the component schemas
		host     string            // hostAdded
	}

	// mountSnapshot is what a router registered before it was mounted.
	mountSnapshot struct {
		hosts    []string
		routes   []RouteInfo
		names    []RouteInfo
		paths    map[string]PathItem
		patterns map[string]string
		openapi  OpenAPI
	}
)

// NamePrefix puts prefix before the route names of a mounted router, as the
// router it is mounted on sees them, so a router can be mounted more than
// once, such as one per instance of a module:
//
//	r.Mount("/_/comments", comments, router.NamePrefix("comments."))
//	r.RoutePath("comments.post") // "/_/comments/post"
//
// The mounted router resolves its own names with the prefix first, so
// comments.RoutePath("post") is "/_/comments/post" as well, and the names
// of the routers it is mounted on without it.
func NamePrefix(prefix string) MountOption {
	return func(options *mountOptions) {
		options.namePrefix = prefix
	}
}

// AbsolutePaths mounts a router whose patterns include the mount path
// already, such as a router that registers its routes below a base path it
// was given:
//
//	admin := router.NewSite()
//	admin.Get("/admin/{$}", dashboard).As("admin.dashboard")
//	r.Mount("/admin", admin, router.AbsolutePaths())
//
// Requests at and below the mount path reach the mounted router unchanged,
// and its routes, names and OpenAPI paths are taken over as they are,
// without the mount path in front. Routes of such a router outside the
// mount path are listed, but never reached. A handler that is not a router
// sees the full path as well, instead of the path below the mount.
func AbsolutePaths() MountOption {
	return func(options *mountOptions) {
		options.absolute = true
	}
}

// newMountOptions returns the options of Mount and ServeAt with options
// applied.
func newMountOptions(options []MountOption) mountOptions {
	var mountOpts mountOptions
	for _, option := range options {
		option(&mountOpts)
	}

	return mountOpts
}

const (
	routeAdded mountChangeKind = iota
	routeUpdated
	routeNamed
	docsChanged
	hostAdded
)

// Mount serves handler at pattern and below it, and returns the mount as a
// route, so it can be named, labelled and get route middleware that runs
// for every request of the mount.
//
// A handler that is not a *Router sees the path without the mount path, as
// with http.StripPrefix (see AbsolutePaths).
//
// A *Router becomes part of this router: its routes, route names, hosts and
// OpenAPI paths, registered before or after Mount, show up here below the
// mount path, and it is frozen with this router on the first request. Its
// handlers and middleware see the full request URL, as this router received
// it, and its reverse routing (RoutePath, RoutePathWithParams, RoutePathArgs,
// FuncMap and the like) resolves through the outermost router it is mounted
// on, so it gives the paths the routes are served at, and can reach the
// names of the routers it is mounted on. The mount path itself and the mount
// path with a trailing slash both reach the mounted router's "/" or "/{$}".
//
// A handler with a Router() *Router method, such as a type that wraps its
// router in middleware of its own, is mounted as that router, and requests
// are served through the handler: the handler sees the full request URL,
// and the router matches below the mount path when the handler passes the
// request on to it.
//
// Labels given to the mount (RouteRegistration.Label) are given to every
// route of a mounted router as well. See NamePrefix and AbsolutePaths for
// the options.
func (r *Router) Mount(pattern string, handler http.Handler, options ...MountOption) *RouteRegistration {
	mountOpts := newMountOptions(options)
	pattern = mountPattern(r.fullPattern(pattern))

	link := r.newMountLink(pattern, handler, mountOpts)
	entry := r.newMountEntry(pattern, handler, link, mountOpts.absolute)
	r.registerMount(entry)

	if link != nil {
		r.attachMount(link, entry)
	}

	return &RouteRegistration{
		method: "*",
		path:   pattern,
		router: r,
		entry:  entry,
		mount:  link,
	}
}

// mountPattern returns the ServeMux pattern of a mount at a full pattern:
// the pattern with a trailing slash, so it matches the paths below it.
func mountPattern(pattern string) string {
	if pattern == "" || pattern[len(pattern)-1] != '/' {
		return pattern + "/"
	}

	return pattern
}

// mountedRouterOf returns the router a mounted handler is, or the router of
// a handler with a Router() *Router method, which wraps it (wrapped). ok is
// false for any other handler.
func mountedRouterOf(handler http.Handler) (mounted *Router, wrapped bool, ok bool) {
	if mountedRouter, isRouter := handler.(*Router); isRouter {
		return mountedRouter, false, true
	}

	if provider, isProvider := handler.(interface{ Router() *Router }); isProvider && provider.Router() != nil {
		return provider.Router(), true, true
	}

	return nil, false, false
}

// newMountLink returns the mount at pattern of the router handler is or
// wraps (see Mount), and nil for a handler that is not a router. It panics
// on a router mounted on itself.
func (r *Router) newMountLink(pattern string, handler http.Handler, mountOpts mountOptions) *mountLink {
	mountedRouter, wrapped, isRouter := mountedRouterOf(handler)
	if !isRouter {
		return nil
	}

	rootRouter := r.rootParent()

	mountedRoot := mountedRouter.rootParent()
	if mountedRoot == rootRouter || rootRouter.isMountedOn(mountedRoot) {
		panic("router: cannot mount a router on itself")
	}

	mountPath := strings.TrimSuffix(pattern, "/")
	link := &mountLink{
		parent:     rootRouter,
		child:      mountedRoot,
		host:       r.hostPattern,
		path:       mountPath,
		prefix:     mountPath,
		pattern:    pattern,
		target:     handler,
		wrapper:    wrapped,
		strip:      !mountOpts.absolute && mountPath != "",
		namePrefix: mountOpts.namePrefix,
	}

	if mountOpts.absolute {
		link.prefix = ""
	}

	return link
}

// newMountEntry returns the route of a mount at pattern. It serves the
// requests through link, the mount of a router, or else through handler,
// which sees the path below the mount path unless absolute is set.
func (r *Router) newMountEntry(pattern string, handler http.Handler, link *mountLink, absolute bool) *routeEntry {
	var mountHandler http.Handler

	switch {
	case link != nil:
		mountHandler = link
	case absolute:
		mountHandler = handler
	default:
		mountHandler = http.StripPrefix(strings.TrimSuffix(pattern, "/"), handler)
	}

	entry := newRouteEntry(mountHandler, r.middlewares)
	entry.root = r.rootParent()
	entry.method = "*"
	entry.pattern = pattern
	entry.host = r.hostPattern
	entry.routeHandler = handler
	entry.mount = link
	entry.isMount = true
	entry.keepURL = true

	return entry
}

// registerMount registers the route of a mount on the ServeMux, and for a
// mounted router also at the mount path without its trailing slash, so the
// mount path itself reaches the router's root route.
func (r *Router) registerMount(entry *routeEntry) {
	mountPath := strings.TrimSuffix(entry.pattern, "/")
	rootRouter := r.rootParent()

	rootRouter.update(func() {
		rootRouter.registerRouteInfoLocked(
			r.hostPattern,
			"*",
			entry.pattern,
			entry.routeHandler,
			r.middlewares,
			func(mux *http.ServeMux) {
				mux.Handle(entry.pattern, entry)

				if entry.mount != nil && mountPath != "" {
					mux.Handle(mountPath, entry)
				}
			},
		)
		r.markRoutesLocked()
	})
}

// attachMount makes the router mounted at link part of r, now that entry,
// the route of the mount, serves it: r takes over what the router
// registered so far, and its later registrations through flush.
func (r *Router) attachMount(link *mountLink, entry *routeEntry) {
	link.handler = entry
	snapshot := link.child.attach(link)

	rootRouter := r.rootParent()
	rootRouter.update(func() {
		rootRouter.ensureMutableLocked()
		rootRouter.mergeMountLocked(link, snapshot)
		rootRouter.mounted = append(rootRouter.mounted, link.child)
	})
}

// ServeHTTP serves a request of the mount: it records the mount in the
// request's state, for the routes of the mounted router, MatchedRoute and
// the NotFound handlers, and passes the request on with the path below the
// mount path.
func (link *mountLink) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	state := requestStateOf(req)
	if state == nil || state.innermost() != link.parent {
		req, state = withRequestState(req, link.parent)
	}

	link.serve(w, req, state)
}

// serve serves a request of the mount with the request's state.
func (link *mountLink) serve(w http.ResponseWriter, req *http.Request, state *requestState) {
	if !state.mounted() {
		outer := *req.URL
		state.url = &outer
	}

	state.push(link)

	if !link.strip {
		link.target.ServeHTTP(w, req)

		return
	}

	stripped, ok := stripPath(req, link.path)
	if !ok {
		http.NotFound(w, req)

		return
	}

	if !link.wrapper {
		link.target.ServeHTTP(w, stripped)

		return
	}

	// The wrapper sees the request URL as the outer router received it; the
	// router it passes the request on to matches with the stripped one.
	state.wrapped = link.child
	state.wrappedURL = stripped.URL
	link.target.ServeHTTP(w, withRequestURL(req, state.url))
}

// ServeAt serves req with r as if r were mounted at prefix, for this request
// only: for a router whose place is not known when routes are registered,
// such as the router of a module on a page of a CMS, whose address editors
// can change. The handler that calls ServeAt decides where the router is:
//
//	site.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
//		page := pages.Resolve(req.URL.Path) // such as "/en/news"
//		page.Module.Routes.ServeAt(w, req, page.Path)
//	}))
//
// Requests at prefix and below it are served as by Mount: r matches with
// the path below prefix, its handlers and middleware see the request URL as
// the handler that calls ServeAt received it, the public URL, and
// MatchedRoute gives r's routes below prefix, as Mount would ("/{slug}" as
// "/en/news/{slug}", "/{$}" as "/en/news/{$}"), with the labels of the
// route that called ServeAt. Other requests get a 404. Use For in r's
// handlers for reverse routing with public paths.
//
// prefix is the public path of r's root route, "/" or "/{$}", as given:
// "/en/news" has it at /en/news, "/en/" at /en/. Its other routes are below
// prefix without its trailing slash, such as /en/news/{slug}.
//
// NamePrefix puts a prefix before r's route names as MatchedRoute reports
// them to the routers the request came through; AbsolutePaths does not
// apply. r should not be mounted with Mount as well.
func (r *Router) ServeAt(w http.ResponseWriter, req *http.Request, prefix string, options ...MountOption) {
	var (
		link  = r.prefixLink(prefix, newMountOptions(options).namePrefix)
		state = requestStateOf(req)
	)

	if state == nil {
		req, state = withRequestState(req, nil)
	} else {
		link.parent = state.innermost()
		link.prefix = state.relativePath(link.path)
		link.labels = state.routeLabels(link.parent)
	}

	link.serve(w, req, state)
}

// prefixLink returns the mount of r at prefix for a single request
// (ServeAt). An empty prefix is "/".
func (r *Router) prefixLink(prefix string, namePrefix string) *mountLink {
	prefix = rootedPrefix(prefix)

	mountPath := strings.TrimRight(prefix, "/")
	link := &mountLink{
		child:      r.rootParent(),
		path:       mountPath,
		prefix:     mountPath,
		pattern:    mountPath + "/",
		strip:      mountPath != "",
		dynamic:    true,
		root:       prefix,
		namePrefix: namePrefix,
	}

	link.target = link.child

	return link
}

// For returns r for reverse routing in the handlers of a request that r
// serves at a prefix (ServeAt): RoutePath, RoutePathWithParams,
// RoutePathArgs, RouteURL, HasRoute, IsActiveRoute, FuncMap and their
// variants give the public paths of r's routes for req, below its prefix:
//
//	routes.Get("/{slug}", func(w http.ResponseWriter, req *http.Request) {
//		self, _ := routes.For(req).RoutePathArgs("show", "slug", req.PathValue("slug"))
//		// "/en/news/hello"
//	}).As("show")
//
// The root route, "/" or "/{$}", is at the prefix exactly as ServeAt was
// given it: "/en/news" without a trailing slash, so a link or a form action
// to the page itself is not redirected to it, and "/en/" with one. At gives
// the same paths without a request.
//
// For any other request it returns r itself. The router For returns is for
// reverse routing only.
func (r *Router) For(req *http.Request) *Router {
	if req == nil {
		return r
	}

	state := requestStateOf(req)
	if state == nil {
		return r
	}

	root := r.rootParent()

	for i := len(state.links) - 1; i >= 0; i-- {
		link := state.links[i]
		if !link.dynamic || link.child != root {
			continue
		}

		return r.reverseAt(link)
	}

	return r
}

// At returns r for reverse routing below prefix without a request: the
// paths For gives in the handlers of a request that r serves at prefix
// (ServeAt), for links built elsewhere, such as in a search index, a feed
// or a block on another page that lists a module's items:
//
//	news.At("/en/news").RoutePathArgs("show", "slug", "hello") // "/en/news/hello"
//	news.At("/en/news").RoutePath("index")                     // "/en/news"
//
// RoutePath, RoutePathWithParams, RoutePathArgs, RouteURL, HasRoute,
// IsActiveRoute, FuncMap and their variants give public paths below prefix.
// As with ServeAt, prefix is the public path of r's root route, "/" or
// "/{$}", as given: At("/en/news") has it at /en/news, without a trailing
// slash, and At("/en/") at /en/; r's other routes are below prefix without
// its trailing slash. An empty prefix is "/", where r's paths are its own.
//
// prefix is a path used as it is given, as ServeAt uses it: it is not
// escaped, while the parameters filled in below it are. Give it the way
// it goes into a link, and without { or }.
//
// The router At returns is for reverse routing only. It resolves r's own
// route names, as For does, and is for a router that is not mounted with
// Mount, as ServeAt is.
func (r *Router) At(prefix string) *Router {
	prefix = rootedPrefix(prefix)

	// Reverse routing reads only the paths of the link (publicRoute), so
	// the link is not built to serve requests, as ServeAt's is.
	link := &mountLink{
		path: strings.TrimRight(prefix, "/"),
		root: prefix,
	}

	return r.reverseAt(link)
}

// rootedPrefix returns the prefix of ServeAt and At with a leading slash:
// "/" for an empty prefix.
func rootedPrefix(prefix string) string {
	if prefix == "" || prefix[0] != '/' {
		return "/" + prefix
	}

	return prefix
}

// reverseAt returns r for reverse routing with the public paths of link,
// a mount of r at a prefix (For, At).
func (r *Router) reverseAt(link *mountLink) *Router {
	return &Router{
		parent:       r,
		basePath:     r.basePath,
		hostPattern:  r.hostPattern,
		handleStatus: r.handleStatus,
		servedAt:     link,
	}
}

// publicRoute returns a route of the router served at the link (ServeAt)
// at its public path.
func (link *mountLink) publicRoute(routeInfo RouteInfo) RouteInfo {
	if routeInfo.Path == "/" {
		routeInfo.Path = link.root
	} else {
		routeInfo.Path = link.path + routeInfo.Path
	}

	routeInfo.Pattern = link.path + routeInfo.Pattern

	return routeInfo
}

// mountInfo returns the mount as the router it is mounted on has it, for a
// request none of the mounted router's routes matched.
func (link *mountLink) mountInfo() RouteInfo {
	if entry, ok := link.handler.(*routeEntry); ok {
		return entry.info()
	}

	// A mount of a single request (ServeAt) is below the route that served
	// it, and has its labels.
	return RouteInfo{
		Method:  "*",
		Path:    link.prefix + "/",
		Pattern: link.prefix + "/",
		Handler: link.child,
		Labels:  slices.Clone(link.labels),
		Mount:   true,
	}
}

// label returns the first of the mount's labels that is one of wanted, or
// its first label when wanted is empty: the labels it gives the routes of
// the mounted router.
func (link *mountLink) label(wanted []string) (string, bool) {
	// The labels of a mount of a single request (ServeAt) never change;
	// those of a Mount change with Label on the router it is mounted on.
	if link.dynamic || link.parent == nil {
		return firstLabel(link.labels, wanted)
	}

	link.parent.mu.RLock()
	defer link.parent.mu.RUnlock()

	return firstLabel(link.labels, wanted)
}

// mountLabel is label for the mount as a registration of its own, as
// mountInfo reports it.
func (link *mountLink) mountLabel(wanted []string) (string, bool) {
	if entry, ok := link.handler.(*routeEntry); ok {
		return entry.label(wanted)
	}

	return link.label(wanted)
}

// stripPath returns req with prefix taken off its path, keeping the mount
// root itself, which becomes "/", and false when the path is not below
// prefix.
func stripPath(req *http.Request, prefix string) (*http.Request, bool) {
	stripped, ok := stripURL(req.URL, prefix)
	if !ok {
		return nil, false
	}

	return withRequestURL(req, &stripped), true
}

// stripURL returns u with prefix taken off its path, as stripPath does.
func stripURL(u *url.URL, prefix string) (url.URL, bool) {
	path := strings.TrimPrefix(u.Path, prefix)
	rawPath := strings.TrimPrefix(u.RawPath, prefix)

	if len(path) >= len(u.Path) || (u.RawPath != "" && len(rawPath) >= len(u.RawPath)) {
		return url.URL{}, false
	}

	if path == "" {
		path = "/"
	}

	if u.RawPath != "" && rawPath == "" {
		rawPath = "/"
	}

	stripped := *u
	stripped.Path = path
	stripped.RawPath = rawPath

	return stripped, true
}

// attach records that root router r is mounted at link, and returns what r
// registered so far; later registrations reach link.parent through flush.
func (r *Router) attach(link *mountLink) mountSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	snapshot := mountSnapshot{
		hosts:    slices.Sorted(maps.Keys(r.hostMuxes)),
		routes:   slices.Clone(r.routeList),
		patterns: maps.Clone(r.patternMap),
	}

	for _, name := range slices.Sorted(maps.Keys(r.routes)) {
		snapshot.names = append(snapshot.names, r.routes[name])
	}

	if r.openapi != nil {
		snapshot.paths = maps.Clone(r.openapi.Paths)
		snapshot.openapi = OpenAPI{
			Tags:     slices.Clone(r.openapi.Tags),
			Security: slices.Clone(r.openapi.Security),
			Components: Components{
				Schemas:         maps.Clone(r.openapi.Components.Schemas),
				SecuritySchemes: maps.Clone(r.openapi.Components.SecuritySchemes),
			},
		}
	}

	r.mountedOn = append(r.mountedOn, link)

	return snapshot
}

// mergeMountLocked takes over what a router registered before it was
// mounted at link.
func (r *Router) mergeMountLocked(link *mountLink, snapshot mountSnapshot) {
	for _, host := range snapshot.hosts {
		r.mountOnHostLocked(link, host)
	}

	for _, routeInfo := range snapshot.routes {
		r.addRouteLocked(link.translateRoute(routeInfo), "duplicate mounted route", nil)
	}

	for _, routeInfo := range snapshot.names {
		r.nameRouteLocked(link.translateRoute(routeInfo))
	}

	if r.openapi == nil {
		return
	}

	r.mergeSchemasLocked(snapshot.openapi.Components.Schemas)

	for _, path := range slices.Sorted(maps.Keys(snapshot.paths)) {
		pattern := firstNonEmpty(snapshot.patterns[path], path)
		r.mergeDocsLocked(link.prefix+path, link.prefix+pattern, snapshot.paths[path])
	}

	if len(snapshot.openapi.Components.SecuritySchemes) > 0 {
		if r.openapi.Components.SecuritySchemes == nil {
			r.openapi.Components.SecuritySchemes = make(map[string]SecurityScheme)
		}

		for name, securityScheme := range snapshot.openapi.Components.SecuritySchemes {
			if _, exists := r.openapi.Components.SecuritySchemes[name]; !exists {
				r.openapi.Components.SecuritySchemes[name] = securityScheme
			}
		}
	}

	r.openapi.Tags = appendMissingTags(r.openapi.Tags, snapshot.openapi.Tags)
	r.openapi.Security = append(r.openapi.Security, snapshot.openapi.Security...)
}

// applyMountChangeLocked takes over a registration of a router mounted at
// link, translated to this router's paths.
func (r *Router) applyMountChangeLocked(link *mountLink, change mountChange) {
	r.ensureMutableLocked()

	switch change.kind {
	case routeAdded:
		r.addRouteLocked(change.route, "duplicate mounted route", nil)
	case routeUpdated:
		r.updateRouteLocked(change.route)
	case routeNamed:
		r.nameRouteLocked(change.route)
	case docsChanged:
		if r.openapi != nil {
			r.mergeSchemasLocked(change.schemas)
			r.mergeDocsLocked(change.path, change.pattern, change.pathItem)
		}
	case hostAdded:
		r.mountOnHostLocked(link, change.host)
	}
}

// mountOnHostLocked serves the router mounted at link on a host it has
// routes for.
func (r *Router) mountOnHostLocked(link *mountLink, host string) {
	if host == link.host {
		return
	}

	mux := r.targetMuxLocked(host)
	mux.Handle(link.pattern, link.handler)

	if link.path != "" {
		mux.Handle(link.path, link.handler)
	}
}

// updateRouteLocked replaces a route, keeping its name.
func (r *Router) updateRouteLocked(routeInfo RouteInfo) {
	i, exists := r.routeKeys[routeKey(routeInfo)]
	if !exists {
		return
	}

	routeInfo.Name = firstNonEmpty(routeInfo.Name, r.routeList[i].Name)
	r.routeList[i] = routeInfo
	r.recordLocked(mountChange{kind: routeUpdated, route: routeInfo})
}

func (r *Router) mergeSchemasLocked(schemas map[string]Schema) {
	if len(schemas) == 0 {
		return
	}

	if r.openapi.Components.Schemas == nil {
		r.openapi.Components.Schemas = make(map[string]Schema)
	}

	for name, schema := range schemas {
		if _, exists := r.openapi.Components.Schemas[name]; !exists {
			r.openapi.Components.Schemas[name] = schema
		}
	}
}

func (r *Router) mergeDocsLocked(path string, pattern string, pathItem PathItem) {
	r.openapi.Paths[path] = mergePathItem(r.openapi.Paths[path], pathItem)
	r.patternMap[path] = pattern
	r.recordDocsLocked(path)
}

// recordDocsLocked queues the OpenAPI path for the routers r is mounted on.
func (r *Router) recordDocsLocked(path string) {
	if len(r.mountedOn) == 0 {
		return
	}

	r.outbox = append(r.outbox, mountChange{
		kind:     docsChanged,
		path:     path,
		pattern:  firstNonEmpty(r.patternMap[path], path),
		pathItem: r.openapi.Paths[path],
		schemas:  maps.Clone(r.openapi.Components.Schemas),
	})
}

// recordLocked queues a change of root router r for the routers it is
// mounted on; flush passes it on.
func (r *Router) recordLocked(change mountChange) {
	if len(r.mountedOn) == 0 {
		return
	}

	r.outbox = append(r.outbox, change)
}

// update runs fn with root router r locked, and then passes the changes fn
// made on to the routers r is mounted on. Locks are never nested: a router
// is unlocked before the router it is mounted on is locked.
func (r *Router) update(fn func()) {
	r.mu.Lock()
	func() {
		defer r.mu.Unlock()

		fn()
	}()

	r.flush()
}

// flush passes the queued changes of root router r on to the routers it is
// mounted on, and theirs on to the routers they are mounted on.
func (r *Router) flush() {
	r.mu.Lock()
	changes := r.outbox
	links := slices.Clone(r.mountedOn)
	r.outbox = nil
	r.mu.Unlock()

	if len(changes) == 0 {
		return
	}

	for _, link := range links {
		link.parent.update(func() {
			for _, change := range changes {
				link.parent.applyMountChangeLocked(link, link.translate(change))
			}
		})
	}
}

// isMountedOn reports whether root router r is mounted on ancestor, directly
// or through other routers.
func (r *Router) isMountedOn(ancestor *Router) bool {
	r.mu.RLock()
	links := slices.Clone(r.mountedOn)
	r.mu.RUnlock()

	for _, link := range links {
		if link.parent == ancestor || link.parent.isMountedOn(ancestor) {
			return true
		}
	}

	return false
}

// reverseRoot is the router that resolves r's route names: the outermost
// router r is mounted on, following the first Mount of each router, or r's
// root router when it is not mounted. prefix is what r's names get there
// (NamePrefix).
func (r *Router) reverseRoot() (root *Router, prefix string) {
	root = r.rootParent()

	for {
		var link *mountLink

		root.mu.RLock()

		if len(root.mountedOn) > 0 {
			link = root.mountedOn[0]
		}

		root.mu.RUnlock()

		if link == nil {
			return root, prefix
		}

		root = link.parent
		prefix = link.namePrefix + prefix
	}
}

// translate turns a change of the mounted router into the same change of
// the router it is mounted on.
func (link *mountLink) translate(change mountChange) mountChange {
	switch change.kind {
	case routeAdded, routeUpdated, routeNamed:
		change.route = link.translateRoute(change.route)
	case docsChanged:
		change.path = link.prefix + change.path
		change.pattern = link.prefix + change.pattern
	}

	return change
}

// translateRoute returns a route of the mounted router as a route of the
// router it is mounted on: below the mount path, on the mount's host unless
// it has its own, with the mount's name prefix and labels.
func (link *mountLink) translateRoute(routeInfo RouteInfo) RouteInfo {
	routeInfo.Host = firstNonEmpty(routeInfo.Host, link.host)
	routeInfo.Path = link.prefix + routeInfo.Path
	routeInfo.Pattern = link.prefix + routeInfo.Pattern
	routeInfo.Middlewares = slices.Clone(routeInfo.Middlewares)
	routeInfo.Labels = joinLabels(link.labels, routeInfo.Labels)

	if routeInfo.Redirect != "" {
		routeInfo.Redirect = link.prefix + routeInfo.Redirect
	}

	if routeInfo.Name != "" {
		routeInfo.Name = link.namePrefix + routeInfo.Name
	}

	return routeInfo
}

// joinLabels returns the labels of first and then of second, in a new slice,
// nil when there are none.
func joinLabels(first []string, second []string) []string {
	if len(first)+len(second) == 0 {
		return nil
	}

	labels := make([]string, 0, len(first)+len(second))
	labels = append(labels, first...)

	return append(labels, second...)
}
