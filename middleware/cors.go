package middleware

import (
	"net/http"
	"net/url"
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
				next.ServeHTTP(w, req)
				return
			}

			w.Header().Add("Vary", "Origin")

			var allowedOrigin string
			if allowAllOrigins {
				if options.AllowCredentials {
					if !isValidOrigin(origin) {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					allowedOrigin = origin
				} else {
					allowedOrigin = "*"
				}
			} else {
				for _, o := range allowedOrigins {
					if o == origin {
						allowedOrigin = origin
						break
					}
				}
				if allowedOrigin == "" {
					for _, wo := range wildcardOrigins {
						if originMatchesWildcard(origin, wo) {
							allowedOrigin = origin
							break
						}
					}
				}
				if allowedOrigin == "" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
			}

			if req.Method == http.MethodOptions {
				w.Header().Add("Vary", "Access-Control-Request-Method")
				w.Header().Add("Vary", "Access-Control-Request-Headers")
				reqMethod := req.Header.Get("Access-Control-Request-Method")
				if reqMethod != "" && len(options.AllowedMethods) > 0 && !containsToken(options.AllowedMethods, reqMethod) {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				reqHeaders := headerTokens(req.Header.Get("Access-Control-Request-Headers"))
				if len(reqHeaders) > 0 && len(options.AllowedHeaders) > 0 && !containsAllTokens(options.AllowedHeaders, reqHeaders) {
					w.WriteHeader(http.StatusForbidden)
					return
				}

				w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
				if options.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				if len(options.AllowedMethods) > 0 {
					w.Header().Set("Access-Control-Allow-Methods", strings.Join(options.AllowedMethods, ", "))
				} else {
					if reqMethod != "" {
						w.Header().Set("Access-Control-Allow-Methods", reqMethod)
					}
				}
				if len(options.AllowedHeaders) > 0 {
					w.Header().Set("Access-Control-Allow-Headers", strings.Join(options.AllowedHeaders, ", "))
				} else {
					if len(reqHeaders) > 0 {
						w.Header().Set("Access-Control-Allow-Headers", strings.Join(reqHeaders, ", "))
					}
				}
				if options.MaxAge > 0 {
					w.Header().Set("Access-Control-Max-Age", strconv.Itoa(options.MaxAge))
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			if options.AllowCredentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			if len(options.ExposedHeaders) > 0 {
				w.Header().Set("Access-Control-Expose-Headers", strings.Join(options.ExposedHeaders, ", "))
			}
			next.ServeHTTP(w, req)
		})
	}
}

func originMatchesWildcard(origin string, wildcardSuffix string) bool {
	originURL, err := url.Parse(origin)
	if err != nil || originURL.Hostname() == "" {
		return false
	}

	suffix := strings.TrimPrefix(wildcardSuffix, ".")
	host := strings.ToLower(originURL.Hostname())
	return host != suffix && strings.HasSuffix(host, "."+suffix)
}

func isValidOrigin(origin string) bool {
	if origin == "null" {
		return true
	}

	originURL, err := url.Parse(origin)
	return err == nil && originURL.Scheme != "" && originURL.Hostname() != ""
}

func containsToken(tokens []string, value string) bool {
	for _, token := range tokens {
		if strings.EqualFold(strings.TrimSpace(token), strings.TrimSpace(value)) {
			return true
		}
	}

	return false
}

func containsAllTokens(allowed []string, requested []string) bool {
	for _, value := range requested {
		if !containsToken(allowed, value) {
			return false
		}
	}

	return true
}

func headerTokens(header string) []string {
	if header == "" {
		return nil
	}

	parts := strings.Split(header, ",")
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			tokens = append(tokens, part)
		}
	}

	return tokens
}
