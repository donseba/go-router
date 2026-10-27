package middleware

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// cspOf serves one GET request and returns the Content-Security-Policy
// values of the response as the client gets them.
func cspOf(t *testing.T, handler http.Handler) []string {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	return recorder.Result().Header.Values("Content-Security-Policy")
}

// withPolicy is an outer middleware that sets a policy before the next
// handler runs.
func withPolicy(policy string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", policy)
		next.ServeHTTP(w, r)
	})
}

// writeOK writes a body.
var writeOK = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("ok"))
})

func expectCSP(t *testing.T, got []string, want ...string) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Errorf("Content-Security-Policy =\n%q\nwant\n%q", got, want)
	}
}

func TestCSPSetsPolicyBeforeTheHandler(t *testing.T) {
	var seen string

	handler := CSP(StrictCSP)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = w.Header().Get("Content-Security-Policy")
	}))

	expectCSP(t, cspOf(t, handler), StrictCSP)

	if seen != StrictCSP {
		t.Errorf("the handler saw %q", seen)
	}
}

func TestCSPOverridesDirectivesOfAnOuterPolicy(t *testing.T) {
	handler := withPolicy("default-src 'self'; frame-ancestors 'self'", CSP("frame-ancestors 'none'; object-src 'none'")(writeOK))

	expectCSP(t, cspOf(t, handler), "default-src 'self'; frame-ancestors 'none'; object-src 'none'")
}

func TestCSPInnerWins(t *testing.T) {
	inner := CSP(StrictCSP)(CSP("frame-ancestors 'none'")(writeOK))
	expectCSP(t, cspOf(t, inner), strings.Replace(StrictCSP, "frame-ancestors 'self'", "frame-ancestors 'none'", 1))

	// The outer policy wins nothing, but its directive keeps its place.
	outer := CSP("frame-ancestors 'none'")(CSP(StrictCSP)(writeOK))
	expectCSP(t, cspOf(t, outer), "frame-ancestors 'self'; "+strings.TrimSuffix(StrictCSP, "; frame-ancestors 'self'"))
}

func TestCSPMergesThePolicyOfTheHandler(t *testing.T) {
	handler := CSP("default-src 'self'; frame-ancestors 'self'")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "frame-ancestors https://parent.example; img-src data:")
		w.WriteHeader(http.StatusOK)
	}))

	expectCSP(t, cspOf(t, handler), "default-src 'self'; frame-ancestors https://parent.example; img-src data:")
}

func TestCSPHandlerDeletesThePolicy(t *testing.T) {
	handler := CSP(StrictCSP)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		AddCSPSource(w, "script-src", "'sha256-abc'")
		w.Header().Del("Content-Security-Policy")
		_, _ = w.Write([]byte("ok"))
	}))

	expectCSP(t, cspOf(t, handler))
}

func TestCSPKeepsFurtherPolicies(t *testing.T) {
	handler := withPolicy(
		"default-src 'self', frame-ancestors 'self'",
		CSP("img-src data:")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			AddCSPSource(w, "script-src", "'sha256-abc'")
			w.Header().Add("Content-Security-Policy", "upgrade-insecure-requests")
			_, _ = w.Write([]byte("ok"))
		})),
	)

	expectCSP(t, cspOf(t, handler),
		"default-src 'self'; img-src data:; script-src 'self' 'sha256-abc'",
		"frame-ancestors 'self'",
		"upgrade-insecure-requests",
	)
}

func TestAddCSPSource(t *testing.T) {
	hashed := strings.Replace(StrictCSP, "script-src 'self'", "script-src 'self' 'sha256-abc' 'sha256-def'", 1)

	tests := map[string]http.HandlerFunc{
		"before writing": func(w http.ResponseWriter, r *http.Request) {
			AddCSPSource(w, "script-src", "'sha256-abc'")
			AddCSPSource(w, "script-src", "'sha256-def'", "'sha256-abc'")
			_, _ = w.Write([]byte("ok"))
		},
		"before the handler sets the header": func(w http.ResponseWriter, r *http.Request) {
			AddCSPSource(w, "script-src", "'sha256-abc'", "'sha256-def'")
			w.Header().Set("Content-Security-Policy", "script-src 'self'")
			w.WriteHeader(http.StatusOK)
		},
		"without writing": func(w http.ResponseWriter, r *http.Request) {
			AddCSPSource(w, "script-src", "'sha256-abc'", "'sha256-def'")
		},
		"through another writer": func(w http.ResponseWriter, r *http.Request) {
			wrapped := &lastModifiedWriter{ResponseWriter: w}
			AddCSPSource(wrapped, "script-src", "'sha256-abc'", "'sha256-def'")
			w.Header().Set("Content-Security-Policy", "script-src 'self'")
			_, _ = wrapped.Write([]byte("ok"))
		},
	}

	for name, handler := range tests {
		t.Run(name, func(t *testing.T) {
			expectCSP(t, cspOf(t, CSP(StrictCSP)(handler)), hashed)
		})
	}
}

func TestAddCSPSourceAfterWritingChangesNothing(t *testing.T) {
	handler := CSP(StrictCSP)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		AddCSPSource(w, "script-src", "'sha256-abc'")
	}))

	expectCSP(t, cspOf(t, handler), StrictCSP)
}

func TestAddCSPSourceWithoutCSP(t *testing.T) {
	handler := withPolicy("default-src 'self'", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		AddCSPSource(w, "style-src", "'sha256-abc'")
	}))

	expectCSP(t, cspOf(t, handler), "default-src 'self'; style-src 'self' 'sha256-abc'")

	// A response without a policy gets none: a directive with only the
	// hash would forbid everything else.
	bare := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		AddCSPSource(w, "script-src", "'sha256-abc'")
	})

	expectCSP(t, cspOf(t, bare))
	expectCSP(t, cspOf(t, CSP("")(bare)))
}

func TestCSPAppliesAtTheFinalStatus(t *testing.T) {
	handler := CSP("default-src 'self'")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		AddCSPSource(w, "script-src", "'sha256-abc'")
		w.WriteHeader(http.StatusNotFound)
	}))

	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	_ = response.Body.Close()

	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", response.StatusCode)
	}

	expectCSP(t, response.Header.Values("Content-Security-Policy"), "default-src 'self'; script-src 'self' 'sha256-abc'")
}

// TestCSPInsideCache checks that a page served from the output cache
// carries the policy with the hashes its handler added, with CSP inside
// the cache and outside it.
func TestCSPInsideCache(t *testing.T) {
	var renders int

	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		renders++
		AddCSPSource(w, "script-src", "'sha256-abc'")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<p>page</p>"))
	})

	want := strings.Replace(StrictCSP, "script-src 'self'", "script-src 'self' 'sha256-abc'", 1)

	stacks := map[string]func() http.Handler{
		"inside": func() http.Handler {
			return NewCache().Middleware(CSP(StrictCSP)(page))
		},
		"outside": func() http.Handler {
			return CSP(StrictCSP)(NewCache().Middleware(page))
		},
	}

	for name, build := range stacks {
		t.Run(name, func(t *testing.T) {
			renders = 0
			handler := build()

			for i := range 3 {
				expectCSP(t, cspOf(t, handler), want)

				if renders != 1 {
					t.Fatalf("request %d rendered %d times", i, renders)
				}
			}
		})
	}
}

func TestSecurityHeadersMergesThePolicy(t *testing.T) {
	framing := SecurityHeaders(SecurityHeadersOptions{
		FrameOptions:          "DENY",
		ContentSecurityPolicy: "frame-ancestors 'none'",
	})

	handler := CSP(StrictCSP)(framing(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		AddCSPSource(w, "style-src", "'sha256-abc'")
		_, _ = w.Write([]byte("ok"))
	})))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	want := strings.Replace(StrictCSP, "frame-ancestors 'self'", "frame-ancestors 'none'", 1)
	want = strings.Replace(want, "style-src 'self'", "style-src 'self' 'sha256-abc'", 1)

	expectCSP(t, recorder.Result().Header.Values("Content-Security-Policy"), want)

	if got := recorder.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q", got)
	}

	// Alone it sets the policy as before.
	expectCSP(t, cspOf(t, framing(writeOK)), "frame-ancestors 'none'")
}

// TestCSPConcurrent checks that responses served at once share nothing.
func TestCSPConcurrent(t *testing.T) {
	handler := CSP(StrictCSP)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		AddCSPSource(w, "script-src", "'sha256-"+r.URL.Query().Get("n")+"'")
		_, _ = w.Write([]byte("ok"))
	}))

	var group sync.WaitGroup

	for i := range 32 {
		group.Add(1)

		go func() {
			defer group.Done()

			n := strings.Repeat("a", i+1)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?n="+n, nil))

			want := strings.Replace(StrictCSP, "script-src 'self'", "script-src 'self' 'sha256-"+n+"'", 1)
			if got := recorder.Result().Header.Get("Content-Security-Policy"); got != want {
				t.Errorf("request %d: %q", i, got)
			}
		}()
	}

	group.Wait()
}
