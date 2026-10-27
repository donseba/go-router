package router

import (
	"bytes"
	"errors"
	"html/template"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func reverseTestRouter() *Router {
	r := NewSite()
	noop := func(w http.ResponseWriter, req *http.Request) {}

	r.Get("/users/{id}", noop).As("users.show")
	r.Get("/users/{id}/posts/{post}", noop).As("users.posts.show")
	r.Get("/files/{path...}", noop).As("files.show")
	r.Get("/{$}", noop).As("home")
	r.Subdomain("*", "example.com", func(tenant *Router) {
		tenant.Get("/dashboard/{section}", noop).As("tenant.dashboard")
	})
	r.Host("admin.example.com", func(admin *Router) {
		admin.Get("/users/{id}", noop).As("admin.users.show")
	})

	return r
}

// captureDefaultLog sends the records of slog.Default to the returned
// buffer until the test ends.
func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()

	var (
		logs     bytes.Buffer
		previous = slog.Default()
		output   = log.Writer()
		flags    = log.Flags()
	)

	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))

	// SetDefault also sends the log package's output to the handler.
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(output)
		log.SetFlags(flags)
	})

	return &logs
}

// TestRoutePathArgsWithParams checks RoutePathArgs with a map of
// parameters and query values.
func TestRoutePathArgsWithParams(t *testing.T) {
	r := reverseTestRouter()

	tests := []struct {
		name   string
		route  string
		params map[string]any
		query  []url.Values
		want   string
		err    error
	}{
		{name: "single", route: "users.show", params: map[string]any{"id": 42}, want: "/users/42"},
		{name: "escaped", route: "users.show", params: map[string]any{"id": "a b/c"}, want: "/users/a%20b%2Fc"},
		{
			name:   "several",
			route:  "users.posts.show",
			params: map[string]any{"id": 1, "post": "hello"},
			want:   "/users/1/posts/hello",
		},
		{name: "no params", route: "home", want: "/"},
		{
			name:   "wildcard keeps slashes",
			route:  "files.show",
			params: map[string]any{"path": "docs/a b/c.txt"},
			want:   "/files/docs/a%20b/c.txt",
		},
		{
			name:   "wildcard leading slash",
			route:  "files.show",
			params: map[string]any{"path": "/docs/c.txt"},
			want:   "/files/docs/c.txt",
		},
		{
			name:   "wildcard segments",
			route:  "files.show",
			params: map[string]any{"path": []string{"a/b", "c"}},
			want:   "/files/a%2Fb/c",
		},
		{name: "wildcard empty", route: "files.show", params: map[string]any{"path": ""}, want: "/files/"},
		{
			name:   "query",
			route:  "users.show",
			params: map[string]any{"id": 42},
			query:  []url.Values{{"tab": {"posts"}, "q": {"a&b"}}},
			want:   "/users/42?q=a%26b&tab=posts",
		},
		{name: "empty query", route: "users.show", params: map[string]any{"id": 42}, query: []url.Values{nil}, want: "/users/42"},
		{
			name:   "subdomain allowed on wildcard host",
			route:  "tenant.dashboard",
			params: map[string]any{"section": "x", "subdomain": "acme"},
			want:   "/dashboard/x",
		},
		{name: "unknown route", route: "nope", err: ErrUnknownRoute},
		{name: "missing", route: "users.posts.show", params: map[string]any{"id": 1}, err: ErrMissingParam},
		{name: "empty", route: "users.show", params: map[string]any{"id": ""}, err: ErrMissingParam},
		{name: "unknown", route: "users.show", params: map[string]any{"id": 1, "slug": "x"}, err: ErrUnknownParam},
		{
			name:   "subdomain unknown without wildcard host",
			route:  "users.show",
			params: map[string]any{"id": 1, "subdomain": "x"},
			err:    ErrUnknownParam,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []any{tt.params}
			for _, query := range tt.query {
				args = append(args, query)
			}

			got, err := r.RoutePathArgs(tt.route, args...)
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
}

func TestRoutePathArgsMatchesRoute(t *testing.T) {
	r := NewSite()
	r.Get("/files/{path...}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(req.PathValue("path")))
	}).As("files.show")

	path, err := r.RoutePathArgs("files.show", map[string]any{"path": "a b/c%d/e"})
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

	if got := w.Body.String(); got != "a b/c%d/e" {
		t.Fatalf("expected the built path %q to route back to the value, got %q", path, got)
	}
}

func TestRoutePathWithParamsReportsMissingParams(t *testing.T) {
	logs := captureDefaultLog(t)

	r := NewSite()
	r.Get("/users/{id}/posts/{post}", func(w http.ResponseWriter, req *http.Request) {}).As("users.posts.show")
	r.Get("/files/{path...}", func(w http.ResponseWriter, req *http.Request) {}).As("files.show")

	if got := r.RoutePathWithParams("users.posts.show", map[string]any{"id": 1}); got != "/users/1/posts/{post}" {
		t.Fatalf("expected the lenient path, got %q", got)
	}

	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "route=users.posts.show") {
		t.Fatalf("expected a warning about the missing parameter, got %q", logs.String())
	}

	if got := r.RoutePathWithParams("files.show", map[string]any{"path": "a/b"}); got != "/files/a/b" {
		t.Fatalf("expected the wildcard to be filled, got %q", got)
	}

	if got := r.RoutePathWithParams("nope", nil); got != "" {
		t.Fatalf("expected empty path for an unknown route, got %q", got)
	}
}

func TestFuncMapRoute(t *testing.T) {
	r := reverseTestRouter()

	tmpl := template.Must(template.New("page").Funcs(r.FuncMap()).Parse(
		`<a href="{{ route "users.posts.show" "id" .ID "post" "a b" .Query }}">x</a>`,
	))

	var out bytes.Buffer

	err := tmpl.Execute(&out, map[string]any{"ID": 5, "Query": url.Values{"x": {"1"}, "y": {"2"}}})
	if err != nil {
		t.Fatal(err)
	}

	if got := out.String(); got != `<a href="/users/5/posts/a%20b?x=1&amp;y=2">x</a>` {
		t.Fatalf("unexpected template output %q", got)
	}

	tmpl = template.Must(template.New("broken").Funcs(r.FuncMap()).Parse(`{{ route "users.posts.show" "id" 5 }}`))
	if err := tmpl.Execute(&out, nil); err == nil || !strings.Contains(err.Error(), "post") {
		t.Fatalf("expected a template error for the missing parameter, got %v", err)
	}

	tmpl = template.Must(template.New("map").Funcs(r.FuncMap()).Parse(`{{ route "users.show" .Params }}`))
	out.Reset()

	if err := tmpl.Execute(&out, map[string]any{"Params": map[string]any{"id": 9}}); err != nil || out.String() != "/users/9" {
		t.Fatalf("expected params from a map, got %q, %v", out.String(), err)
	}
}
