package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"testing/fstest"
)

func staticFiles() fstest.MapFS {
	return fstest.MapFS{
		"app.css":       {Data: []byte("css")},
		"img/logo.svg":  {Data: []byte("svg")},
		"a b/c.txt":     {Data: []byte("spaced")},
		"favicon.ico":   {Data: []byte("icon")},
		"docs/guide.md": {Data: []byte("guide")},
	}
}

func TestServeFilesIsANamedRoute(t *testing.T) {
	r := NewSite()

	var seen string

	r.ServeFiles("/assets", http.FS(staticFiles())).As("assets").Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			seen = req.URL.Path
			next.ServeHTTP(w, req)
		})
	})
	r.ServeFiles("/static/{path...}", http.FS(staticFiles())).As("static")

	tests := []struct {
		route string
		args  []any
		want  string
	}{
		{route: "assets", args: []any{"file", "app.css"}, want: "/assets/app.css"},
		{route: "assets", args: []any{"file", "img/logo.svg"}, want: "/assets/img/logo.svg"},
		{route: "assets", args: []any{"file", "a b/c.txt"}, want: "/assets/a%20b/c.txt"},
		{route: "static", args: []any{"path", "docs/guide.md"}, want: "/static/docs/guide.md"},
	}

	for _, tt := range tests {
		got, err := r.RoutePathArgs(tt.route, tt.args...)
		if err != nil || got != tt.want {
			t.Fatalf("RoutePathArgs(%q, %v): expected %q, got %q, %v", tt.route, tt.args, tt.want, got, err)
		}
	}

	if got := r.RoutePath("assets"); got != "/assets/{file...}" {
		t.Fatalf("expected the files route to end in its wildcard, got %q", got)
	}

	// The spaced file comes last of the assets: seen is the path of the
	// last request of the route.
	bodies := []struct{ target, want string }{
		{"/static/docs/guide.md", "guide"},
		{"/assets/app.css", "css"},
		{"/assets/img/logo.svg", "svg"},
		{"/assets/a%20b/c.txt", "spaced"},
	}

	for _, body := range bodies {
		target, want := body.target, body.want

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))

		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Fatalf("%s: expected %q, got %d %q", target, want, w.Code, w.Body.String())
		}
	}

	if seen != "/assets/a b/c.txt" {
		t.Fatalf("expected route middleware to see the request path, got %q", seen)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/missing.css", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a missing file, got %d", w.Code)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets", nil))

	if w.Code != http.StatusMovedPermanently && w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected the files root without slash to redirect, got %d", w.Code)
	}
}

func TestServeFilesInMountedRouter(t *testing.T) {
	parent := NewSite()
	child := NewSite()
	parent.Mount("/admin", child)
	child.ServeFiles("/assets/", http.FS(staticFiles())).As("admin.asset")

	if got, err := child.RoutePathArgs("admin.asset", "file", "img/logo.svg"); err != nil || got != "/admin/assets/img/logo.svg" {
		t.Fatalf("expected the public path of the mounted files route, got %q, %v", got, err)
	}

	if got := serve(t, parent, http.MethodGet, "/admin/assets/img/logo.svg"); got != "svg" {
		t.Fatalf("expected the mounted router to serve the file, got %q", got)
	}
}

func TestServeFileIsANamedRoute(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/robots.txt"

	if err := os.WriteFile(path, []byte("robots"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewSite()

	var used bool

	r.ServeFile("/robots.txt", path).As("robots").Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			used = true
			next.ServeHTTP(w, req)
		})
	})

	if got := r.RoutePath("robots"); got != "/robots.txt" {
		t.Fatalf("expected the named file route, got %q", got)
	}

	if got := serve(t, r, http.MethodGet, "/robots.txt"); got != "robots" || !used {
		t.Fatalf("expected the file through route middleware, got %q, middleware %v", got, used)
	}
}

func TestServeFilesWalk(t *testing.T) {
	r := NewSite()
	r.ServeFiles("/assets/", http.FS(staticFiles()))

	var routes []string

	_ = r.Walk(func(route RouteInfo) error {
		routes = append(routes, fmt.Sprintf("%s %s", route.Method, route.Pattern))

		return nil
	})

	if len(routes) != 1 || routes[0] != "GET /assets/{file...}" {
		t.Fatalf("expected the files route in the route table, got %v", routes)
	}
}
