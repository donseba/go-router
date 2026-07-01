package router

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestOpenAPIHeadAndOptions(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.UseOpenapiDocs(true)

	r.Head("/health", func(w http.ResponseWriter, req *http.Request) {}, Docs{
		Summary:   "Health head",
		Responses: map[string]Response{"204": {Description: "No Content"}},
	})
	r.Options("/health", func(w http.ResponseWriter, req *http.Request) {}, Docs{
		Summary:   "Health options",
		Responses: map[string]Response{"204": {Description: "No Content"}},
	})

	pathItem := r.OpenAPI().Paths["/health"]
	if pathItem.Head == nil || pathItem.Head.Summary != "Health head" {
		t.Fatalf("expected HEAD operation, got %#v", pathItem.Head)
	}
	if pathItem.Options == nil || pathItem.Options.Summary != "Health options" {
		t.Fatalf("expected OPTIONS operation, got %#v", pathItem.Options)
	}
}

func TestRoutePathWithParamsEscapesValues(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.Get("/files/{name}", func(w http.ResponseWriter, req *http.Request) {}).As("files.show")

	if got := r.RoutePathWithParams("files.show", map[string]any{"name": "hello world/a"}); got != "/files/hello%20world%2Fa" {
		t.Fatalf("expected escaped route path, got %q", got)
	}
}

func TestRouteURL(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.Host("admin.example.com", func(admin *Router) {
		admin.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {}).As("admin.users.show")
	})
	r.Subdomain("*", "example.com", func(tenant *Router) {
		tenant.Get("/dashboard", func(w http.ResponseWriter, req *http.Request) {}).As("tenant.dashboard")
	})
	r.Get("/local/{id}", func(w http.ResponseWriter, req *http.Request) {}).As("local.show")

	if got := r.RouteHost("admin.users.show"); got != "admin.example.com" {
		t.Fatalf("expected route host, got %q", got)
	}
	if got := r.RouteURL("admin.users.show", map[string]any{"id": "a b"}, "https"); got != "https://admin.example.com/users/a%20b" {
		t.Fatalf("expected absolute route URL, got %q", got)
	}
	if got := r.RouteURL("local.show", map[string]any{"id": 12}, "https"); got != "/local/12" {
		t.Fatalf("expected local route URL path, got %q", got)
	}
	if got := r.RouteURL("tenant.dashboard", map[string]any{"subdomain": "acme"}, "https"); got != "https://acme.example.com/dashboard" {
		t.Fatalf("expected wildcard subdomain route URL, got %q", got)
	}
}

func TestRouteTable(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.Get("/users", func(w http.ResponseWriter, req *http.Request) {}).As("users.index")

	table := r.RouteTable()
	for _, want := range []string{"HOST\tMETHOD\tPATH\tNAME", "GET", "/users", "users.index"} {
		if !strings.Contains(table, want) {
			t.Fatalf("expected route table to contain %q, got %q", want, table)
		}
	}
}

func TestOpenAPISchemaGeneration(t *testing.T) {
	type Audit struct {
		Version string `json:"version"`
	}
	type Profile struct {
		Bio string `json:"bio,omitempty"`
	}
	type User struct {
		Audit
		ID        int64          `json:"id" validate:"required"`
		Name      string         `json:"name"`
		Email     string         `json:"email" format:"email"`
		Role      string         `json:"role" enum:"admin|user"`
		Profile   *Profile       `json:"profile,omitempty"`
		Tags      []string       `json:"tags,omitempty"`
		Metadata  map[string]int `json:"metadata,omitempty"`
		CreatedAt time.Time      `json:"created_at"`
		Ignored   string         `json:"-"`
	}

	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.UseOpenapiDocs(true)
	r.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {}, Docs{
		Out: map[string]DocOut{
			"200": {
				ApplicationType: "application/json",
				Description:     "OK",
				Object:          User{},
			},
		},
	})

	schemas := r.OpenAPI().Components.Schemas
	userSchema := schemas["User"]
	if userSchema.Properties["id"].Type != "integer" || userSchema.Properties["id"].Format != "int64" {
		t.Fatalf("expected int64 id schema, got %#v", userSchema.Properties["id"])
	}
	if !slicesContains(userSchema.Required, "id") {
		t.Fatalf("expected id to be required, got %#v", userSchema.Required)
	}
	if _, exists := userSchema.Properties["Ignored"]; exists {
		t.Fatalf("expected ignored field to be omitted, got %#v", userSchema.Properties)
	}
	if userSchema.Properties["profile"].Ref != "#/components/schemas/Profile" || !userSchema.Properties["profile"].Nullable {
		t.Fatalf("expected nullable profile ref, got %#v", userSchema.Properties["profile"])
	}
	if userSchema.Properties["tags"].Type != "array" || userSchema.Properties["tags"].Items.Type != "string" {
		t.Fatalf("expected string array tags schema, got %#v", userSchema.Properties["tags"])
	}
	if userSchema.Properties["created_at"].Type != "string" || userSchema.Properties["created_at"].Format != "date-time" {
		t.Fatalf("expected date-time schema, got %#v", userSchema.Properties["created_at"])
	}
	if userSchema.Properties["email"].Format != "email" {
		t.Fatalf("expected email format, got %#v", userSchema.Properties["email"])
	}
	if len(userSchema.Properties["role"].Enum) != 2 {
		t.Fatalf("expected role enum, got %#v", userSchema.Properties["role"])
	}
	if userSchema.Properties["metadata"].AdditionalProperties == nil || userSchema.Properties["metadata"].AdditionalProperties.Type != "integer" {
		t.Fatalf("expected metadata map schema, got %#v", userSchema.Properties["metadata"])
	}
	if userSchema.Properties["version"].Type != "string" {
		t.Fatalf("expected embedded audit field, got %#v", userSchema.Properties)
	}
	if _, exists := schemas["Profile"]; !exists {
		t.Fatal("expected nested Profile schema")
	}
}

func slicesContains(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}

	return false
}

func TestRouteURLServesGeneratedURL(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.Host("app.example.com", func(app *Router) {
		app.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {
			_, _ = w.Write([]byte(req.PathValue("id")))
		}).As("app.users.show")
	})

	routeURL := r.RouteURL("app.users.show", map[string]any{"id": "abc"}, "https")
	parsedURL, err := url.Parse(routeURL)
	if err != nil {
		t.Fatalf("failed to parse generated URL: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, parsedURL.RequestURI(), nil)
	req.Host = parsedURL.Host
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if got := w.Body.String(); got != "abc" {
		t.Fatalf("expected generated URL to route correctly, got %q", got)
	}
}

func TestRouterFreezesAfterFirstRequest(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	defer func() {
		if recover() == nil {
			t.Fatal("expected registration after first request to panic")
		}
	}()

	r.Get("/late", func(w http.ResponseWriter, req *http.Request) {})
}

func TestRouterInterceptorWritersUnwrap(t *testing.T) {
	rr := httptest.NewRecorder()
	excludeWriter := &excludeHeaderWriter{ResponseWriter: rr}
	interceptor := &routingStatusInterceptWriter{ResponseWriter: excludeWriter}

	if got := excludeWriter.Unwrap(); got != rr {
		t.Fatalf("expected exclude writer to unwrap original writer")
	}
	if got := interceptor.Unwrap(); got != excludeWriter {
		t.Fatalf("expected interceptor to unwrap exclude writer")
	}
}
