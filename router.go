package router

import (
	"net/http"
	"strings"
	"sync"
)

var (
	DefaultRedirectTrailingSlash = true
)

type (
	Router struct {
		mux                   *http.ServeMux
		basePath              string
		redirectTrailingSlash bool
		middlewares           []Middleware
		parent                *Router // Reference to the parent router

		notFoundHandler         http.HandlerFunc
		methodNotAllowedHandler http.HandlerFunc

		routes map[string]*RouteInfo
		docs   *RouteInfo
		mu     sync.RWMutex

		openapi *OpenAPI // <-- new openapi locations
	}

	RouteInfo struct {
		Title       string       `json:"Title,omitempty"`
		Description string       `json:"Description,omitempty"`
		Params      []DocsParam  `json:"Params,omitempty"`
		Path        string       `json:"Path,omitempty"`
		Methods     []string     `json:"Methods,omitempty"`
		Routes      []*RouteInfo `json:"Routes,omitempty"`
	}

	Docs struct {
		Title       string
		Description string
		Params      []DocsParam
	}

	DocsParam struct {
		Name        string `json:"Name,omitempty"`
		Type        string `json:"Type,omitempty"`
		Description string `json:"Description,omitempty"`
	}

	Middleware func(http.Handler) http.Handler
)

func New(ht *http.ServeMux) *Router {
	return &Router{
		mux:                   ht,
		redirectTrailingSlash: DefaultRedirectTrailingSlash,
		routes:                make(map[string]*RouteInfo),
		docs: &RouteInfo{
			Path:   "/",
			Routes: []*RouteInfo{},
		},
	}
}

func NewDefault() *Router {
	return &Router{
		mux:                   http.NewServeMux(),
		redirectTrailingSlash: DefaultRedirectTrailingSlash,
		routes:                make(map[string]*RouteInfo),
		docs: &RouteInfo{
			Path:   "/",
			Routes: []*RouteInfo{},
		},
	}
}

func (r *Router) Get(pattern string, handler http.HandlerFunc, docs ...Docs) {
	r.handle(http.MethodGet, pattern, handler, docs...)
}

func (r *Router) Head(pattern string, handler http.HandlerFunc, docs ...Docs) {
	r.handle(http.MethodHead, pattern, handler, docs...)
}

func (r *Router) Post(pattern string, handler http.HandlerFunc, docs ...Docs) {
	r.handle(http.MethodPost, pattern, handler, docs...)
}

func (r *Router) Put(pattern string, handler http.HandlerFunc, docs ...Docs) {
	r.handle(http.MethodPut, pattern, handler, docs...)
}

func (r *Router) Patch(pattern string, handler http.HandlerFunc, docs ...Docs) {
	r.handle(http.MethodPatch, pattern, handler, docs...)
}

func (r *Router) Delete(pattern string, handler http.HandlerFunc, docs ...Docs) {
	r.handle(http.MethodDelete, pattern, handler, docs...)
}

func (r *Router) Group(basePath string, fn func(*Router), docs ...Docs) {
	subRouter := &Router{
		mux:                     r.mux,
		basePath:                r.basePath + basePath,
		redirectTrailingSlash:   r.redirectTrailingSlash,
		middlewares:             append([]Middleware{}, r.middlewares...),
		parent:                  r,
		notFoundHandler:         r.notFoundHandler,
		methodNotAllowedHandler: r.methodNotAllowedHandler,
	}

	// Create RouteInfo for this group
	groupPath := subRouter.basePath
	groupDocs := &RouteInfo{
		Path:   groupPath,
		Routes: []*RouteInfo{},
	}

	if len(docs) > 0 {
		groupDocs.Title = docs[0].Title
		groupDocs.Description = docs[0].Description
		groupDocs.Params = docs[0].Params
	}

	// Add the group's RouteInfo to the parent's docs
	if r.docs != nil {
		r.docs.Routes = append(r.docs.Routes, groupDocs)
	} else {
		rootRouter := r.rootParent()
		rootRouter.docs.Routes = append(rootRouter.docs.Routes, groupDocs)
	}

	// Store in routes map
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	rootRouter.routes[groupPath] = groupDocs
	rootRouter.mu.Unlock()

	// Assign the docs to the subRouter
	subRouter.docs = groupDocs

	fn(subRouter)
}

func (r *Router) RedirectTrailingSlash(redirect bool) {
	r.redirectTrailingSlash = redirect
}

func (r *Router) MethodNotAllowed(handler http.HandlerFunc) {
	r.methodNotAllowedHandler = handler
}

func (r *Router) NotFound(handler http.HandlerFunc) {
	r.notFoundHandler = handler
}

func (r *Router) Use(middleware Middleware) {
	r.middlewares = append(r.middlewares, middleware)
}

func (r *Router) ServeFiles(pattern string, fs http.FileSystem) {
	if r.basePath != "" {
		pattern = r.basePath + pattern
	}

	// Ensure pattern ends with "/" for directory serving
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
	r.mux.Handle("GET "+pattern, finalHandler)
}

func (r *Router) ServeFile(pattern string, filepath string) {
	if r.basePath != "" {
		pattern = r.basePath + pattern
	}

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
	r.mux.Handle(fullPattern, finalHandler)
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r.redirectTrailingSlash {
		if req.URL.Path != "/" && req.URL.Path[len(req.URL.Path)-1] == '/' {
			http.Redirect(w, req, req.URL.Path[:len(req.URL.Path)-1], http.StatusMovedPermanently)
			return
		}
	}

	interceptor := &routingStatusInterceptWriter{
		ResponseWriter: &excludeHeaderWriter{
			ResponseWriter:  w,
			excludedHeaders: []string{HeaderFlagDoNotIntercept},
		},
		intercept404: func() bool {
			return r.notFoundHandler != nil && w.Header().Get(HeaderFlagDoNotIntercept) == ""
		},
		intercept405: func() bool {
			return r.methodNotAllowedHandler != nil && w.Header().Get(HeaderFlagDoNotIntercept) == ""
		},
	}

	r.mux.ServeHTTP(interceptor, req)

	switch {
	case interceptor.intercepted && interceptor.statusCode == http.StatusNotFound:
		r.notFoundHandler.ServeHTTP(interceptor.ResponseWriter, req)
	case interceptor.intercepted && interceptor.statusCode == http.StatusMethodNotAllowed:
		// Set the Allow header
		pattern := req.URL.Path
		allowedMethods := r.getMethodsForPattern(pattern)
		if len(allowedMethods) > 0 {
			interceptor.ResponseWriter.Header().Set("Allow", strings.Join(allowedMethods, ", "))
		}

		r.methodNotAllowedHandler.ServeHTTP(interceptor.ResponseWriter, req)
	}
}

func (r *Router) handle(method, pattern string, handler http.HandlerFunc, docs ...Docs) {
	if r.basePath != "" {
		pattern = r.basePath + pattern
	}
	if pattern == "" {
		pattern = "/"
	} else if pattern[0] != '/' {
		pattern = "/" + pattern
	}

	fullPattern := method + " " + pattern

	var finalHandler http.Handler = handler
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		finalHandler = r.middlewares[i](finalHandler)
	}

	// Register OPTIONS handler
	r.registerOptionsHandler(pattern)

	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()

	// Get or create RouteInfo for the pattern
	routeInfo, exists := rootRouter.routes[pattern]
	if !exists {
		// Create new RouteInfo
		routeInfo = &RouteInfo{
			Path:    pattern,
			Methods: []string{method},
		}

		if len(docs) > 0 {
			routeInfo.Title = docs[0].Title
			routeInfo.Description = docs[0].Description
			routeInfo.Params = docs[0].Params
		}

		// Add to the current router's docs
		if r.docs != nil {
			r.docs.Routes = append(r.docs.Routes, routeInfo)
		} else {
			rootRouter.docs.Routes = append(rootRouter.docs.Routes, routeInfo)
		}

		// Store in routes map
		rootRouter.routes[pattern] = routeInfo
	} else {
		// Update existing RouteInfo
		routeInfo.Methods = appendIfMissing(routeInfo.Methods, method)
		if len(docs) > 0 {
			// Update Title, Description, Params if provided
			routeInfo.Title = docs[0].Title
			routeInfo.Description = docs[0].Description
			routeInfo.Params = docs[0].Params
		}
	}

	rootRouter.mux.Handle(fullPattern, finalHandler)
}

func (r *Router) rootParent() *Router {
	if r.parent == nil {
		return r
	}
	return r.parent.rootParent()
}

func appendIfMissing(slice []string, s string) []string {
	for _, item := range slice {
		if item == s {
			return slice
		}
	}
	return append(slice, s)
}

func (r *Router) getMethodsForPattern(pattern string) []string {
	rootRouter := r.rootParent()
	rootRouter.mu.RLock()
	defer rootRouter.mu.RUnlock()
	if routeInfo, exists := rootRouter.routes[pattern]; exists {
		return routeInfo.Methods
	}
	return nil
}

func (r *Router) registerOptionsHandler(pattern string) {
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()

	fullPattern := "OPTIONS " + pattern

	// Check if OPTIONS handler is already registered
	if _, exists := rootRouter.routes[fullPattern]; exists {
		return
	}

	// Get methods for the pattern
	routeInfo, exists := rootRouter.routes[pattern]
	if !exists || len(routeInfo.Methods) == 0 {
		return
	}

	methods := appendIfMissing(routeInfo.Methods, http.MethodOptions)

	// Create the OPTIONS handler
	optionsHandler := func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Allow", strings.Join(methods, ", "))
		w.WriteHeader(http.StatusNoContent)
	}

	// Register the handler
	rootRouter.mux.HandleFunc(fullPattern, optionsHandler)

	// Store the OPTIONS handler in routes
	rootRouter.routes[fullPattern] = &RouteInfo{
		Path:    pattern,
		Methods: []string{http.MethodOptions},
	}
}

func (r *Router) GetRoutes() map[string]*RouteInfo {
	rootRouter := r.rootParent()
	return rootRouter.routes
}

// GetDocs returns the root documentation tree
func (r *Router) GetDocs() *RouteInfo {
	rootRouter := r.rootParent()
	return rootRouter.docs
}
