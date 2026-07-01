package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNamedRoutes(t *testing.T) {
	mux := http.NewServeMux()
	r := New(mux, "Example API", "1.0.0")

	r.Get("/{$}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, "home")
	}).As("home")

	r.Group("/users", func(users *Router) {
		users.Get("/{id}", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintf(w, "user %s", req.PathValue("id"))
		}).As("users.show")
	})

	if got := r.RoutePath("home"); got != "/" {
		t.Fatalf("expected home route path %q, got %q", "/", got)
	}

	if got := r.RoutePath("users.show"); got != "/users/{id}" {
		t.Fatalf("expected users.show route path %q, got %q", "/users/{id}", got)
	}

	if got := r.RoutePathWithParams("users.show", map[string]any{"id": 123}); got != "/users/123" {
		t.Fatalf("expected route path with params %q, got %q", "/users/123", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/users/123", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if got := w.Body.String(); got != "user 123" {
		t.Fatalf("expected response %q, got %q", "user 123", got)
	}
}

func TestFuncMap(t *testing.T) {
	mux := http.NewServeMux()
	r := New(mux, "Example API", "1.0.0")
	r.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {}).As("users.show")

	funcMap := r.FuncMap()

	routePath := funcMap["routePath"].(func(string) string)
	if got := routePath("users.show"); got != "/users/{id}" {
		t.Fatalf("expected routePath helper to return %q, got %q", "/users/{id}", got)
	}

	routePathWithParams := funcMap["routePathWithParams"].(func(string, map[string]any) string)
	if got := routePathWithParams("users.show", map[string]any{"id": int64(456)}); got != "/users/456" {
		t.Fatalf("expected routePathWithParams helper to return %q, got %q", "/users/456", got)
	}
}
