package router

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// newsModule is a router not mounted anywhere, served at the address of the
// page it is on, as a CMS module.
func newsModule(seen *[]string) *Router {
	news := NewSite(WithNotFound(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		*seen = append(*seen, "404 "+req.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})))

	news.Get("/{$}", func(w http.ResponseWriter, req *http.Request) {
		routes := news.For(req)
		active, _ := routes.IsActiveRouteExact(req, "index")

		*seen = append(*seen, fmt.Sprintf("index %s %s active=%v", req.URL.Path, routes.RoutePath("index"), active))
	}).As("index")

	news.Get("/{slug}", func(w http.ResponseWriter, req *http.Request) {
		routes := news.For(req)
		self, err := routes.RoutePathArgs("show", "slug", req.PathValue("slug"))
		active, _ := routes.IsActiveRoute(req, "index")
		matched, _ := MatchedRoute(req)

		*seen = append(*seen, fmt.Sprintf("show %s %s %v active=%v matched=%s", req.URL.Path, self, err, active, matched.Path))
	}).As("show").Label("item")

	return news
}

func TestServeAt(t *testing.T) {
	var (
		seen    []string
		matched []string
		news    = newsModule(&seen)
	)

	site := NewSite()
	site.TrackRoutes()
	site.UseAll(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)

			route, ok := MatchedRoute(req)
			matched = append(matched, fmt.Sprintf("%v %s %s %v mount=%v", ok, route.Path, route.Name, route.Labels, route.Mount))
		})
	})

	// The site resolves the page: news is at /en/news, and on the home
	// page of /nl/.
	site.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		prefix := "/nl/"
		if strings.HasPrefix(req.URL.Path, "/en/news") {
			prefix = "/en/news"
		}

		news.ServeAt(w, req, prefix, NamePrefix("news."))
	})).Label("public")

	for _, target := range []string{"/en/news/hello", "/en/news", "/en/news/", "/en/news/a/b", "/nl/", "/nl/hallo"} {
		serve(t, site, http.MethodGet, target)
	}

	wantSeen := []string{
		"show /en/news/hello /en/news/hello <nil> active=true matched=/en/news/{slug}",
		"index /en/news /en/news active=true",
		"index /en/news/ /en/news active=false",
		"404 /en/news/a/b",
		"index /nl/ /nl/ active=true",
		"show /nl/hallo /nl/hallo <nil> active=true matched=/nl/{slug}",
	}

	if !slices.Equal(seen, wantSeen) {
		t.Fatalf("handlers saw\n%q\nwant\n%q", seen, wantSeen)
	}

	wantMatched := []string{
		"true /en/news/{slug} news.show [public item] mount=false",
		"true /en/news/ news.index [public] mount=false",
		"true /en/news/ news.index [public] mount=false",
		"true /en/news/ - [public] mount=true",
		"true /nl/ news.index [public] mount=false",
		"true /nl/{slug} news.show [public item] mount=false",
	}

	for i := range wantMatched {
		wantMatched[i] = strings.Replace(wantMatched[i], " - ", "  ", 1)
	}

	if !slices.Equal(matched, wantMatched) {
		t.Fatalf("matched\n%q\nwant\n%q", matched, wantMatched)
	}

	// Outside a request served at a prefix, For is the router itself.
	if news.For(nil) != news || news.RoutePath("index") != "/" {
		t.Fatalf("For outside ServeAt: %q", news.RoutePath("index"))
	}
}

func TestServeAtWithoutARouter(t *testing.T) {
	var seen []string

	news := newsModule(&seen)
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		news.ServeAt(w, req, "/blog")
	})

	serve(t, handler, http.MethodGet, "/blog/post")
	serve(t, handler, http.MethodGet, "/blog/a/b")

	if w := record(handler, http.MethodGet, "/elsewhere"); w.Code != http.StatusNotFound {
		t.Fatalf("outside the prefix: %d", w.Code)
	}

	// The router answers a 405 itself: no router serves the request around it.
	if w := record(handler, http.MethodPost, "/blog/post"); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("405: %d", w.Code)
	}

	want := []string{
		"show /blog/post /blog/post <nil> active=true matched=/blog/{slug}",
		"404 /blog/a/b",
	}

	if !slices.Equal(seen, want) {
		t.Fatalf("got\n%q\nwant\n%q", seen, want)
	}
}

func TestServeAtInAMountedRouter(t *testing.T) {
	var (
		seen []string
		path string
		news = newsModule(&seen)
	)

	site := NewSite()
	site.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Handlers of a mounted router see the public URL, so the prefix
		// is a public path.
		news.ServeAt(w, req, "/site/news")
	})).Label("public")

	root := NewSite()
	root.TrackRoutes()
	root.UseAll(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)

			route, _ := MatchedRoute(req)
			path = fmt.Sprintf("%s %v", route.Path, route.Labels)
		})
	})
	root.Mount("/site", site).Label("site")

	serve(t, root, http.MethodGet, "/site/news/hello")

	if path != "/site/news/{slug} [site public item]" {
		t.Fatalf("matched %q", path)
	}

	if want := "show /site/news/hello /site/news/hello <nil> active=true matched=/site/news/{slug}"; len(seen) != 1 ||
		seen[0] != want {
		t.Fatalf("got %q", seen)
	}
}

func TestServeAtUseAllAndRedirects(t *testing.T) {
	news := NewSite()
	news.RedirectTrailingSlash(true)

	var around string

	news.UseAll(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			around = req.URL.Path
			next.ServeHTTP(w, req)
		})
	})
	news.Get("/{slug}", noopHandler)

	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		news.ServeAt(w, req, "/en/news")
	})

	w := record(handler, http.MethodGet, "/en/news/hello/")
	if w.Code != DefaultRedirectStatusCode || w.Header().Get("Location") != "/en/news/hello" {
		t.Fatalf("redirect: %d %q", w.Code, w.Header().Get("Location"))
	}

	if around != "/en/news/hello/" {
		t.Fatalf("UseAll saw %q", around)
	}
}

func TestMountedRouterRedirectsToThePublicPath(t *testing.T) {
	child := NewSite()
	child.RedirectTrailingSlash(true)
	child.Get("/users", noopHandler)

	parent := NewSite()
	parent.Mount("/admin", child)

	w := record(parent, http.MethodGet, "/admin/users/")
	if w.Code != DefaultRedirectStatusCode || w.Header().Get("Location") != "/admin/users" {
		t.Fatalf("redirect: %d %q", w.Code, w.Header().Get("Location"))
	}
}
