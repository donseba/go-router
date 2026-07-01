package middleware

import (
	"net"
	"net/http"
	"strings"
)

type RealIPOptions struct {
	TrustedProxies []string
}

func RealIP(next http.Handler) http.Handler {
	return realIP(true, nil)(next)
}

func RealIPWithOptions(options RealIPOptions) func(http.Handler) http.Handler {
	trustedProxies := parseTrustedProxies(options.TrustedProxies)
	return realIP(false, trustedProxies)
}

func realIP(trustAll bool, trustedProxies []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip := clientIP(r, trustAll || isTrustedProxy(remoteIP(r.RemoteAddr), trustedProxies)); ip != "" {
				r.RemoteAddr = ip
			}

			next.ServeHTTP(w, r)
		})
	}
}

func parseTrustedProxies(proxies []string) []*net.IPNet {
	trustedProxies := make([]*net.IPNet, 0, len(proxies))
	for _, proxy := range proxies {
		proxy = strings.TrimSpace(proxy)
		if proxy == "" {
			continue
		}

		if strings.Contains(proxy, "/") {
			if _, ipNet, err := net.ParseCIDR(proxy); err == nil {
				trustedProxies = append(trustedProxies, ipNet)
			}
			continue
		}

		if ip := net.ParseIP(proxy); ip != nil {
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			trustedProxies = append(trustedProxies, &net.IPNet{
				IP:   ip,
				Mask: net.CIDRMask(bits, bits),
			})
		}
	}

	return trustedProxies
}

func isTrustedProxy(ip net.IP, trustedProxies []*net.IPNet) bool {
	if ip == nil {
		return false
	}

	for _, trustedProxy := range trustedProxies {
		if trustedProxy.Contains(ip) {
			return true
		}
	}

	return false
}

func remoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}

	return net.ParseIP(host)
}

func clientIP(r *http.Request, trustProxyHeaders bool) string {
	if trustProxyHeaders {
		if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
			if ip := strings.TrimSpace(strings.Split(forwardedFor, ",")[0]); ip != "" {
				return ip
			}
		}

		if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
			return realIP
		}
	}

	if forwardedFor := r.Header.Get("Forwarded"); trustProxyHeaders && forwardedFor != "" {
		if ip := strings.TrimSpace(strings.Split(forwardedFor, ",")[0]); ip != "" {
			for _, part := range strings.Split(ip, ";") {
				keyValue := strings.SplitN(strings.TrimSpace(part), "=", 2)
				if len(keyValue) == 2 && strings.EqualFold(keyValue[0], "for") {
					return strings.Trim(strings.TrimSpace(keyValue[1]), `"`)
				}
			}
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}

	return r.RemoteAddr
}
