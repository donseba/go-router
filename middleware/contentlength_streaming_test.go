package middleware

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContentLengthFlushStreamsThroughLogger(t *testing.T) {
	rec := httptest.NewRecorder()
	handler := Logger(LoggerOptions{Logger: log.New(io.Discard, "", 0)})(ContentLengthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, "data: first\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
		if !rec.Flushed || rec.Body.String() != "data: first\n\n" {
			t.Fatalf("first event remained buffered: %q", rec.Body.String())
		}
		if rec.Code != http.StatusCreated || rec.Result().Header.Get("Content-Type") != "text/event-stream" {
			t.Fatalf("stream headers or status missing: %d %v", rec.Code, rec.Result().Header)
		}
		_, _ = fmt.Fprint(w, "data: second\n\n")
		if rec.Body.String() != "data: first\n\ndata: second\n\n" {
			t.Fatalf("later event remained buffered: %q", rec.Body.String())
		}
	})))
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events", nil))
	if rec.Body.String() != "data: first\n\ndata: second\n\n" {
		t.Fatalf("stream duplicated at handler completion: %q", rec.Body.String())
	}
	if got := rec.Result().Header.Get("Content-Length"); got != "" {
		t.Fatalf("stream received automatic Content-Length %q", got)
	}
}

func TestContentLengthFlushSuppressesBody(t *testing.T) {
	for _, tc := range []struct {
		method string
		status int
	}{{http.MethodHead, http.StatusOK}, {http.MethodGet, http.StatusNoContent}, {http.MethodGet, http.StatusNotModified}} {
		rec := httptest.NewRecorder()
		handler := ContentLengthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = fmt.Fprint(w, "before")
			w.(http.Flusher).Flush()
			_, _ = fmt.Fprint(w, "after")
		}))
		handler.ServeHTTP(rec, httptest.NewRequest(tc.method, "/", nil))
		if !rec.Flushed || rec.Body.Len() != 0 || rec.Code != tc.status {
			t.Errorf("%s/%d flushed forbidden body: %q", tc.method, tc.status, rec.Body.String())
		}
	}
}

type nonFlushingWriter struct{ http.ResponseWriter }

func TestContentLengthFlushReturnsUnsupported(t *testing.T) {
	rec := httptest.NewRecorder()
	handler := ContentLengthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "body")
		if err := http.NewResponseController(w).Flush(); !errors.Is(err, http.ErrNotSupported) {
			t.Fatalf("unsupported flush error = %v", err)
		}
	}))
	handler.ServeHTTP(nonFlushingWriter{rec}, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Body.String() != "body" {
		t.Fatalf("unsupported flush lost or duplicated body: %q", rec.Body.String())
	}
}
