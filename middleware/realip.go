package middleware

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// RealIPOptions configures RealIPWithOptions.
type RealIPOptions struct {
	// TrustedProxies lists the proxies whose forwarding headers are believed,
	// as IP addresses or CIDR ranges, for example "10.0.0.0/8" or "127.0.0.1".
	// Without trusted proxies the forwarding headers are ignored, since any
	// client can send them. Trusting "0.0.0.0/0" and "::/0" believes every
	// client and is only safe when the application cannot be reached except
	// through its proxies.
	TrustedProxies []string
}

type clientIPContextKey struct{}

type clientInfo struct {
	ip           string
	trustedProxy bool
}

// RealIP stores the client IP in the request context (see ClientIP). It trusts
// no proxies, so the client IP is the address of the direct peer and
// forwarding headers are ignored. Use RealIPWithOptions behind proxies.
func RealIP(next http.Handler) http.Handler {
	return RealIPWithOptions(RealIPOptions{})(next)
}

// RealIPWithOptions returns middleware that finds the client IP and stores it
// in the request context (see ClientIP). Forwarding headers are only believed
// when the direct peer is a trusted proxy. X-Forwarded-For is then read from
// the right, skipping trusted proxies: the first address that is not a trusted
// proxy is the client. X-Real-IP, and then Forwarded, are used when
// X-Forwarded-For is absent.
//
// When the client differs from the direct peer, r.RemoteAddr is rewritten to
// the client IP with port 0, so it stays parseable by net.SplitHostPort.
// Otherwise r.RemoteAddr is left alone.
//
// It panics when a trusted proxy is not an IP address or CIDR range; use
// TryRealIP to get an error instead.
func RealIPWithOptions(options RealIPOptions) func(http.Handler) http.Handler {
	middleware, err := TryRealIP(options)
	if err != nil {
		panic(err)
	}

	return middleware
}

// TryRealIP is RealIPWithOptions returning an error for an invalid trusted
// proxy instead of panicking.
func TryRealIP(options RealIPOptions) (func(http.Handler) http.Handler, error) {
	trustedProxies, err := ParseTrustedProxies(options.TrustedProxies)
	if err != nil {
		return nil, err
	}

	isTrusted := func(ip netip.Addr) bool {
		return slices.ContainsFunc(trustedProxies, func(prefix netip.Prefix) bool {
			return prefix.Contains(ip)
		})
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer := peerAddr(r.RemoteAddr)
			info := clientInfo{
				trustedProxy: peer.IsValid() && isTrusted(peer),
			}

			client := peer
			if info.trustedProxy {
				client = forwardedClient(r, peer, isTrusted)
			}

			info.ip = addrString(client, r.RemoteAddr)
			r = r.WithContext(context.WithValue(r.Context(), clientIPContextKey{}, info))

			if client != peer {
				r.RemoteAddr = net.JoinHostPort(client.String(), "0")
			}

			next.ServeHTTP(w, r)
		})
	}, nil
}

// ParseTrustedProxies parses IP addresses and CIDR ranges. Empty entries are
// skipped; anything else that is not an address or range is an error.
func ParseTrustedProxies(proxies []string) ([]netip.Prefix, error) {
	trusted := make([]netip.Prefix, 0, len(proxies))

	for _, proxy := range proxies {
		proxy = strings.TrimSpace(proxy)
		if proxy == "" {
			continue
		}

		prefix, err := netip.ParsePrefix(proxy)
		if err != nil {
			ip, ipErr := netip.ParseAddr(proxy)
			if ipErr != nil {
				return nil, fmt.Errorf("middleware: trusted proxy %q: not an IP address or CIDR range", proxy)
			}

			ip = ip.Unmap()
			prefix = netip.PrefixFrom(ip, ip.BitLen())
		}

		trusted = append(trusted, prefix.Masked())
	}

	return trusted, nil
}

// ClientIP returns the client IP that RealIP or RealIPWithOptions found. When
// neither ran, it is the IP of the direct peer, or r.RemoteAddr when that is
// not an IP address (for example a Unix socket).
func ClientIP(r *http.Request) string {
	if ip := GetClientIP(r.Context()); ip != "" {
		return ip
	}

	return addrString(peerAddr(r.RemoteAddr), r.RemoteAddr)
}

// GetClientIP returns the client IP stored by RealIP or RealIPWithOptions, or
// "" when neither ran.
func GetClientIP(ctx context.Context) string {
	if info, ok := ctx.Value(clientIPContextKey{}).(clientInfo); ok {
		return info.ip
	}

	return ""
}

// FromTrustedProxy reports whether the request came directly from a trusted
// proxy (see RealIPWithOptions), so that its other forwarding headers, such as
// X-Forwarded-Proto and X-Forwarded-Host, can be believed as well.
func FromTrustedProxy(r *http.Request) bool {
	info, ok := r.Context().Value(clientIPContextKey{}).(clientInfo)

	return ok && info.trustedProxy
}

// forwardedClient finds the client behind a trusted peer. Only the entries the
// trusted proxies appended can be believed, so the hops are read from the
// right and trusted proxies are skipped; the first other address is the
// client. A malformed hop cannot be followed further and ends the walk at the
// last trusted proxy.
func forwardedClient(r *http.Request, peer netip.Addr, trusted func(netip.Addr) bool) netip.Addr {
	hops := headerHops(r.Header.Values("X-Forwarded-For"))
	if len(hops) == 0 {
		if ip, err := parseHop(r.Header.Get("X-Real-IP")); err == nil {
			return ip
		}

		hops = forwardedForHops(r.Header.Values("Forwarded"))
	}

	client := peer

	for i := len(hops) - 1; i >= 0; i-- {
		ip, err := parseHop(hops[i])
		if err != nil {
			return client
		}

		client = ip
		if !trusted(ip) {
			return ip
		}
	}

	return client
}

func headerHops(values []string) []string {
	var hops []string

	for _, value := range values {
		hops = append(hops, strings.Split(value, ",")...)
	}

	return hops
}

// forwardedForHops returns the for= parameters of RFC 7239 Forwarded headers.
// An element without one becomes an empty hop, which ends the walk.
func forwardedForHops(values []string) []string {
	var hops []string

	for _, element := range headerHops(values) {
		hop := ""

		for _, pair := range strings.Split(element, ";") {
			key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if ok && strings.EqualFold(strings.TrimSpace(key), "for") {
				hop = strings.Trim(strings.TrimSpace(value), `"`)
			}
		}

		hops = append(hops, hop)
	}

	return hops
}

// parseHop parses one forwarding entry, which may carry a port and, for IPv6,
// brackets.
func parseHop(hop string) (netip.Addr, error) {
	hop = strings.TrimSpace(hop)

	if addrPort, err := netip.ParseAddrPort(hop); err == nil {
		return addrPort.Addr().Unmap(), nil
	}

	hop = strings.TrimSuffix(strings.TrimPrefix(hop, "["), "]")

	ip, err := netip.ParseAddr(hop)

	return ip.Unmap(), err
}

// peerAddr is the IP of the direct peer, or the zero Addr when RemoteAddr is
// not an IP address.
func peerAddr(remoteAddr string) netip.Addr {
	if addrPort, err := netip.ParseAddrPort(remoteAddr); err == nil {
		return addrPort.Addr().Unmap()
	}

	ip, _ := netip.ParseAddr(remoteAddr)

	return ip.Unmap()
}

func addrString(ip netip.Addr, remoteAddr string) string {
	if !ip.IsValid() {
		return remoteAddr
	}

	return ip.String()
}
