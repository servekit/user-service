// Package phone normalizes and validates phone numbers and ISO region codes
// for storage and captcha key generation.
package phone

import (
	"fmt"
	"strings"

	"github.com/nyaruka/phonenumbers"
)

// NormalizeRegionCode returns the uppercase form of an ISO 3166-1 alpha-2
// region code (e.g. "cn" → "CN"). Returns "" when the input does not look
// like a region code. Unlike a dialing code, an ISO region code has no
// prefixes to strip — the only legal representation is exactly two letters.
func NormalizeRegionCode(rc string) string {
	rc = strings.TrimSpace(rc)
	if len(rc) != 2 {
		return ""
	}
	r0, r1 := rc[0], rc[1]
	if !isAlpha(r0) || !isAlpha(r1) {
		return ""
	}
	return strings.ToUpper(rc)
}

// NormalizePhone strips formatting characters and the optional "+"/leading
// dialing prefix, returning pure national-number digits. Returns "" for
// empty input. This is a best-effort cleanup; callers that need strict
// validation should use phonenumbers.Parse downstream (e.g. message-service).
func NormalizePhone(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CaptchaKey returns the canonical key used for captcha Generate/Verify.
// For phone channels: "<region_code>|<phone>" (e.g. "CN|13800138000").
// For email channels (region code empty): the email string itself.
func CaptchaKey(regionCode, phone string) string {
	if regionCode == "" {
		return phone
	}
	return fmt.Sprintf("%s|%s", regionCode, phone)
}

// FormatE164 returns the E.164 representation ("+<dialing><national>") of a
// phone number using libphonenumber. regionCode is the ISO 3166-1 alpha-2
// code required to disambiguate the country (e.g. "CN"); phone is the local
// national number without "+". Returns "" if parsing fails.
func FormatE164(regionCode, phone string) string {
	rc := NormalizeRegionCode(regionCode)
	p := NormalizePhone(phone)
	if rc == "" || p == "" {
		return ""
	}
	num, err := phonenumbers.Parse(p, rc)
	if err != nil {
		return ""
	}
	return phonenumbers.Format(num, phonenumbers.E164)
}

// IsChinaRegion returns true if the normalized region code is China (CN).
func IsChinaRegion(rc string) bool {
	return NormalizeRegionCode(rc) == "CN"
}

// RegionCodeForDialing converts a numeric dialing code (e.g. "86", "1") to
// its primary ISO 3166-1 alpha-2 region code ("CN", "US"). Used at boundaries
// where a third party (e.g. WeChat) returns a dialing code rather than a
// region. Returns "" if the dialing code is unknown. Note: dialing codes are
// not 1:1 with regions (e.g. "1" covers US, CA, JM, ...); this returns the
// primary region only, suitable for a hint but not authoritative.
func RegionCodeForDialing(dialing string) string {
	d := NormalizeDialingCode(dialing)
	if d == "" {
		return ""
	}
	var n int
	for _, r := range d {
		n = n*10 + int(r-'0')
	}
	return phonenumbers.GetRegionCodeForCountryCode(n)
}

// NormalizeDialingCode strips +, 00, whitespace, and non-digits from a
// dialing code, returning pure digits. Returns "" for empty input. Used at
// third-party boundaries that still hand back dialing codes.
func NormalizeDialingCode(cc string) string {
	var b strings.Builder
	for _, r := range cc {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	s = strings.TrimPrefix(s, "00")
	return s
}

// Validate returns an error if region code or phone is malformed.
// Empty values are rejected.
func Validate(regionCode, phone string) error {
	if NormalizeRegionCode(regionCode) == "" {
		return fmt.Errorf("phone: region code is empty or not ISO alpha-2")
	}
	if NormalizePhone(phone) == "" {
		return fmt.Errorf("phone: phone number is empty")
	}
	return nil
}

// --- internal helpers ---

// isAlpha reports whether b is an ASCII letter.
func isAlpha(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}
