package middleware

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestLogHandler checks that records logged with a request's context carry
// its ID once, also when they name it themselves.
func TestLogHandler(t *testing.T) {
	var log bytes.Buffer

	ctx := WithRequestID(context.Background(), "abc")
	logger := slog.New(LogHandler(LogHandler(slog.NewTextHandler(&log, nil))))
	logger.InfoContext(ctx, "hello")
	logger.Info("outside")
	logger.InfoContext(ctx, "named", "request_id", "abc")
	logger.With("component", "pages").InfoContext(ctx, "with")

	if !strings.Contains(log.String(), "msg=hello request_id=abc") || strings.Count(log.String(), "request_id") != 3 {
		t.Fatalf("log:\n%s", log.String())
	}
}

// TestObserve checks the status, size and query-free access log of
// requests.
func TestObserve(t *testing.T) {
	var (
		log   bytes.Buffer
		infos []ResponseInfo
	)

	handler := RequestID(Logger(LoggerOptions{
		Logger:    slog.New(slog.NewTextHandler(&log, nil)),
		OmitQuery: true,
		Attrs: func(r *http.Request, info ResponseInfo) []slog.Attr {
			return []slog.Attr{slog.String("class", "public")}
		},
	})(Observe(func(_ *http.Request, info ResponseInfo) {
		infos = append(infos, info)
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)

			return
		}

		_, _ = io.WriteString(w, "hello")
	}))))

	serveRequest(handler, httptest.NewRequest(http.MethodGet, "/en/?token=secret", nil))
	serveRequest(handler, httptest.NewRequest(http.MethodGet, "/missing", nil))
	serveRequest(handler, httptest.NewRequest(http.MethodPost, "/admin/pages", nil))

	if len(infos) != 3 || infos[0].Status != http.StatusOK || infos[0].Bytes != 5 || infos[1].Status != http.StatusNotFound ||
		infos[0].Panicked {
		t.Fatalf("infos %+v", infos)
	}

	lines := log.String()
	logged := strings.Count(lines, "msg=request") == 3 && !strings.Contains(lines, "secret") &&
		strings.Contains(lines, "uri=/en/ ") && strings.Contains(lines, "status=404") &&
		strings.Contains(lines, "request_id=") && strings.Contains(lines, "class=public")

	if !logged {
		t.Fatalf("access log:\n%s", lines)
	}
}

// A logger that adds request IDs itself gets one request_id per access log
// line, not two.
func TestLoggerWithLogHandler(t *testing.T) {
	var log bytes.Buffer

	logger := slog.New(LogHandler(slog.NewJSONHandler(&log, nil)))
	handler := RequestID(Logger(LoggerOptions{Logger: logger})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello")
	})))

	serveRequest(handler, httptest.NewRequest(http.MethodGet, "/en/", nil))

	if count := strings.Count(log.String(), `"request_id"`); count != 1 {
		t.Fatalf("want one request_id, got %d:\n%s", count, log.String())
	}
}

// TestObservePassingPanics checks that a request whose panic passes through
// is still observed and logged: as a 500 when nothing was written, with its
// status otherwise, and that the panic goes on.
func TestObservePassingPanics(t *testing.T) {
	var (
		log   bytes.Buffer
		infos []ResponseInfo
	)

	logged := Logger(LoggerOptions{Logger: slog.New(slog.NewTextHandler(&log, nil))})
	observed := Observe(func(_ *http.Request, info ResponseInfo) {
		infos = append(infos, info)
	})

	handler := logged(observed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/started" {
			w.WriteHeader(http.StatusAccepted)
		}

		panic(http.ErrAbortHandler)
	})))

	for _, path := range []string{"/en/", "/started"} {
		func() {
			defer func() {
				if recovered := recover(); recovered != http.ErrAbortHandler {
					t.Errorf("%s: the panic does not go on: %v", path, recovered)
				}
			}()

			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		}()
	}

	if len(infos) != 2 || infos[0].Status != http.StatusInternalServerError || !infos[0].Panicked ||
		infos[1].Status != http.StatusAccepted {
		t.Fatalf("infos %+v", infos)
	}

	lines := log.String()
	if !strings.Contains(lines, "uri=/en/ status=500") || !strings.Contains(lines, "uri=/started status=202") ||
		!strings.Contains(lines, "panicked=true") {
		t.Fatalf("access log:\n%s", lines)
	}
}

// TestObserveHeader checks that the observer sees the response headers the
// handler wrote, such as those of a 404 search engines must not index.
func TestObserveHeader(t *testing.T) {
	var infos []ResponseInfo

	handler := Observe(func(_ *http.Request, info ResponseInfo) {
		infos = append(infos, info)
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/private" {
			w.Header().Set("X-Robots-Tag", "noindex")
		}

		http.NotFound(w, r)
	}))

	for _, path := range []string{"/private", "/public"} {
		serveRequest(handler, httptest.NewRequest(http.MethodGet, path, nil))
	}

	if len(infos) != 2 || infos[0].Header.Get("X-Robots-Tag") != "noindex" || infos[1].Header.Get("X-Robots-Tag") != "" ||
		infos[1].Header.Get("Content-Type") == "" {
		t.Fatalf("infos %+v", infos)
	}
}
