package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

type benchmarkResponseWriter struct {
	header http.Header
	status int
}

func newBenchmarkResponseWriter() *benchmarkResponseWriter {
	return &benchmarkResponseWriter{
		header: make(http.Header),
		status: http.StatusOK,
	}
}

func (w *benchmarkResponseWriter) Header() http.Header {
	return w.header
}

func (w *benchmarkResponseWriter) Write(data []byte) (int, error) {
	return len(data), nil
}

func (w *benchmarkResponseWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}

func (w *benchmarkResponseWriter) Reset() {
	clear(w.header)
	w.status = http.StatusOK
}

// BenchmarkRouter measures the performance of the router under load.
func BenchmarkRouter(b *testing.B) {
	// Set up the router
	mux := http.NewServeMux()
	router := New(mux, "Example API", "1.0.0")

	// Register a large number of routes to simulate complexity
	numRoutes := 1000

	// Register routes at the root level
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("/users/%d", i)
		router.Get(path, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "User")
		})
	}

	// Register routes within a group
	router.Group("/api", func(api *Router) {
		for i := 0; i < numRoutes; i++ {
			path := fmt.Sprintf("/items/%d", i)
			api.Get(path, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "Item")
			})
		}
	})

	// Create test requests for benchmarking
	requests := make([]*http.Request, 0, numRoutes*2)
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("/users/%d", i)
		req := httptest.NewRequest("GET", path, nil)
		requests = append(requests, req)
	}
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("/api/items/%d", i)
		req := httptest.NewRequest("GET", path, nil)
		requests = append(requests, req)
	}

	// Reset the timer to exclude setup time
	b.ResetTimer()

	// Run the benchmark
	for i := 0; i < b.N; i++ {
		for _, req := range requests {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Result().StatusCode != http.StatusOK {
				b.Errorf("Expected status 200, got %d", w.Result().StatusCode)
			}
		}
	}
}

func BenchmarkRouterVsServeMuxRouter(b *testing.B) {
	mux := http.NewServeMux()
	router := New(mux, "Example API", "1.0.0")
	benchRegisterRouterRoutes(router, 1000)
	requests := benchRequests(1000)
	w := newBenchmarkResponseWriter()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, req := range requests {
			w.Reset()
			router.ServeHTTP(w, req)
			if w.status != http.StatusOK {
				b.Fatalf("expected status 200, got %d", w.status)
			}
		}
	}
}

func BenchmarkRouterVsServeMuxStdlib(b *testing.B) {
	mux := http.NewServeMux()
	benchRegisterServeMuxRoutes(mux, 1000)
	requests := benchRequests(1000)
	w := newBenchmarkResponseWriter()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, req := range requests {
			w.Reset()
			mux.ServeHTTP(w, req)
			if w.status != http.StatusOK {
				b.Fatalf("expected status 200, got %d", w.status)
			}
		}
	}
}

func benchRegisterRouterRoutes(router *Router, numRoutes int) {
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("/users/%d", i)
		router.Get(path, func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, "User")
		})
	}

	router.Group("/api", func(api *Router) {
		for i := 0; i < numRoutes; i++ {
			path := fmt.Sprintf("/items/%d", i)
			api.Get(path, func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, "Item")
			})
		}
	})
}

func benchRegisterServeMuxRoutes(mux *http.ServeMux, numRoutes int) {
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("GET /users/%d", i)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, "User")
		})
	}

	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("GET /api/items/%d", i)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, "Item")
		})
	}
}

func benchRequests(numRoutes int) []*http.Request {
	requests := make([]*http.Request, 0, numRoutes*2)
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("/users/%d", i)
		requests = append(requests, httptest.NewRequest(http.MethodGet, path, nil))
	}
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("/api/items/%d", i)
		requests = append(requests, httptest.NewRequest(http.MethodGet, path, nil))
	}

	return requests
}

func BenchmarkHostRouting(b *testing.B) {
	mux := http.NewServeMux()
	router := New(mux, "Example API", "1.0.0")
	router.Subdomain("*", "example.com", func(tenant *Router) {
		tenant.Get("/dashboard", func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, "Dashboard")
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Host = "acme.example.com"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}

func BenchmarkRoutePathWithParams(b *testing.B) {
	mux := http.NewServeMux()
	router := New(mux, "Example API", "1.0.0")
	router.Get("/users/{id}/files/{name}", func(w http.ResponseWriter, r *http.Request) {}).As("files.show")

	params := map[string]any{
		"id":   123,
		"name": "hello world/a",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = router.RoutePathWithParams("files.show", params)
	}
}

func BenchmarkRouteURL(b *testing.B) {
	mux := http.NewServeMux()
	router := New(mux, "Example API", "1.0.0")
	router.Host("admin.example.com", func(admin *Router) {
		admin.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) {}).As("admin.users.show")
	})

	params := map[string]any{"id": 123}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = router.RouteURL("admin.users.show", params, "https")
	}
}

func BenchmarkRouteTable(b *testing.B) {
	mux := http.NewServeMux()
	router := New(mux, "Example API", "1.0.0")
	for i := 0; i < 1000; i++ {
		path := fmt.Sprintf("/routes/%d", i)
		router.Get(path, func(w http.ResponseWriter, r *http.Request) {}).As(fmt.Sprintf("routes.%d", i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = router.RouteTable()
	}
}
