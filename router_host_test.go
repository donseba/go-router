package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostRoutes(t *testing.T) {
	mux := http.NewServeMux()
	r := New(mux, "Example API", "1.0.0")

	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, "default")
	}).As("default.home")

	r.Host("admin.example.com", func(admin *Router) {
		admin.Get("/", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprint(w, "admin")
		}).As("admin.home")
	})

	r.Host("*.example.com", func(wildcard *Router) {
		wildcard.Get("/", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprint(w, "wildcard")
		}).As("tenant.home")
	})

	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "exact host wins", host: "admin.example.com", want: "admin"},
		{name: "host port is ignored", host: "admin.example.com:8080", want: "admin"},
		{name: "wildcard subdomain", host: "acme.example.com", want: "wildcard"},
		{name: "nested wildcard subdomain", host: "app.acme.example.com", want: "wildcard"},
		{name: "apex does not match wildcard", host: "example.com", want: "default"},
		{name: "unknown host falls back to default", host: "other.test", want: "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Host = tt.host
			w := httptest.NewRecorder()

			r.ServeHTTP(w, req)

			if got := w.Body.String(); got != tt.want {
				t.Fatalf("expected response %q, got %q", tt.want, got)
			}
		})
	}

	routes := r.Routes()
	if got := routes["admin.home"].Host; got != "admin.example.com" {
		t.Fatalf("expected admin.home host %q, got %q", "admin.example.com", got)
	}
	if got := routes["tenant.home"].Host; got != "*.example.com" {
		t.Fatalf("expected tenant.home host %q, got %q", "*.example.com", got)
	}
}

func TestSubdomainHelpers(t *testing.T) {
	mux := http.NewServeMux()
	r := New(mux, "Example API", "1.0.0")

	r.Subdomain("app", "example.com", func(app *Router) {
		app.Get("/", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprint(w, "app")
		})
	})

	r.Subdomain("*", "example.test", func(tenant *Router) {
		tenant.Get("/", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprint(w, "tenant")
		})
	})

	tests := []struct {
		host string
		want string
	}{
		{host: "app.example.com", want: "app"},
		{host: "demo.example.test", want: "tenant"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = tt.host
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		if got := w.Body.String(); got != tt.want {
			t.Fatalf("expected response %q, got %q", tt.want, got)
		}
	}
}
