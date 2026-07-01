package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/donseba/go-router"
	"github.com/donseba/go-router/middleware"
)

func main() {
	r := router.New(http.NewServeMux(), "Production Example", "1.0.0")
	r.RedirectTrailingSlash(true)

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIPWithOptions(middleware.RealIPOptions{
		TrustedProxies: []string{"127.0.0.1/32", "::1/128"},
	}))
	r.Use(middleware.Logger())
	r.Use(middleware.Recover)
	r.Use(middleware.Timeout(10 * time.Second))
	r.Use(middleware.SecurityHeaders())

	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprintln(w, "public site")
	}).As("home")

	r.Subdomain("admin", "example.test", func(admin *router.Router) {
		admin.Use(adminOnly)
		admin.Get("/", func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintln(w, "admin")
		}).As("admin.home")
	})

	r.Subdomain("*", "example.test", func(tenant *router.Router) {
		tenant.Get("/dashboard", func(w http.ResponseWriter, req *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"tenant": tenantName(req.Host),
			})
		}).As("tenant.dashboard")
	})

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}

func adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Admin") != "true" {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, req)
	})
}

func tenantName(host string) string {
	host, _, err := net.SplitHostPort(host)
	if err != nil {
		host = reqHostWithoutPort(host)
	}

	for i, char := range host {
		if char == '.' {
			return host[:i]
		}
	}

	return host
}

func reqHostWithoutPort(host string) string {
	for i, char := range host {
		if char == ':' {
			return host[:i]
		}
	}

	return host
}
