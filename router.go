package router

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
)

var (
	DefaultRedirectTrailingSlash = true
	DefaultUseOpenapiDocs        = false
	OpenApiVersion               = "3.0.1"
)

type (
	Router struct {
		mux                   *http.ServeMux
		basePath              string
		redirectTrailingSlash bool
		openapiDocs           bool
		middlewares           []Middleware
		parent                *Router // Reference to the parent router

		notFoundHandler         http.HandlerFunc
		methodNotAllowedHandler http.HandlerFunc
		internalServerError     http.HandlerFunc

		once    sync.Once
		mu      sync.RWMutex
		openapi *OpenAPI
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
	}
}

func (r *Router) AddServerEndpoint(url string, description string) {
	r.openapi.Servers = append(r.openapi.Servers, Server{
		URL:         url,
		Description: description,
	})
}

func (r *Router) Get(pattern string, handler http.HandlerFunc, doc ...Docs) {
	r.handle(http.MethodGet, pattern, handler, doc...)
}

func (r *Router) Head(pattern string, handler http.HandlerFunc, doc ...Docs) {
	r.handle(http.MethodHead, pattern, handler, doc...)
}

func (r *Router) Post(pattern string, handler http.HandlerFunc, doc ...Docs) {
	r.handle(http.MethodPost, pattern, handler, doc...)
}

func (r *Router) Put(pattern string, handler http.HandlerFunc, doc ...Docs) {
	r.handle(http.MethodPut, pattern, handler, doc...)
}

func (r *Router) Patch(pattern string, handler http.HandlerFunc, doc ...Docs) {
	r.handle(http.MethodPatch, pattern, handler, doc...)
}

func (r *Router) Delete(pattern string, handler http.HandlerFunc, doc ...Docs) {
	r.handle(http.MethodDelete, pattern, handler, doc...)
}

func (r *Router) Group(basePath string, fn func(*Router), op ...Operation) {
	subRouter := &Router{
		basePath:                r.basePath + basePath,
		redirectTrailingSlash:   r.redirectTrailingSlash,
		middlewares:             append([]Middleware{}, r.middlewares...),
		parent:                  r,
		notFoundHandler:         r.notFoundHandler,
		methodNotAllowedHandler: r.methodNotAllowedHandler,
		openapiDocs:             r.openapiDocs,
	}

	fn(subRouter)
}

func (r *Router) RedirectTrailingSlash(redirect bool) {
	r.redirectTrailingSlash = redirect
}

func (r *Router) UseOpenapiDocs(use bool) {
	r.openapiDocs = use
}

func (r *Router) MethodNotAllowed(handler http.HandlerFunc) {
	r.methodNotAllowedHandler = handler
}

func (r *Router) NotFound(handler http.HandlerFunc) {
	r.notFoundHandler = handler
}

func (r *Router) InternalServerError(handler http.HandlerFunc) {
	r.internalServerError = handler
}

func (r *Router) Use(middleware Middleware) {
	r.middlewares = append(r.middlewares, middleware)
}

func (r *Router) ServeFiles(pattern string, fs http.FileSystem) {
	if r.basePath != "" {
		pattern = r.basePath + pattern
	}

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
	// just before serving add all the option handlers based on the openapi paths
	if r.openapiDocs {
		r.once.Do(func() {
			for p, _ := range r.openapi.Paths {
				fmt.Println("Registering options handler for", p)
				r.registerOptionsHandler(p)
			}
		})
	}

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
		intercept500: func() bool {
			return r.internalServerError != nil && w.Header().Get(HeaderFlagDoNotIntercept) == ""
		},
	}

	r.mux.ServeHTTP(interceptor, req)

	switch {
	case interceptor.intercepted && interceptor.statusCode == http.StatusNotFound:
		r.notFoundHandler.ServeHTTP(interceptor.ResponseWriter, req)
	case interceptor.intercepted && interceptor.statusCode == http.StatusInternalServerError:
		r.internalServerError.ServeHTTP(interceptor.ResponseWriter, req)
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

	r.registerRoute(method, pattern, handler)
	if r.openapiDocs {
		r.registerDocs(method, pattern, docs...)
	}
}

func (r *Router) registerRoute(method, pattern string, handler http.HandlerFunc) {
	var (
		fullPattern               = method + " " + pattern
		finalHandler http.Handler = handler
	)

	for i := len(r.middlewares) - 1; i >= 0; i-- {
		finalHandler = r.middlewares[i](finalHandler)
	}

	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()

	rootRouter.mux.Handle(fullPattern, finalHandler)
	return
}

func (r *Router) registerDocs(method, pattern string, docs ...Docs) string {
	if len(docs) == 0 {
		return ""
	}

	var (
		stripPattern = strings.ReplaceAll(pattern, "{$}", "") //strip {$} from the pattern for the docs
		doc          = &docs[0]
	)

	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()

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

	return stripPattern
}

func (r *Router) rootParent() *Router {
	if r.parent == nil {
		return r
	}
	return r.parent.rootParent()
}

func PrependIfMissing(slice []string, s string) []string {
	for _, item := range slice {
		if item == s {
			return slice
		}
	}

	return append([]string{s}, slice...)
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

	var methods []string
	if routeInfo, exists := rootRouter.openapi.Paths[pattern]; exists {
		if routeInfo.Get != nil {
			methods = appendIfMissing(methods, http.MethodGet)
		}
		if routeInfo.Post != nil {
			methods = appendIfMissing(methods, http.MethodPost)
		}
		if routeInfo.Put != nil {
			methods = appendIfMissing(methods, http.MethodPut)
		}
		if routeInfo.Delete != nil {
			methods = appendIfMissing(methods, http.MethodDelete)
		}
		if routeInfo.Patch != nil {
			methods = appendIfMissing(methods, http.MethodPatch)
		}
	}
	return methods
}

func (r *Router) registerOptionsHandler(pattern string) {
	rootRouter := r.rootParent()
	rootRouter.mu.Lock()
	defer rootRouter.mu.Unlock()

	// Get methods for the pattern
	routeInfo, exists := rootRouter.openapi.Paths[pattern]
	if !exists || len(routeInfo.Methods()) == 0 {
		return
	}

	// Create the OPTIONS handler with the Allow header
	methods := PrependIfMissing(routeInfo.Methods(), http.MethodOptions)
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
		obj := reflect.ValueOf(docOut.Object)
		if obj.Kind() == reflect.Ptr {
			obj = obj.Elem()
		}

		pType := "object"
		name := obj.Type().Name()
		schema := Schema{
			Ref: fmt.Sprintf("#/components/schemas/%s", name),
		}

		if _, ok := schemas[name]; !ok {
			if obj.Kind() == reflect.Slice {
				pType = "array"
				elementType := obj.Type().Elem()
				obj = reflect.New(elementType).Elem()
				name = obj.Type().Name()
				schema = Schema{
					Type: pType,
					Items: &Schema{
						Ref: fmt.Sprintf("#/components/schemas/%s", name),
					},
				}
			}

			properties := make(map[string]Schema)

			for i := 0; i < obj.NumField(); i++ {
				field := obj.Field(i)
				fieldName := obj.Type().Field(i).Name
				fieldType := field.Type().Name()

				properties[fieldName] = Schema{
					Type: fieldType,
				}
			}

			if componentSchemas == nil {
				componentSchemas = make(map[string]Schema)
			}

			componentSchemas[name] = Schema{
				Type:       pType,
				Properties: properties,
			}
		}

		if routeResponse == nil {
			routeResponse = make(map[string]Response)
		}

		routeResponse[responseCode] = Response{
			Description: docOut.Description,
			Content: map[string]MediaType{
				docOut.ApplicationType: {
					Schema: &schema,
				},
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
		obj := reflect.ValueOf(docIn.Object)
		if obj.Kind() == reflect.Ptr {
			obj = obj.Elem()
		}

		name := obj.Type().Name()
		if _, ok := schemas[name]; !ok {
			properties := make(map[string]Schema)
			for i := 0; i < obj.NumField(); i++ {
				field := obj.Field(i)
				fieldName := obj.Type().Field(i).Name
				fieldType := field.Type().Name()

				properties[fieldName] = Schema{
					Type: fieldType,
				}
			}

			if componentSchemas == nil {
				componentSchemas = make(map[string]Schema)
			}

			componentSchemas[name] = Schema{
				Type:       "object",
				Properties: properties,
			}
		}

		if requestBody == nil {
			requestBody = &RequestBody{
				Content: make(map[string]MediaType),
			}
		}

		requestBody.Content[contentType] = MediaType{
			Schema: &Schema{
				Ref: fmt.Sprintf("#/components/schemas/%s", name),
			},
		}
	}

	return componentSchemas, requestBody
}

// OpenAPI returns the root documentation tree
func (r *Router) OpenAPI() *OpenAPI {
	rootRouter := r.rootParent()
	return rootRouter.openapi
}
