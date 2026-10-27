package router

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
)

func TestHandleAndMountReturnTheirRoute(t *testing.T) {
	r := NewSite()

	var used []string

	mark := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				used = append(used, name+" "+req.URL.Path)
				next.ServeHTTP(w, req)
			})
		}
	}

	r.Handle("/theme/", http.StripPrefix("/theme/", text("theme"))).As("theme").Use(mark("theme")).Label("asset")
	r.HandleFunc("/feed.xml", text("feed")).As("feed")
	r.Mount("/debug", text("debug")).As("debug").Use(mark("debug")).Label("operations")

	child := NewSite()
	child.Get("/users", text("users")).As("users")
	r.Mount("/admin", child).As("admin").Use(mark("admin")).Label("admin")

	if got := r.RoutePath("theme"); got != "/theme/" {
		t.Fatalf("theme: %q", got)
	}

	if got := r.RoutePath("admin"); got != "/admin/" {
		t.Fatalf("admin: %q", got)
	}

	for _, test := range []struct{ target, want string }{
		{"/theme/site.css", "theme"},
		{"/feed.xml", "feed"},
		{"/debug/vars", "debug"},
		{"/admin/users", "users"},
	} {
		if body := serve(t, r, http.MethodGet, test.target); body != test.want {
			t.Errorf("%s: got %q, want %q", test.target, body, test.want)
		}
	}

	if want := []string{"theme /theme/site.css", "debug /debug/vars", "admin /admin/users"}; !slices.Equal(used, want) {
		t.Fatalf("middleware: %q, want %q", used, want)
	}

	labels := map[string][]string{}
	_ = r.Walk(func(route RouteInfo) error {
		labels[route.Method+" "+route.Pattern] = route.Labels

		return nil
	})

	for route, want := range map[string][]string{
		" /theme/":         {"asset"},
		"* /debug/":        {"operations"},
		"* /admin/":        {"admin"},
		"GET /admin/users": {"admin"},
		" /feed.xml":       nil,
	} {
		if !slices.Equal(labels[route], want) {
			t.Errorf("%s: labels %q, want %q", route, labels[route], want)
		}
	}
}

func TestMountNamePrefix(t *testing.T) {
	r := NewSite()

	actions := func(key string) *Router {
		routes := NewSite()
		routes.Post("/post", text(key)).As("post")

		r.Mount("/_/"+key, routes, NamePrefix(key+"."))

		return routes
	}

	comments := actions("comments")
	actions("guestbook")
	r.Get("/{$}", noopHandler).As("home")

	if got := r.RoutePath("comments.post"); got != "/_/comments/post" {
		t.Fatalf("comments.post: %q", got)
	}

	if got := r.RoutePath("guestbook.post"); got != "/_/guestbook/post" {
		t.Fatalf("guestbook.post: %q", got)
	}

	// The mounted router finds its own names first, and the outer ones.
	if got := comments.RoutePath("post"); got != "/_/comments/post" {
		t.Fatalf("own post: %q", got)
	}

	if got := comments.RoutePath("home"); got != "/" {
		t.Fatalf("outer home: %q", got)
	}

	if body := serve(t, r, http.MethodPost, "/_/guestbook/post"); body != "guestbook" {
		t.Fatalf("got %q", body)
	}
}

// absoluteAdmin is an admin like go-webthings': its routes are below its
// base path, and it wraps its router in middleware of its own.
type absoluteAdmin struct {
	router *Router
}

func newAbsoluteAdmin(base string) *absoluteAdmin {
	admin := &absoluteAdmin{router: NewSite()}
	admin.router.Group(base, func(routes *Router) {
		routes.Get("/{$}", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprint(w, "dashboard "+req.Header.Get("X-Admin"))
		}).As("admin.dashboard")
		routes.Get("/pages/{id}", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprint(w, "page "+req.PathValue("id")+" "+req.URL.Path)
		}).As("admin.pages.edit")
	})

	return admin
}

func (a *absoluteAdmin) Router() *Router {
	return a.router
}

func (a *absoluteAdmin) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	req.Header.Set("X-Admin", "wrapped "+req.URL.Path)
	a.router.ServeHTTP(w, req)
}

func TestMountAbsolutePathsAndWrappedRouter(t *testing.T) {
	r := NewSite()
	r.NotFound(text("site 404"))

	admin := newAbsoluteAdmin("/admin")
	r.Mount("/admin", admin, AbsolutePaths()).Label("admin")

	// The admin knows its paths before and after it is mounted.
	if got := admin.Router().RoutePath("admin.dashboard"); got != "/admin/" {
		t.Fatalf("dashboard: %q", got)
	}

	if got := r.RoutePathWithParams("admin.pages.edit", map[string]any{"id": 7}); got != "/admin/pages/7" {
		t.Fatalf("pages.edit: %q", got)
	}

	for target, want := range map[string]string{
		"/admin/":         "dashboard wrapped /admin/",
		"/admin/pages/7":  "page 7 /admin/pages/7",
		"/admin/missing":  "site 404",
		"/adminstrations": "site 404",
	} {
		if body := serve(t, r, http.MethodGet, target); body != want {
			t.Errorf("%s: got %q, want %q", target, body, want)
		}
	}

	// The bare mount path reaches the router, which redirects it to /admin/.
	if w := record(r, http.MethodGet, "/admin"); w.Header().Get("Location") != "/admin/" {
		t.Fatalf("bare path: %d %v", w.Code, w.Header())
	}

	route, ok := r.Match(http.MethodGet, "/admin/pages/7")
	if !ok || route.Name != "admin.pages.edit" || route.Pattern != "/admin/pages/{id}" || !route.HasLabel("admin") {
		t.Fatalf("match: %+v %v", route, ok)
	}
}

func TestMountWrappedRouterBelowItsPath(t *testing.T) {
	r := NewSite()

	inner := NewSite()
	inner.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, req.URL.Path+" "+req.PathValue("id")+" "+req.Header.Get("X-Wrapper"))
	}).As("users.show")

	wrapper := &wrappedRouter{router: inner}
	r.Mount("/admin", wrapper)

	if body := serve(t, r, http.MethodGet, "/admin/users/7"); body != "/admin/users/7 7 /admin/users/7" {
		t.Fatalf("got %q", body)
	}

	if got := inner.RoutePath("users.show"); got != "/admin/users/{id}" {
		t.Fatalf("users.show: %q", got)
	}

	// With UseAll on the wrapped router.
	r2 := NewSite()
	inner2 := NewSite()

	var aroundPath string

	inner2.UseAll(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			aroundPath = req.URL.Path
			next.ServeHTTP(w, req)
		})
	})
	inner2.Get("/x", text("x"))
	r2.Mount("/m", &wrappedRouter{router: inner2})

	if body := serve(t, r2, http.MethodGet, "/m/x"); body != "x" || aroundPath != "/m/x" {
		t.Fatalf("got %q, UseAll saw %q", body, aroundPath)
	}
}

type wrappedRouter struct {
	router *Router
}

func (w *wrappedRouter) Router() *Router {
	return w.router
}

func (w *wrappedRouter) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	req.Header.Set("X-Wrapper", req.URL.Path)
	w.router.ServeHTTP(rw, req)
}

func TestMatch(t *testing.T) {
	r := NewSite()
	r.Get("/users/{id}", noopHandler).As("users.show").Label("public")
	r.Handle("/theme/", text("theme")).Label("asset")

	media := NewSite()
	media.Get("/v/{file...}", noopHandler).As("media.variant")
	r.Mount("/media", media).Label("media")
	media.Get("/late", noopHandler).Label("late") // after the mount and its label

	r.Host("admin.example.com", func(admin *Router) {
		admin.Get("/{$}", noopHandler).As("host.home")
	})

	tests := []struct {
		method  string
		path    string
		ok      bool
		pattern string
		name    string
		labels  []string
	}{
		{http.MethodGet, "/users/7", true, "/users/{id}", "users.show", []string{"public"}},
		{http.MethodGet, "/users/7?tab=posts", true, "/users/{id}", "users.show", []string{"public"}},
		{http.MethodPost, "/users/7", false, "", "", nil},
		{http.MethodGet, "/theme/site.css", true, "/theme/", "", []string{"asset"}},
		{http.MethodGet, "/media/v/a/b.png", true, "/media/v/{file...}", "media.variant", []string{"media"}},
		{http.MethodGet, "/media/missing", true, "/media/", "", []string{"media"}}, // the mount
		{http.MethodGet, "/media/late", true, "/media/late", "", []string{"media", "late"}},
		{http.MethodGet, "/nothing", false, "", "", nil},
		{http.MethodGet, "/", false, "", "", nil},
	}

	for _, test := range tests {
		route, ok := r.Match(test.method, test.path)
		if ok != test.ok || route.Pattern != test.pattern || route.Name != test.name || !slices.Equal(route.Labels, test.labels) {
			t.Errorf("%s %s: got %+v %v", test.method, test.path, route, ok)
		}
	}

	var walked []string
	_ = r.Walk(func(route RouteInfo) error {
		if route.Pattern == "/media/late" {
			walked = route.Labels
		}

		return nil
	})

	if !slices.Equal(walked, []string{"media", "late"}) {
		t.Fatalf("walk labels %q", walked)
	}

	// The mounted router matches with its own paths.
	if route, ok := media.Match(http.MethodGet, "/v/a.png"); !ok || route.Pattern != "/v/{file...}" {
		t.Fatalf("mounted: %+v %v", route, ok)
	}
}

func TestMatchedRoute(t *testing.T) {
	r := NewSite()
	r.TrackRoutes()

	var seen []string

	r.UseAll(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)

			route, ok := MatchedRoute(req)
			if !ok {
				seen = append(seen, "none")

				return
			}

			seen = append(seen, fmt.Sprintf("%s %s %s %v", route.Pattern, route.Name, route.Path, route.Labels))
		})
	})

	r.Get("/users/{id}", noopHandler).As("users.show").Label("public")
	r.Mount("/debug", text("debug")).Label("operations")

	admin := NewSite()
	admin.Get("/pages", noopHandler).As("pages").Label("pages")
	r.Group("/x", func(x *Router) {
		x.Mount("/admin", admin, NamePrefix("admin.")).Label("admin")
	})

	for _, target := range []string{"/users/7", "/debug/vars", "/x/admin/pages", "/x/admin/missing", "/missing"} {
		serve(t, r, http.MethodGet, target)
	}

	want := []string{
		"/users/{id} users.show /users/{id} [public]",
		"/debug/  /debug/ [operations]",
		"/x/admin/pages admin.pages /x/admin/pages [admin pages]",
		"/x/admin/  /x/admin/ [admin]", // the mount: admin has no route for it
		"none",
	}

	if !slices.Equal(seen, want) {
		t.Fatalf("matched routes:\n%q\nwant\n%q", seen, want)
	}

	// Without TrackRoutes, nothing is recorded.
	plain := NewSite()

	var ok bool

	plain.Get("/a", func(w http.ResponseWriter, req *http.Request) {
		_, ok = MatchedRoute(req)
	})
	serve(t, plain, http.MethodGet, "/a")

	if ok {
		t.Fatal("matched route without TrackRoutes")
	}
}

func TestMatchedRouteOfAMountedTrackingRouter(t *testing.T) {
	parent := NewSite()

	child := NewSite()
	child.TrackRoutes()

	var path string

	child.UseAll(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)

			route, _ := MatchedRoute(req)
			path = route.Path
		})
	})
	child.Get("/a/{id}", noopHandler)
	parent.Mount("/child", child)

	serve(t, parent, http.MethodGet, "/child/a/1")

	if path != "/child/a/{id}" {
		t.Fatalf("got %q", path)
	}
}
