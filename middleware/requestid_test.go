package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIDValidation(t *testing.T) {
	tests := []struct {
		incoming string
		keep     bool
	}{
		{incoming: "abc-DEF_123.4:5", keep: true},
		{incoming: strings.Repeat("a", 64), keep: true},
		{incoming: strings.Repeat("a", 65)},
		{incoming: "has space"},
		{incoming: "line\nbreak"},
		{incoming: "quote\""},
		{incoming: "unicodé"},
	}

	for _, tt := range tests {
		var got string

		handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = GetRequestID(r.Context())
		}))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(RequestIDHeader, tt.incoming)

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if tt.keep && got != tt.incoming {
			t.Errorf("%q: expected the incoming ID to be kept, got %q", tt.incoming, got)
		}

		if !tt.keep && (got == tt.incoming || len(got) != 32) {
			t.Errorf("%q: expected a new ID, got %q", tt.incoming, got)
		}

		if header := w.Header().Get(RequestIDHeader); header != got {
			t.Errorf("%q: response header %q differs from context ID %q", tt.incoming, header, got)
		}
	}
}

type otherRequestIDKey struct{}

func TestRequestIDWithOptions(t *testing.T) {
	var got, other string

	handler := RequestIDWithOptions(RequestIDOptions{
		Header:   "X-Trace",
		Generate: func() string { return "generated" },
		WithContext: func(ctx context.Context, id string) context.Context {
			return context.WithValue(ctx, otherRequestIDKey{}, id)
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = GetRequestID(r.Context())
		other, _ = r.Context().Value(otherRequestIDKey{}).(string)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "ignored-header")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got != "generated" || other != "generated" {
		t.Fatalf("expected generated ID in both contexts, got %q and %q", got, other)
	}

	if header := w.Header().Get("X-Trace"); header != "generated" {
		t.Fatalf("expected ID in the configured header, got %q", header)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Trace", "from-proxy")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got != "from-proxy" || other != "from-proxy" {
		t.Fatalf("expected incoming ID in both contexts, got %q and %q", got, other)
	}
}

func TestWithRequestID(t *testing.T) {
	ctx := WithRequestID(context.Background(), "abc")
	if got := GetRequestID(ctx); got != "abc" {
		t.Fatalf("expected stored ID, got %q", got)
	}

	if got := GetRequestID(context.Background()); got != "" {
		t.Fatalf("expected no ID, got %q", got)
	}
}
