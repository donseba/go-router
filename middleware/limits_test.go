package middleware

import (
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	limiter := NewLimiter(2, time.Minute)
	now := time.Now()

	if !limiter.Allow("a", now) || !limiter.Allow("a", now.Add(time.Second)) || limiter.Allow("a", now.Add(2*time.Second)) {
		t.Fatal("the third request in the window was allowed")
	}

	if !limiter.Allow("b", now) {
		t.Fatal("another key was limited")
	}

	// The window slides: the first request falls out after a minute.
	if !limiter.Allow("a", now.Add(time.Minute)) {
		t.Fatal("a request after the window was refused")
	}

	// Keys whose requests are all old are forgotten.
	limiter.Allow("c", now.Add(10*time.Minute))

	if len(limiter.counts) != 1 {
		t.Fatalf("stale keys kept: %v", limiter.counts)
	}

	var off *Limiter
	if NewLimiter(0, time.Minute) != nil || NewLimiter(1, 0) != nil || !off.Allow("a", now) {
		t.Fatal("a limiter without a limit limits")
	}
}

func TestRateLimit(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})

	post := func(handler http.Handler, method string, remote string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/comments", nil)
		r.RemoteAddr = remote

		return serveRequest(handler, r)
	}

	limited := RateLimit(RateLimitOptions{Limit: 2, Window: time.Minute, Skip: SafeMethod})(ok)

	for i, want := range []int{200, 200, 429} {
		if response := post(limited, http.MethodPost, "192.0.2.1:1234"); response.Code != want {
			t.Fatalf("post %d: got %d", i, response.Code)
		}
	}

	if response := post(limited, http.MethodGet, "192.0.2.1:1234"); response.Code != http.StatusOK {
		t.Fatalf("a skipped request was limited: %d", response.Code)
	}

	if response := post(limited, http.MethodPost, "192.0.2.2:1234"); response.Code != http.StatusOK {
		t.Fatalf("another client was limited: %d", response.Code)
	}

	back := RateLimit(RateLimitOptions{
		Limit:  1,
		Window: time.Minute,
		Key:    func(r *http.Request) string { return r.Header.Get("X-User") },
		OnLimit: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/form?comment=throttled", http.StatusSeeOther)
		}),
	})(ok)

	post(back, http.MethodPost, "192.0.2.1:1")

	if response := post(back, http.MethodPost, "192.0.2.3:1"); response.Code != http.StatusSeeOther {
		t.Fatalf("OnLimit did not answer: %d", response.Code)
	}

	if off := RateLimit(RateLimitOptions{})(ok); post(off, http.MethodPost, "192.0.2.1:1").Code != http.StatusOK {
		t.Fatal("a rate limit without a limit limits")
	}
}

func trustedRequest(target string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.RemoteAddr = "10.0.0.1:1234"

	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Add(headers[i], headers[i+1])
	}

	var served *http.Request

	realIP := RealIPWithOptions(RealIPOptions{TrustedProxies: []string{"10.0.0.0/8"}})
	realIP(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		served = r
	})).ServeHTTP(httptest.NewRecorder(), r)

	return served
}

func TestOrigin(t *testing.T) {
	direct := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	direct.Header.Set("X-Forwarded-Proto", "https")
	direct.Header.Set("X-Forwarded-Host", "evil.example")

	if Origin(direct) != "http://example.com" {
		t.Fatalf("forwarding headers believed without a trusted proxy: %s", Origin(direct))
	}

	secure := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	secure.TLS = &tls.ConnectionState{}

	if Scheme(secure) != "https" {
		t.Fatalf("TLS scheme %s", Scheme(secure))
	}

	tests := []struct {
		headers []string
		want    string
	}{
		{nil, "http://example.com"},
		{[]string{"X-Forwarded-Proto", "https", "X-Forwarded-Host", "www.example.com"}, "https://www.example.com"},
		{[]string{"X-Forwarded-Proto", "http, https", "X-Forwarded-Host", "a.example, b.example:8443"}, "https://b.example:8443"},
		{[]string{"X-Forwarded-Proto", "gopher", "X-Forwarded-Host", "bad host/"}, "http://example.com"},
		{[]string{"X-Forwarded-Host", "[2001:db8::1]:443"}, "http://[2001:db8::1]:443"},
		{[]string{"Forwarded", `for=192.0.2.1;proto=https;host="example.org"`}, "https://example.org"},
		{[]string{"X-Forwarded-Host", "example.com:99999999"}, "http://example.com"},
	}

	for _, test := range tests {
		if got := Origin(trustedRequest("http://example.com/", test.headers...)); got != test.want {
			t.Errorf("%q: got %s, want %s", test.headers, got, test.want)
		}
	}
}

func TestSameOriginReferer(t *testing.T) {
	tests := map[string]string{
		"http://example.com/en/news?page=2": "/en/news?page=2",
		"https://example.com/en/":           "/en/",
		"http://EXAMPLE.com/a":              "/a",
		"http://other.example/en/":          "",
		"javascript:alert(1)":               "",
		"http://example.com//evil.example":  "",
		"":                                  "",
	}

	for referer, want := range tests {
		r := httptest.NewRequest(http.MethodPost, "http://example.com/_/comments/post", nil)
		r.Header.Set("Referer", referer)

		got, ok := SameOriginReferer(r)
		if got != want || ok != (want != "") {
			t.Errorf("%q: got %q %v, want %q", referer, got, ok, want)
		}
	}

	proxied := trustedRequest("http://internal:8080/", "X-Forwarded-Host", "example.com", "Referer", "https://example.com/en/")
	if got, ok := SameOriginReferer(proxied); !ok || got != "/en/" {
		t.Fatalf("behind a proxy: %q %v", got, ok)
	}
}

// TestSameOrigin checks addresses against the host a request arrived on:
// http and https, any case, and the host a trusted proxy reports, not
// r.Host behind it.
func TestSameOrigin(t *testing.T) {
	tests := map[string]bool{
		"http://example.com/en/":     true,
		"https://EXAMPLE.com/a?b=c":  true,
		"HTTPS://example.com/":       true,
		"http://example.com:8080/":   false,
		"http://other.example/":      false,
		"ftp://example.com/":         false,
		"javascript://example.com/":  false,
		"/en/":                       false,
		"//example.com/en/":          false,
		"http://example.com.evil/en": false,
	}

	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)

	for address, want := range tests {
		parsed, err := url.Parse(address)
		if err != nil {
			t.Fatal(err)
		}

		if got := SameOrigin(r, parsed); got != want {
			t.Errorf("%q: got %v, want %v", address, got, want)
		}
	}

	if SameOrigin(r, nil) {
		t.Fatal("a nil URL is same-origin")
	}

	current, _ := url.Parse("https://example.com/en/news")

	proxied := trustedRequest("http://internal:8080/", "X-Forwarded-Host", "example.com")
	if !SameOrigin(proxied, current) {
		t.Fatal("behind a trusted proxy, the reported host is not used")
	}

	untrusted := httptest.NewRequest(http.MethodGet, "http://internal:8080/", nil)
	untrusted.Header.Set("X-Forwarded-Host", "example.com")

	if SameOrigin(untrusted, current) {
		t.Fatal("X-Forwarded-Host of an untrusted client is believed")
	}
}

func TestBodyLimit(t *testing.T) {
	read := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)

		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "handler: too large", http.StatusRequestEntityTooLarge)

			return
		}

		_, _ = io.WriteString(w, "ok")
	})

	request := func(body string, length int64) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.ContentLength = length

		return r
	}

	limited := BodyLimit(5)(read)

	if response := serveRequest(limited, request("12345", 5)); response.Body.String() != "ok" {
		t.Fatalf("within the limit: %q", response.Body.String())
	}

	if response := serveRequest(limited, request("123456", 6)); response.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(response.Body.String(), "handler") {
		t.Fatalf("over the limit: %d %q", response.Code, response.Body.String())
	}

	early := BodyLimit(5, BodyLimitOptions{OnTooLarge: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "early", http.StatusRequestEntityTooLarge)
	})})(read)

	if response := serveRequest(early, request("123456", 6)); !strings.Contains(response.Body.String(), "early") {
		t.Fatalf("declared length: %q", response.Body.String())
	}

	// A body without a length is still limited while it is read.
	if response := serveRequest(early, request("123456", -1)); !strings.Contains(response.Body.String(), "handler") {
		t.Fatalf("unknown length: %q", response.Body.String())
	}
}

func TestCacheHeaders(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("own") {
			w.Header().Set("Cache-Control", "private")
		}

		_, _ = io.WriteString(w, "ok")
	}

	noStore := serveRequest(NoStore(http.HandlerFunc(handler)), httptest.NewRequest(http.MethodGet, "/", nil))
	if got := noStore.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("NoStore: %q", got)
	}

	cached := CacheControl("public, max-age=60")(http.HandlerFunc(handler))

	own := serveRequest(cached, httptest.NewRequest(http.MethodGet, "/?own", nil))
	if got := own.Header().Get("Cache-Control"); got != "private" {
		t.Fatalf("the handler's Cache-Control was replaced: %q", got)
	}

	headers := http.Header{"x-robots-tag": {"noindex, nofollow"}, "X-Frame-Options": {"DENY"}}
	response := serveRequest(Headers(headers)(http.HandlerFunc(handler)), httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Header().Get("X-Robots-Tag") != "noindex, nofollow" || response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("Headers: %v", response.Header())
	}

	robots := serveRequest(
		SecurityHeaders(SecurityHeadersOptions{XRobotsTag: "noindex"})(http.HandlerFunc(handler)),
		httptest.NewRequest(http.MethodGet, "/", nil),
	)
	if robots.Header().Get("X-Robots-Tag") != "noindex" {
		t.Fatalf("SecurityHeaders: %v", robots.Header())
	}
}
