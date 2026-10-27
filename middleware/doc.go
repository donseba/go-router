// Package middleware holds net/http middleware for go-router and any other
// http.Handler: request IDs, logging through log/slog, recovery, the client
// IP behind trusted proxies, security headers and Content-Security-Policy,
// CORS, compression, ETags and Last-Modified checks, an output cache, and
// rate and body limits. Each middleware has the shape
// func(http.Handler) http.Handler. The package uses the standard library
// only.
package middleware
