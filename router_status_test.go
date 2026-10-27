package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func notFoundRouter() *Router {
	r := NewSite()
	r.HandleStatus(http.StatusNotFound, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, "router 404")
	})

	return r
}

func ownNotFound(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = fmt.Fprint(w, "own 404")
}

// unwrapWriter is a middleware response writer that implements Unwrap.
type unwrapWriter struct {
	http.ResponseWriter
}

func (w *unwrapWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func wrapWriter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		next.ServeHTTP(&unwrapWriter{ResponseWriter: w}, req)
	})
}

func TestNoInterceptKeepsAResponse(t *testing.T) {
	r := notFoundRouter()
	r.Use(wrapWriter)
	r.Get("/pages/{slug}", func(w http.ResponseWriter, req *http.Request) {
		if req.PathValue("slug") == "missing" {
			NoIntercept(w)
			ownNotFound(w, req)

			return
		}

		w.WriteHeader(http.StatusNotFound)
	})

	if got := serve(t, r, http.MethodGet, "/pages/missing"); got != "own 404" {
		t.Fatalf("expected the handler's own 404, got %q", got)
	}

	if got := serve(t, r, http.MethodGet, "/pages/other"); got != "router 404" {
		t.Fatalf("expected NoIntercept to apply to one response only, got %q", got)
	}

	// Without status handlers it does nothing.
	plain := NewSite()
	plain.Get("/", func(w http.ResponseWriter, req *http.Request) {
		NoIntercept(w)
		ownNotFound(w, req)
	})

	w := httptest.NewRecorder()
	plain.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if w.Code != http.StatusNotFound || w.Body.String() != "own 404" || len(w.Header()) > 1 {
		t.Fatalf("expected the plain response, got %d %q %v", w.Code, w.Body.String(), w.Header())
	}
}

// TestNoInterceptInMountedRouter checks that NoIntercept in a mounted
// router keeps a response from the status handlers of the router it is
// mounted on.
func TestNoInterceptInMountedRouter(t *testing.T) {
	parent := notFoundRouter()
	child := NewSite()
	parent.Use(wrapWriter)
	parent.Mount("/docs", child)
	child.Get("/{page}", func(w http.ResponseWriter, req *http.Request) {
		NoIntercept(w)
		ownNotFound(w, req)
	})
	child.Get("/other/{page}", ownNotFound)

	if got := serve(t, parent, http.MethodGet, "/docs/missing"); got != "own 404" {
		t.Fatalf("expected the mounted route's own 404, got %q", got)
	}

	if got := serve(t, parent, http.MethodGet, "/docs/other/missing"); got != "router 404" {
		t.Fatalf("expected the parent's 404 for other routes, got %q", got)
	}
}

type aroundKey struct{}

func TestUseAllWrapsUnmatchedResponses(t *testing.T) {
	r := notFoundRouter()
	r.RedirectTrailingSlash(true)

	var seen []string

	tag := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				seen = append(seen, name+" "+req.URL.Path)
				w.Header().Add("X-Around", name)
				next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), aroundKey{}, name)))
			})
		}
	}

	r.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, req.Context().Value(aroundKey{}), " ", req.PathValue("id"))
	})
	r.Post("/form", noopHandler)
	r.UseAll(tag("a"), tag("b"))

	tests := []struct {
		method string
		target string
		code   int
	}{
		{method: http.MethodGet, target: "/users/7", code: http.StatusOK},
		{method: http.MethodGet, target: "/missing", code: http.StatusNotFound},
		{method: http.MethodGet, target: "/form", code: http.StatusMethodNotAllowed},
		{method: http.MethodGet, target: "/users/7/", code: DefaultRedirectStatusCode},
	}

	for _, tt := range tests {
		seen = nil
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tt.method, tt.target, nil))

		if w.Code != tt.code {
			t.Fatalf("%s %s: expected %d, got %d", tt.method, tt.target, tt.code, w.Code)
		}

		if got := strings.Join(w.Header().Values("X-Around"), ","); got != "a,b" {
			t.Fatalf("%s %s: expected the UseAll middleware in order, got %q", tt.method, tt.target, got)
		}

		if want := "a " + tt.target + "|b " + tt.target; strings.Join(seen, "|") != want {
			t.Fatalf("%s %s: expected %q, got %q", tt.method, tt.target, want, strings.Join(seen, "|"))
		}
	}

	if got := serve(t, r, http.MethodGet, "/users/7"); got != "b 7" {
		t.Fatalf("expected the handler to get the context of the UseAll middleware, got %q", got)
	}
}

func TestUseAllInMountedRouterSeesPublicPath(t *testing.T) {
	parent := NewSite()
	child := NewSite()
	parent.Mount("/admin", child)

	var seen []string

	child.UseAll(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			seen = append(seen, req.URL.Path)
			next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), aroundKey{}, "around")))
		})
	})
	child.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, req.URL.Path, " ", req.PathValue("id"), " ", req.Context().Value(aroundKey{}))
	})

	if got := serve(t, parent, http.MethodGet, "/admin/users/7"); got != "/admin/users/7 7 around" {
		t.Fatalf("expected the mounted route to match below the mount, got %q", got)
	}

	w := httptest.NewRecorder()
	parent.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/missing", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	if strings.Join(seen, ",") != "/admin/users/7,/admin/missing" {
		t.Fatalf("expected UseAll of the mounted router to see public paths, got %v", seen)
	}
}

// panics reports whether fn panics.
func panics(fn func()) (panicked bool) {
	defer func() {
		panicked = recover() != nil
	}()

	fn()

	return false
}

func TestUseAllRules(t *testing.T) {
	r := NewSite()

	r.Group("/admin", func(admin *Router) {
		onGroup := panics(func() {
			admin.UseAll(wrapWriter)
		})
		if !onGroup {
			t.Fatal("expected UseAll on a group to panic")
		}
	})

	r.Get("/", noopHandler)

	afterRoutes := panics(func() {
		r.UseAll(wrapWriter)
	})
	if afterRoutes {
		t.Fatal("expected UseAll after routes to work")
	}

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	afterRequest := panics(func() {
		r.UseAll(wrapWriter)
	})
	if !afterRequest {
		t.Fatal("expected UseAll after the first request to panic")
	}
}

func TestAsChains(t *testing.T) {
	r := NewSite()

	var used bool

	r.Get("/users/{id}", noopHandler).As("users.show").Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			used = true
			next.ServeHTTP(w, req)
		})
	})

	serve(t, r, http.MethodGet, "/users/1")

	if !used || r.RoutePath("users.show") != "/users/{id}" {
		t.Fatalf("expected As to return the route for chaining, used %v, path %q", used, r.RoutePath("users.show"))
	}
}

func TestRouterMountedTwiceResolvesThroughFirstMount(t *testing.T) {
	first := NewSite()
	second := NewSite()
	child := NewSite()
	child.Get("/{$}", noopHandler).As("child.home")

	first.Mount("/a", child)
	second.Mount("/b", child)

	if got := child.RoutePath("child.home"); got != "/a/" {
		t.Fatalf("expected the child to resolve through its first mount, got %q", got)
	}

	if got := second.RoutePath("child.home"); got != "/b/" {
		t.Fatalf("expected the second parent to resolve its own mount, got %q", got)
	}

	if got := first.RoutePath("child.home"); got != "/a/" {
		t.Fatalf("expected the first parent to resolve its own mount, got %q", got)
	}
}

func TestRouterMountedTwiceOnOneRouterPanicsOnNames(t *testing.T) {
	parent := NewSite()
	child := NewSite()
	child.Get("/{$}", noopHandler).As("child.home")
	parent.Mount("/a", child)

	if _, err := parent.TryMount("/b", child); err == nil || !strings.Contains(err.Error(), "duplicate route name") {
		t.Fatalf("expected a named router mounted twice on one router to fail, got %v", err)
	}
}

func TestUseAllKeepsTheHotPathFree(t *testing.T) {
	r := NewSite()
	r.UseAll(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)
		})
	})
	// A static path: the ServeMux allocates for wildcards.
	r.Get("/users", noopHandler)

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	w := newBenchmarkResponseWriter()

	allocs := testing.AllocsPerRun(100, func() {
		w.Reset()
		r.ServeHTTP(w, req)
	})

	if allocs != 0 {
		t.Fatalf("expected no allocations, got %v", allocs)
	}
}
