package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donseba/go-router/middleware"
)

func TestRecover(t *testing.T) {
	t.Run("Recover from panic", func(t *testing.T) {
		mux := http.NewServeMux()
		r := New(mux, "Example API", "1.0.0")

		r.Use(middleware.Recover)

		r.Get("/panic", func(w http.ResponseWriter, r *http.Request) {
			panic("Panic!")
		})

		ts := httptest.NewServer(r)
		defer ts.Close()

		res, err := http.Get(ts.URL + "/panic")
		if err != nil {
			t.Error(err)
		}

		if res.StatusCode != http.StatusInternalServerError {
			t.Errorf("Expected status code %d, got %d", http.StatusInternalServerError, res.StatusCode)
		}

	})
}

func TestTimer(t *testing.T) {
	t.Run("Timer middleware", func(t *testing.T) {
		mux := http.NewServeMux()
		r := New(mux, "Example API", "1.0.0")

		r.Use(middleware.Timer)

		r.Get("/timer", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		ts := httptest.NewServer(r)
		defer ts.Close()

		res, err := http.Get(ts.URL + "/timer")
		if err != nil {
			t.Error(err)
		}

		if res.StatusCode != http.StatusOK {
			t.Errorf("Expected status code %d, got %d", http.StatusOK, res.StatusCode)
		}
	})
}
