package mini

import (
	"context"
	"fmt"

	userv1 "github.com/servekit/api/gen/go/user/v1"
	"github.com/servekit/user-service/internal/identity"
	phoneutil "github.com/servekit/user-service/internal/utils/phone"
	"github.com/servekit/user-service/pkg/xcodes"
)

// Provider handles WeChat Mini Program login by delegating to the Manager.
type Provider struct {
	appID string
	mgr   *Manager
}

// NewProvider creates a new mini-program Provider.
func NewProvider(appID string, mgr *Manager) *Provider {
	return &Provider{
		appID: appID,
		mgr:   mgr,
	}
}

// Provider returns the identity provider enum value.
func (Provider) Provider() userv1.IdentityProvider {
	return userv1.IdentityProvider_IDENTITY_PROVIDER_WECHAT_MINIPROGRAM
}

// ExchangeCode exchanges the wx.login code for session info via
// code2session. codeVerifier is accepted for interface conformance but
// ignored — Mini Program is not a redirect-based OAuth flow, PKCE does
// not apply.
func (p *Provider) ExchangeCode(ctx context.Context, code, codeVerifier string) (*identity.SocialResult, error) {
	_ = codeVerifier // accepted for interface conformance; Mini Program has no PKCE
	resp, err := p.mgr.SignIn(ctx, p.appID, code)
	if err != nil {
		return nil, xcodes.ErrOAuthExchangeFailed.Wrap(err)
	}

	return &identity.SocialResult{
		Provider:    userv1.IdentityProvider_IDENTITY_PROVIDER_WECHAT_MINIPROGRAM,
		ProviderUID: resp.OpenID,
		SessionKey:  resp.SessionKey,
		UnionID:     resp.UnionID,
	}, nil
}

// GetPhoneNumber exchanges a getPhoneNumber button code for the user's ISO
// region code and local phone number. WeChat's API returns a numeric dialing
// code in PhoneInfo.CountryCode (e.g. "86"); we convert it to ISO 3166-1
// alpha-2 ("CN") here so callers receive a single canonical region format.
// Returns "" region code if WeChat's dialing code is unrecognized.
func (p *Provider) GetPhoneNumber(ctx context.Context, phoneCode string) (regionCode, phone string, err error) {
	resp, gerr := p.mgr.GetPhoneNumber(ctx, p.appID, phoneCode)
	if gerr != nil {
		return "", "", xcodes.ErrOAuthFailed.Wrap(gerr)
	}
	if resp.PhoneInfo == nil {
		return "", "", xcodes.ErrOAuthFailed.New(fmt.Sprintf("wechat get phone number: no phone info in response (appid=%s)", p.appID))
	}
	return phoneutil.RegionCodeForDialing(resp.PhoneInfo.CountryCode), resp.PhoneInfo.PurePhoneNumber, nil
}
