package router

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func activeTestRouter() *Router {
	r := NewSite()
	r.Get("/{$}", noopHandler).As("home")
	r.Get("/admin/{$}", noopHandler).As("admin.dashboard")
	r.Get("/admin/news", noopHandler).As("admin.news")
	r.Get("/admin/newsletter", noopHandler).As("admin.newsletter")
	r.Get("/users/{id}", noopHandler).As("users.show")
	r.Get("/users/{id}/posts/{post}", noopHandler).As("users.posts.show")
	r.Get("/docs/{path...}", noopHandler).As("docs")

	return r
}

func TestIsActiveRoute(t *testing.T) {
	r := activeTestRouter()

	tests := []struct {
		name    string
		current string
		route   string
		args    []any
		want    bool
		exact   bool
	}{
		{name: "same path", current: "/admin/news", route: "admin.news", want: true, exact: true},
		{name: "below", current: "/admin/news/7/edit", route: "admin.news", want: true},
		{name: "below with slash", current: "/admin/news/", route: "admin.news", want: true},
		{name: "no path boundary", current: "/admin/newsletter", route: "admin.news"},
		{name: "other route", current: "/admin/news", route: "admin.newsletter"},
		{name: "subtree route", current: "/admin/", route: "admin.dashboard", want: true, exact: true},
		{name: "below subtree route", current: "/admin/news", route: "admin.dashboard", want: true},
		{name: "subtree route without slash", current: "/admin", route: "admin.dashboard"},
		{name: "home", current: "/", route: "home", want: true, exact: true},
		{name: "below home", current: "/admin", route: "home", want: true},
		{name: "wildcard", current: "/users/7", route: "users.show", want: true, exact: true},
		{name: "below wildcard", current: "/users/7/posts/2", route: "users.show", want: true},
		{name: "empty wildcard", current: "/users/", route: "users.show"},
		{name: "param", current: "/users/7", route: "users.show", args: []any{"id", 7}, want: true, exact: true},
		{name: "other param", current: "/users/8", route: "users.show", args: []any{"id", 7}},
		{name: "param below", current: "/users/7/posts/2", route: "users.show", args: []any{"id", "7"}, want: true},
		{
			name:    "partial params",
			current: "/users/7/posts/2",
			route:   "users.posts.show",
			args:    []any{"id", 7},
			want:    true,
			exact:   true,
		},
		{
			name:    "params map",
			current: "/users/7/posts/2",
			route:   "users.posts.show",
			args:    []any{map[string]any{"id": 7, "post": 3}},
		},
		{name: "escaped param", current: "/users/a%20b", route: "users.show", args: []any{"id", "a b"}, want: true, exact: true},
		{name: "escaped slash", current: "/users/a%2Fb", route: "users.show", args: []any{"id", "a/b"}, want: true, exact: true},
		{
			name:    "query ignored",
			current: "/users/7?tab=posts",
			route:   "users.show",
			args:    []any{"id", 7, url.Values{"x": {"y"}}},
			want:    true,
			exact:   true,
		},
		{name: "rest", current: "/docs/guides/start", route: "docs", want: true, exact: true},
		{
			name:    "rest value",
			current: "/docs/guides/start",
			route:   "docs",
			args:    []any{"path", "guides/start"},
			want:    true,
			exact:   true,
		},
		{name: "rest value prefix", current: "/docs/guides/start", route: "docs", args: []any{"path", "guides"}, want: true},
		{name: "rest other value", current: "/docs/guides/start", route: "docs", args: []any{"path", "api"}},
		{name: "full URL", current: "https://example.com/admin/news/7?x=1", route: "admin.news", want: true},
		{name: "unknown route", current: "/admin/news", route: "nope"},
		{name: "empty", current: "", route: "home"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.IsActiveRoute(tt.current, tt.route, tt.args...)
			if err != nil || got != tt.want {
				t.Fatalf("IsActiveRoute(%q, %q, %v): expected %v, got %v, %v", tt.current, tt.route, tt.args, tt.want, got, err)
			}

			got, err = r.IsActiveRouteExact(tt.current, tt.route, tt.args...)
			if err != nil || got != tt.exact {
				t.Fatalf(
					"IsActiveRouteExact(%q, %q, %v): expected %v, got %v, %v",
					tt.current,
					tt.route,
					tt.args,
					tt.exact,
					got,
					err,
				)
			}
		})
	}
}

func TestIsActiveRouteCurrentTypes(t *testing.T) {
	r := activeTestRouter()
	req := httptest.NewRequest(http.MethodPost, "/admin/news/7", nil)

	currents := []any{
		req,
		req.URL,
		*req.URL,
		"/admin/news/7",
	}

	for _, current := range currents {
		if got, err := r.IsActiveRoute(current, "admin.news"); err != nil || !got {
			t.Fatalf("%T: expected active, got %v, %v", current, got, err)
		}
	}

	for _, current := range []any{nil, (*url.URL)(nil), (*http.Request)(nil)} {
		if got, err := r.IsActiveRoute(current, "home"); err != nil || got {
			t.Fatalf("%T: expected not active without a page, got %v, %v", current, got, err)
		}
	}

	if _, err := r.IsActiveRoute(42, "home"); err == nil {
		t.Fatal("expected an error for a current page of another type")
	}

	if _, err := r.IsActiveRoute("/users/7", "users.show", "slug", "x"); !errors.Is(err, ErrUnknownParam) {
		t.Fatalf("expected ErrUnknownParam, got %v", err)
	}

	if _, err := r.IsActiveRoute("/users/7", "users.show", "id"); err == nil {
		t.Fatal("expected an error for a key without a value")
	}
}

func TestActiveRouteTemplateFuncs(t *testing.T) {
	r := activeTestRouter()

	tmpl := template.Must(template.New("t").Funcs(r.FuncMap()).Parse(
		`{{ isActiveRoute .Req "admin.news" }} {{ isActiveRoute .Path "admin.news" }} ` +
			`{{ isActiveRoute .URL "admin.news" }} {{ isActiveRoute .Path "users.show" "id" 7 }} ` +
			`{{ isActiveRouteExact .Path "admin.news" }} {{ isActiveRouteContains .Path "letter" }}`,
	))

	var out bytes.Buffer

	err := tmpl.Execute(&out, map[string]any{
		"Req":  httptest.NewRequest(http.MethodGet, "/admin/newsletter", nil),
		"Path": "/admin/news",
		"URL":  &url.URL{Path: "/admin/news/3"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := out.String(); got != "false true true false true false" {
		t.Fatalf("unexpected template output %q", got)
	}
}

func TestHasRouteAndRouteOr(t *testing.T) {
	logs := captureDefaultLog(t)

	r := activeTestRouter()

	if !r.HasRoute("users.show") || r.HasRoute("news.index") {
		t.Fatal("expected HasRoute to report the named routes")
	}

	tmpl := template.Must(template.New("t").Funcs(r.FuncMap()).Parse(
		`{{ if hasRoute "news.index" }}news{{ end }}|{{ routeOr "news.index" "#" }}|` +
			`{{ routeOr "news.show" "" "id" 3 }}|{{ routeOr "users.show" "#" "id" 3 }}`,
	))

	var out bytes.Buffer
	if err := tmpl.Execute(&out, nil); err != nil {
		t.Fatal(err)
	}

	if got := out.String(); got != "|#||/users/3" {
		t.Fatalf("unexpected template output %q", got)
	}

	if logs.Len() > 0 {
		t.Fatalf("expected no warnings for optional routes, got %q", logs.String())
	}

	tmpl = template.Must(template.New("t").Funcs(r.FuncMap()).Parse(`{{ routeOr "users.show" "#" }}`))

	err := tmpl.Execute(&out, nil)
	if err == nil || !strings.Contains(err.Error(), ErrMissingParam.Error()) {
		t.Fatalf("expected a missing parameter of an existing route to fail, got %v", err)
	}
}
