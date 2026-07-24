// internal/utils/phone/phone_test.go
package phone

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeRegionCode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"CN", "CN"},
		{"cn", "CN"},
		{"Us", "US"},
		{"  HK  ", "HK"},
		{"GB", "GB"},
		// Reject: not two letters.
		{"", ""},
		{"C", ""},
		{"ABC", ""},
		{"86", ""}, // dialing code, not region code
		{"+86", ""},
		{"A1", ""},  // digit not allowed
		{"C N", ""}, // internal space breaks the 2-letter check
	}
	for _, tt := range tests {
		got := NormalizeRegionCode(tt.in)
		require.Equal(t, tt.want, got, "input %q", tt.in)
	}
}

func TestNormalizePhone(t *testing.T) {
	// New semantics: digits only, no country-code stripping.
	// Callers must pass region + local phone separately.
	tests := []struct {
		in   string
		want string
	}{
		{"13800138000", "13800138000"},
		{" 13800138000 ", "13800138000"},
		{"138-0013-8000", "13800138000"},
		{"(+86) 13800138000", "8613800138000"}, // caller is expected to split first
		{"", ""},
	}
	for _, tt := range tests {
		got := NormalizePhone(tt.in)
		require.Equal(t, tt.want, got, "input %q", tt.in)
	}
}

func TestCaptchaKey(t *testing.T) {
	require.Equal(t, "CN|13800138000", CaptchaKey("CN", "13800138000"))
	require.Equal(t, "user@example.com", CaptchaKey("", "user@example.com"))
}

func TestFormatE164(t *testing.T) {
	tests := []struct {
		name   string
		region string
		phone  string
		want   string
	}{
		{name: "CN mobile", region: "CN", phone: "13800138000", want: "+8613800138000"},
		{name: "US mobile", region: "US", phone: "5551234567", want: "+15551234567"},
		{name: "HK mobile", region: "HK", phone: "51234567", want: "+85251234567"},
		{name: "lowercase region", region: "cn", phone: "13800138000", want: "+8613800138000"},
		{name: "empty region", region: "", phone: "13800138000", want: ""},
		{name: "empty phone", region: "CN", phone: "", want: ""},
		{name: "garbage phone", region: "CN", phone: "abc", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatE164(tt.region, tt.phone)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestIsChinaRegion(t *testing.T) {
	require.True(t, IsChinaRegion("CN"))
	require.True(t, IsChinaRegion("cn"))
	require.False(t, IsChinaRegion("US"))
	require.False(t, IsChinaRegion("86")) // dialing code, not region
	require.False(t, IsChinaRegion(""))
}

func TestRegionCodeForDialing(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"86", "CN"},
		{"+86", "CN"},
		{"0086", "CN"},
		{"1", "US"}, // NANP primary region
		{"852", "HK"},
		{"44", "GB"},
		{"", ""},
		{"abc", ""},
	}
	for _, tt := range tests {
		got := RegionCodeForDialing(tt.in)
		require.Equal(t, tt.want, got, "input %q", tt.in)
	}
}

func TestNormalizeDialingCode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"86", "86"},
		{"+86", "86"},
		{" 86 ", "86"},
		{"0086", "86"},
		{"852", "852"},
		{"", ""},
	}
	for _, tt := range tests {
		got := NormalizeDialingCode(tt.in)
		require.Equal(t, tt.want, got, "input %q", tt.in)
	}
}

func TestValidate(t *testing.T) {
	require.NoError(t, Validate("CN", "13800138000"))
	require.Error(t, Validate("", "13800138000"))
	require.Error(t, Validate("86", "13800138000")) // dialing code rejected
	require.Error(t, Validate("CN", ""))
}
