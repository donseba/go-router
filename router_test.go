package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donseba/go-router/middleware"
)

type testStruct struct {
	method        string
	routePath     string
	requestPath   string
	requestMethod string
	handler       http.HandlerFunc
	result        string

	groupPath   string
	groupRoutes []testStruct
}

func TestRouter(t *testing.T) {
	mux := http.NewServeMux()
	r := New(mux, "Example API", "1.0.0")

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "whoopsiedaisy page not found", http.StatusNotFound)
	})

	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "whoopsiedaisy method not allowed", http.StatusMethodNotAllowed)
	})

	r.Use(middleware.Timer)

	var tests = []testStruct{
		{
			method:      http.MethodGet,
			routePath:   "/{$}",
			requestPath: "/",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, err := fmt.Fprint(w, "Welcome to the Home Page")
				if err != nil {
					t.Error(err)
				}
			},
			result: "Welcome to the Home Page",
		},
		{
			method:      http.MethodPost,
			routePath:   "/search",
			requestPath: "/search",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, err := fmt.Fprint(w, "Search Results")
				if err != nil {
					t.Error(err)
				}
			},
			result: "Search Results",
		},
		{
			method:      http.MethodGet,
			routePath:   "/users/{id}",
			requestPath: "/users/123",
			handler: func(w http.ResponseWriter, r *http.Request) {
				userID := r.PathValue("id")
				_, err := fmt.Fprintf(w, "User ID: %s", userID)
				if err != nil {
					t.Error(err)
				}
			},
			result: "User ID: 123",
		},
		{
			groupPath: "/api",
			groupRoutes: []testStruct{
				{
					method:      http.MethodGet,
					routePath:   "/users",
					requestPath: "/api/users",
					handler: func(w http.ResponseWriter, r *http.Request) {
						_, err := fmt.Fprint(w, "API Users")
						if err != nil {
							t.Error(err)
						}
					},
					result: "API Users",
				},
				{
					method:      http.MethodPost,
					routePath:   "/users",
					requestPath: "/api/users",
					handler: func(w http.ResponseWriter, r *http.Request) {
						_, err := fmt.Fprint(w, "API Create User")
						if err != nil {
							t.Error(err)
						}
					},
					result: "API Create User",
				},
			},
		},
		{
			method:      http.MethodPatch,
			routePath:   "/users/{id}",
			requestPath: "/users/123",
			handler: func(w http.ResponseWriter, r *http.Request) {
				userID := r.PathValue("id")
				_, err := fmt.Fprintf(w, "Updated User ID: %s", userID)
				if err != nil {
					t.Error(err)
				}
			},
			result: "Updated User ID: 123",
		},
		{
			method:        http.MethodPut,
			routePath:     "/users",
			requestPath:   "/users",
			requestMethod: http.MethodDelete,
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, err := fmt.Fprint(w, "This should not be called")
				if err != nil {
					t.Error(err)
				}
			},
			result: "whoopsiedaisy method not allowed\n",
		},
		{
			method:      http.MethodGet,
			routePath:   "/some-random-url",
			requestPath: "/invalid",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, err := fmt.Fprint(w, "This should not be called")
				if err != nil {
					t.Error(err)
				}
			},
			result: "whoopsiedaisy page not found\n",
		},
	}

	var registerRoutes func(r *Router, tests []testStruct)

	registerRoutes = func(r *Router, tests []testStruct) {
		for _, tt := range tests {
			if tt.groupPath != "" {
				r.Group(tt.groupPath, func(subRouter *Router) {
					registerRoutes(subRouter, tt.groupRoutes)
				})
			} else {
				switch tt.method {
				case http.MethodGet:
					r.Get(tt.routePath, tt.handler)
				case http.MethodPost:
					r.Post(tt.routePath, tt.handler)
				case http.MethodPut:
					r.Put(tt.routePath, tt.handler)
				case http.MethodDelete:
					r.Delete(tt.routePath, tt.handler)
				case http.MethodHead:
					r.Head(tt.routePath, tt.handler)
				case http.MethodPatch:
					r.Patch(tt.routePath, tt.handler)
				default:
					t.Errorf("Unsupported method: %s", tt.method)
				}
			}
		}
	}

	// Register all routes before testing
	registerRoutes(r, tests)

	// Create test server
	ht := httptest.NewServer(r)
	defer ht.Close()

	// Function to recursively run tests
	var runTests func(basePath string, tests []testStruct)
	runTests = func(basePath string, tests []testStruct) {
		for _, tt := range tests {
			if tt.groupPath != "" {
				newBasePath := basePath + tt.groupPath
				runTests(newBasePath, tt.groupRoutes)
			} else {
				method := tt.method
				if tt.requestMethod != "" {
					method = tt.requestMethod
				}

				req := httptest.NewRequest(method, tt.requestPath, nil)
				w := httptest.NewRecorder()

				r.ServeHTTP(w, req)

				if w.Body.String() != tt.result {
					t.Errorf("For path %s, expected %q, got %q", tt.requestPath, tt.result, w.Body.String())
				}
			}
		}
	}

	// Run the tests
	runTests("", tests)
}
