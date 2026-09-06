// Client environment parsing for session and login-log display columns.
//
// The parse kernel lives in go-common/useragent (self-maintained engine +
// curated rule data derived from Matomo device-detector; see that package's
// NOTICE). This file is the thin clientinfo adapter: it maps the
// useragent.Result onto the three display columns (OS, Browser, Device) and
// the API-client classification, keeping the call sites in user-service
// stable. Callers always keep the raw UserAgent string, so the parse rules
// can be refined and old rows re-derived later.

package clientinfo

import (
	"github.com/servekit/go-common/useragent"
)

// ParseUA extracts OS, Browser, and Device from a raw User-Agent string.
func ParseUA(ua string) (osName, browser, device string) {
	r := useragent.Parse(ua)
	// Desktop browsers freeze the macOS token at 10_15_7, so the version is
	// noise — the bare name (GitHub/Google account-security convention).
	if r.OS == "macOS" {
		r.OSVersion = ""
	}
	return joinVersion(r.OS, r.OSVersion),
		joinVersion(r.Client, r.ClientVersion),
		r.DeviceModel
}

// IsApiClient reports whether the UA belongs to a non-browser HTTP client
// (okhttp, curl, ...). Their UAs carry no OS either, so device
// classification reports API rather than Web.
func IsApiClient(ua string) bool {
	if ua == "" {
		return false
	}
	return useragent.Parse(ua).ClientKind == useragent.KindLibrary
}

// DeviceClass parses the UA's device class (desktop / smartphone / tablet /
// mobile; empty when unknown). Login-time device-type classification uses it
// to place HarmonyOS and other non-Android phones correctly.
func DeviceClass(ua string) useragent.DeviceClass {
	return useragent.Parse(ua).DeviceClass
}

// joinVersion renders "Chrome 152.0.0.0" / "iOS 17.2"; a bare name when the
// version is unknown.
func joinVersion(name, version string) string {
	if name == "" {
		return ""
	}
	if version == "" {
		return name
	}
	return name + " " + version
}
