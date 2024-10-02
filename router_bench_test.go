package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
