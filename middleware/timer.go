package middleware

import (
	"log"
	"net/http"
	"time"
)

func Timer(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()

		next.ServeHTTP(w, r)

		log.Printf("[go-router] %-10s %-7s %s", time.Since(t), r.Method, r.URL.Path)
	}

	return http.HandlerFunc(fn)
}
