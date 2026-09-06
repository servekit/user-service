// Package clientinfo captures the caller's environment (IP + User-Agent)
// and hands it to services in a transport-agnostic form.
//
// Capture happens at the HTTP edge: Wrap extracts the client IP (leftmost
// X-Forwarded-For entry, falling back to the immediate remote address) and
// the raw User-Agent from the incoming request, then rewrites them as the
// trusted X-Client-IP / X-Client-UA headers — which grpc-gateway forwards to
// gRPC as metadata (via the Grpc-Metadata- prefix; the gateway drops
// X-Real-IP and renames User-Agent to grpcgateway-user-agent, so the raw
// headers are NOT dependable downstream).
//
// Consumers call FromCtx to read the normalized ClientInfo — with OS /
// Browser / Device parsed from the UA — regardless of transport (module-mode
// in-process, grpc-gateway transcode, or direct gRPC). Like pkg/auth, inbound
// copies of the trusted headers are stripped at the edge so a client cannot
// spoof its environment.
package clientinfo

import (
	"context"
	"strings"

	"google.golang.org/grpc/metadata"
)

// Trusted client-info metadata, written by Wrap at the HTTP edge and read by
// FromCtx. Lowercase because gRPC metadata keys must be; the HTTP wire form
// carries the Grpc-Metadata- prefix that grpc-gateway strips on forwarding.
const (
	XClientIP     = "x-client-ip"
	XClientUA     = "x-client-ua"
	XClientDevice = "x-client-device"

	clientIPHeader     = "Grpc-Metadata-X-Client-Ip"
	clientUAHeader     = "Grpc-Metadata-X-Client-Ua"
	clientDeviceHeader = "Grpc-Metadata-X-Client-Device"
)

// Column ceilings shared by every table that stores the caller environment
// (user_sessions, user_auth_logs, user_register_profiles): Postgres
// varchar(n) counts characters and rejects longer values, which would fail
// the whole register/login transaction. Oversized (hostile or buggy) input
// is truncated instead of rejected; sqlite (tests) never enforces, so this
// clamp is the only guard.
const (
	maxUserAgentRunes = 512
	maxDeviceRunes    = 128
)

// ClientInfo is the normalized caller environment. UserAgent stays raw;
// OS/Browser/Device are best-effort parses (empty when unknown) so callers
// can always store the original string and re-parse later.
type ClientInfo struct {
	IP        string
	UserAgent string
	OS        string
	Browser   string
	Device    string
}

// FromCtx reads the client info attached to an incoming request. It returns
// the zero value when the edge middleware did not run (direct gRPC calls,
// tests) — callers treat that as "unknown" rather than an error.
func FromCtx(ctx context.Context) ClientInfo {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ClientInfo{}
	}
	ua := clamp(firstValue(md, XClientUA), maxUserAgentRunes)
	osName, browser, device := ParseUA(ua)
	// The client-hint model (when the browser opted in) beats UA parsing —
	// UA reduction froze the Android token to "K", and iPhones never
	// carried a model at all.
	if model := clamp(firstValue(md, XClientDevice), maxDeviceRunes); model != "" {
		device = model
	}
	return ClientInfo{
		IP:        firstValue(md, XClientIP),
		UserAgent: ua,
		OS:        osName,
		Browser:   browser,
		Device:    device,
	}
}

// clamp limits s to at most max runes — the unit varchar(n) enforces —
// cutting on a rune boundary so the result stays valid UTF-8. The byte-length
// fast path skips the rune conversion for the common short input.
func clamp(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit])
}

// firstValue returns the first metadata value for key, or "" when absent.
// MD.Get only lowercases the lookup key while grpc-gateway's annotation can
// leave canonical casing on map keys (X-Client-Ip), so fall back to a
// case-insensitive scan — same tolerance as pkg/auth.
func firstValue(md metadata.MD, key string) string {
	if values := md.Get(key); len(values) > 0 {
		return values[0]
	}
	for k, values := range md {
		if len(values) > 0 && strings.EqualFold(k, key) {
			return values[0]
		}
	}
	return ""
}
