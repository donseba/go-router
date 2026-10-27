package router

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"text/template"
)

// atModule is a router not mounted anywhere, with a root route, routes
// below it with and without parameters, and a route ending in a slash.
func atModule() *Router {
	news := NewSite()
	news.Get("/{$}", noopHandler).As("index")
	news.Get("/{slug}", noopHandler).As("show")
	news.Get("/tags/{tag...}", noopHandler).As("tag")
	news.Get("/archive/", noopHandler).As("archive")

	return news
}

// reversed lists what a router gives for atModule's routes.
func reversed(routes *Router) string {
	show, err := routes.RoutePathArgs("show", "slug", "a b/c", url.Values{"page": {"2"}})
	if err != nil {
		return err.Error()
	}

	tag, err := routes.RoutePathArgs("tag", "tag", "go/web dev")
	if err != nil {
		return err.Error()
	}

	return strings.Join([]string{
		routes.RoutePath("index"),
		show,
		tag,
		routes.RoutePath("archive"),
		routes.RoutePathWithParams("show", map[string]any{"slug": "x"}),
		routes.RouteURL("show", map[string]any{"slug": "x"}, "https"),
	}, " ")
}

func TestAt(t *testing.T) {
	news := atModule()

	tests := []struct {
		prefix string
		want   string
	}{
		{"/en/news", "/en/news /en/news/a%20b%2Fc?page=2 /en/news/tags/go/web%20dev /en/news/archive/ /en/news/x /en/news/x"},
		{"/en/news/", "/en/news/ /en/news/a%20b%2Fc?page=2 /en/news/tags/go/web%20dev /en/news/archive/ /en/news/x /en/news/x"},
		{"en/news", "/en/news /en/news/a%20b%2Fc?page=2 /en/news/tags/go/web%20dev /en/news/archive/ /en/news/x /en/news/x"},
		{"/en/", "/en/ /en/a%20b%2Fc?page=2 /en/tags/go/web%20dev /en/archive/ /en/x /en/x"},
		{"/", "/ /a%20b%2Fc?page=2 /tags/go/web%20dev /archive/ /x /x"},
		{"", "/ /a%20b%2Fc?page=2 /tags/go/web%20dev /archive/ /x /x"},
		// The prefix is used as given: an escaped one is not escaped again.
		{
			"/nl/%C3%BCber",
			"/nl/%C3%BCber /nl/%C3%BCber/a%20b%2Fc?page=2 /nl/%C3%BCber/tags/go/web%20dev /nl/%C3%BCber/archive/ /nl/%C3%BCber/x /nl/%C3%BCber/x",
		},
	}

	for _, test := range tests {
		if got := reversed(news.At(test.prefix)); got != test.want {
			t.Errorf("At(%q)\n got %s\nwant %s", test.prefix, got, test.want)
		}
	}

	// The router itself keeps its own paths.
	if got := news.RoutePath("index") + " " + news.RoutePath("show"); got != "/ /{slug}" {
		t.Fatalf("router after At: %q", got)
	}

	at := news.At("/en/news")

	if !at.HasRoute("show") || at.HasRoute("missing") {
		t.Fatal("HasRoute")
	}

	if _, err := at.RoutePathArgs("missing"); err == nil {
		t.Fatal("unknown route without an error")
	}

	if _, err := at.RoutePathArgs("show"); err == nil {
		t.Fatal("missing parameter without an error")
	}

	active, _ := at.IsActiveRoute("/en/news/hello", "show", "slug", "hello")
	exact, _ := at.IsActiveRouteExact("/en/news/", "index")
	index, _ := at.IsActiveRouteExact("/en/news", "index")

	if !active || exact || !index {
		t.Fatalf("active routes: %v %v %v", active, exact, index)
	}

	tmpl := template.Must(template.New("t").Funcs(at.FuncMap()).Parse(
		`{{routePath "index"}} {{route "show" "slug" "hello"}} {{routeOr "missing" "-"}}`,
	))

	var out strings.Builder
	if err := tmpl.Execute(&out, nil); err != nil {
		t.Fatal(err)
	}

	if out.String() != "/en/news /en/news/hello -" {
		t.Fatalf("FuncMap: %q", out.String())
	}
}

// TestAtIsForWithoutARequest checks that At gives the paths For gives in a
// request served at the same prefix.
func TestAtIsForWithoutARequest(t *testing.T) {
	news := atModule()

	var fromFor string

	news.Get("/for/{$}", func(w http.ResponseWriter, req *http.Request) {
		fromFor = reversed(news.For(req))
	})

	for _, prefix := range []string{"/en/news", "/en/news/", "/en/", "/"} {
		site := NewSite()
		site.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			news.ServeAt(w, req, prefix, NamePrefix("news."))
		}))

		fromFor = ""
		record(site, http.MethodGet, strings.TrimSuffix(prefix, "/")+"/for/")

		if want := reversed(news.At(prefix)); fromFor != want {
			t.Errorf("prefix %q: For gives\n%s\nAt gives\n%s", prefix, fromFor, want)
		}
	}
}

// TestPathFunc checks that the RoutePathArgs method values of a router and
// of the routers For and At return are PathFuncs giving their paths.
func TestPathFunc(t *testing.T) {
	news := atModule()

	var fromFor PathFunc

	news.Get("/for/{$}", func(w http.ResponseWriter, req *http.Request) {
		fromFor = news.For(req).RoutePathArgs
	})

	site := NewSite()
	site.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		news.ServeAt(w, req, "/en/news")
	}))

	record(site, http.MethodGet, "/en/news/for/")

	if fromFor == nil {
		t.Fatal("For's handler did not run")
	}

	tests := []struct {
		name  string
		paths PathFunc
		want  string
	}{
		{name: "router", paths: news.RoutePathArgs, want: "/hello"},
		{name: "For", paths: fromFor, want: "/en/news/hello"},
		{name: "At", paths: news.At("/en/news").RoutePathArgs, want: "/en/news/hello"},
	}

	for _, tt := range tests {
		got, err := tt.paths("show", "slug", "hello")
		if err != nil || got != tt.want {
			t.Errorf("%s: got %q, %v, want %q", tt.name, got, err, tt.want)
		}

		if _, err := tt.paths("unknown"); !errors.Is(err, ErrUnknownRoute) {
			t.Errorf("%s: unknown route: got %v, want ErrUnknownRoute", tt.name, err)
		}
	}
}

// TestRootRouteAtAPrefixHasNoTrailingSlash checks that a form posting to
// the root route of a router served at a prefix reaches it with its body,
// on a site that redirects paths with a trailing slash.
func TestRootRouteAtAPrefixHasNoTrailingSlash(t *testing.T) {
	form := NewSite()

	var action string

	form.Get("/{$}", func(w http.ResponseWriter, req *http.Request) {
		action = form.For(req).RoutePath("submit")
	}).As("form")
	form.Post("/{$}", func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		_, _ = fmt.Fprintf(w, "posted %s", body)
	}).As("submit")

	site := NewSite()
	site.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// As a CMS that has its pages without a trailing slash.
		if clean := strings.TrimSuffix(req.URL.Path, "/"); clean != "" && clean != req.URL.Path {
			http.Redirect(w, req, clean, http.StatusMovedPermanently)

			return
		}

		form.ServeAt(w, req, "/en/contact")
	}))

	record(site, http.MethodGet, "/en/contact")

	if action != "/en/contact" || form.At("/en/contact").RoutePath("submit") != "/en/contact" {
		t.Fatalf("action %q, At %q", action, form.At("/en/contact").RoutePath("submit"))
	}

	w := httptest.NewRecorder()
	site.ServeHTTP(w, httptest.NewRequest(http.MethodPost, action, strings.NewReader("name=Ada")))

	if w.Code != http.StatusOK || w.Body.String() != "posted name=Ada" {
		t.Fatalf("post to %q: %d %q", action, w.Code, w.Body.String())
	}
}
