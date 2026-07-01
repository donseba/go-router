package router

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTryRegistrationReturnsError(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	if _, err := r.TryGet("/users", func(w http.ResponseWriter, req *http.Request) {}); err != nil {
		t.Fatalf("expected first registration to succeed: %v", err)
	}
	if _, err := r.TryGet("/users", func(w http.ResponseWriter, req *http.Request) {}); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestTryAsReturnsError(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.Get("/one", func(w http.ResponseWriter, req *http.Request) {}).As("same")
	route := r.Get("/two", func(w http.ResponseWriter, req *http.Request) {})

	if err := route.TryAs("same"); err == nil {
		t.Fatal("expected duplicate route name error")
	}
}

func TestParamHelpers(t *testing.T) {
	r := New(http.NewServeMux(), "Example API", "1.0.0")
	r.Get("/users/{id}/active/{active}", func(w http.ResponseWriter, req *http.Request) {
		id, err := IntParam(req, "id")
		if err != nil {
			t.Fatal(err)
		}
		active, err := BoolParam(req, "active")
		if err != nil {
			t.Fatal(err)
		}
		if id != 42 || !active || Param(req, "id") != "42" {
			t.Fatalf("unexpected params id=%d active=%v", id, active)
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/users/42/active/true", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
}

func TestJSONHelpers(t *testing.T) {
	type input struct {
		Name string `json:"name"`
	}

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"Ada"}`))
	var decoded input
	if err := DecodeJSON(req, &decoded); err != nil {
		t.Fatalf("expected decode success: %v", err)
	}
	if decoded.Name != "Ada" {
		t.Fatalf("expected decoded name Ada, got %q", decoded.Name)
	}

	w := httptest.NewRecorder()
	if err := JSON(w, http.StatusCreated, decoded); err != nil {
		t.Fatalf("expected JSON response success: %v", err)
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("expected json content type, got %q", got)
	}

	w = httptest.NewRecorder()
	if err := Error(w, http.StatusTeapot, errors.New("short and stout")); err != nil {
		t.Fatalf("expected error response success: %v", err)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("short and stout")) {
		t.Fatalf("expected error message in response, got %q", w.Body.String())
	}
}
