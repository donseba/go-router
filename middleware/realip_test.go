package middleware

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRealIPClientIP(t *testing.T) {
	tests := []struct {
		name           string
		trusted        []string
		remoteAddr     string
		headers        map[string][]string
		wantIP         string
		wantRemoteAddr string
		wantTrusted    bool
	}{
		{
			name:           "no trusted proxies ignores forwarding headers",
			remoteAddr:     "203.0.113.5:1234",
			headers:        map[string][]string{"X-Forwarded-For": {"198.51.100.1"}, "X-Real-Ip": {"198.51.100.2"}},
			wantIP:         "203.0.113.5",
			wantRemoteAddr: "203.0.113.5:1234",
		},
		{
			name:           "untrusted peer ignores forwarding headers",
			trusted:        []string{"10.0.0.0/8"},
			remoteAddr:     "127.0.0.1:1234",
			headers:        map[string][]string{"X-Forwarded-For": {"203.0.113.10"}},
			wantIP:         "127.0.0.1",
			wantRemoteAddr: "127.0.0.1:1234",
		},
		{
			name:           "trusted peer without headers",
			trusted:        []string{"10.0.0.0/8"},
			remoteAddr:     "10.0.0.2:1234",
			wantIP:         "10.0.0.2",
			wantRemoteAddr: "10.0.0.2:1234",
			wantTrusted:    true,
		},
		{
			name:           "rightmost untrusted hop wins over a spoofed leftmost one",
			trusted:        []string{"10.0.0.0/8"},
			remoteAddr:     "10.0.0.2:1234",
			headers:        map[string][]string{"X-Forwarded-For": {"6.6.6.6, 203.0.113.10, 10.0.0.1"}},
			wantIP:         "203.0.113.10",
			wantRemoteAddr: "203.0.113.10:0",
			wantTrusted:    true,
		},
		{
			name:           "multiple header lines are one list",
			trusted:        []string{"10.0.0.0/8"},
			remoteAddr:     "10.0.0.2:1234",
			headers:        map[string][]string{"X-Forwarded-For": {"6.6.6.6", "203.0.113.10", "10.0.0.1"}},
			wantIP:         "203.0.113.10",
			wantRemoteAddr: "203.0.113.10:0",
			wantTrusted:    true,
		},
		{
			name:           "all hops trusted gives the leftmost",
			trusted:        []string{"10.0.0.0/8"},
			remoteAddr:     "10.0.0.2:1234",
			headers:        map[string][]string{"X-Forwarded-For": {"10.0.0.9, 10.0.0.1"}},
			wantIP:         "10.0.0.9",
			wantRemoteAddr: "10.0.0.9:0",
			wantTrusted:    true,
		},
		{
			name:           "malformed rightmost hop stops at the peer",
			trusted:        []string{"10.0.0.0/8"},
			remoteAddr:     "10.0.0.2:1234",
			headers:        map[string][]string{"X-Forwarded-For": {"203.0.113.10, garbage"}},
			wantIP:         "10.0.0.2",
			wantRemoteAddr: "10.0.0.2:1234",
			wantTrusted:    true,
		},
		{
			name:           "malformed hop stops at the last trusted proxy",
			trusted:        []string{"10.0.0.0/8"},
			remoteAddr:     "10.0.0.2:1234",
			headers:        map[string][]string{"X-Forwarded-For": {"garbage, 10.0.0.1"}},
			wantIP:         "10.0.0.1",
			wantRemoteAddr: "10.0.0.1:0",
			wantTrusted:    true,
		},
		{
			name:           "hop with a port",
			trusted:        []string{"10.0.0.0/8"},
			remoteAddr:     "10.0.0.2:1234",
			headers:        map[string][]string{"X-Forwarded-For": {"203.0.113.10:5555"}},
			wantIP:         "203.0.113.10",
			wantRemoteAddr: "203.0.113.10:0",
			wantTrusted:    true,
		},
		{
			name:           "X-Real-IP without X-Forwarded-For",
			trusted:        []string{"127.0.0.1"},
			remoteAddr:     "127.0.0.1:1234",
			headers:        map[string][]string{"X-Real-Ip": {"203.0.113.10"}},
			wantIP:         "203.0.113.10",
			wantRemoteAddr: "203.0.113.10:0",
			wantTrusted:    true,
		},
		{
			name:           "Forwarded without other headers",
			trusted:        []string{"127.0.0.1"},
			remoteAddr:     "127.0.0.1:1234",
			headers:        map[string][]string{"Forwarded": {`for=192.0.2.60;proto=http, for="[2001:db8::1]:4711"`}},
			wantIP:         "2001:db8::1",
			wantRemoteAddr: "[2001:db8::1]:0",
			wantTrusted:    true,
		},
		{
			name:           "IPv4-mapped IPv6 peer matches an IPv4 range",
			trusted:        []string{"10.0.0.0/8"},
			remoteAddr:     "[::ffff:10.0.0.2]:1234",
			headers:        map[string][]string{"X-Forwarded-For": {"203.0.113.10"}},
			wantIP:         "203.0.113.10",
			wantRemoteAddr: "203.0.113.10:0",
			wantTrusted:    true,
		},
		{
			name:           "IPv6 trusted proxy",
			trusted:        []string{"::1"},
			remoteAddr:     "[::1]:1234",
			headers:        map[string][]string{"X-Forwarded-For": {"2001:db8::7"}},
			wantIP:         "2001:db8::7",
			wantRemoteAddr: "[2001:db8::7]:0",
			wantTrusted:    true,
		},
		{
			name:           "remote address that is not an IP",
			trusted:        []string{"10.0.0.0/8"},
			remoteAddr:     "@",
			headers:        map[string][]string{"X-Forwarded-For": {"203.0.113.10"}},
			wantIP:         "@",
			wantRemoteAddr: "@",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				gotIP         string
				gotRemoteAddr string
				gotTrusted    bool
			)

			realIP := RealIPWithOptions(RealIPOptions{TrustedProxies: tt.trusted})
			handler := realIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotIP = ClientIP(r)
				gotRemoteAddr = r.RemoteAddr
				gotTrusted = FromTrustedProxy(r)
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr

			for key, values := range tt.headers {
				req.Header[http.CanonicalHeaderKey(key)] = values
			}

			handler.ServeHTTP(httptest.NewRecorder(), req)

			if gotIP != tt.wantIP {
				t.Errorf("ClientIP = %q, want %q", gotIP, tt.wantIP)
			}

			if gotRemoteAddr != tt.wantRemoteAddr {
				t.Errorf("RemoteAddr = %q, want %q", gotRemoteAddr, tt.wantRemoteAddr)
			}

			if gotTrusted != tt.wantTrusted {
				t.Errorf("FromTrustedProxy = %v, want %v", gotTrusted, tt.wantTrusted)
			}

			if _, _, err := net.SplitHostPort(gotRemoteAddr); err != nil && gotRemoteAddr != "@" {
				t.Errorf("RemoteAddr %q is not host:port: %v", gotRemoteAddr, err)
			}
		})
	}
}

func TestRealIPIgnoresSpoofedHeaders(t *testing.T) {
	var got string

	handler := RealIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = GetClientIP(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.5:1234"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("X-Real-IP", "127.0.0.1")
	req.Header.Set("Forwarded", "for=127.0.0.1")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got != "203.0.113.5" {
		t.Fatalf("expected the peer address, got %q", got)
	}
}

func TestClientIPWithoutMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "[2001:db8::1]:443"

	if got := ClientIP(req); got != "2001:db8::1" {
		t.Fatalf("expected the peer address, got %q", got)
	}

	if got := GetClientIP(req.Context()); got != "" {
		t.Fatalf("expected no stored client IP, got %q", got)
	}

	if FromTrustedProxy(req) {
		t.Fatal("expected no trusted proxy without middleware")
	}
}

func TestTryRealIPRejectsInvalidProxies(t *testing.T) {
	if _, err := TryRealIP(RealIPOptions{TrustedProxies: []string{"10.0.0.0/8", "localhost"}}); err == nil {
		t.Fatal("expected an error for an invalid trusted proxy")
	}

	if _, err := TryRealIP(RealIPOptions{TrustedProxies: []string{" 10.0.0.1 ", "", "::1/128"}}); err != nil {
		t.Fatalf("expected valid proxies to parse: %v", err)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("expected RealIPWithOptions to panic for an invalid trusted proxy")
		}
	}()

	RealIPWithOptions(RealIPOptions{TrustedProxies: []string{"10.0.0.0/33"}})
}
