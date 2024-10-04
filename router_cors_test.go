package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donseba/go-router/middleware"
)

// Assuming the CORS middleware and related types are defined in the same package
// If they are in a different package, import them accordingly

func TestCORSMiddleware(t *testing.T) {
	// Define the CORS options for the test
	corsOptions := middleware.CORSOptions{
		AllowedOrigins:   []string{"https://example.com", "*.example.com"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		ExposedHeaders:   []string{"X-Custom-Header"},
		AllowCredentials: true,
		MaxAge:           3600,
	}

	// Create the CORS middleware
	corsMiddleware := middleware.CORS(corsOptions)

	// Define a simple handler to use for testing
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responseBody := []byte("Test successful")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		//w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
		w.WriteHeader(http.StatusOK)
		w.Write(responseBody)
	})

	// Wrap the test handler with the CORS middleware
	handlerWithCORS := middleware.ContentLengthMiddleware(corsMiddleware(testHandler))

	// Define test cases
	testCases := []struct {
		name                  string
		method                string
		origin                string
		expectedStatus        int
		expectedAllowOrigin   string
		expectedAllowMethods  string
		expectedAllowHeaders  string
		expectedExposeHeaders string
		expectedAllowCreds    string
		expectedMaxAge        string
		expectedResponseBody  string
		expectedContentType   string
		expectedContentLength string
		expectedAccessControl bool
	}{
		{
			name:                  "Allowed origin, simple request",
			method:                "GET",
			origin:                "https://example.com",
			expectedStatus:        http.StatusOK,
			expectedAllowOrigin:   "https://example.com",
			expectedAllowMethods:  "", // Not set in simple requests
			expectedAllowHeaders:  "", // Not set in simple requests
			expectedExposeHeaders: "X-Custom-Header",
			expectedAllowCreds:    "true",
			expectedMaxAge:        "", // Not set in simple requests
			expectedResponseBody:  "Test successful",
			expectedContentType:   "text/plain; charset=utf-8",
			expectedContentLength: "15",
			expectedAccessControl: true,
		},
		{
			name:                  "Allowed origin with wildcard, simple request",
			method:                "GET",
			origin:                "https://api.example.com",
			expectedStatus:        http.StatusOK,
			expectedAllowOrigin:   "https://api.example.com",
			expectedAllowMethods:  "",
			expectedAllowHeaders:  "",
			expectedExposeHeaders: "X-Custom-Header",
			expectedAllowCreds:    "true",
			expectedMaxAge:        "",
			expectedResponseBody:  "Test successful",
			expectedContentType:   "text/plain; charset=utf-8",
			expectedContentLength: "15",
			expectedAccessControl: true,
		},
		{
			name:                  "Disallowed origin, simple request",
			method:                "GET",
			origin:                "https://notallowed.com",
			expectedStatus:        http.StatusForbidden,
			expectedAllowOrigin:   "",
			expectedAllowMethods:  "",
			expectedAllowHeaders:  "",
			expectedExposeHeaders: "",
			expectedAllowCreds:    "",
			expectedMaxAge:        "",
			expectedResponseBody:  "",
			expectedContentType:   "",
			expectedContentLength: "0",
			expectedAccessControl: false,
		},
		{
			name:                  "Allowed origin, preflight request",
			method:                "OPTIONS",
			origin:                "https://example.com",
			expectedStatus:        http.StatusNoContent,
			expectedAllowOrigin:   "https://example.com",
			expectedAllowMethods:  "GET, POST, PUT, DELETE, OPTIONS",
			expectedAllowHeaders:  "Content-Type, Authorization",
			expectedExposeHeaders: "",
			expectedAllowCreds:    "true",
			expectedMaxAge:        "3600",
			expectedResponseBody:  "",
			expectedContentType:   "",
			expectedContentLength: "0",
			expectedAccessControl: true,
		},
		{
			name:                  "Disallowed origin, preflight request",
			method:                "OPTIONS",
			origin:                "https://notallowed.com",
			expectedStatus:        http.StatusForbidden,
			expectedAllowOrigin:   "",
			expectedAllowMethods:  "",
			expectedAllowHeaders:  "",
			expectedExposeHeaders: "",
			expectedAllowCreds:    "",
			expectedMaxAge:        "",
			expectedResponseBody:  "",
			expectedContentType:   "",
			expectedContentLength: "0",
			expectedAccessControl: false,
		},
		{
			name:                  "No origin header, simple request",
			method:                "GET",
			origin:                "",
			expectedStatus:        http.StatusOK,
			expectedAllowOrigin:   "",
			expectedAllowMethods:  "",
			expectedAllowHeaders:  "",
			expectedExposeHeaders: "",
			expectedAllowCreds:    "",
			expectedMaxAge:        "",
			expectedResponseBody:  "Test successful",
			expectedContentType:   "text/plain; charset=utf-8",
			expectedContentLength: "15",
			expectedAccessControl: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a new HTTP request
			req, err := http.NewRequest(tc.method, "http://localhost/test", nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			// Set the Origin header if provided
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}

			// Create a ResponseRecorder to capture the response
			rr := httptest.NewRecorder()

			// Serve the request
			handlerWithCORS.ServeHTTP(rr, req)

			// Check the status code
			if rr.Code != tc.expectedStatus {
				t.Errorf("Expected status code %d, got %d", tc.expectedStatus, rr.Code)
			}

			// Check CORS headers
			headers := rr.Header()

			if tc.expectedAccessControl {
				if headers.Get("Access-Control-Allow-Origin") != tc.expectedAllowOrigin {
					t.Errorf("Expected Access-Control-Allow-Origin %q, got %q", tc.expectedAllowOrigin, headers.Get("Access-Control-Allow-Origin"))
				}
				if headers.Get("Access-Control-Allow-Methods") != tc.expectedAllowMethods {
					t.Errorf("Expected Access-Control-Allow-Methods %q, got %q", tc.expectedAllowMethods, headers.Get("Access-Control-Allow-Methods"))
				}
				if headers.Get("Access-Control-Allow-Headers") != tc.expectedAllowHeaders {
					t.Errorf("Expected Access-Control-Allow-Headers %q, got %q", tc.expectedAllowHeaders, headers.Get("Access-Control-Allow-Headers"))
				}
				if headers.Get("Access-Control-Expose-Headers") != tc.expectedExposeHeaders {
					t.Errorf("Expected Access-Control-Expose-Headers %q, got %q", tc.expectedExposeHeaders, headers.Get("Access-Control-Expose-Headers"))
				}
				if headers.Get("Access-Control-Allow-Credentials") != tc.expectedAllowCreds {
					t.Errorf("Expected Access-Control-Allow-Credentials %q, got %q", tc.expectedAllowCreds, headers.Get("Access-Control-Allow-Credentials"))
				}
				if headers.Get("Access-Control-Max-Age") != tc.expectedMaxAge {
					t.Errorf("Expected Access-Control-Max-Age %q, got %q", tc.expectedMaxAge, headers.Get("Access-Control-Max-Age"))
				}
			} else {
				if headers.Get("Access-Control-Allow-Origin") != "" {
					t.Errorf("Expected no Access-Control-Allow-Origin header, got %q", headers.Get("Access-Control-Allow-Origin"))
				}
			}

			// Check response body
			if rr.Body.String() != tc.expectedResponseBody {
				t.Errorf("Expected response body %q, got %q", tc.expectedResponseBody, rr.Body.String())
			}

			// Check Content-Type header
			if headers.Get("Content-Type") != tc.expectedContentType {
				t.Errorf("Expected Content-Type %q, got %q", tc.expectedContentType, headers.Get("Content-Type"))
			}

			// Check Content-Length header
			if headers.Get("Content-Length") != tc.expectedContentLength {
				t.Errorf("Expected Content-Length %q, got %q", tc.expectedContentLength, headers.Get("Content-Length"))
			}
		})
	}
}
