package router

import (
	"net/http"
	"slices"
	"testing"
)

func TestMatchReportsTheMountOfAMountedRouter(t *testing.T) {
	r := NewSite()

	admin := NewSite()
	admin.Get("/users/{id}", noopHandler).As("admin.users.show")
	admin.Post("/login", noopHandler)
	r.Mount("/admin", admin).As("admin").Label("admin")

	inner := NewSite()
	inner.Get("/x", noopHandler)
	admin.Mount("/inner", inner).Label("inner")

	tests := []struct {
		method  string
		path    string
		pattern string
		mount   bool
		labels  []string
	}{
		{http.MethodGet, "/admin/users/7", "/admin/users/{id}", false, []string{"admin"}},
		{http.MethodGet, "/admin/missing", "/admin/", true, []string{"admin"}},
		{http.MethodGet, "/admin", "/admin/", true, []string{"admin"}},
		{http.MethodGet, "/admin/login", "/admin/", true, []string{"admin"}}, // POST only
		{http.MethodPost, "/admin/login", "/admin/login", false, []string{"admin"}},
		{http.MethodGet, "/admin/inner/x", "/admin/inner/x", false, []string{"admin", "inner"}},
		{http.MethodGet, "/admin/inner/y", "/admin/inner/", true, []string{"admin", "inner"}},
	}

	for _, test := range tests {
		route, ok := r.Match(test.method, test.path)
		if !ok || route.Pattern != test.pattern || route.Mount != test.mount || !slices.Equal(route.Labels, test.labels) {
			t.Errorf("%s %s: got %+v %v", test.method, test.path, route, ok)
		}
	}

	if route, _ := r.Match(http.MethodGet, "/admin/nothing"); route.Name != "admin" || route.Handler != admin {
		t.Fatalf("mount: %+v", route)
	}

	if _, ok := r.Match(http.MethodGet, "/elsewhere"); ok {
		t.Fatal("matched outside the mount")
	}

	// Walk lists mounts with Mount set, routes without.
	mounts := map[string]bool{}
	_ = r.Walk(func(route RouteInfo) error {
		mounts[route.Pattern] = route.Mount

		return nil
	})

	if !mounts["/admin/"] || !mounts["/admin/inner/"] || mounts["/admin/users/{id}"] {
		t.Fatalf("walk: %v", mounts)
	}
}

func TestOwns(t *testing.T) {
	r := NewSite()

	admin := NewSite()
	admin.Get("/users/{id}", noopHandler)
	r.Mount("/admin", admin).Label("admin")
	r.Mount("/media", text("media")).Label("media")
	r.Post("/login", noopHandler).Label("auth")
	r.Get("/docs/{page}", noopHandler).Label("docs")
	r.Get("/docs/index", noopHandler).Label("index")
	r.Handle("/", text("site")).Label("site")

	tests := []struct {
		path    string
		pattern string
		method  string
		mount   bool
		label   string
	}{
		{"/admin/anything", "/admin/", "*", true, "admin"},
		{"/admin/users/7", "/admin/users/{id}", http.MethodGet, false, "admin"},
		{"/admin", "/admin/", "*", true, "admin"},
		{"/media/a/b.png", "/media/", "*", true, "media"},
		{"/login", "/login", http.MethodPost, false, "auth"},
		{"/login?next=/", "/login", http.MethodPost, false, "auth"},
		{"/docs/intro", "/docs/{page}", http.MethodGet, false, "docs"},
		{"/docs/index", "/docs/index", http.MethodGet, false, "index"},
		{"/about", "/", "", false, "site"},
		{"/", "/", "", false, "site"},
	}

	for _, test := range tests {
		route, ok := r.Owns(test.path)
		if !ok || route.Pattern != test.pattern || route.Method != test.method || route.Mount != test.mount ||
			!route.HasLabel(test.label) {
			t.Errorf("%s: got %+v %v", test.path, route, ok)
		}
	}

	// Without a catch-all, a path nothing receives.
	plain := NewSite()
	plain.Get("/a", noopHandler)
	plain.HandleFunc("PURGE /cache/", noopHandler) // a method of its own

	if route, ok := plain.Owns("/b"); ok {
		t.Fatalf("owned: %+v", route)
	}

	if route, ok := plain.Owns("/cache/x"); !ok || route.Method != "PURGE" {
		t.Fatalf("other method: %+v %v", route, ok)
	}
}

func TestMoreSpecific(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"/login", "/", true},
		{"/", "/login", false},
		{"/admin/users/{id}", "/admin/", true},
		{"/docs/index", "/docs/{page}", true},
		{"/docs/{page}", "/docs/index", false},
		{"/a/{$}", "/a/", true},
		{"/v/{file...}", "/v/", false},
		{"/v/", "/v/{file...}", false},
		{"/x", "/x", false},
		{"/{$}", "/", true},
	}

	for _, test := range tests {
		if got := moreSpecific(test.a, test.b); got != test.want {
			t.Errorf("moreSpecific(%q, %q) = %v", test.a, test.b, got)
		}
	}
}

func TestHandleWithAMethod(t *testing.T) {
	r := NewSite()
	r.Group("/api", func(api *Router) {
		api.Handle("POST /items", text("created")).As("items.create")
	})

	if w := record(r, http.MethodPost, "/api/items"); w.Body.String() != "created" {
		t.Fatalf("POST: %d %q", w.Code, w.Body.String())
	}

	if w := record(r, http.MethodGet, "/api/items"); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", w.Code)
	}

	route, ok := r.Match(http.MethodPost, "/api/items")
	if !ok || route.Method != http.MethodPost || route.Pattern != "/api/items" || r.RoutePath("items.create") != "/api/items" {
		t.Fatalf("route: %+v %v", route, ok)
	}
}
