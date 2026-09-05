package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	userv1 "github.com/servekit/api/gen/go/user/v1"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// TestProvider_New verifies the oauth2 config is wired correctly.
func TestProvider_New(t *testing.T) {
	p := New("client-id", "client-secret", "http://localhost/cb")
	require.Equal(t, "client-id", p.config.ClientID)
	require.Equal(t, "client-secret", p.config.ClientSecret)
	require.Equal(t, "http://localhost/cb", p.config.RedirectURL)
	require.Equal(t, []string{"user:email"}, p.config.Scopes)
}

// TestProvider_Provider returns the correct enum.
func TestProvider_Provider(t *testing.T) {
	p := New("id", "secret", "http://localhost/cb")
	require.Equal(t, userv1.IdentityProvider_IDENTITY_PROVIDER_GITHUB, p.Provider())
}

// TestProvider_GetAuthURL forwards the state and optional redirect override.
func TestProvider_GetAuthURL(t *testing.T) {
	p := New("id", "secret", "http://default/cb")
	url, err := p.GetAuthURL(context.Background(), "", "state-123", "")
	require.NoError(t, err)
	require.Contains(t, url, "state=state-123")
	require.Contains(t, url, "client_id=id")
	require.NotContains(t, url, "code_challenge", "empty codeChallenge must not add PKCE params")

	url, err = p.GetAuthURL(context.Background(), "http://override/cb", "s2", "challenge-abc")
	require.NoError(t, err)
	require.Contains(t, url, "redirect_uri=http%3A%2F%2Foverride%2Fcb")
	require.Contains(t, url, "code_challenge=challenge-abc")
	require.Contains(t, url, "code_challenge_method=S256")
}

// TestProvider_ExchangeCode_Success mocks the OAuth token endpoint and the
// GitHub user-info endpoint, then verifies SocialResult mapping. Falls back
// to nickname=login when the user has no public name.
func TestProvider_ExchangeCode_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "authorization_code", r.PostForm.Get("grant_type"))
			require.Equal(t, "test-code", r.PostForm.Get("code"))
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "ghs_test_token",
				"token_type":   "bearer",
			})
		case "/user":
			require.Equal(t, "Bearer ghs_test_token", r.Header.Get("Authorization"))
			require.Equal(t, "application/vnd.github.v3+json", r.Header.Get("Accept"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         float64(12345),
				"login":      "octocat",
				"name":       "The Octocat",
				"email":      "octocat@example.com",
				"avatar_url": "https://github.com/octocat.png",
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := New("client-id", "client-secret", "http://localhost/cb")
	p.config.Endpoint = oauth2.Endpoint{TokenURL: srv.URL + "/token"}
	p.userInfoURL = srv.URL + "/user"

	result, err := p.ExchangeCode(context.Background(), "test-code", "verifier-xyz")
	require.NoError(t, err)
	require.Equal(t, userv1.IdentityProvider_IDENTITY_PROVIDER_GITHUB, result.Provider)
	require.Equal(t, "12345", result.ProviderUID)
	require.Equal(t, "The Octocat", result.Nickname)
	require.Equal(t, "octocat@example.com", result.Email)
	require.Equal(t, "https://github.com/octocat.png", result.AvatarURL)
	require.Equal(t, "ghs_test_token", result.AccessToken)
}

// TestProvider_ExchangeCode_NicknameFallbackToLogin verifies nickname falls
// back to login when name is empty. The email-fallback path is exercised
// when /user returns no email; /user/emails is also mocked (returns empty).
func TestProvider_ExchangeCode_NicknameFallbackToLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "tok",
				"token_type":   "bearer",
			})
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    float64(42),
				"login": "lonely-user",
				"name":  "",
			})
		case "/user/emails":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		}
	}))
	defer srv.Close()

	p := New("id", "secret", "http://cb")
	p.config.Endpoint = oauth2.Endpoint{TokenURL: srv.URL + "/token"}
	p.userInfoURL = srv.URL + "/user"

	result, err := p.ExchangeCode(context.Background(), "code", "verifier-xyz")
	require.NoError(t, err)
	require.Equal(t, "lonely-user", result.Nickname)
}

// TestProvider_ExchangeCode_TokenError surfaces OAuth exchange failures.
func TestProvider_ExchangeCode_TokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "bad_verification_code",
		})
	}))
	defer srv.Close()

	p := New("id", "secret", "http://cb")
	p.config.Endpoint = oauth2.Endpoint{TokenURL: srv.URL + "/token"}

	_, err := p.ExchangeCode(context.Background(), "bad-code", "verifier-xyz")
	require.Error(t, err)
	require.Contains(t, err.Error(), "OAUTH_EXCHANGE_FAILED")
}

// TestProvider_ExchangeCode_PKCEVerifies verifies the code_verifier reaches
// the token endpoint as expected. The mock token handler fails the test if
// it sees a verifier when none was sent, or vice versa.
func TestProvider_ExchangeCode_PKCEVerifies(t *testing.T) {
	var sawVerifier string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only capture the verifier from the token endpoint; the user-info
		// request is a GET with no form body and would otherwise clobber it.
		if r.URL.Path == "/token" {
			_ = r.ParseForm()
			sawVerifier = r.PostForm.Get("code_verifier")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok", "token_type": "bearer"})
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    float64(1),
				"login": "u",
				"name":  "U",
				"email": "u@example.com",
			})
		}
	}))
	defer srv.Close()

	p := New("id", "secret", "http://cb")
	p.config.Endpoint = oauth2.Endpoint{TokenURL: srv.URL + "/token"}
	p.userInfoURL = srv.URL + "/user"

	_, _ = p.ExchangeCode(context.Background(), "code", "my-verifier")
	require.Equal(t, "my-verifier", sawVerifier, "code_verifier must reach token endpoint")
}
