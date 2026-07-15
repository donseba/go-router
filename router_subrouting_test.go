package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/fstest"
)

func TestMiddlewareOnlyGroup(t *testing.T) {
	mux := http.NewServeMux()
	r := New(mux, "Example API", "1.0.0")

	r.With(func(group *Router) {
		group.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("X-Group", "yes")
				next.ServeHTTP(w, req)
			})
		})

		group.Get("/dashboard", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprint(w, "dashboard")
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if got := w.Body.String(); got != "dashboard" {
		t.Fatalf("expected response %q, got %q", "dashboard", got)
	}
	if got := w.Header().Get("X-Group"); got != "yes" {
		t.Fatalf("expected middleware header %q, got %q", "yes", got)
	}
}

func TestMountRouterUnderPathWithNamedRoutes(t *testing.T) {
	parent := New(http.NewServeMux(), "Parent API", "1.0.0")
	child := New(http.NewServeMux(), "Child API", "1.0.0")

	child.Get("/", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, "child root")
	}).As("child.home")
	child.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprintf(w, "user %s", req.PathValue("id"))
	}).As("child.users.show")

	parent.Mount("/api", child)

	req := httptest.NewRequest(http.MethodGet, "/api/users/42", nil)
	w := httptest.NewRecorder()

	parent.ServeHTTP(w, req)

	if got := w.Body.String(); got != "user 42" {
		t.Fatalf("expected mounted response %q, got %q", "user 42", got)
	}
	if got := parent.RoutePath("child.home"); got != "/api" {
		t.Fatalf("expected mounted child home route %q, got %q", "/api", got)
	}
	if got := parent.RoutePathWithParams("child.users.show", map[string]any{"id": 42}); got != "/api/users/42" {
		t.Fatalf("expected mounted child user route %q, got %q", "/api/users/42", got)
	}
}

func TestMountedRouterHandlersSeePublicRequestURL(t *testing.T) {
	parent := New(http.NewServeMux(), "Parent API", "1.0.0")
	child := New(http.NewServeMux(), "Child API", "1.0.0")

	child.Get("/", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, req.URL.Path)
	})
	child.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprintf(w, "path=%s raw=%s escaped=%s query=%s uri=%s id=%s",
			req.URL.Path,
			req.URL.RawPath,
			req.URL.EscapedPath(),
			req.URL.RawQuery,
			req.RequestURI,
			req.PathValue("id"),
		)
	})
	parent.Mount("/api", child)

	t.Run("mount root", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		w := httptest.NewRecorder()

		parent.ServeHTTP(w, req)

		if got := w.Body.String(); got != "/api" {
			t.Fatalf("expected mounted root URL path %q, got %q", "/api", got)
		}
	})

	t.Run("route parameters query and escaped path", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/users/a%2Fb?tab=profile", nil)
		w := httptest.NewRecorder()

		parent.ServeHTTP(w, req)

		want := "path=/api/users/a/b raw=/api/users/a%2Fb escaped=/api/users/a%2Fb query=tab=profile uri=/api/users/a%2Fb?tab=profile id=a/b"
		if got := w.Body.String(); got != want {
			t.Fatalf("expected mounted handler request %q, got %q", want, got)
		}
	})
}

func TestMountedRouterMiddlewareSeesPublicRequestURL(t *testing.T) {
	parent := New(http.NewServeMux(), "Parent API", "1.0.0")
	child := New(http.NewServeMux(), "Child API", "1.0.0")
	var parentPath, childPath string

	parent.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			parentPath = req.URL.Path
			next.ServeHTTP(w, req)
		})
	})
	child.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			childPath = req.URL.Path
			next.ServeHTTP(w, req)
		})
	})
	child.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {})
	parent.Mount("/api", child)

	req := httptest.NewRequest(http.MethodGet, "/api/users/42", nil)
	parent.ServeHTTP(httptest.NewRecorder(), req)

	if parentPath != "/api/users/42" {
		t.Fatalf("expected parent mount middleware path %q, got %q", "/api/users/42", parentPath)
	}
	if childPath != "/api/users/42" {
		t.Fatalf("expected child route middleware path %q, got %q", "/api/users/42", childPath)
	}
}

func TestNestedMountedRouterHandlerSeesOutermostRequestURL(t *testing.T) {
	parent := New(http.NewServeMux(), "Parent API", "1.0.0")
	middle := New(http.NewServeMux(), "Middle API", "1.0.0")
	child := New(http.NewServeMux(), "Child API", "1.0.0")

	child.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprintf(w, "%s %s", req.URL.Path, req.PathValue("id"))
	})
	middle.Mount("/v1", child)
	parent.Mount("/api", middle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/42", nil)
	w := httptest.NewRecorder()
	parent.ServeHTTP(w, req)

	if got := w.Body.String(); got != "/api/v1/users/42 42" {
		t.Fatalf("expected nested handler to see outer URL and path value, got %q", got)
	}
}

func TestMountedArbitraryHandlerStillSeesStrippedPrefix(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.Mount("/assets", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, req.URL.Path)
	}))

	req := httptest.NewRequest(http.MethodGet, "/assets/app.css", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Body.String(); got != "/app.css" {
		t.Fatalf("expected arbitrary mounted handler path %q, got %q", "/app.css", got)
	}
}

func TestMountRouterUnderHostPreservesChildHostRoutes(t *testing.T) {
	parent := New(http.NewServeMux(), "Parent API", "1.0.0")
	child := New(http.NewServeMux(), "Child API", "1.0.0")

	child.Get("/public", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, "parent host")
	}).As("child.public")
	child.Host("child.example.com", func(hosted *Router) {
		hosted.Get("/private", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprint(w, "child host")
		}).As("child.private")
	})

	parent.Host("parent.example.com", func(hostedParent *Router) {
		hostedParent.Mount("/app", child)
	})

	tests := []struct {
		host string
		path string
		want string
	}{
		{host: "parent.example.com", path: "/app/public", want: "parent host"},
		{host: "child.example.com", path: "/app/private", want: "child host"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		req.Host = tt.host
		w := httptest.NewRecorder()

		parent.ServeHTTP(w, req)

		if got := w.Body.String(); got != tt.want {
			t.Fatalf("for %s %s expected %q, got %q", tt.host, tt.path, tt.want, got)
		}
	}

	if got := parent.RouteHost("child.public"); got != "parent.example.com" {
		t.Fatalf("expected parent host for child public route, got %q", got)
	}
	if got := parent.RouteHost("child.private"); got != "child.example.com" {
		t.Fatalf("expected child host for child private route, got %q", got)
	}
}

func TestWalk(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.Use(func(next http.Handler) http.Handler {
		return next
	})
	homeHandler := func(w http.ResponseWriter, req *http.Request) {}
	r.Get("/", homeHandler).As("home")
	r.Host("admin.example.com", func(admin *Router) {
		admin.Get("/settings", func(w http.ResponseWriter, req *http.Request) {}).As("admin.settings")
	})

	var routes []RouteInfo
	err := r.Walk(func(route RouteInfo) error {
		routes = append(routes, route)
		return nil
	})
	if err != nil {
		t.Fatalf("expected walk to succeed: %v", err)
	}

	if !slices.ContainsFunc(routes, func(route RouteInfo) bool {
		return route.Name == "home" &&
			route.Method == http.MethodGet &&
			route.Path == "/" &&
			route.Handler != nil &&
			len(route.Middlewares) == 1
	}) {
		t.Fatalf("expected walk to include home route, got %#v", routes)
	}

	if !slices.ContainsFunc(routes, func(route RouteInfo) bool {
		return route.Name == "admin.settings" && route.Host == "admin.example.com" && route.Path == "/settings"
	}) {
		t.Fatalf("expected walk to include admin route, got %#v", routes)
	}
}

func TestMountRouterMergesOpenAPI(t *testing.T) {
	parent := New(http.NewServeMux(), "Parent API", "1.0.0")
	child := New(http.NewServeMux(), "Child API", "1.0.0")
	parent.UseOpenapiDocs(true)
	child.UseOpenapiDocs(true)

	type User struct {
		ID string `json:"id"`
	}

	child.OpenAPI().Tags = append(child.OpenAPI().Tags, Tag{Name: "users"})
	child.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {}, Docs{
		Tags:    []string{"users"},
		Summary: "Show user",
		Out: map[string]DocOut{
			"200": {
				ApplicationType: "application/json",
				Description:     "OK",
				Object:          User{},
			},
		},
	})

	parent.Mount("/api", child)

	operation := parent.OpenAPI().Paths["/api/users/{id}"].Get
	if operation == nil {
		t.Fatal("expected mounted OpenAPI operation for /api/users/{id}")
	}
	if operation.Summary != "Show user" {
		t.Fatalf("expected mounted summary %q, got %q", "Show user", operation.Summary)
	}
	if _, exists := parent.OpenAPI().Components.Schemas["User"]; !exists {
		t.Fatal("expected mounted component schema User")
	}
	if !slices.ContainsFunc(parent.OpenAPI().Tags, func(tag Tag) bool {
		return tag.Name == "users"
	}) {
		t.Fatalf("expected mounted OpenAPI tags, got %#v", parent.OpenAPI().Tags)
	}
}

func TestDuplicateRouteNamesPanic(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.Get("/one", func(w http.ResponseWriter, req *http.Request) {}).As("same")
	route := r.Get("/two", func(w http.ResponseWriter, req *http.Request) {})

	defer func() {
		if recover() == nil {
			t.Fatal("expected duplicate route name panic")
		}
	}()

	route.As("same")
}

func TestDuplicateRoutesPanic(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.Get("/users", func(w http.ResponseWriter, req *http.Request) {})

	defer func() {
		if recover() == nil {
			t.Fatal("expected duplicate route panic")
		}
	}()

	r.Get("/users", func(w http.ResponseWriter, req *http.Request) {})
}

func TestGroupOpenAPIDocsInheritance(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.UseOpenapiDocs(true)

	r.Group("/api", func(api *Router) {
		api.UseDocs(Docs{
			Tags:     []string{"api"},
			Security: []map[string][]string{{"bearerAuth": []string{}}},
		})
		api.Get("/users", func(w http.ResponseWriter, req *http.Request) {}, Docs{
			Summary: "List users",
			Responses: map[string]Response{
				"200": {Description: "OK"},
			},
		})
	})

	operation := r.OpenAPI().Paths["/api/users"].Get
	if operation == nil {
		t.Fatal("expected OpenAPI operation for /api/users")
	}
	if !slices.Contains(operation.Tags, "api") {
		t.Fatalf("expected inherited tag api, got %#v", operation.Tags)
	}
	if operation.Summary != "List users" {
		t.Fatalf("expected route summary to be preserved, got %q", operation.Summary)
	}
	if len(operation.Security) != 1 {
		t.Fatalf("expected inherited security, got %#v", operation.Security)
	}
}

func TestTrailingSlashBehaviorInsideGroups(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.Get("/public/", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, "public")
	})
	r.Group("/admin", func(admin *Router) {
		admin.RedirectTrailingSlash(true)
		admin.Get("/users", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprint(w, "admin users")
		})
	})

	tests := []struct {
		path string
		code int
	}{
		{path: "/admin/users/", code: http.StatusTemporaryRedirect},
		{path: "/public/", code: http.StatusOK},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		if got := w.Code; got != tt.code {
			t.Fatalf("for %s expected status %d, got %d", tt.path, tt.code, got)
		}
	}
}

func TestStaticFilesInsideGroupsAndHosts(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	files := fstest.MapFS{
		"asset.txt": {Data: []byte("asset")},
	}

	r.Group("/assets", func(assets *Router) {
		assets.ServeFiles("/", http.FS(files))
	})
	r.Host("cdn.example.com", func(cdn *Router) {
		cdn.ServeFiles("/static", http.FS(files))
	})

	tests := []struct {
		host string
		path string
		want string
	}{
		{path: "/assets/asset.txt", want: "asset"},
		{host: "cdn.example.com", path: "/static/asset.txt", want: "asset"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		req.Host = tt.host
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		if got := w.Body.String(); got != tt.want {
			t.Fatalf("for %s expected response %q, got %q", tt.path, tt.want, got)
		}
	}
}
