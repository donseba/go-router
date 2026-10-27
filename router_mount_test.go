package router

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func noopHandler(w http.ResponseWriter, req *http.Request) {}

func serve(t *testing.T, handler http.Handler, method string, target string) string {
	t.Helper()

	req := httptest.NewRequest(method, target, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	return w.Body.String()
}

func TestRoutePathKeepsTrailingSlash(t *testing.T) {
	r := NewSite()
	r.Get("/admin/{$}", noopHandler).As("admin.dashboard")
	r.Get("/docs/", noopHandler).As("docs")
	r.Get("/users", noopHandler).As("users")
	r.Get("/users/", noopHandler).As("users.slash")
	r.Get("/{$}", noopHandler).As("home")

	tests := map[string]string{
		"admin.dashboard": "/admin/",
		"docs":            "/docs/",
		"users":           "/users",
		"users.slash":     "/users/",
		"home":            "/",
	}

	for name, want := range tests {
		if got := r.RoutePath(name); got != want {
			t.Fatalf("RoutePath(%q): expected %q, got %q", name, want, got)
		}

		if got, err := r.RoutePathArgs(name); err != nil || got != want {
			t.Fatalf("RoutePathArgs(%q): expected %q, got %q, %v", name, want, got, err)
		}
	}

	if got := r.Routes()["admin.dashboard"].Pattern; got != "/admin/{$}" {
		t.Fatalf("expected the registered pattern %q, got %q", "/admin/{$}", got)
	}
}

func TestExactAndPrefixPatternsAreDifferentRoutes(t *testing.T) {
	r := NewSite()
	r.Get("/admin/{$}", noopHandler).As("admin.dashboard")
	r.Handle("/admin/", http.HandlerFunc(noopHandler))
	r.Get("/admin", noopHandler)

	var patterns []string
	_ = r.Walk(func(route RouteInfo) error {
		patterns = append(patterns, route.Method+" "+route.Pattern+" "+route.Path)
		return nil
	})

	want := []string{"GET /admin/{$} /admin/", " /admin/ /admin/", "GET /admin /admin"}
	if !slices.Equal(patterns, want) {
		t.Fatalf("expected routes %q, got %q", want, patterns)
	}

	if _, err := r.TryHandle("/admin/", http.HandlerFunc(noopHandler)); err == nil {
		t.Fatal("expected the same pattern twice to fail")
	}
}

func TestRoutePathArgs(t *testing.T) {
	r := reverseTestRouter()

	tests := []struct {
		name  string
		route string
		args  []any
		want  string
		err   error
	}{
		{name: "pairs", route: "users.posts.show", args: []any{"id", 1, "post", "a b"}, want: "/users/1/posts/a%20b"},
		{name: "map", route: "users.posts.show", args: []any{map[string]any{"id": 1, "post": 2}}, want: "/users/1/posts/2"},
		{
			name:  "string map and pair",
			route: "users.posts.show",
			args:  []any{map[string]string{"id": "1"}, "post", 2},
			want:  "/users/1/posts/2",
		},
		{name: "query", route: "users.show", args: []any{"id", 7, url.Values{"tab": {"posts"}}}, want: "/users/7?tab=posts"},
		{name: "unknown route", route: "nope", err: ErrUnknownRoute},
		{name: "missing", route: "users.show", err: ErrMissingParam},
		{name: "unknown param", route: "users.show", args: []any{"id", 1, "x", 2}, err: ErrUnknownParam},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.RoutePathArgs(tt.route, tt.args...)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("expected error %v, got %q, %v", tt.err, got, err)
				}

				return
			}

			if err != nil || got != tt.want {
				t.Fatalf("expected %q, got %q, %v", tt.want, got, err)
			}
		})
	}

	if _, err := r.RoutePathArgs("users.show", "id"); err == nil {
		t.Fatal("expected a key without a value to fail")
	}

	if _, err := r.RoutePathArgs("users.show", 42); err == nil {
		t.Fatal("expected an argument of another type to fail")
	}

	if _, ok := r.FuncMap()["route"].(func(string, ...any) (string, error)); !ok {
		t.Fatal("expected the route template function to keep its type")
	}
}

// TestRoutePathArgsPairs checks the key value pairs RoutePathArgs reads
// without a map: on a mounted router, a router At a prefix and a wildcard
// subdomain route, with later values replacing earlier ones and the errors
// of a map of parameters, each parameter named once.
func TestRoutePathArgsPairs(t *testing.T) {
	handler := func(http.ResponseWriter, *http.Request) {}

	root := NewSite()
	root.Host("*.example.com", func(tenant *Router) {
		tenant.Get("/t/{id}", handler).As("tenant")
	})

	media := NewSite()
	root.Mount("/media", media, NamePrefix("media."))
	media.Get("/v/{signature}/{key...}", handler).As("variant")

	news := NewSite()
	news.Get("/{$}", handler).As("list")
	news.Get("/{year}/{slug...}", handler).As("archive")

	at := news.At("/en/news")

	tests := []struct {
		name   string
		router *Router
		route  string
		args   []any
		want   string
		err    string
	}{
		{
			name:   "mounted",
			router: media,
			route:  "variant",
			args:   []any{"signature", "ab c", "key", "x/y z.webp"},
			want:   "/media/v/ab%20c/x/y%20z.webp",
		},
		{
			name:   "segments",
			router: media,
			route:  "variant",
			args:   []any{"signature", "s", "key", []string{"a/b", "c"}},
			want:   "/media/v/s/a%2Fb/c",
		},
		{
			name:   "leading slash",
			router: media,
			route:  "variant",
			args:   []any{"signature", "s", "key", "/a/"},
			want:   "/media/v/s/a/",
		},
		{name: "at", router: at, route: "archive", args: []any{"year", 2026, "slug", "a/b"}, want: "/en/news/2026/a/b"},
		{name: "at root", router: at, route: "list", want: "/en/news"},
		{
			name:   "later wins",
			router: at,
			route:  "archive",
			args:   []any{"year", 1, "slug", "x", "year", int64(2)},
			want:   "/en/news/2/x",
		},
		{name: "subdomain ignored", router: root, route: "tenant", args: []any{"subdomain", "acme", "id", 3}, want: "/t/3"},
		{
			name: "missing and empty", router: media, route: "variant", args: []any{"signature", ""},
			err: `router: route "variant": missing route parameter "key", "signature"`,
		},
		{
			name: "unknown once", router: at, route: "archive", args: []any{"year", 1, "slug", "x", "zz", 1, "aa", 2, "zz", 3},
			err: `router: route "archive": unknown route parameter "aa", "zz"`,
		},
		{
			name: "unknown host param", router: at, route: "archive", args: []any{"year", 1, "slug", "x", "tenant", "acme"},
			err: `router: route "archive": unknown route parameter "tenant"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.router.RoutePathArgs(tt.route, tt.args...)
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("expected error %q, got %q, %v", tt.err, got, err)
				}

				return
			}

			if err != nil || got != tt.want {
				t.Fatalf("expected %q, got %q, %v", tt.want, got, err)
			}
		})
	}
}

func TestMountedRouterTakesOverLaterRegistrations(t *testing.T) {
	parent := NewSite()
	parent.Get("/{$}", noopHandler).As("home")

	child := NewSite()
	child.Get("/early", noopHandler).As("child.early")
	parent.Mount("/admin", child)

	// Registered after Mount.
	child.Get("/{$}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, "dashboard "+req.URL.Path)
	}).As("child.dashboard")
	child.Group("/users", func(users *Router) {
		users.Get("/{id}", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprint(w, "user "+req.PathValue("id")+" "+req.URL.Path)
		}).As("child.users.show")
	})

	for name, want := range map[string]string{
		"child.early":      "/admin/early",
		"child.dashboard":  "/admin/",
		"child.users.show": "/admin/users/{id}",
		"home":             "/",
	} {
		if got := parent.RoutePath(name); got != want {
			t.Fatalf("parent RoutePath(%q): expected %q, got %q", name, want, got)
		}

		// The mounted router resolves through its parent: the same public
		// paths, and the parent's own names.
		if got := child.RoutePath(name); got != want {
			t.Fatalf("child RoutePath(%q): expected %q, got %q", name, want, got)
		}
	}

	if got, err := child.RoutePathArgs("child.users.show", "id", 42); err != nil || got != "/admin/users/42" {
		t.Fatalf("expected the child's path with params to be public, got %q, %v", got, err)
	}

	if got := serve(t, parent, http.MethodGet, "/admin/"); got != "dashboard /admin/" {
		t.Fatalf("expected the dashboard at /admin/, got %q", got)
	}

	if got := serve(t, parent, http.MethodGet, "/admin/users/42"); got != "user 42 /admin/users/42" {
		t.Fatalf("expected the late user route, got %q", got)
	}

	var walked []string
	_ = parent.Walk(func(route RouteInfo) error {
		walked = append(walked, route.Method+" "+route.Path+" "+route.Name)
		return nil
	})

	for _, want := range []string{"GET /admin/ child.dashboard", "GET /admin/users/{id} child.users.show", "GET /admin/early child.early"} {
		if !slices.Contains(walked, want) {
			t.Fatalf("expected walk to include %q, got %q", want, walked)
		}
	}

	// The child's own Routes and Walk stay relative to the child.
	if got := child.Routes()["child.dashboard"].Path; got != "/" {
		t.Fatalf("expected the child's own route table to stay relative, got %q", got)
	}
}

func TestMountedRouterTemplateFuncsUsePublicPaths(t *testing.T) {
	parent := NewSite()
	child := NewSite()
	funcs := child.FuncMap()
	parent.Mount("/admin/", child)
	child.Get("/users/{id}", noopHandler).As("users.show")

	tmpl := template.Must(template.New("t").Funcs(funcs).Parse(
		`{{ route "users.show" "id" 3 }} {{ isActiveRoute .URL "users.show" }}`,
	))

	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]any{"URL": &url.URL{Path: "/admin/users/{id}"}}); err != nil {
		t.Fatal(err)
	}

	if got := out.String(); got != "/admin/users/3 true" {
		t.Fatalf("expected public paths in templates, got %q", got)
	}
}

func TestNestedMountTakesOverLaterRegistrations(t *testing.T) {
	root := NewSite()
	middle := NewSite()
	leaf := NewSite()

	root.Mount("/api", middle)
	middle.Mount("/v1", leaf)
	leaf.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, req.URL.Path+" "+req.PathValue("id"))
	}).As("users.show")

	if got := leaf.RoutePath("users.show"); got != "/api/v1/users/{id}" {
		t.Fatalf("expected the leaf to resolve through the outermost router, got %q", got)
	}

	if got := middle.Routes()["users.show"].Path; got != "/v1/users/{id}" {
		t.Fatalf("expected the middle router to know the route below its mount, got %q", got)
	}

	if got := serve(t, root, http.MethodGet, "/api/v1/users/9"); got != "/api/v1/users/9 9" {
		t.Fatalf("expected the nested late route, got %q", got)
	}

	if _, err := leaf.TryGet("/late", noopHandler); err == nil {
		t.Fatal("expected the leaf to be frozen with the root")
	}
}

func TestMountedRouterLateHostIsServed(t *testing.T) {
	parent := NewSite()
	child := NewSite()
	parent.Mount("/app", child)

	child.Host("child.example.com", func(hosted *Router) {
		hosted.Get("/private", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprint(w, "child host")
		}).As("child.private")
	})

	req := httptest.NewRequest(http.MethodGet, "/app/private", nil)
	req.Host = "child.example.com"
	w := httptest.NewRecorder()
	parent.ServeHTTP(w, req)

	if got := w.Body.String(); got != "child host" {
		t.Fatalf("expected the late host route, got %q", got)
	}

	if got := parent.RouteURL("child.private", nil, "https"); got != "https://child.example.com/app/private" {
		t.Fatalf("expected the late host route URL, got %q", got)
	}
}

func TestMountedRouterLateDocs(t *testing.T) {
	parent := New(http.NewServeMux(), "Parent", "1")
	child := New(http.NewServeMux(), "Child", "1")
	parent.UseOpenapiDocs(true)
	child.UseOpenapiDocs(true)
	parent.Mount("/api", child)

	type Item struct {
		ID string `json:"id"`
	}

	child.Get("/items/{id}", noopHandler, Docs{
		Summary: "Show item",
		Out: map[string]DocOut{
			"200": {ApplicationType: "application/json", Description: "OK", Object: Item{}},
		},
	})

	operation := parent.OpenAPI().Paths["/api/items/{id}"].Get
	if operation == nil || operation.Summary != "Show item" {
		t.Fatalf("expected the late mounted operation, got %#v", operation)
	}

	if _, exists := parent.OpenAPI().Components.Schemas["Item"]; !exists {
		t.Fatal("expected the late mounted schema")
	}
}

func TestMountedRouterLateDuplicateName(t *testing.T) {
	parent := NewSite()
	parent.Get("/home", noopHandler).As("home")

	child := NewSite()
	parent.Mount("/admin", child)

	if err := child.Get("/home", noopHandler).TryAs("home"); err == nil {
		t.Fatal("expected a name the parent has to fail")
	}

	if err := child.Get("/other", noopHandler).TryAs("other"); err != nil {
		t.Fatalf("expected a fresh name to work, got %v", err)
	}
}

func TestMountOnItselfPanics(t *testing.T) {
	parent := NewSite()
	child := NewSite()
	parent.Mount("/child", child)

	if _, err := parent.TryMount("/self", parent); err == nil {
		t.Fatal("expected mounting a router on itself to fail")
	}

	if _, err := child.TryMount("/parent", parent); err == nil {
		t.Fatal("expected mounting a parent on its child to fail")
	}
}

func recordMiddleware(name string, calls *[]string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			*calls = append(*calls, name+" "+req.URL.Path)
			next.ServeHTTP(w, req)
		})
	}
}

func TestRouteMiddleware(t *testing.T) {
	var calls []string

	r := NewSite()
	r.Use(recordMiddleware("a", &calls))
	r.Group("/admin", func(admin *Router) {
		admin.Use(recordMiddleware("b", &calls))
		admin.Get("/users", func(w http.ResponseWriter, req *http.Request) {
			calls = append(calls, "handler")
		}).Use(recordMiddleware("c", &calls), recordMiddleware("d", &calls)).Use(recordMiddleware("e", &calls)).As("admin.users")
		admin.Get("/other", noopHandler)
	})

	route := r.Get("/late", noopHandler)
	serve(t, r, http.MethodGet, "/admin/users")

	want := []string{"a /admin/users", "b /admin/users", "c /admin/users", "d /admin/users", "e /admin/users", "handler"}
	if !slices.Equal(calls, want) {
		t.Fatalf("expected middleware order %q, got %q", want, calls)
	}

	calls = nil
	serve(t, r, http.MethodGet, "/admin/other")

	if want := []string{"a /admin/other", "b /admin/other"}; !slices.Equal(calls, want) {
		t.Fatalf("expected route middleware to stay on its route, got %q", calls)
	}

	var middlewares int
	_ = r.Walk(func(route RouteInfo) error {
		if route.Name == "admin.users" {
			middlewares = len(route.Middlewares)
		}

		return nil
	})

	if middlewares != 5 {
		t.Fatalf("expected walk to report 5 middlewares, got %d", middlewares)
	}

	if err := route.TryUse(recordMiddleware("x", &calls)); err == nil {
		t.Fatal("expected route Use after the first request to fail")
	}
}

func TestRouteMiddlewareInMountedRouter(t *testing.T) {
	var calls []string

	parent := NewSite()
	child := NewSite()
	parent.Mount("/admin", child)

	forbidden := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			calls = append(calls, "check "+req.URL.Path)
			http.Error(w, "forbidden", http.StatusForbidden)
		})
	}

	child.Post("/users/{id}", noopHandler).Use(forbidden).As("users.delete")

	if got := serve(t, parent, http.MethodPost, "/admin/users/1"); !strings.Contains(got, "forbidden") {
		t.Fatalf("expected the route middleware to answer, got %q", got)
	}

	if want := []string{"check /admin/users/1"}; !slices.Equal(calls, want) {
		t.Fatalf("expected the route middleware to see the public path, got %q", calls)
	}

	var middlewares int
	_ = parent.Walk(func(route RouteInfo) error {
		if route.Name == "users.delete" {
			middlewares = len(route.Middlewares)
		}

		return nil
	})

	if middlewares != 1 {
		t.Fatalf("expected the parent's walk to report the route middleware, got %d", middlewares)
	}
}

func TestOpenAPIExactPatternKeepsMethods(t *testing.T) {
	r := New(http.NewServeMux(), "API", "1")
	r.UseOpenapiDocs(true)
	r.Get("/items/{$}", noopHandler, Docs{Summary: "List"})
	r.Post("/items/{$}", noopHandler, Docs{Summary: "Create"})

	item := r.OpenAPI().Paths["/items/"]
	if item.Get == nil || item.Post == nil {
		t.Fatalf("expected GET and POST on /items/, got %#v", item)
	}
}
