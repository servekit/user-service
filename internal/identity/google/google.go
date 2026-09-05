// Package google implements the Google OAuth2 social login provider.
package google

import (
	"context"
	"encoding/json"
	"net/http"

	userv1 "github.com/servekit/api/gen/go/user/v1"
	"github.com/servekit/user-service/internal/identity"
	"github.com/servekit/user-service/internal/utils/httpx"
	"github.com/servekit/user-service/pkg/xcodes"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Provider handles Google OAuth2 authentication.
type Provider struct {
	config *oauth2.Config
	// userInfoURL overrides the default Google user-info endpoint when non-empty.
	// Intended for tests; production code leaves this empty.
	userInfoURL string
}

const defaultGoogleUserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"

// New creates a new Google Provider.
func New(clientID, clientSecret, redirectURL string) *Provider {
	return &Provider{
		config: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     google.Endpoint,
			RedirectURL:  redirectURL,
			Scopes:       []string{"openid", "email", "profile"},
		},
	}
}

// Provider returns the identity provider enum value.
func (Provider) Provider() userv1.IdentityProvider {
	return userv1.IdentityProvider_IDENTITY_PROVIDER_GOOGLE
}

// GetAuthURL returns the OAuth authorization URL. The optional redirectURL,
// when non-empty, is applied per-call via oauth2.SetAuthURLParam so we never
// mutate the shared config (which would race with concurrent callers).
// codeChallenge is the PKCE S256 code_challenge; empty disables PKCE.
func (p *Provider) GetAuthURL(_ context.Context, redirectURL, state, codeChallenge string) (string, error) {
	opts := []oauth2.AuthCodeOption{}
	if redirectURL != "" {
		opts = append(opts, oauth2.SetAuthURLParam("redirect_uri", redirectURL))
	}
	if codeChallenge != "" {
		opts = append(opts,
			oauth2.SetAuthURLParam("code_challenge", codeChallenge),
			oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		)
	}
	return p.config.AuthCodeURL(state, opts...), nil
}

// ExchangeCode exchanges the OAuth code for user info. codeVerifier is the
// PKCE code_verifier matching the challenge sent at GetAuthURL time; empty
// disables PKCE.
func (p *Provider) ExchangeCode(ctx context.Context, code, codeVerifier string) (*identity.SocialResult, error) {
	opts := []oauth2.AuthCodeOption{}
	if codeVerifier != "" {
		opts = append(opts, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	}
	token, err := p.config.Exchange(ctx, code, opts...)
	if err != nil {
		return nil, xcodes.ErrOAuthExchangeFailed.Wrap(err)
	}

	userInfoURL := p.userInfoURL
	if userInfoURL == "" {
		userInfoURL = defaultGoogleUserInfoURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, http.NoBody)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	token.SetAuthHeader(req)

	resp, err := httpx.Default.Do(req)
	if err != nil {
		return nil, xcodes.ErrUserInfoFetchFailed.Wrap(err)
	}
	defer resp.Body.Close()
	if err := httpx.CheckStatus(resp); err != nil {
		return nil, xcodes.ErrUserInfoFetchFailed.Wrap(err)
	}

	var user googleUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, xcodes.ErrUserInfoFetchFailed.Wrap(err)
	}

	return &identity.SocialResult{
		Provider:    userv1.IdentityProvider_IDENTITY_PROVIDER_GOOGLE,
		ProviderUID: user.Sub,
		Nickname:    user.Name,
		Email:       user.Email,
		AvatarURL:   user.Picture,
		AccessToken: token.AccessToken,
	}, nil
}

// --- internal helpers ---

// googleUser is the JSON body returned by Google's userinfo endpoint.
type googleUser struct {
	Sub     string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}
