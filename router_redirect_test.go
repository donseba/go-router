package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// redirectSite is a site with routes the ServeMux redirects to: a route
// with a rest wildcard, a mount of a handler, a mounted router with a route
// that ends in a slash, and a catch-all for the site's pages.
func redirectSite() *Router {
	r := NewSite()
	r.Get("/theme/{file...}", noopHandler).Label("asset")
	r.Mount("/media", text("media")).Label("media")

	admin := NewSite()
	admin.Get("/users/", noopHandler).As("users").Label("users")
	admin.Get("/files/{file...}", noopHandler)
	r.Mount("/admin", admin, NamePrefix("admin.")).Label("admin")

	r.Handle("/", text("site"))

	return r
}

func TestMatchFollowsRedirects(t *testing.T) {
	r := redirectSite()

	tests := []struct {
		method   string
		path     string
		pattern  string
		redirect string
		label    string
	}{
		{http.MethodGet, "/theme", "/theme/{file...}", "/theme/", "asset"},
		{http.MethodGet, "/theme/site.css", "/theme/{file...}", "", "asset"},
		{http.MethodGet, "/theme//site.css", "/theme/{file...}", "/theme/site.css", "asset"},
		{http.MethodGet, "/x/../theme", "/theme/{file...}", "/theme/", "asset"}, // two redirects
		{http.MethodGet, "/media", "/media/", "/media/", "media"},
		{http.MethodGet, "/admin//users/", "/admin/users/", "/admin/users/", "admin"},
		{http.MethodGet, "/admin/users", "/admin/users/", "/admin/users/", "admin"}, // the mounted router's redirect
		{http.MethodGet, "/admin/files", "/admin/files/{file...}", "/admin/files/", "admin"},
		{http.MethodGet, "/en/news/hello", "/", "", ""},
		{http.MethodPost, "/theme", "/", "", ""}, // only GET has the route with the slash
	}

	for _, test := range tests {
		route, ok := r.Match(test.method, test.path)
		if !ok || route.Pattern != test.pattern || route.Redirect != test.redirect {
			t.Errorf("%s %s: got %q, redirect %q, %v", test.method, test.path, route.Pattern, route.Redirect, ok)
		}

		label, ok := r.MatchLabel(test.method, test.path)
		if !ok || label != test.label {
			t.Errorf("%s %s: label %q %v, want %q", test.method, test.path, label, ok, test.label)
		}
	}

	if route, ok := r.Owns("/theme"); !ok || route.Pattern != "/theme/{file...}" || route.Redirect != "/theme/" {
		t.Fatalf("owns: %+v %v", route, ok)
	}

	if label, ok := r.OwnsLabel("/admin//users"); !ok || label != "admin" {
		t.Fatalf("owns label: %q %v", label, ok)
	}

	// A query string is not part of the redirect.
	if route, ok := r.Match(http.MethodGet, "/theme?v=1"); !ok || route.Redirect != "/theme/" {
		t.Fatalf("query: %+v %v", route, ok)
	}
}

func TestMatchFollowsTheTrailingSlashRedirect(t *testing.T) {
	r := NewSite()
	r.RedirectTrailingSlash(true)
	r.Get("/about", noopHandler)
	r.Get("/dir/", noopHandler)

	if route, ok := r.Match(http.MethodGet, "/about/"); !ok || route.Pattern != "/about" || route.Redirect != "/about" {
		t.Fatalf("trailing slash: %+v %v", route, ok)
	}

	// A route that ends in a slash keeps it: the ServeMux would put it back.
	if route, ok := r.Match(http.MethodGet, "/dir/"); !ok || route.Pattern != "/dir/" || route.Redirect != "" {
		t.Fatalf("route with a slash: %+v %v", route, ok)
	}
}

func TestRedirectTrailingSlashKeepsSlashRoutesBelowAMountAndServeAt(t *testing.T) {
	blog := NewSite()
	blog.Get("/", text("blog"))
	blog.Get("/posts/", text("posts"))
	blog.Get("/about", text("about"))

	r := NewSite()
	r.RedirectTrailingSlash(true)
	r.Mount("/blog", blog)

	news := NewSite()
	news.RedirectTrailingSlash(true)
	news.Get("/{$}", text("news"))
	news.Get("/archive/", text("archive"))

	at := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		news.ServeAt(w, req, "/en")
	})

	tests := []struct {
		handler  http.Handler
		path     string
		code     int
		location string
	}{
		{handler: r, path: "/blog/posts/", code: http.StatusOK},
		{handler: r, path: "/blog/about/", code: http.StatusTemporaryRedirect, location: "/blog/about"},
		{handler: r, path: "/blog/", code: http.StatusTemporaryRedirect, location: "/blog"},
		{handler: at, path: "/en/", code: http.StatusOK},
		{handler: at, path: "/en/archive/", code: http.StatusOK},
	}

	for _, tt := range tests {
		w := record(tt.handler, http.MethodGet, tt.path)

		if w.Code != tt.code || w.Header().Get("Location") != tt.location {
			t.Errorf("%s: status %d, Location %q; want %d, %q", tt.path, w.Code, w.Header().Get("Location"), tt.code, tt.location)
		}
	}
}

func TestRedirectTrailingSlashKeepsRoutesThatEndInASlash(t *testing.T) {
	r := NewSite()
	r.RedirectTrailingSlash(true)
	r.Get("/about", text("about"))
	r.Get("/dir/", text("dir"))
	r.Get("/admin/{$}", text("admin"))
	r.Get("/files/{name...}", text("files"))
	r.ServeFiles("/assets/", http.FS(fstest.MapFS{"app.css": {Data: []byte("body{}")}}))
	r.Host("cdn.example.com", func(cdn *Router) {
		cdn.Get("/dir/", text("cdn dir"))
	})

	panel := NewSite()
	panel.Group("/panel", func(routes *Router) {
		routes.Get("/{$}", text("panel"))
	})
	r.Mount("/panel", panel, AbsolutePaths())

	tests := []struct {
		host     string
		path     string
		code     int
		location string
	}{
		{host: "cdn.example.com", path: "/dir/", code: http.StatusOK},
		{path: "/panel/", code: http.StatusOK},
		{path: "/about/", code: http.StatusTemporaryRedirect, location: "/about"},
		{path: "/dir/", code: http.StatusOK},
		{path: "/admin/", code: http.StatusOK},
		{path: "/files/", code: http.StatusOK},
		{path: "/assets/", code: http.StatusOK},
		{path: "/assets/app.css", code: http.StatusOK},
		// A path no route serves is still redirected, to its 404.
		{path: "/nothing/", code: http.StatusTemporaryRedirect, location: "/nothing"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		if tt.host != "" {
			req.Host = tt.host
		}

		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if location := w.Header().Get("Location"); w.Code != tt.code || location != tt.location {
			t.Errorf("%s%s: status %d, Location %q; want %d, %q", tt.host, tt.path, w.Code, location, tt.code, tt.location)
		}
	}
}

func TestMatchReportsFalseForRedirectsToNothing(t *testing.T) {
	r := NewSite()
	r.Get("/a", noopHandler)

	if route, ok := r.Match(http.MethodGet, "/x/../b"); ok {
		t.Fatalf("matched: %+v", route)
	}
}

func TestMountedServeMuxRedirectsToThePublicPath(t *testing.T) {
	r := redirectSite()

	// What a ServeMux answers for its own trailing slash redirects.
	mux := http.NewServeMux()
	mux.HandleFunc("/users/", noopHandler)
	want := record(mux, http.MethodGet, "/users").Code

	tests := map[string]string{
		"/admin/users":         "/admin/users/",
		"/admin/users?tab=all": "/admin/users/?tab=all",
		"/admin/files":         "/admin/files/",
	}

	for target, location := range tests {
		w := record(r, http.MethodGet, target)
		if w.Code != want || w.Header().Get("Location") != location {
			t.Errorf("%s: %d %q, want %d %q", target, w.Code, w.Header().Get("Location"), want, location)
		}
	}

	// A router served at a prefix (ServeAt) redirects below its prefix.
	news := NewSite()
	news.Get("/{slug}/", noopHandler)

	site := NewSite()
	site.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		news.ServeAt(w, req, "/en/news")
	}))

	if w := record(site, http.MethodGet, "/en/news/hello"); w.Code != want || w.Header().Get("Location") != "/en/news/hello/" {
		t.Fatalf("served at a prefix: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestLabelLookups(t *testing.T) {
	r := NewSite()
	r.TrackRoutes()

	var (
		label   string
		matched bool
	)

	r.UseAll(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)

			label, matched = MatchedLabel(req, "admin", "asset", "users")
		})
	})

	r.Get("/theme/{file...}", noopHandler).Label("static", "asset")
	r.Get("/plain", noopHandler)

	admin := NewSite()
	admin.Get("/users", noopHandler).Label("users")
	r.Mount("/admin", admin).Label("section", "admin")

	tests := []struct {
		path    string
		wanted  []string
		label   string
		matched bool
	}{
		{"/theme/a.css", []string{"admin", "asset"}, "asset", true},
		{"/theme/a.css", nil, "static", true}, // the first label
		{"/admin/users", []string{"users", "admin"}, "admin", true},
		{"/admin/users", nil, "section", true}, // the mount's labels come first
		{"/admin/missing", []string{"users", "admin"}, "admin", true},
		{"/plain", nil, "", true},
		{"/missing", nil, "", false},
	}

	for _, test := range tests {
		got, ok := r.MatchLabel(http.MethodGet, test.path, test.wanted...)
		if got != test.label || ok != test.matched {
			t.Errorf("match %s %q: %q %v", test.path, test.wanted, got, ok)
		}

		got, ok = r.OwnsLabel(test.path, test.wanted...)
		if got != test.label || ok != test.matched {
			t.Errorf("owns %s %q: %q %v", test.path, test.wanted, got, ok)
		}
	}

	served := []struct {
		path    string
		label   string
		matched bool
	}{
		{"/theme/a.css", "asset", true},
		{"/admin/users", "admin", true},
		{"/admin/missing", "admin", true},
		{"/plain", "", true},
		{"/missing", "", false},
	}

	for _, test := range served {
		serve(t, r, http.MethodGet, test.path)

		if label != test.label || matched != test.matched {
			t.Errorf("matched %s: %q %v", test.path, label, matched)
		}
	}

	if label, ok := MatchedLabel(nil); ok || label != "" {
		t.Fatalf("nil request: %q %v", label, ok)
	}
}

func TestMatchedLabelOfARouterServedAtAPrefix(t *testing.T) {
	news := NewSite()
	news.Get("/{slug}", noopHandler).Label("article")

	var labels []string

	site := NewSite()
	site.TrackRoutes()
	site.UseAll(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)

			label, _ := MatchedLabel(req)
			labels = append(labels, label)
		})
	})
	site.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		news.ServeAt(w, req, "/en/news")
	})).Label("page")

	serve(t, site, http.MethodGet, "/en/news/hello")
	serve(t, site, http.MethodGet, "/en/news/a/b")

	// The route of the page comes first; a path news has no route for is
	// the page's.
	if len(labels) != 2 || labels[0] != "page" || labels[1] != "page" {
		t.Fatalf("labels: %q", labels)
	}
}

func TestTryMountAndTryLabel(t *testing.T) {
	r := NewSite()

	admin := NewSite()
	admin.Get("/admin/users", noopHandler)

	mount, err := r.TryMount("/admin", admin, AbsolutePaths())
	if err != nil || mount == nil {
		t.Fatalf("mount: %v", err)
	}

	if err := mount.TryLabel("admin"); err != nil {
		t.Fatalf("label: %v", err)
	}

	if label, ok := r.MatchLabel(http.MethodGet, "/admin/users"); !ok || label != "admin" {
		t.Fatalf("mount label: %q %v", label, ok)
	}

	if again, err := r.TryMount("/admin", NewSite()); err == nil || again != nil {
		t.Fatalf("mount path taken: %v %v", again, err)
	}

	route, err := r.TryHandle("/feed.xml", text("feed"))
	if err != nil || route == nil {
		t.Fatalf("handle: %v", err)
	}

	if again, err := r.TryHandleFunc("/feed.xml", text("feed")); err == nil || again != nil {
		t.Fatalf("pattern taken: %v %v", again, err)
	}

	serve(t, r, http.MethodGet, "/feed.xml")

	if err := route.TryLabel("late"); err == nil {
		t.Fatal("expected a label after the first request to fail")
	}
}

type contextKey struct{}

func TestRequestStateKeepsTheContext(t *testing.T) {
	r := NewSite()
	r.TrackRoutes()

	var (
		value any
		done  bool
	)

	r.Get("/a", func(w http.ResponseWriter, req *http.Request) {
		value = req.Context().Value(contextKey{})
		done = req.Context().Err() != nil
	}).Label("a")

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "kept"))
	cancel()

	req := httptest.NewRequest(http.MethodGet, "/a", nil).WithContext(ctx)
	r.ServeHTTP(httptest.NewRecorder(), req)

	if value != "kept" || !done {
		t.Fatalf("context: %v, canceled %v", value, done)
	}
}

func TestMatchBelowManyMounts(t *testing.T) {
	r := NewSite()
	parent := r

	// More mounts than a match keeps without allocating.
	for i := range 6 {
		child := NewSite()
		parent.Mount(fmt.Sprintf("/m%d", i), child).Label(fmt.Sprintf("m%d", i))
		parent = child
	}

	parent.Get("/leaf", noopHandler).Label("leaf")

	route, ok := r.Match(http.MethodGet, "/m0/m1/m2/m3/m4/m5/leaf")
	if !ok || route.Pattern != "/m0/m1/m2/m3/m4/m5/leaf" || len(route.Labels) != 7 {
		t.Fatalf("deep route: %+v %v", route, ok)
	}

	if label, ok := r.MatchLabel(http.MethodGet, "/m0/m1/m2/m3/m4/m5/leaf", "leaf", "m5"); !ok || label != "m5" {
		t.Fatalf("deep label: %q %v", label, ok)
	}

	// The innermost mount that has no route for the path.
	if route, ok := r.Owns("/m0/m1/m2/m3/m4/m5/missing"); !ok || !route.Mount || route.Pattern != "/m0/m1/m2/m3/m4/m5/" {
		t.Fatalf("deep mount: %+v %v", route, ok)
	}
}

func TestMatchKeepsRedirectHandlersOfTheServeMux(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/old", http.RedirectHandler("/new", http.StatusMovedPermanently))

	r := New(mux, "", "")
	r.Handle("/", text("site"))

	// A redirect registered on the ServeMux is a handler of its own, not a
	// redirect of the ServeMux to "/old/".
	if route, ok := r.Match(http.MethodGet, "/old"); ok {
		t.Fatalf("matched: %+v", route)
	}
}
