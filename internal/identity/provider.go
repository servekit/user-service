// Package identity defines the contracts for social login providers and the
// subpackages that implement them (github, google, apple, tencent/wechat,
// tencent/mini). The interfaces and the result type live here so provider
// implementations and consumers (the service layer) share a single source of
// truth independent of any specific provider.
package identity

import (
	"context"

	userv1 "github.com/servekit/user-service/gen/user/v1"
)

// SocialProvider is the core interface for social login providers.
// All social providers must implement ExchangeCode to exchange a code for user info.
//
// codeVerifier is the PKCE code_verifier (RFC 7636) issued by user-service at
// GetOAuthURL time and threaded back through SocialLogin. Providers that do
// not use PKCE (WeChat qrconnect, WeChat Mini Program) accept the value but
// ignore it.
type SocialProvider interface {
	Provider() userv1.IdentityProvider

	ExchangeCode(ctx context.Context, code, codeVerifier string) (*SocialResult, error)
}

// RedirectProvider is an optional interface for providers that require redirect-based OAuth.
// Providers like GitHub, Google, WeChat (web) implement this in addition to SocialProvider.
//
// codeChallenge is the PKCE code_challenge (S256-encoded, RFC 7636) computed
// from the verifier issued at the same time. Providers that do not use PKCE
// accept the value but ignore it.
type RedirectProvider interface {
	SocialProvider

	GetAuthURL(ctx context.Context, redirectURL, state, codeChallenge string) (string, error)
}

// SocialResult is returned after a successful code exchange.
type SocialResult struct {
	Provider    userv1.IdentityProvider
	ProviderUID string // user's unique ID from the social provider (openid, sub, etc.)
	Email       string
	RegionCode  string // ISO 3166-1 alpha-2, e.g. "CN"
	Phone       string // pure national number, e.g. "13800138000"
	Nickname    string
	AvatarURL   string
	AccessToken string // OAuth access token (google, github, wechat)
	SessionKey  string // WeChat Mini Program session key
	UnionID     string // WeChat UnionID (cross-app user identifier)
}
