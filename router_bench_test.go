package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

type benchmarkResponseWriter struct {
	header http.Header
	status int
}

func newBenchmarkResponseWriter() *benchmarkResponseWriter {
	return &benchmarkResponseWriter{
		header: make(http.Header),
		status: http.StatusOK,
	}
}

func (w *benchmarkResponseWriter) Header() http.Header {
	return w.header
}

func (w *benchmarkResponseWriter) Write(data []byte) (int, error) {
	return len(data), nil
}

func (w *benchmarkResponseWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}

func (w *benchmarkResponseWriter) Reset() {
	clear(w.header)
	w.status = http.StatusOK
}

// BenchmarkRouter measures the performance of the router under load.
func BenchmarkRouter(b *testing.B) {
	// Set up the router
	mux := http.NewServeMux()
	router := New(mux, "Example API", "1.0.0")

	// Register a large number of routes to simulate complexity
	numRoutes := 1000

	// Register routes at the root level
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("/users/%d", i)
		router.Get(path, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "User")
		})
	}

	// Register routes within a group
	router.Group("/api", func(api *Router) {
		for i := 0; i < numRoutes; i++ {
			path := fmt.Sprintf("/items/%d", i)
			api.Get(path, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "Item")
			})
		}
	})

	// Create test requests for benchmarking
	requests := make([]*http.Request, 0, numRoutes*2)
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("/users/%d", i)
		req := httptest.NewRequest("GET", path, nil)
		requests = append(requests, req)
	}
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("/api/items/%d", i)
		req := httptest.NewRequest("GET", path, nil)
		requests = append(requests, req)
	}

	// Reset the timer to exclude setup time
	b.ResetTimer()

	// Run the benchmark
	for i := 0; i < b.N; i++ {
		for _, req := range requests {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Result().StatusCode != http.StatusOK {
				b.Errorf("Expected status 200, got %d", w.Result().StatusCode)
			}
		}
	}
}

func BenchmarkRouterVsServeMuxRouter(b *testing.B) {
	mux := http.NewServeMux()
	router := New(mux, "Example API", "1.0.0")
	benchRegisterRouterRoutes(router, 1000)
	requests := benchRequests(1000)
	w := newBenchmarkResponseWriter()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, req := range requests {
			w.Reset()
			router.ServeHTTP(w, req)
			if w.status != http.StatusOK {
				b.Fatalf("expected status 200, got %d", w.status)
			}
		}
	}
}

func BenchmarkRouterVsServeMuxStdlib(b *testing.B) {
	mux := http.NewServeMux()
	benchRegisterServeMuxRoutes(mux, 1000)
	requests := benchRequests(1000)
	w := newBenchmarkResponseWriter()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, req := range requests {
			w.Reset()
			mux.ServeHTTP(w, req)
			if w.status != http.StatusOK {
				b.Fatalf("expected status 200, got %d", w.status)
			}
		}
	}
}

func benchRegisterRouterRoutes(router *Router, numRoutes int) {
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("/users/%d", i)
		router.Get(path, func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, "User")
		})
	}

	router.Group("/api", func(api *Router) {
		for i := 0; i < numRoutes; i++ {
			path := fmt.Sprintf("/items/%d", i)
			api.Get(path, func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, "Item")
			})
		}
	})
}

func benchRegisterServeMuxRoutes(mux *http.ServeMux, numRoutes int) {
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("GET /users/%d", i)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, "User")
		})
	}

	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("GET /api/items/%d", i)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, "Item")
		})
	}
}

func benchRequests(numRoutes int) []*http.Request {
	requests := make([]*http.Request, 0, numRoutes*2)
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("/users/%d", i)
		requests = append(requests, httptest.NewRequest(http.MethodGet, path, nil))
	}
	for i := 0; i < numRoutes; i++ {
		path := fmt.Sprintf("/api/items/%d", i)
		requests = append(requests, httptest.NewRequest(http.MethodGet, path, nil))
	}

	return requests
}

func BenchmarkHostRouting(b *testing.B) {
	mux := http.NewServeMux()
	router := New(mux, "Example API", "1.0.0")
	router.Subdomain("*", "example.com", func(tenant *Router) {
		tenant.Get("/dashboard", func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, "Dashboard")
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Host = "acme.example.com"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}

func BenchmarkRoutePathWithParams(b *testing.B) {
	mux := http.NewServeMux()
	router := New(mux, "Example API", "1.0.0")
	router.Get("/users/{id}/files/{name}", func(w http.ResponseWriter, r *http.Request) {}).As("files.show")

	params := map[string]any{
		"id":   123,
		"name": "hello world/a",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = router.RoutePathWithParams("files.show", params)
	}
}

func BenchmarkRouteURL(b *testing.B) {
	mux := http.NewServeMux()
	router := New(mux, "Example API", "1.0.0")
	router.Host("admin.example.com", func(admin *Router) {
		admin.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) {}).As("admin.users.show")
	})

	params := map[string]any{"id": 123}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = router.RouteURL("admin.users.show", params, "https")
	}
}

func BenchmarkRouteTable(b *testing.B) {
	mux := http.NewServeMux()
	router := New(mux, "Example API", "1.0.0")
	for i := 0; i < 1000; i++ {
		path := fmt.Sprintf("/routes/%d", i)
		router.Get(path, func(w http.ResponseWriter, r *http.Request) {}).As(fmt.Sprintf("routes.%d", i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = router.RouteTable()
	}
}

// benchTrackedSite is a site with TrackRoutes and an UseAll middleware that
// reads the matched route, as metrics do, with an admin router mounted on it.
func benchTrackedSite() *Router {
	r := NewSite()
	r.TrackRoutes()

	r.UseAll(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)

			_, _ = MatchedRoute(req)
		})
	})

	r.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) {}).Label("public")

	admin := NewSite()
	admin.Get("/pages/{id}", func(w http.ResponseWriter, r *http.Request) {})
	r.Mount("/admin", admin).Label("admin")

	return r
}

func benchServe(b *testing.B, r http.Handler, target string, status int) {
	b.Helper()

	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := newBenchmarkResponseWriter()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		w.Reset()
		r.ServeHTTP(w, req)

		if w.status != status {
			b.Fatalf("expected status %d, got %d", status, w.status)
		}
	}
}

func BenchmarkTrackedRoute(b *testing.B) {
	benchServe(b, benchTrackedSite(), "/users/7", http.StatusOK)
}

func BenchmarkTrackedMountedRoute(b *testing.B) {
	benchServe(b, benchTrackedSite(), "/admin/pages/7", http.StatusOK)
}

func BenchmarkTrackedMountedNotFound(b *testing.B) {
	benchServe(b, benchTrackedSite(), "/admin/missing", http.StatusNotFound)
}

func BenchmarkMountedRoute(b *testing.B) {
	r := NewSite()

	admin := NewSite()
	admin.Get("/pages/{id}", func(w http.ResponseWriter, r *http.Request) {})
	r.Mount("/admin", admin)

	benchServe(b, r, "/admin/pages/7", http.StatusOK)
}

func BenchmarkServeAt(b *testing.B) {
	news := NewSite()
	news.Get("/{slug}", func(w http.ResponseWriter, r *http.Request) {})

	site := NewSite()
	site.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		news.ServeAt(w, req, "/en/news")
	}))

	benchServe(b, site, "/en/news/hello", http.StatusOK)
}

func BenchmarkOwns(b *testing.B) {
	r := benchTrackedSite()
	r.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	for _, path := range []string{"/users/7", "/admin/missing", "/about"} {
		b.Run(path, func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				if _, ok := r.Owns(path); !ok {
					b.Fatal("not owned")
				}
			}
		})
	}
}

func BenchmarkMatch(b *testing.B) {
	r := benchTrackedSite()

	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, ok := r.Match(http.MethodGet, "/admin/missing"); !ok {
			b.Fatal("no match")
		}
	}
}

// The ways benchClassifySite reads the route of each request.
const (
	benchUntracked = "untracked"
	benchRoute     = "MatchedRoute"
	benchLabels    = "MatchedLabel"
)

// benchClassifySite is a site shaped like a CMS's root router: labelled core
// routes and mounts next to a catch-all for the site's pages. Unless
// untracked, it records routes and reads the matched route's label after
// each request, as metrics and access logs do, with MatchedRoute or with
// MatchedLabel.
func benchClassifySite(read string) *Router {
	var (
		r  = NewSite()
		ok = func(w http.ResponseWriter, r *http.Request) {}
	)

	switch read {
	case benchRoute:
		r.TrackRoutes()
		r.UseAll(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req)

				if route, ok := MatchedRoute(req); ok && len(route.Labels) > 0 {
					benchLabel = route.Labels[0]
				}
			})
		})
	case benchLabels:
		r.TrackRoutes()
		r.UseAll(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req)

				benchLabel, _ = MatchedLabel(req)
			})
		})
	}

	actions := NewSite()
	r.Mount("/_", actions).Label("action")

	polls := NewSite()
	polls.Post("/vote", ok).As("vote")
	actions.Mount("/polls", polls, NamePrefix("polls."))

	r.Mount("/media", http.HandlerFunc(ok)).Label("media")
	r.Get("/theme/{file...}", ok).Label("asset")
	r.Get("/sitemap.xml", ok).Label("seo")

	admin := NewSite()
	admin.Get("/admin/pages", ok)
	r.Mount("/admin", admin, AbsolutePaths()).Label("admin")

	r.Handle("/", http.HandlerFunc(ok))
	r.Get("/healthz", ok).Label("operations")

	return r
}

var benchLabel string

// benchClassifyPaths are a public page (the catch-all), a route of a
// mounted router, a mount's 404, a path the ServeMux redirects to the path
// with a trailing slash and an unclean path it redirects to the clean one.
var benchClassifyPaths = []string{"/en/news/hello", "/admin/pages", "/admin/missing", "/theme", "/admin//pages"}

func BenchmarkClassifyServe(b *testing.B) {
	for _, read := range []string{benchUntracked, benchRoute, benchLabels} {
		r := benchClassifySite(read)

		for _, path := range benchClassifyPaths {
			b.Run(read+path, func(b *testing.B) {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				w := newBenchmarkResponseWriter()

				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					w.Reset()
					r.ServeHTTP(w, req)
				}
			})
		}
	}
}

func BenchmarkClassifyMatch(b *testing.B) {
	r := benchClassifySite(benchUntracked)

	for _, path := range append(benchClassifyPaths, "/_/polls/vote") {
		b.Run("Match"+path, func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_, _ = r.Match(http.MethodGet, path)
			}
		})

		b.Run("MatchLabel"+path, func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				benchLabel, _ = r.MatchLabel(http.MethodGet, path)
			}
		})

		b.Run("Owns"+path, func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_, _ = r.Owns(path)
			}
		})

		b.Run("OwnsLabel"+path, func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				benchLabel, _ = r.OwnsLabel(path)
			}
		})
	}
}

// benchReverseSites returns a media router mounted with a name prefix, as
// a site mounts a module, and a news router served at a prefix (ServeAt).
func benchReverseSites() (media *Router, news *Router) {
	root := NewSite()
	root.TrackRoutes()

	media = NewSite()
	root.Mount("/media", media, NamePrefix("media.")).Label("media")
	media.Get("/v/{signature}/{key...}", func(http.ResponseWriter, *http.Request) {}).As("variant")

	news = NewSite()
	news.Get("/{$}", func(http.ResponseWriter, *http.Request) {}).As("list")
	news.Get("/{slug}", func(http.ResponseWriter, *http.Request) {}).As("post")

	root.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		news.ServeAt(w, req, "/en/news")
	}))

	return media, news
}

// BenchmarkRoutePathArgs measures RoutePathArgs with key value pairs: on a
// mounted route, on a router At a prefix built once, and with At built for
// every link.
func BenchmarkRoutePathArgs(b *testing.B) {
	media, news := benchReverseSites()

	b.Run("mounted", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			_, _ = media.RoutePathArgs(
				"variant",
				"signature",
				"abcdefghijklmnop",
				"key",
				"0b5e1d3c-1111-2222-3333-444455556666_800x0.webp",
			)
		}
	})

	b.Run("at-once", func(b *testing.B) {
		at := news.At("/en/news")

		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			_, _ = at.RoutePathArgs("post", "slug", "hello")
		}
	})

	b.Run("at-each", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			_, _ = news.At("/en/news").RoutePathArgs("post", "slug", "hello")
		}
	})

	b.Run("int", func(b *testing.B) {
		at := news.At("/en/news")

		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			_, _ = at.RoutePathArgs("post", "slug", 42)
		}
	})

	b.Run("map", func(b *testing.B) {
		params := map[string]any{"slug": "hello"}
		at := news.At("/en/news")

		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			_, _ = at.RoutePathArgs("post", params)
		}
	})
}
