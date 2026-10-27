package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func text(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, body)
	}
}

func status(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(code)
		_, _ = fmt.Fprint(w, body)
	}
}

func record(handler http.Handler, method string, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(method, target, nil))

	return w
}

func TestNotFoundAnswersOnlyUnmatchedRequests(t *testing.T) {
	r := NewSite()
	r.NotFound(status(http.StatusNotFound, "site 404"))

	// Use still works after NotFound, and runs around it.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Use", "yes")
			next.ServeHTTP(w, req)
		})
	})

	r.Get("/users", text("users"))
	r.Get("/codes/{code}", status(http.StatusNotFound, "no such code"))
	r.Get("/docs/", text("docs"))

	// The ServeMux's own redirect to the path with a trailing slash stays.
	plain := NewSite()
	plain.Get("/docs/", text("docs"))
	redirect := record(plain, http.MethodGet, "/docs").Code

	tests := []struct {
		method string
		target string
		code   int
		body   string
		allow  string
	}{
		{http.MethodGet, "/users", http.StatusOK, "users", ""},
		{http.MethodGet, "/missing", http.StatusNotFound, "site 404", ""},
		{http.MethodGet, "/codes/x", http.StatusNotFound, "no such code", ""},
		{http.MethodPost, "/users", http.StatusMethodNotAllowed, "Method Not Allowed\n", "GET, HEAD"},
		{http.MethodHead, "/users", http.StatusOK, "users", ""},
		{http.MethodGet, "/docs", redirect, "", ""},
	}

	for _, test := range tests {
		w := record(r, test.method, test.target)
		if w.Code != test.code || (test.body != "" && w.Body.String() != test.body) || w.Header().Get("Allow") != test.allow {
			t.Errorf("%s %s: got %d %q Allow %q, want %d %q Allow %q",
				test.method, test.target, w.Code, w.Body.String(), w.Header().Get("Allow"), test.code, test.body, test.allow)
		}
	}

	if w := record(r, http.MethodGet, "/missing"); w.Header().Get("X-Use") != "yes" {
		t.Fatalf("NotFound did not run inside Use middleware")
	}

	var routes int
	_ = r.Walk(func(RouteInfo) error {
		routes++

		return nil
	})

	if routes != 3 {
		t.Fatalf("NotFound is a route: %d routes", routes)
	}
}

func TestMethodNotAllowedHandler(t *testing.T) {
	r := NewSite(WithMethodNotAllowed(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = fmt.Fprint(w, "allowed: "+w.Header().Get("Allow"))
	})))
	r.Get("/items", noopHandler)
	r.Delete("/items", noopHandler)

	w := record(r, http.MethodPut, "/items")
	if w.Code != http.StatusMethodNotAllowed || w.Body.String() != "allowed: DELETE, GET, HEAD" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}

	// Without a NotFound handler a missing path is the ServeMux's 404.
	if w := record(r, http.MethodGet, "/missing"); w.Code != http.StatusNotFound || w.Body.String() != "404 page not found\n" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

func TestNotFoundOfGroupsAndHosts(t *testing.T) {
	r := NewSite(WithNotFound(text("site")))
	r.Group("/api", func(api *Router) {
		api.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				next.ServeHTTP(w, req)
			})
		})
		api.NotFound(text(`{"error":"not found"}`))
		api.Get("/users", noopHandler)
	})
	r.Host("admin.example.com", func(admin *Router) {
		admin.NotFound(text("admin"))
		admin.Get("/{$}", noopHandler)
	})
	r.Get("/apis", noopHandler)

	tests := map[string]string{
		"http://example.com/missing":       "site",
		"http://example.com/api/missing":   `{"error":"not found"}`,
		"http://example.com/api":           `{"error":"not found"}`,
		"http://example.com/apiary":        "site",
		"http://admin.example.com/missing": "admin",
	}

	for target, want := range tests {
		w := record(r, http.MethodGet, target)
		if w.Body.String() != want {
			t.Errorf("%s: got %q, want %q", target, w.Body.String(), want)
		}
	}

	if got := record(r, http.MethodGet, "/api/missing").Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("group middleware did not run: %q", got)
	}
}

func TestNotFoundIsNotIntercepted(t *testing.T) {
	r := NewSite()
	r.HandleStatus(http.StatusNotFound, status(http.StatusNotFound, "status handler"))
	r.NotFound(status(http.StatusNotFound, "not found handler"))
	r.Get("/gone", status(http.StatusNotFound, "own"))

	if body := serve(t, r, http.MethodGet, "/missing"); body != "not found handler" {
		t.Fatalf("got %q", body)
	}

	// Route responses are still the business of HandleStatus.
	if body := serve(t, r, http.MethodGet, "/gone"); body != "status handler" {
		t.Fatalf("got %q", body)
	}
}

func TestNotFoundInMountedRouters(t *testing.T) {
	parent := NewSite()
	parent.NotFound(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, "parent "+req.URL.Path)
	}))

	child := NewSite()
	child.Get("/users", noopHandler)
	parent.Mount("/child", child)

	own := NewSite()
	own.NotFound(text("own"))
	own.Get("/{$}", noopHandler)
	parent.Mount("/own", own)

	tests := map[string]string{
		"/missing":         "parent /missing",
		"/child/missing":   "parent /child/missing",
		"/own/missing":     "own",
		"/child/users/new": "parent /child/users/new",
	}

	for target, want := range tests {
		if body := serve(t, parent, http.MethodGet, target); body != want {
			t.Errorf("%s: got %q, want %q", target, body, want)
		}
	}

	w := record(parent, http.MethodPost, "/child/users")
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("got %d Allow %q", w.Code, w.Header().Get("Allow"))
	}
}

func TestNotFoundLeavesACatchAllAlone(t *testing.T) {
	r := NewSite(WithNotFound(text("not found")))
	r.Handle("/", text("site"))

	if body := serve(t, r, http.MethodGet, "/anything"); body != "site" {
		t.Fatalf("got %q", body)
	}

	// A ServeMux with a "/" of its own keeps it.
	mux := http.NewServeMux()
	mux.Handle("/", text("mux"))

	withMux := New(mux, "", "")
	withMux.NotFound(text("not found"))
	withMux.Get("/page", noopHandler)

	if body := serve(t, withMux, http.MethodGet, "/anything"); body != "mux" {
		t.Fatalf("got %q", body)
	}
}

func TestNotFoundKeepsTheHotPathFree(t *testing.T) {
	r := NewSite(WithNotFound(text("not found")))
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

func TestNotFoundAfterFirstRequestPanics(t *testing.T) {
	r := NewSite()
	r.Get("/", noopHandler)
	serve(t, r, http.MethodGet, "/")

	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()

	r.NotFound(text("late"))
}

func TestMethodNotAllowedHandlerOwnsAllow(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		code    int
		allow   string
	}{
		{"405 keeps Allow", status(http.StatusMethodNotAllowed, "no"), http.StatusMethodNotAllowed, "GET, HEAD"},
		{"404 drops Allow", status(http.StatusNotFound, "not found"), http.StatusNotFound, ""},
		{"implicit 200 drops Allow", text("page"), http.StatusOK, ""},
		{"own Allow is kept", func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusNotFound)
		}, http.StatusNotFound, "GET"},
	}

	for _, test := range tests {
		r := NewSite(WithMethodNotAllowed(test.handler))
		r.Get("/users", noopHandler)

		w := record(r, http.MethodPost, "/users")
		if w.Code != test.code || w.Header().Get("Allow") != test.allow {
			t.Errorf("%s: got %d Allow %q", test.name, w.Code, w.Header().Get("Allow"))
		}
	}
}

// TestUseCoverage pins down which requests Use middleware sees, as the Use
// documentation lists them.
func TestUseCoverage(t *testing.T) {
	var seen []string

	r := NewSite()
	r.RedirectTrailingSlash(true)
	r.HandleStatus(http.StatusTeapot, status(http.StatusTeapot, "status handler"))

	r.Group("/site", func(site *Router) {
		site.NotFound(status(http.StatusNotFound, "site 404")) // before Use
		site.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				seen = append(seen, req.Method+" "+req.URL.Path)
				next.ServeHTTP(w, req)
			})
		})
		site.Get("/page", noopHandler)
		site.Get("/tea", status(http.StatusTeapot, "tea"))

		child := NewSite()
		child.Get("/x", noopHandler)
		site.Mount("/child", child)
	})
	r.Get("/other", noopHandler)

	for _, target := range []string{
		"/site/page",          // route: seen
		"/site/missing",       // NotFound of the group: seen
		"/site/child/missing", // a mounted router's 404: seen, the mount is a route
		"/site/tea",           // the route is seen, the status handler is not twice
		"/site/page/",         // trailing slash redirect: not seen
		"/other",              // a route of another group: not seen
		"/missing",            // plain 404 outside the group: not seen
	} {
		record(r, http.MethodGet, target)
	}

	want := []string{"GET /site/page", "GET /site/missing", "GET /site/child/missing", "GET /site/tea"}
	if !slices.Equal(seen, want) {
		t.Fatalf("Use saw\n%q\nwant\n%q", seen, want)
	}
}
