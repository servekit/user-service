// Package wechat implements the WeChat web QR-code (qrconnect) social login provider.
package wechat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	userv1 "github.com/servekit/user-service/gen/user/v1"
	"github.com/servekit/user-service/internal/identity"
	"github.com/servekit/user-service/internal/utils/httpx"
	"github.com/servekit/user-service/pkg/xcodes"

	"golang.org/x/oauth2"
)

// WeChatEndpoint is the WeChat OAuth2 endpoint.
var WeChatEndpoint = oauth2.Endpoint{
	AuthURL:  "https://open.weixin.qq.com/connect/qrconnect",
	TokenURL: "https://api.weixin.qq.com/sns/oauth2/access_token",
}

// Provider handles WeChat OAuth2 authentication (web QR code login).
type Provider struct {
	config *oauth2.Config
	// apiBase overrides the default WeChat API host when non-empty.
	// Intended for tests; production code leaves this empty.
	apiBase string
}

const defaultWeChatAPIBase = "https://api.weixin.qq.com"

// New creates a new WeChat Provider.
func New(appID, appSecret, redirectURL string) *Provider {
	return &Provider{
		config: &oauth2.Config{
			ClientID:     appID,
			ClientSecret: appSecret,
			Endpoint:     WeChatEndpoint,
			RedirectURL:  redirectURL,
			Scopes:       []string{"snsapi_login"},
		},
	}
}

// Provider returns the identity provider enum value.
func (Provider) Provider() userv1.IdentityProvider {
	return userv1.IdentityProvider_IDENTITY_PROVIDER_WECHAT
}

// GetAuthURL returns the OAuth authorization URL. The optional redirectURL,
// when non-empty, is applied per-call via oauth2.SetAuthURLParam so we never
// mutate the shared config (which would race with concurrent callers).
//
// codeChallenge is accepted for interface conformance but ignored — WeChat
// qrconnect does not document PKCE support (as of 2024-12). If WeChat adds
// PKCE later, wire it here (q.Set("code_challenge", codeChallenge);
// q.Set("code_challenge_method", "S256")).
func (p *Provider) GetAuthURL(_ context.Context, redirectURL, state, codeChallenge string) (string, error) {
	_ = codeChallenge // accepted for interface conformance; WeChat qrconnect has no PKCE
	opts := []oauth2.AuthCodeOption{}
	if redirectURL != "" {
		opts = append(opts, oauth2.SetAuthURLParam("redirect_uri", redirectURL))
	}
	return p.config.AuthCodeURL(state, opts...), nil
}

// ExchangeCode exchanges the OAuth code for user info.
//
// codeVerifier is accepted for interface conformance but ignored — see
// GetAuthURL doc.
func (p *Provider) ExchangeCode(ctx context.Context, code, codeVerifier string) (*identity.SocialResult, error) {
	_ = codeVerifier // accepted for interface conformance; WeChat qrconnect has no PKCE
	token, err := p.config.Exchange(ctx, code)
	if err != nil {
		return nil, xcodes.ErrOAuthExchangeFailed.Wrap(err)
	}

	openid := extraString(token, "openid")
	unionid := extraString(token, "unionid")

	apiBase := p.apiBase
	if apiBase == "" {
		apiBase = defaultWeChatAPIBase
	}
	userInfoURL := apiBase + "/sns/userinfo?access_token=" +
		url.QueryEscape(token.AccessToken) + "&openid=" + url.QueryEscape(openid)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, http.NoBody)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := httpx.Default.Do(req)
	if err != nil {
		return nil, xcodes.ErrUserInfoFetchFailed.Wrap(err)
	}
	defer resp.Body.Close()
	if err := httpx.CheckStatus(resp); err != nil {
		return nil, xcodes.ErrUserInfoFetchFailed.Wrap(err)
	}

	var user wechatUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, xcodes.ErrUserInfoFetchFailed.Wrap(err)
	}
	if user.ErrCode != 0 {
		return nil, xcodes.ErrUserInfoFetchFailed.New(
			fmt.Sprintf("wechat sns/userinfo errcode=%d msg=%s", user.ErrCode, user.ErrMsg),
		)
	}

	return &identity.SocialResult{
		Provider:    userv1.IdentityProvider_IDENTITY_PROVIDER_WECHAT,
		ProviderUID: openid,
		Nickname:    user.Nickname,
		AvatarURL:   user.HeadImgURL,
		AccessToken: token.AccessToken,
		UnionID:     unionid,
	}, nil
}

// --- internal helpers ---

// wechatUser is the JSON body returned by WeChat's /sns/userinfo endpoint.
// ErrCode/ErrMsg are populated when the call fails (e.g. invalid access_token);
// otherwise OpenID/Nickname/HeadImgURL/UnionID carry the profile fields.
type wechatUser struct {
	ErrCode    int    `json:"errcode"`
	ErrMsg     string `json:"errmsg"`
	OpenID     string `json:"openid"`
	Nickname   string `json:"nickname"`
	HeadImgURL string `json:"headimgurl"`
	UnionID    string `json:"unionid"`
}

func extraString(t *oauth2.Token, key string) string {
	if v, ok := t.Extra(key).(string); ok {
		return v
	}
	return ""
}
