package middleware

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoverWritesInternalServerError(t *testing.T) {
	var logs bytes.Buffer

	handler := RequestID(RecoverWithOptions(RecoverOptions{
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		panic("boom")
	})))

	req := httptest.NewRequest(http.MethodGet, "/explode", nil)
	req.Header.Set(RequestIDHeader, "known-id")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", w.Code)
	}

	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("expected a plain text error, got content type %q", got)
	}

	got := logs.String()
	for _, want := range []string{"level=ERROR", "panic recovered", "panic=boom", "path=/explode", "request_id=known-id", "stack=", "recover_test.go"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected log to contain %q, got %q", want, got)
		}
	}
}

func TestRecoverAbortsStartedResponse(t *testing.T) {
	for name, start := range map[string]func(http.ResponseWriter){
		"write": func(w http.ResponseWriter) {
			_, _ = fmt.Fprint(w, "partial")
		},
		"write header": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusAccepted)
		},
		"flush": func(w http.ResponseWriter) {
			_ = http.NewResponseController(w).Flush()
		},
	} {
		t.Run(name, func(t *testing.T) {
			handler := RecoverWithOptions(RecoverOptions{
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				start(w)
				panic("boom")
			}))

			w := httptest.NewRecorder()

			defer func() {
				if recovered := recover(); recovered != http.ErrAbortHandler {
					t.Fatalf("expected http.ErrAbortHandler, got %v", recovered)
				}

				if w.Code == http.StatusInternalServerError || strings.Contains(w.Body.String(), "Internal Server Error") {
					t.Fatalf("expected no 500 after the response started, got %d %q", w.Code, w.Body.String())
				}
			}()

			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		})
	}
}

func TestRecoverWritesAfterInformationalStatus(t *testing.T) {
	handler := RecoverWithOptions(RecoverOptions{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		panic("boom")
	}))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.Contains(w.Body.String(), "Internal Server Error") {
		t.Fatalf("expected a 500 body after 103 Early Hints, got %q", w.Body.String())
	}
}

func TestRecoverPassesAbortHandlerOn(t *testing.T) {
	var logs bytes.Buffer

	handler := RecoverWithOptions(RecoverOptions{
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(fmt.Errorf("client went away: %w", http.ErrAbortHandler))
	}))

	defer func() {
		recovered := recover()

		err, ok := recovered.(error)
		if !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("expected the abort panic to pass, got %v", recovered)
		}

		if logs.Len() != 0 {
			t.Fatalf("expected no log for an aborted handler, got %q", logs.String())
		}
	}()

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}
