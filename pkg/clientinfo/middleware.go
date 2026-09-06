// HTTP edge middleware: capture the caller's environment before it crosses
// the gateway boundary.

package clientinfo

import (
	"net"
	"net/http"
	"strings"
)

// Wrap returns HTTP middleware that stamps the trusted X-Client-IP /
// X-Client-UA headers onto every request (public routes included — login is
// where the info matters most) before delegating to next. Mount it at the
// edge, e.g. composed with the pkg/auth middleware over the gateway mux.
func Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Client-info headers are this middleware's output only. Drop inbound
		// copies — both plain and Grpc-Metadata- wire forms — so a client
		// cannot spoof its environment.
		r.Header.Del(XClientIP)
		r.Header.Del(XClientUA)
		r.Header.Del(clientIPHeader)
		r.Header.Del(clientUAHeader)
		r.Header.Del(clientDeviceHeader)
		r.Header.Del(XClientDevice)

		r.Header.Set(clientIPHeader, clientIP(r))
		if ua := r.UserAgent(); ua != "" {
			r.Header.Set(clientUAHeader, ua)
		}
		// Chrome's UA reduction froze the Android model token to "K"; the
		// real model only arrives via the opt-in Sec-CH-UA-Model hint. Ask
		// for it once and every subsequent browser request carries it.
		w.Header().Add("Accept-CH", "Sec-CH-UA-Model")
		if model := r.Header.Get("Sec-CH-UA-Model"); model != "" {
			r.Header.Set(clientDeviceHeader, model)
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP resolves the originating client address. The edge proxy (nginx)
// maintains X-Forwarded-For as "<client>, <proxy chain>"; the leftmost
// entry is the original caller. Behind no proxy, RemoteAddr is the client
// itself (host:port, so the port is stripped).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for _, part := range strings.Split(xff, ",") {
			if ip := strings.TrimSpace(part); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
