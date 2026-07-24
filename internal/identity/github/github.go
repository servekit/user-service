// Package github implements the GitHub OAuth2 social login provider.
package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	userv1 "github.com/servekit/user-service/gen/user/v1"
	"github.com/servekit/user-service/internal/identity"
	"github.com/servekit/user-service/internal/utils/httpx"
	"github.com/servekit/user-service/pkg/xcodes"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

// Provider handles GitHub OAuth2 authentication.
type Provider struct {
	config *oauth2.Config
	// userInfoURL overrides the default GitHub user-info endpoint when non-empty.
	// Intended for tests; production code leaves this empty.
	userInfoURL string
}

const (
	defaultGitHubUserInfoURL = "https://api.github.com/user"
	defaultGitHubEmailsURL   = "https://api.github.com/user/emails"
)

// New creates a new GitHub Provider.
func New(clientID, clientSecret, redirectURL string) *Provider {
	return &Provider{
		config: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     github.Endpoint,
			RedirectURL:  redirectURL,
			Scopes:       []string{"user:email"},
		},
	}
}

// Provider returns the identity provider enum value.
func (Provider) Provider() userv1.IdentityProvider {
	return userv1.IdentityProvider_IDENTITY_PROVIDER_GITHUB
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
		userInfoURL = defaultGitHubUserInfoURL
	}

	user, err := fetchGitHubUser(ctx, userInfoURL, token.AccessToken)
	if err != nil {
		return nil, err
	}

	nickname := user.Name
	if nickname == "" {
		nickname = user.Login
	}

	email := user.Email
	if email == "" {
		// Private primary emails don't show on /user; fall back to /user/emails.
		emailsURL := defaultGitHubEmailsURL
		if p.userInfoURL != "" {
			// Tests override host via userInfoURL; keep emails URL consistent.
			emailsURL = strings.Replace(defaultGitHubEmailsURL, "https://api.github.com", strings.TrimSuffix(p.userInfoURL, "/user"), 1)
		}
		email, err = fetchPrimaryEmail(ctx, emailsURL, token.AccessToken)
		if err != nil {
			return nil, err
		}
	}

	return &identity.SocialResult{
		Provider:    userv1.IdentityProvider_IDENTITY_PROVIDER_GITHUB,
		ProviderUID: strconv.FormatInt(user.ID, 10),
		Nickname:    nickname,
		Email:       email,
		AvatarURL:   user.AvatarURL,
		AccessToken: token.AccessToken,
	}, nil
}

// --- internal helpers ---

type githubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

func fetchGitHubUser(ctx context.Context, url, accessToken string) (*githubUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := httpx.Default.Do(req)
	if err != nil {
		return nil, xcodes.ErrUserInfoFetchFailed.Wrap(err)
	}
	defer resp.Body.Close()
	if err := httpx.CheckStatus(resp); err != nil {
		return nil, xcodes.ErrUserInfoFetchFailed.Wrap(err)
	}

	var user githubUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, xcodes.ErrUserInfoFetchFailed.Wrap(err)
	}
	return &user, nil
}

type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func fetchPrimaryEmail(ctx context.Context, url, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return "", xcodes.ErrInternal.Wrap(err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := httpx.Default.Do(req)
	if err != nil {
		return "", xcodes.ErrUserInfoFetchFailed.Wrap(err)
	}
	defer resp.Body.Close()
	if err := httpx.CheckStatus(resp); err != nil {
		return "", xcodes.ErrUserInfoFetchFailed.Wrap(err)
	}

	var emails []githubEmail
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return "", xcodes.ErrUserInfoFetchFailed.Wrap(err)
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email, nil
		}
	}
	// Fall back to any verified email if no primary is set.
	for _, e := range emails {
		if e.Verified {
			return e.Email, nil
		}
	}
	return "", nil
}
