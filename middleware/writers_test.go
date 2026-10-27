package middleware

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stack wraps handler in every bundled middleware that wraps the response
// writer, plus a few that do not.
func stack(handler http.Handler) http.Handler {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	return RequestID(RealIP(Logger(LoggerOptions{Logger: discard})(RecoverWithOptions(RecoverOptions{Logger: discard})(
		TimerWithOptions(TimerOptions{Logger: discard})(SecurityHeaders()(CSP(StrictCSP)(handler))),
	))))
}

func TestStreamingThroughMiddleware(t *testing.T) {
	for name, flush := range map[string]func(http.ResponseWriter) error{
		"http.Flusher": func(w http.ResponseWriter) error {
			flusher, ok := w.(http.Flusher)
			if !ok {
				return fmt.Errorf("%T does not implement http.Flusher", w)
			}

			flusher.Flush()

			return nil
		},
		"http.ResponseController": func(w http.ResponseWriter) error {
			return http.NewResponseController(w).Flush()
		},
	} {
		t.Run(name, func(t *testing.T) {
			received := make(chan struct{})
			errs := make(chan error, 1)

			server := httptest.NewServer(stack(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: first\n\n")

				if err := flush(w); err != nil {
					errs <- err

					return
				}

				select {
				case <-received:
				case <-time.After(5 * time.Second):
					errs <- fmt.Errorf("the first event did not reach the client")

					return
				}

				_, _ = fmt.Fprint(w, "data: second\n\n")
				errs <- nil
			})))
			defer server.Close()

			res, err := http.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}

			defer func() {
				_ = res.Body.Close()
			}()

			reader := bufio.NewReader(res.Body)

			line, err := reader.ReadString('\n')
			if err != nil || line != "data: first\n" {
				t.Fatalf("expected the first event before the handler finished, got %q %v", line, err)
			}

			close(received)

			rest, _ := io.ReadAll(reader)

			if err := <-errs; err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(string(rest), "data: second") {
				t.Fatalf("expected the second event, got %q", rest)
			}
		})
	}
}

func TestHijackThroughMiddleware(t *testing.T) {
	server := httptest.NewServer(stack(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, fmt.Sprintf("%T does not implement http.Hijacker", w), http.StatusInternalServerError)

			return
		}

		conn, buf, err := hijacker.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}

		defer func() {
			_ = conn.Close()
		}()

		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 8\r\nConnection: close\r\n\r\nhijacked")
		_ = buf.Flush()
	})))
	defer server.Close()

	res, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	body, _ := io.ReadAll(res.Body)
	if string(body) != "hijacked" {
		t.Fatalf("expected the hijacked response, got %d %q", res.StatusCode, body)
	}
}

func TestLoggerRecordsStatusAfterEarlyHints(t *testing.T) {
	lrw := &responseRecorder{ResponseWriter: httptest.NewRecorder()}

	lrw.WriteHeader(http.StatusEarlyHints)
	lrw.WriteHeader(http.StatusNotFound)

	if lrw.status != http.StatusNotFound {
		t.Fatalf("expected the final status to be logged, got %d", lrw.status)
	}
}

func TestTimerLogs(t *testing.T) {
	var logs strings.Builder

	timed := TimerWithOptions(TimerOptions{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	handler := timed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/timed", nil))

	for _, want := range []string{"request timed", "method=PUT", "path=/timed", "duration="} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("expected log to contain %q, got %q", want, logs.String())
		}
	}
}

// TestHeaderSetIsolated checks that the values Headers and SecurityHeaders
// set are new for each response: changing or appending to them in one
// response leaves the next, and the other headers, as they were.
func TestHeaderSetIsolated(t *testing.T) {
	set := newHeaderSet(http.Header{
		"x-one":     {"a"},
		"X-Two":     {"b", "c"},
		"X-Three":   {"d"},
		"X-Nothing": {},
	})

	first := http.Header{"X-One": {"old"}}
	set.apply(first)

	if got := first.Values("X-One"); len(got) != 1 || got[0] != "a" {
		t.Fatalf("X-One replaced: %q", got)
	}

	first["X-One"][0] = "changed"
	first.Add("X-Two", "appended")
	first.Add("X-One", "appended")

	if got := first.Values("X-Three"); len(got) != 1 || got[0] != "d" {
		t.Fatalf("an append overwrote X-Three: %q", got)
	}

	second := http.Header{}
	set.apply(second)

	for name, want := range map[string][]string{"X-One": {"a"}, "X-Two": {"b", "c"}, "X-Three": {"d"}, "X-Nothing": {}} {
		if got := second[name]; len(got) != len(want) || len(want) > 0 && strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: want %q, got %q", name, want, got)
		}
	}

	security := serveRequest(
		SecurityHeaders()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})),
		httptest.NewRequest(http.MethodGet, "/", nil),
	)
	for name, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer"} {
		if got := security.Header().Get(name); got != want {
			t.Errorf("%s: want %q, got %q", name, want, got)
		}
	}

	if _, set := security.Header()["Content-Security-Policy"]; set {
		t.Errorf("empty Content-Security-Policy set")
	}

	none := serveRequest(
		SecurityHeaders(SecurityHeadersOptions{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})),
		httptest.NewRequest(http.MethodGet, "/", nil),
	)
	if len(none.Header()) != 0 {
		t.Errorf("headers without options: %v", none.Header())
	}
}
