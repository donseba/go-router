package router

import (
	"fmt"
	"net/http"
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
	Router struct {
		mux                   *http.ServeMux
		basePath              string
		redirectTrailingSlash bool
		openapiDocs           bool
		middlewares           []Middleware
		parent                *Router // Reference to the parent router

		handleStatus map[int]http.HandlerFunc
		patternMap   map[string]string

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
		handleStatus: make(map[int]http.HandlerFunc),
		patternMap:   make(map[string]string),
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

func (r *Router) Group(basePath string, fn func(*Router)) {
	subRouter := &Router{
		basePath:              r.basePath + basePath,
		redirectTrailingSlash: r.redirectTrailingSlash,
		middlewares:           append([]Middleware{}, r.middlewares...),
		parent:                r,
		openapiDocs:           r.openapiDocs,
		handleStatus:          r.handleStatus,
	}

	fn(subRouter)
}

func (r *Router) RedirectTrailingSlash(redirect bool) {
	r.redirectTrailingSlash = redirect
}

func (r *Router) UseOpenapiDocs(use bool) {
	r.openapiDocs = use
}

func (r *Router) HandleStatus(httpStatus int, handler http.HandlerFunc) {
	r.handleStatus[httpStatus] = handler
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
			http.Redirect(w, req, req.URL.Path[:len(req.URL.Path)-1], DefaultRedirectStatusCode)
			return
		}
	}

	interceptor := &routingStatusInterceptWriter{
		ResponseWriter: &excludeHeaderWriter{
			ResponseWriter:  w,
			excludedHeaders: []string{HeaderFlagDoNotIntercept},
		},
		interceptMap: make(map[int]func() bool),
	}

	for k, v := range r.handleStatus {
		interceptor.interceptMap[k] = func() bool {
			return v != nil && w.Header().Get(HeaderFlagDoNotIntercept) == ""
		}
	}

	r.mux.ServeHTTP(interceptor, req)

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
			obj := reflect.ValueOf(docOut.Object)
			if obj.Kind() == reflect.Ptr {
				obj = obj.Elem()
			}

			pType := "object"
			name := obj.Type().Name()
			schema = &Schema{
				Ref: fmt.Sprintf("#/components/schemas/%s", name),
			}

			if _, ok := schemas[name]; !ok {
				if obj.Kind() == reflect.Slice {
					pType = "array"
					elementType := obj.Type().Elem()
					obj = reflect.New(elementType).Elem()
					name = obj.Type().Name()
					schema = &Schema{
						Type: pType,
						Items: &Schema{
							Ref: fmt.Sprintf("#/components/schemas/%s", name),
						},
					}
				}

				properties := make(map[string]Schema)

				for i := 0; i < obj.NumField(); i++ {
					fieldType := obj.Type().Field(i)
					fieldName := fieldType.Name
					jsonTag := fieldType.Tag.Get("json")
					if jsonTag != "" && jsonTag != "-" {
						fieldName = strings.Split(jsonTag, ",")[0]
					}

					fieldKind := fieldType.Type.Kind()
					var typeName string
					switch fieldKind {
					case reflect.String:
						typeName = "string"
					case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
						typeName = "integer"
					case reflect.Float32, reflect.Float64:
						typeName = "number"
					case reflect.Bool:
						typeName = "boolean"
					case reflect.Struct:
						typeName = "object"
					case reflect.Slice, reflect.Array:
						typeName = "array"
					default:
						typeName = "string" // Default to string if unknown
					}

					properties[fieldName] = Schema{
						Type: typeName,
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
		} else {
			// Handle nil docOut.Object by setting schema to nil
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
