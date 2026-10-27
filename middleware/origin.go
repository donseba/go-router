package middleware

import (
	"net/http"
	"net/url"
	"strings"
)

// Scheme returns the scheme a request arrived with, "https" or "http": its
// own TLS state, or, when the request came from a trusted proxy (see
// FromTrustedProxy), what the proxy reports in X-Forwarded-Proto, or else in
// the proto of Forwarded. The headers alone are not believed, since any
// client can send them. Of a list, the last entry counts: the one the
// trusted proxy itself added.
//
// Use it, for example, to mark a cookie Secure:
//
//	http.SetCookie(w, &http.Cookie{Name: "vote", Value: id, Secure: middleware.Scheme(req) == "https"})
func Scheme(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	if !FromTrustedProxy(r) {
		return scheme
	}

	proto := lastEntry(r.Header.Values("X-Forwarded-Proto"))
	if proto == "" {
		proto = forwardedParam(r.Header.Values("Forwarded"), "proto")
	}

	switch proto = strings.ToLower(proto); proto {
	case "http", "https":
		return proto
	default:
		return scheme
	}
}

// Host returns the host, with its port if any, a request arrived on:
// r.Host, or, when the request came from a trusted proxy, what the proxy
// reports in X-Forwarded-Host, or else in the host of Forwarded, when that
// is a valid host name or IP address with an optional port.
func Host(r *http.Request) string {
	if !FromTrustedProxy(r) {
		return r.Host
	}

	host := lastEntry(r.Header.Values("X-Forwarded-Host"))
	if host == "" {
		host = forwardedParam(r.Header.Values("Forwarded"), "host")
	}

	if !validHost(host) {
		return r.Host
	}

	return host
}

// Origin returns the scheme and host a request arrived on, such as
// "https://example.com", for absolute URLs and same-origin checks: Scheme
// and Host.
func Origin(r *http.Request) string {
	return Scheme(r) + "://" + Host(r)
}

// SameOrigin reports whether u is an absolute http or https address on the
// host the request arrived on: its host equals Host(r), ignoring case, so
// it trusts what a trusted proxy reports. Use it for an address the client
// sends, such as the page an htmx request was made on (HX-Current-URL), to
// tell whether it is a page of this site:
//
//	current, err := url.Parse(req.Header.Get("HX-Current-URL"))
//	if err == nil && middleware.SameOrigin(req, current) {
//		// current.Path is a page of this site.
//	}
//
// The schemes are not compared: behind a proxy that does not report the
// scheme, a request may seem to arrive on http for a page on https. Hosts
// compare as written, so example.com and example.com:443 differ. A nil u,
// a relative address and another scheme are not same-origin.
func SameOrigin(r *http.Request, u *url.URL) bool {
	if u == nil || u.Host == "" {
		return false
	}

	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return false
	}

	return strings.EqualFold(u.Host, Host(r))
}

// SameOriginReferer returns the path and query of the request's Referer
// when it is an address on the host the request arrived on (SameOrigin),
// for returning a visitor to the page a form was on, and false otherwise.
// Only a local path is returned (see router.LocalURL), so it can be
// redirected to safely.
func SameOriginReferer(r *http.Request) (string, bool) {
	referer, err := url.Parse(r.Referer())
	if err != nil || !SameOrigin(r, referer) {
		return "", false
	}

	target := referer.EscapedPath()
	if !localTarget(target) {
		return "", false
	}

	if referer.RawQuery != "" {
		target += "?" + referer.RawQuery
	}

	return target, true
}

// localTarget reports what router.LocalURL does, which this package cannot
// import: whether target is a path on this site.
func localTarget(target string) bool {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.ContainsAny(target, "\\\r\n") {
		return false
	}

	parsed, err := url.Parse(target)

	return err == nil && parsed.Scheme == "" && parsed.Host == ""
}

// lastEntry is the last comma-separated entry of header values.
func lastEntry(values []string) string {
	if len(values) == 0 {
		return ""
	}

	entries := strings.Split(values[len(values)-1], ",")

	return strings.TrimSpace(entries[len(entries)-1])
}

// forwardedParam returns a parameter of the last element of RFC 7239
// Forwarded headers, the one the trusted proxy added.
func forwardedParam(values []string, name string) string {
	element := lastEntry(values)

	for _, pair := range strings.Split(element, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.Trim(strings.TrimSpace(value), `"`)
		}
	}

	return ""
}

// validHost reports whether host is a host name, or an IPv6 address in
// brackets, with an optional port of up to five digits.
func validHost(host string) bool {
	if host == "" {
		return false
	}

	var (
		port string
		ok   bool
	)

	if strings.HasPrefix(host, "[") {
		port, ok = ipv6HostPort(host)
	} else {
		port, ok = nameHostPort(host)
	}

	return ok && validPort(port)
}

// ipv6HostPort returns what follows an IPv6 address in brackets at the
// start of host, the port, and whether the address is one.
func ipv6HostPort(host string) (string, bool) {
	end := strings.IndexByte(host, ']')
	if end < 0 {
		return "", false
	}

	address := host[1:end]
	if address == "" || !onlyChars(address, ipv6Char) {
		return "", false
	}

	return host[end+1:], true
}

// nameHostPort returns the port of host, from its first colon on, and
// whether what comes before it is a host name or an IPv4 address.
func nameHostPort(host string) (string, bool) {
	name := host
	port := ""

	if i := strings.IndexByte(host, ':'); i >= 0 {
		name = host[:i]
		port = host[i:]
	}

	if name == "" || !onlyChars(name, hostChar) {
		return "", false
	}

	return port, true
}

// validPort reports whether port is empty, or a colon followed by up to
// five digits.
func validPort(port string) bool {
	if port == "" {
		return true
	}

	digits, ok := strings.CutPrefix(port, ":")
	if !ok || digits == "" || len(digits) > 5 {
		return false
	}

	return onlyChars(digits, func(c rune) bool {
		return c >= '0' && c <= '9'
	})
}

// onlyChars reports whether every character of s is allowed.
func onlyChars(s string, allowed func(rune) bool) bool {
	for _, c := range s {
		if !allowed(c) {
			return false
		}
	}

	return true
}

// ipv6Char reports whether c may appear between the brackets of an IPv6
// host.
func ipv6Char(c rune) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c == ':' || c == '.'
}

// hostChar reports whether c may appear in a host name or an IPv4 address.
func hostChar(c rune) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '-'
}
