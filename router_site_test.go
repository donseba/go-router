package router

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewSite(t *testing.T) {
	r := NewSite(WithNotFound(text("not found")))

	r.Get("/about", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, "about")
	}).As("about")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/about", nil))

	if w.Body.String() != "about" {
		t.Fatalf("expected the route, got %q", w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if w.Body.String() != "not found" {
		t.Fatalf("expected the options to be applied, got %q", w.Body.String())
	}

	if len(r.OpenAPI().Paths) != 0 {
		t.Fatalf("expected no OpenAPI paths for a site, got %v", r.OpenAPI().Paths)
	}
}

func TestUseAfterRoutesPanics(t *testing.T) {
	noop := func(next http.Handler) http.Handler { return next }

	r := NewSite()
	r.Use(noop)
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {})

	afterRoute := panics(func() {
		r.Use(noop)
	})
	if !afterRoute {
		t.Fatal("expected Use after a route to panic")
	}

	parent := NewSite()
	parent.Group("/admin", func(admin *Router) {
		admin.Use(noop)
		admin.Get("/", func(w http.ResponseWriter, req *http.Request) {})

		afterGroupRoute := panics(func() {
			admin.Use(noop)
		})
		if !afterGroupRoute {
			t.Fatal("expected Use after a group route to panic")
		}
	})

	afterGroup := panics(func() {
		parent.Use(noop)
	})
	if !afterGroup {
		t.Fatal("expected Use after a group with routes to panic")
	}

	empty := NewSite()
	empty.Group("/empty", func(*Router) {})

	afterEmptyGroup := panics(func() {
		empty.Use(noop)
	})
	if afterEmptyGroup {
		t.Fatal("expected Use after an empty group to work")
	}
}

func TestMountedRouterFreezesWithParent(t *testing.T) {
	parent := NewSite()
	child := NewSite()
	child.Get("/users", func(w http.ResponseWriter, req *http.Request) {})
	parent.Mount("/api", child)

	parent.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if _, err := child.TryGet("/late", func(w http.ResponseWriter, req *http.Request) {}); err == nil {
		t.Fatal("expected registration on a mounted router to fail after the parent served a request")
	}
}

func TestAutomaticOptionsSkipsExplicitOptionsRoute(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.UseOpenapiDocs(true)

	r.Get("/health", func(w http.ResponseWriter, req *http.Request) {}, Docs{Summary: "Health"})
	r.Options("/health", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Allow", "custom")
	}, Docs{Summary: "Health options"})
	r.Options("/ping", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Allow", "custom")
	})
	r.Get("/ping", func(w http.ResponseWriter, req *http.Request) {}, Docs{Summary: "Ping"})

	for _, path := range []string{"/health", "/ping"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodOptions, path, nil))

		if got := w.Header().Get("Allow"); got != "custom" {
			t.Fatalf("%s: expected the explicit OPTIONS handler, got Allow %q", path, got)
		}
	}
}

func TestHandleStatusKeepsStreaming(t *testing.T) {
	r := NewSite()
	r.HandleStatus(http.StatusNotFound, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, "<h1>custom not found</h1>")
	})

	received := make(chan struct{})

	r.Get("/events", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()

		select {
		case <-received:
		case <-time.After(5 * time.Second):
			return
		}

		_, _ = fmt.Fprint(w, "data: second\n\n")
		_ = http.NewResponseController(w).Flush()
	})

	server := httptest.NewServer(r)
	defer server.Close()

	res, err := http.Get(server.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	reader := bufio.NewReader(res.Body)

	line, err := reader.ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("expected the first event while the handler runs, got %q %v", line, err)
	}

	close(received)

	rest, _ := io.ReadAll(reader)
	if !strings.Contains(string(rest), "data: second") {
		t.Fatalf("expected the second event, got %q", rest)
	}
}

func TestHandleStatusHijack(t *testing.T) {
	r := NewSite()
	r.HandleStatus(http.StatusNotFound, func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "custom", http.StatusNotFound)
	})
	r.Get("/ws", func(w http.ResponseWriter, req *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}

		defer func() {
			_ = conn.Close()
		}()

		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 8\r\nConnection: close\r\n\r\nhijacked")
		_ = buf.Flush()
	})

	server := httptest.NewServer(r)
	defer server.Close()

	res, err := http.Get(server.URL + "/ws")
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	body, _ := io.ReadAll(res.Body)
	if string(body) != "hijacked" {
		t.Fatalf("expected the hijacked response, got %q", body)
	}
}

func TestHandleStatusInterception(t *testing.T) {
	r := NewSite()
	r.HandleStatus(http.StatusNotFound, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, "<!doctype html><h1>custom not found</h1>")
	})
	r.Get("/plain", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set(HeaderFlagDoNotIntercept, "1")
		_, _ = fmt.Fprint(w, "no WriteHeader")
	})
	r.Get("/late-status", func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprint(w, "body first")
		w.WriteHeader(http.StatusNotFound)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "custom not found") {
		t.Fatalf("expected the custom 404, got %d %q", w.Code, w.Body.String())
	}

	if got := w.Header().Get("Content-Type"); strings.HasPrefix(got, "text/plain") {
		t.Fatalf("expected the custom page not to inherit text/plain, got %q", got)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/plain", nil))

	if _, leaked := w.Result().Header[http.CanonicalHeaderKey(HeaderFlagDoNotIntercept)]; leaked {
		t.Fatal("expected the internal do-not-intercept header to be removed")
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/late-status", nil))

	if w.Code != http.StatusOK || w.Body.String() != "body first" {
		t.Fatalf("expected a started response not to be intercepted, got %d %q", w.Code, w.Body.String())
	}
}
