package middleware

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestID(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, GetRequestID(r.Context()))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "known-id")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if got := w.Header().Get(RequestIDHeader); got != "known-id" {
		t.Fatalf("expected response request id %q, got %q", "known-id", got)
	}
	if got := w.Body.String(); got != "known-id" {
		t.Fatalf("expected context request id %q, got %q", "known-id", got)
	}
}

func TestRequestIDGeneratesID(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, GetRequestID(r.Context()))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if got := w.Header().Get(RequestIDHeader); len(got) != 32 {
		t.Fatalf("expected generated request id length 32, got %q", got)
	}
	if got := w.Body.String(); got != w.Header().Get(RequestIDHeader) {
		t.Fatalf("expected generated request id in context, got %q", got)
	}
}

func TestRealIP(t *testing.T) {
	handler := RealIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, r.RemoteAddr)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 198.51.100.10")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if got := w.Body.String(); got != "203.0.113.10" {
		t.Fatalf("expected remote addr %q, got %q", "203.0.113.10", got)
	}
}

func TestRealIPWithTrustedProxy(t *testing.T) {
	handler := RealIPWithOptions(RealIPOptions{
		TrustedProxies: []string{"127.0.0.1/32"},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, r.RemoteAddr)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if got := w.Body.String(); got != "203.0.113.10" {
		t.Fatalf("expected trusted forwarded remote addr %q, got %q", "203.0.113.10", got)
	}
}

func TestRealIPWithUntrustedProxyIgnoresForwardedHeaders(t *testing.T) {
	handler := RealIPWithOptions(RealIPOptions{
		TrustedProxies: []string{"10.0.0.0/8"},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, r.RemoteAddr)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if got := w.Body.String(); got != "127.0.0.1" {
		t.Fatalf("expected untrusted forwarded header to be ignored, got %q", got)
	}
}

func TestTimeout(t *testing.T) {
	handler := Timeout(time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusServiceUnavailable {
		t.Fatalf("expected timeout status %d, got %d", http.StatusServiceUnavailable, got)
	}
}

func TestSecurityHeaders(t *testing.T) {
	handler := SecurityHeaders()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff header, got %q", got)
	}
	if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("expected frame options DENY, got %q", got)
	}
	if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("expected referrer policy no-referrer, got %q", got)
	}
}

func TestLogger(t *testing.T) {
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	handler := RequestID(Logger(LoggerOptions{Logger: logger})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, "hello")
	})))

	req := httptest.NewRequest(http.MethodPost, "/items", nil)
	req.Header.Set(RequestIDHeader, "known-id")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	got := logs.String()
	for _, want := range []string{"POST", "/items", "201", "5", "known-id"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected log to contain %q, got %q", want, got)
		}
	}
}

func TestLoggerResponseWriterUnwrap(t *testing.T) {
	rr := httptest.NewRecorder()
	lrw := &loggingResponseWriter{ResponseWriter: rr}

	if got := lrw.Unwrap(); got != rr {
		t.Fatalf("expected logger writer to unwrap original writer")
	}
}

func TestContentLengthResponseWriterUnwrap(t *testing.T) {
	rr := httptest.NewRecorder()
	clw := &contentLengthWriter{ResponseWriter: rr}

	if got := clw.Unwrap(); got != rr {
		t.Fatalf("expected content length writer to unwrap original writer")
	}
}

func TestContentLengthMiddlewareSetsHeaderBeforeWriteHeader(t *testing.T) {
	handler := ContentLengthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, "created")
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, got)
	}
	if got := w.Header().Get("Content-Length"); got != "7" {
		t.Fatalf("expected content length %q, got %q", "7", got)
	}
}
