package service

import (
	"slices"
	"testing"
	"time"

	"github.com/servekit/go-common/captcha"
	"github.com/servekit/go-common/ratelimit"
)

// TestNormalizeCaptchaPurposes covers the config-load rewrite of named
// purpose keys to the numeric form purposeKey() emits. Numeric and unknown
// keys pass through unchanged; nil/empty are no-ops.
func TestNormalizeCaptchaPurposes(t *testing.T) {
	rule := &ratelimit.Rule{Window: 5 * time.Minute, Max: 5}
	cfg := func(keys ...string) map[string]*captcha.PurposeConfig {
		m := make(map[string]*captcha.PurposeConfig, len(keys))
		for _, k := range keys {
			m[k] = &captcha.PurposeConfig{RateRules: []*ratelimit.Rule{rule}}
		}
		return m
	}

	tests := []struct {
		name string
		in   map[string]*captcha.PurposeConfig
		want []string // expected key set after normalization
	}{
		{
			name: "named keys rewritten to numeric",
			in:   cfg("register", "login", "verify_email", "verify_phone", "password_reset", "bind"),
			want: []string{"1", "2", "3", "4", "5", "6"},
		},
		{
			name: "numeric keys pass through unchanged",
			in:   cfg("1", "2"),
			want: []string{"1", "2"},
		},
		{
			name: "unknown keys pass through unchanged",
			in:   cfg("custom", "2"),
			want: []string{"custom", "2"},
		},
		{
			name: "mixed named numeric and unknown",
			in:   cfg("register", "2", "weird"),
			want: []string{"1", "2", "weird"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &captcha.Config{Purposes: tt.in}
			normalizeCaptchaPurposes(c)

			got := keysOf(c.Purposes)
			slices.Sort(got)
			slices.Sort(tt.want)
			if !slices.Equal(got, slices.Clone(tt.want)) {
				t.Fatalf("keys = %v, want %v", got, tt.want)
			}
		})
	}

	// nil and empty-config guards — must be no-ops, no panic.
	normalizeCaptchaPurposes(nil)
	normalizeCaptchaPurposes(&captcha.Config{})
}

func keysOf(m map[string]*captcha.PurposeConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
