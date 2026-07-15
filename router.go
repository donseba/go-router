package router

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"slices"
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

		handleStatus map[int]http.HandlerFunc
		hostMuxes    map[string]*http.ServeMux
		patternMap   map[string]string
		routes       map[string]RouteInfo
		routeList    []RouteInfo
		routeKeys    map[string]struct{}
		redirects    []redirectTrailingSlashPolicy
		frozen       bool
		freezeOnce   sync.Once

		once    sync.Once
		mu      sync.RWMutex
		openapi *OpenAPI
	}

	RouteInfo struct {
		Method      string
		Path        string
		Host        string
		Name        string
		Handler     http.Handler
		Middlewares []Middleware
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

	originalRequestURLContextKey struct{}
)

var originalRequestURLKey originalRequestURLContextKey

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
		routeKeys:    make(map[string]struct{}),
	}
}

func (r *Router) AddServerEndpoint(url string, description string) {
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()
	rootRouter.ensureMutableLocked()

	r.openapi.Servers = append(r.openapi.Servers, Server{
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
	rootRouter.ensureMutableLocked()
	if _, exists := rootRouter.hostMuxes[hostPattern]; !exists {
		rootRouter.hostMuxes[hostPattern] = http.NewServeMux()
	}
	rootRouter.mu.Unlock()

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

func (r *Router) HandleStatus(httpStatus int, handler http.HandlerFunc) {
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()
	rootRouter.ensureMutableLocked()

	r.handleStatus[httpStatus] = handler
}

func (r *Router) Use(middleware Middleware) {
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()
	rootRouter.ensureMutableLocked()

	r.middlewares = append(r.middlewares, middleware)
}

func (r *Router) Handle(pattern string, handler http.Handler) {
	pattern = r.fullPattern(pattern)

	var finalHandler http.Handler = handler
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		finalHandler = r.middlewares[i](finalHandler)
	}
	finalHandler = restoreOriginalRequestURL(finalHandler)

	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()

	rootRouter.registerRouteInfoLocked(r.hostPattern, "", pattern, handler, r.middlewares, func(mux *http.ServeMux) {
		mux.Handle(pattern, finalHandler)
	})
}

func (r *Router) TryHandle(pattern string, handler http.Handler) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = RouteError{Err: recovered}
		}
	}()

	r.Handle(pattern, handler)
	return nil
}

func (r *Router) HandleFunc(pattern string, handler http.HandlerFunc) {
	r.Handle(pattern, handler)
}

func (r *Router) TryHandleFunc(pattern string, handler http.HandlerFunc) error {
	return r.TryHandle(pattern, handler)
}

func (r *Router) Mount(pattern string, handler http.Handler) {
	pattern = r.fullPattern(pattern)
	if pattern == "" || pattern[len(pattern)-1] != '/' {
		pattern += "/"
	}

	mountPrefix := strings.TrimSuffix(pattern, "/")
	var finalHandler http.Handler
	if _, ok := handler.(*Router); ok {
		finalHandler = preserveOriginalRequestURL(stripMountedRouterPrefix(mountPrefix, handler))
	} else {
		finalHandler = http.StripPrefix(mountPrefix, handler)
	}
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		finalHandler = r.middlewares[i](finalHandler)
	}

	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()

	rootRouter.registerRouteInfoLocked(r.hostPattern, "*", pattern, handler, r.middlewares, func(mux *http.ServeMux) {
		mux.Handle(pattern, finalHandler)
		if _, ok := handler.(*Router); ok && mountPrefix != "" {
			mux.Handle(mountPrefix, finalHandler)
		}
	})
	if mountedRouter, ok := handler.(*Router); ok {
		for childHostPattern := range mountedRouter.rootParent().hostMuxes {
			if childHostPattern == r.hostPattern {
				continue
			}
			childMux := rootRouter.targetMuxLocked(childHostPattern)
			childMux.Handle(pattern, finalHandler)
			if mountPrefix != "" {
				childMux.Handle(mountPrefix, finalHandler)
			}
		}
		rootRouter.mountRouterLocked(r.hostPattern, strings.TrimSuffix(pattern, "/"), mountedRouter)
	}
}

func (r *Router) TryMount(pattern string, handler http.Handler) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = RouteError{Err: recovered}
		}
	}()

	r.Mount(pattern, handler)
	return nil
}

func preserveOriginalRequestURL(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Context().Value(originalRequestURLKey) != nil {
			next.ServeHTTP(w, req)
			return
		}

		originalURL := new(url.URL)
		*originalURL = *req.URL
		ctx := context.WithValue(req.Context(), originalRequestURLKey, originalURL)
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

func restoreOriginalRequestURL(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		originalURL, ok := req.Context().Value(originalRequestURLKey).(*url.URL)
		if !ok {
			next.ServeHTTP(w, req)
			return
		}

		restoredRequest := new(http.Request)
		*restoredRequest = *req
		restoredRequest.URL = new(url.URL)
		*restoredRequest.URL = *originalURL
		next.ServeHTTP(w, restoredRequest)
	})
}

// stripMountedRouterPrefix keeps the child router's matching path relative to
// the mount while allowing the mount root itself to match without a redirect.
func stripMountedRouterPrefix(prefix string, next http.Handler) http.Handler {
	if prefix == "" {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := strings.TrimPrefix(req.URL.Path, prefix)
		rawPath := strings.TrimPrefix(req.URL.RawPath, prefix)
		if len(path) >= len(req.URL.Path) || (req.URL.RawPath != "" && len(rawPath) >= len(req.URL.RawPath)) {
			http.NotFound(w, req)
			return
		}

		if path == "" {
			path = "/"
		}
		if req.URL.RawPath != "" && rawPath == "" {
			rawPath = "/"
		}

		strippedRequest := new(http.Request)
		*strippedRequest = *req
		strippedRequest.URL = new(url.URL)
		*strippedRequest.URL = *req.URL
		strippedRequest.URL.Path = path
		strippedRequest.URL.RawPath = rawPath
		next.ServeHTTP(w, strippedRequest)
	})
}

func (r *Router) ServeFiles(pattern string, fs http.FileSystem) {
	pattern = r.fullPattern(pattern)

	// Ensure the pattern ends with "/" for directory serving
	if pattern == "" || pattern[len(pattern)-1] != '/' {
		pattern += "/"
	}

	// Create a file server handler
	fileServer := http.StripPrefix(pattern, http.FileServer(fs))

	// Wrap the file server with middlewares
	var finalHandler http.Handler = fileServer
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		finalHandler = r.middlewares[i](finalHandler)
	}

	// Register the handler for GET method
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()

	rootRouter.registerRouteInfoLocked(r.hostPattern, http.MethodGet, pattern, fileServer, r.middlewares, func(mux *http.ServeMux) {
		mux.Handle("GET "+pattern, finalHandler)
	})
}

func (r *Router) ServeFile(pattern string, filepath string) {
	pattern = r.fullPattern(pattern)

	// Handler to serve the file
	handler := func(w http.ResponseWriter, req *http.Request) {
		http.ServeFile(w, req, filepath)
	}

	// Wrap the handler with middlewares
	var finalHandler http.Handler = http.HandlerFunc(handler)
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		finalHandler = r.middlewares[i](finalHandler)
	}

	// Register the handler for GET method
	fullPattern := "GET " + pattern
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()

	rootRouter.registerRouteInfoLocked(r.hostPattern, http.MethodGet, pattern, http.HandlerFunc(handler), r.middlewares, func(mux *http.ServeMux) {
		mux.Handle(fullPattern, finalHandler)
	})
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// just before serving add all the option handlers based on the openapi paths
	if r.openapiDocs {
		r.once.Do(func() {
			for p, _ := range r.openapi.Paths {
				fmt.Println("Registering options handler for", p)
				r.registerOptionsHandler(p)
			}
		})
	}
	r.freeze()

	if r.shouldRedirectTrailingSlash(req) {
		if req.URL.Path != "/" && req.URL.Path[len(req.URL.Path)-1] == '/' {
			http.Redirect(w, req, req.URL.Path[:len(req.URL.Path)-1], DefaultRedirectStatusCode)
			return
		}
	}

	mux := r.selectMux(req.Host)
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

func (r *Router) handle(method, pattern string, handler http.HandlerFunc, docs ...Docs) *RouteRegistration {
	pattern = r.fullPattern(pattern)

	routeRegistration := r.registerRoute(method, pattern, handler)
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

func (r *Router) registerRoute(method, pattern string, handler http.HandlerFunc) *RouteRegistration {
	var (
		fullPattern               = method + " " + pattern
		finalHandler http.Handler = handler
	)

	for i := len(r.middlewares) - 1; i >= 0; i-- {
		finalHandler = r.middlewares[i](finalHandler)
	}
	finalHandler = restoreOriginalRequestURL(finalHandler)

	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()

	rootRouter.registerRouteInfoLocked(r.hostPattern, method, pattern, http.HandlerFunc(handler), r.middlewares, func(mux *http.ServeMux) {
		mux.Handle(fullPattern, finalHandler)
	})

	return &RouteRegistration{
		method: method,
		path:   pattern,
		router: r,
	}
}

func (rr *RouteRegistration) As(name string) {
	rr.name = name
	rr.syncNamedRoute()
}

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
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()
	rootRouter.ensureMutableLocked()

	if existing, exists := rootRouter.routes[rr.name]; exists && (existing.Method != rr.method || existing.Path != routeNamePath(rr.path) || existing.Host != rr.router.hostPattern) {
		panic(fmt.Sprintf("router: duplicate route name %q", rr.name))
	}

	routeInfo := RouteInfo{
		Method: rr.method,
		Path:   routeNamePath(rr.path),
		Host:   rr.router.hostPattern,
		Name:   rr.name,
	}

	rootRouter.routes[rr.name] = routeInfo
	for i, registeredRoute := range rootRouter.routeList {
		if registeredRoute.Method == routeInfo.Method && registeredRoute.Path == routeInfo.Path && registeredRoute.Host == routeInfo.Host {
			rootRouter.routeList[i].Name = rr.name
			break
		}
	}
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

func (r *Router) RoutePath(name string) string {
	rootRouter := r.rootParent()
	rootRouter.mu.RLock()
	defer rootRouter.mu.RUnlock()

	if routeInfo, ok := rootRouter.routes[name]; ok {
		return routeInfo.Path
	}

	return ""
}

func (r *Router) RoutePathWithParams(name string, params map[string]any) string {
	path := r.RoutePath(name)
	if path == "" {
		return ""
	}

	for key, value := range params {
		path = strings.ReplaceAll(path, "{"+key+"}", url.PathEscape(toString(value)))
	}

	return path
}

func (r *Router) RouteHost(name string) string {
	rootRouter := r.rootParent()
	rootRouter.mu.RLock()
	defer rootRouter.mu.RUnlock()

	if routeInfo, ok := rootRouter.routes[name]; ok {
		return routeInfo.Host
	}

	return ""
}

func (r *Router) RouteURL(name string, params map[string]any, scheme string) string {
	rootRouter := r.rootParent()
	rootRouter.mu.RLock()
	routeInfo, ok := rootRouter.routes[name]
	rootRouter.mu.RUnlock()
	if !ok {
		return ""
	}

	path := r.routePathWithParams(routeInfo.Path, params)
	if routeInfo.Host == "" {
		return path
	}

	host := routeURLHost(routeInfo.Host, params)
	if scheme == "" {
		return "//" + host + path
	}

	return scheme + "://" + host + path
}

func (r *Router) routePathWithParams(path string, params map[string]any) string {
	for key, value := range params {
		path = strings.ReplaceAll(path, "{"+key+"}", url.PathEscape(toString(value)))
	}

	return path
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
		"isActiveRoute": func(u *url.URL, name string) bool {
			path := r.RoutePath(name)
			if path == "" || u == nil {
				return false
			}

			return strings.HasPrefix(u.Path, path)
		},
		"isActiveRouteExact": func(u *url.URL, name string) bool {
			path := r.RoutePath(name)
			if path == "" || u == nil {
				return false
			}

			return u.Path == path
		},
		"isActiveRouteContains": func(u *url.URL, substr string) bool {
			if u == nil {
				return false
			}

			return strings.Contains(u.Path, substr)
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

	if mux, exists := r.hostMuxes[hostPattern]; exists {
		return mux
	}

	mux := http.NewServeMux()
	r.hostMuxes[hostPattern] = mux
	return mux
}

func (r *Router) registerRouteInfoLocked(hostPattern string, method string, pattern string, handler http.Handler, middlewares []Middleware, register func(*http.ServeMux)) {
	r.ensureMutableLocked()

	routeInfo := RouteInfo{
		Method:      method,
		Path:        routeNamePath(pattern),
		Host:        hostPattern,
		Handler:     handler,
		Middlewares: append([]Middleware{}, middlewares...),
	}

	key := routeKey(routeInfo)
	if _, exists := r.routeKeys[key]; exists {
		panic(fmt.Sprintf("router: duplicate route %s %s %s", routeInfo.Host, routeInfo.Method, routeInfo.Path))
	}
	r.routeKeys[key] = struct{}{}

	register(r.targetMuxLocked(hostPattern))
	r.routeList = append(r.routeList, routeInfo)
}

func (r *Router) mountRouterLocked(hostPattern string, mountPath string, mountedRouter *Router) {
	r.ensureMutableLocked()

	mountedRoot := mountedRouter.rootParent()

	for name, routeInfo := range mountedRoot.routes {
		routeInfo.Host = firstNonEmpty(routeInfo.Host, hostPattern)
		routeInfo.Path = joinPaths(mountPath, routeInfo.Path)
		if existing, exists := r.routes[name]; exists && (existing.Method != routeInfo.Method || existing.Path != routeInfo.Path || existing.Host != routeInfo.Host) {
			panic(fmt.Sprintf("router: duplicate route name %q", name))
		}
		r.routes[name] = routeInfo
	}

	for _, routeInfo := range mountedRoot.routeList {
		routeInfo.Host = firstNonEmpty(routeInfo.Host, hostPattern)
		routeInfo.Path = joinPaths(mountPath, routeInfo.Path)
		key := routeKey(routeInfo)
		if _, exists := r.routeKeys[key]; exists {
			panic(fmt.Sprintf("router: duplicate mounted route %s %s %s", routeInfo.Host, routeInfo.Method, routeInfo.Path))
		}
		r.routeKeys[key] = struct{}{}
		r.routeList = append(r.routeList, routeInfo)
	}

	r.mergeMountedOpenAPILocked(mountPath, mountedRoot)
}

func (r *Router) mergeMountedOpenAPILocked(mountPath string, mountedRoot *Router) {
	if r.openapi == nil || mountedRoot.openapi == nil {
		return
	}

	for path, pathItem := range mountedRoot.openapi.Paths {
		mountedPath := joinPaths(mountPath, path)
		existing := r.openapi.Paths[mountedPath]
		r.openapi.Paths[mountedPath] = mergePathItem(existing, pathItem)
		r.patternMap[mountedPath] = mountedPath
	}

	if r.openapi.Components.Schemas == nil {
		r.openapi.Components.Schemas = make(map[string]Schema)
	}
	for name, schema := range mountedRoot.openapi.Components.Schemas {
		if _, exists := r.openapi.Components.Schemas[name]; !exists {
			r.openapi.Components.Schemas[name] = schema
		}
	}

	if len(mountedRoot.openapi.Components.SecuritySchemes) > 0 {
		if r.openapi.Components.SecuritySchemes == nil {
			r.openapi.Components.SecuritySchemes = make(map[string]SecurityScheme)
		}
		for name, securityScheme := range mountedRoot.openapi.Components.SecuritySchemes {
			if _, exists := r.openapi.Components.SecuritySchemes[name]; !exists {
				r.openapi.Components.SecuritySchemes[name] = securityScheme
			}
		}
	}

	r.openapi.Tags = appendMissingTags(r.openapi.Tags, mountedRoot.openapi.Tags)
	r.openapi.Security = append(r.openapi.Security, mountedRoot.openapi.Security...)
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

func (r *Router) shouldRedirectTrailingSlash(req *http.Request) bool {
	if req.URL.Path == "/" || req.URL.Path == "" || req.URL.Path[len(req.URL.Path)-1] != '/' {
		return false
	}

	rootRouter := r.rootParent()
	host := normalizeRequestHost(req.Host)

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
		if !pathHasPrefix(req.URL.Path, prefix) {
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

func joinPaths(base string, path string) string {
	if base == "" || base == "/" {
		return routeNamePath(path)
	}
	if path == "" || path == "/" {
		return routeNamePath(base)
	}

	return routeNamePath(strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(path, "/"))
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

func routeURLHost(host string, params map[string]any) string {
	if !strings.HasPrefix(host, "*.") {
		return host
	}

	for _, key := range []string{"subdomain", "tenant"} {
		if value, exists := params[key]; exists {
			return url.PathEscape(toString(value)) + strings.TrimPrefix(host, "*")
		}
	}

	return host
}

func routeKey(routeInfo RouteInfo) string {
	return routeInfo.Host + "\x00" + routeInfo.Method + "\x00" + routeInfo.Path
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

func routeNamePath(pattern string) string {
	pattern = strings.ReplaceAll(pattern, "{$}", "")
	if pattern == "" {
		return "/"
	}

	if len(pattern) > 1 {
		pattern = strings.TrimSuffix(pattern, "/")
	}

	return pattern
}

func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return fmt.Sprintf("%d", val)
	case int64:
		return fmt.Sprintf("%d", val)
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
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()
	rootRouter.ensureMutableLocked()

	rootRouter.patternMap[stripPattern] = pattern

	// Get or create RouteInfo for the pattern
	pathItem, exists := rootRouter.openapi.Paths[pattern]
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
}

func (r *Router) rootParent() *Router {
	if r.parent == nil {
		return r
	}
	return r.parent.rootParent()
}

func (r *Router) freeze() {
	rootRouter := r.rootParent()
	rootRouter.freezeOnce.Do(func() {
		rootRouter.mu.Lock()
		rootRouter.frozen = true
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

	// Get the original pattern
	pattern, exists := rootRouter.patternMap[strippedPattern]
	if !exists {
		pattern = strippedPattern // Fallback to strippedPattern if mapping is missing
	}

	// Get methods for the pattern
	routeInfo, exists := rootRouter.openapi.Paths[pattern]
	if !exists || len(routeInfo.Methods()) == 0 {
		return
	}

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
