package middleware

import (
	"net/http"
	"strconv"
	"strings"
)

type CORSOptions struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	ExposedHeaders   []string
	AllowCredentials bool
	MaxAge           int
}

// CORS returns a Middleware that handles Cross-Origin Resource Sharing.
func CORS(options CORSOptions) func(http.Handler) http.Handler {
	// Preprocess allowed origins to support wildcards
	var allowAllOrigins bool
	allowedOrigins := make([]string, 0)
	wildcardOrigins := make([]string, 0)
	for _, o := range options.AllowedOrigins {
		if o == "*" {
			allowAllOrigins = true
			break
		} else if strings.HasPrefix(o, "*.") {
			wildcardOrigins = append(wildcardOrigins, o[1:])
		} else {
			allowedOrigins = append(allowedOrigins, o)
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			origin := req.Header.Get("Origin")
			if origin == "" {
				// Not a CORS request
				next.ServeHTTP(w, req)
				return
			}

			// Check if origin is allowed
			var allowedOrigin string
			if allowAllOrigins {
				allowedOrigin = "*"
			} else {
				for _, o := range allowedOrigins {
					if o == origin {
						allowedOrigin = origin
						break
					}
				}
				if allowedOrigin == "" {
					// Check wildcard origins
					for _, wo := range wildcardOrigins {
						if strings.HasSuffix(origin, wo) {
							allowedOrigin = origin
							break
						}
					}
				}
				if allowedOrigin == "" {
					// Origin not allowed
					w.WriteHeader(http.StatusForbidden)
					return
				}
			}

			if req.Method == http.MethodOptions {
				// Preflight request
				w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
				if options.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				if len(options.AllowedMethods) > 0 {
					w.Header().Set("Access-Control-Allow-Methods", strings.Join(options.AllowedMethods, ", "))
				} else {
					// Use the method from the request header
					if reqMethod := req.Header.Get("Access-Control-Request-Method"); reqMethod != "" {
						w.Header().Set("Access-Control-Allow-Methods", reqMethod)
					}
				}
				if len(options.AllowedHeaders) > 0 {
					w.Header().Set("Access-Control-Allow-Headers", strings.Join(options.AllowedHeaders, ", "))
				} else {
					// Use the headers from the request header
					if reqHeaders := req.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
						w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
					}
				}
				if options.MaxAge > 0 {
					w.Header().Set("Access-Control-Max-Age", strconv.Itoa(options.MaxAge))
				}
				w.WriteHeader(http.StatusNoContent)
				return
			} else {
				// Actual request
				w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
				if options.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				if len(options.ExposedHeaders) > 0 {
					w.Header().Set("Access-Control-Expose-Headers", strings.Join(options.ExposedHeaders, ", "))
				}
				// Proceed to the next handler
				next.ServeHTTP(w, req)
			}
		})
	}
}
