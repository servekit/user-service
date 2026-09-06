// Best-effort User-Agent parsing for session and login-log display columns.
// Covers the browsers and OSes that actually reach this system (desktop
// Chrome/Edge/Firefox/Safari, iOS/Android, WeChat's embedded browser) plus
// grpc-go's client marker. Unknown segments stay empty — callers always keep
// the raw UserAgent string, so the parse rules can be refined and old rows
// re-derived later.
package clientinfo

import (
	"strings"
)

// ParseUA extracts OS, Browser, and Device from a raw User-Agent string.
func ParseUA(ua string) (osName, browser, device string) {
	if ua == "" {
		return "", "", ""
	}
	return parseOS(ua), parseBrowser(ua), parseDevice(ua)
}

func parseOS(ua string) string {
	switch {
	case strings.Contains(ua, "iPhone OS "), strings.Contains(ua, "CPU OS "):
		return "iOS " + iosVersion(ua)
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		return "iOS"
	case strings.Contains(ua, "Android"):
		return "Android " + versionAfter(ua, "Android ")
	case strings.Contains(ua, "Windows NT 10."):
		// Browsers cannot distinguish Windows 10 from 11 in the UA.
		return "Windows 10/11"
	case strings.Contains(ua, "Windows NT 6.3"):
		return "Windows 8.1"
	case strings.Contains(ua, "Windows NT 6.2"):
		return "Windows 8"
	case strings.Contains(ua, "Windows NT 6.1"):
		return "Windows 7"
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "Mac OS X "):
		return "macOS " + strings.ReplaceAll(versionAfter(ua, "Mac OS X "), "_", ".")
	case strings.Contains(ua, "Macintosh"), strings.Contains(ua, "Mac OS"):
		return "macOS"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	default:
		return ""
	}
}

func parseBrowser(ua string) string {
	// Order matters: embedded browsers carry the host engine's token too
	// (WeChat UAs contain both Chrome and Safari).
	switch {
	case strings.Contains(ua, "MicroMessenger/"):
		return "WeChat " + versionAfter(ua, "MicroMessenger/")
	case strings.Contains(ua, "Edg/"), strings.Contains(ua, "Edge/"):
		return "Edge " + edgeVersion(ua)
	case strings.Contains(ua, "OPR/"):
		return "Opera " + versionAfter(ua, "OPR/")
	case strings.Contains(ua, "Firefox/"):
		return "Firefox " + versionAfter(ua, "Firefox/")
	case strings.Contains(ua, "Chrome/"):
		return "Chrome " + versionAfter(ua, "Chrome/")
	case strings.Contains(ua, "Version/") && strings.Contains(ua, "Safari/"):
		return "Safari " + versionAfter(ua, "Version/")
	case strings.Contains(ua, "grpc-go/"):
		return "gRPC " + versionAfter(ua, "grpc-go/")
	default:
		return ""
	}
}

func parseDevice(ua string) string {
	switch {
	case strings.Contains(ua, "iPhone"):
		return "iPhone"
	case strings.Contains(ua, "iPad"):
		return "iPad"
	case strings.Contains(ua, "Android"):
		// The device model is the token right after the Android version:
		// "(Linux; Android 14; Pixel 8 Pro)" → "Pixel 8 Pro".
		i := strings.Index(ua, "Android ")
		if i < 0 {
			return ""
		}
		rest := ua[i+len("Android "):]
		if j := strings.IndexAny(rest, ";)"); j >= 0 {
			rest = rest[j:]
		}
		rest = strings.TrimLeft(rest, "; ")
		if j := strings.IndexAny(rest, ";)"); j >= 0 {
			rest = rest[:j]
		}
		rest = strings.TrimSpace(rest)
		if rest == "" || rest == "wv" || strings.Contains(rest, "Build/") {
			return ""
		}
		return rest
	case strings.Contains(ua, "Mobile"):
		return "Mobile"
	default:
		return ""
	}
}

// versionAfter returns the dot/underscore-joined version prefix that follows
// token, stopping at the first character that cannot appear in a version.
func versionAfter(ua, token string) string {
	i := strings.Index(ua, token)
	if i < 0 {
		return ""
	}
	rest := ua[i+len(token):]
	end := strings.IndexFunc(rest, func(r rune) bool {
		return r != '.' && r != '_' && (r < '0' || r > '9')
	})
	if end < 0 {
		end = len(rest)
	}
	return strings.ReplaceAll(rest[:end], "_", ".")
}

// iosVersion reads "17_2" from either "iPhone OS 17_2" or "iPad; CPU OS 16_6".
func iosVersion(ua string) string {
	for _, token := range []string{"iPhone OS ", "CPU OS "} {
		if v := versionAfter(ua, token); v != "" {
			return v
		}
	}
	return ""
}

// edgeVersion reads the modern "Edg/" token first; "Edge/" (legacy) second.
func edgeVersion(ua string) string {
	if v := versionAfter(ua, "Edg/"); v != "" {
		return v
	}
	return versionAfter(ua, "Edge/")
}
