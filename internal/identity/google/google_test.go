package google

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

func TestProvider_New(t *testing.T) {
	p := New("client-id", "client-secret", "http://localhost/cb")
	require.Equal(t, "client-id", p.config.ClientID)
	require.Equal(t, "client-secret", p.config.ClientSecret)
	require.Equal(t, "http://localhost/cb", p.config.RedirectURL)
	require.Equal(t, []string{"openid", "email", "profile"}, p.config.Scopes)
}

func TestProvider_Provider(t *testing.T) {
	p := New("id", "secret", "http://cb")
	require.Equal(t, userv1.IdentityProvider_IDENTITY_PROVIDER_GOOGLE, p.Provider())
}

func TestProvider_GetAuthURL(t *testing.T) {
	p := New("id", "secret", "http://default/cb")
	url, err := p.GetAuthURL(context.Background(), "", "state-xyz", "")
	require.NoError(t, err)
	require.Contains(t, url, "state=state-xyz")
	require.Contains(t, url, "client_id=id")
	require.NotContains(t, url, "code_challenge", "empty codeChallenge must not add PKCE params")

	url, err = p.GetAuthURL(context.Background(), "http://override/cb", "s2", "challenge-abc")
	require.NoError(t, err)
	require.Contains(t, url, "redirect_uri=http%3A%2F%2Foverride%2Fcb")
	require.Contains(t, url, "code_challenge=challenge-abc")
	require.Contains(t, url, "code_challenge_method=S256")
}

func TestProvider_ExchangeCode_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "test-code", r.PostForm.Get("code"))
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "ya29.test_token",
				"token_type":   "bearer",
			})
		case "/userinfo":
			require.Equal(t, "Bearer ya29.test_token", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(map[string]string{
				"sub":     "108214792410",
				"email":   "jane@example.com",
				"name":    "Jane Doe",
				"picture": "https://lh3.googleusercontent.com/jane.png",
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := New("id", "secret", "http://cb")
	p.config.Endpoint = oauth2.Endpoint{TokenURL: srv.URL + "/token"}
	p.userInfoURL = srv.URL + "/userinfo"

	result, err := p.ExchangeCode(context.Background(), "test-code", "verifier-xyz")
	require.NoError(t, err)
	require.Equal(t, userv1.IdentityProvider_IDENTITY_PROVIDER_GOOGLE, result.Provider)
	require.Equal(t, "108214792410", result.ProviderUID)
	require.Equal(t, "Jane Doe", result.Nickname)
	require.Equal(t, "jane@example.com", result.Email)
	require.Equal(t, "https://lh3.googleusercontent.com/jane.png", result.AvatarURL)
	require.Equal(t, "ya29.test_token", result.AccessToken)
}

func TestProvider_ExchangeCode_TokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "invalid_grant",
		})
	}))
	defer srv.Close()

	p := New("id", "secret", "http://cb")
	p.config.Endpoint = oauth2.Endpoint{TokenURL: srv.URL + "/token"}

	_, err := p.ExchangeCode(context.Background(), "bad-code", "verifier")
	require.Error(t, err)
	require.Contains(t, err.Error(), "OAUTH_EXCHANGE_FAILED")
}

// TestProvider_ExchangeCode_PKCEVerifies verifies the code_verifier is sent
// to Google's token endpoint when provided. The handler only captures the
// verifier from the /token path so the subsequent GET /userinfo doesn't
// clobber it.
func TestProvider_ExchangeCode_PKCEVerifies(t *testing.T) {
	var sawVerifier string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_ = r.ParseForm()
			sawVerifier = r.PostForm.Get("code_verifier")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok", "token_type": "bearer"})
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"sub":     "123",
				"email":   "u@example.com",
				"name":    "U",
				"picture": "https://lh3.googleusercontent.com/u.png",
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := New("id", "secret", "http://cb")
	p.config.Endpoint = oauth2.Endpoint{TokenURL: srv.URL + "/token"}
	p.userInfoURL = srv.URL + "/userinfo"

	_, _ = p.ExchangeCode(context.Background(), "code", "my-verifier")
	require.Equal(t, "my-verifier", sawVerifier, "code_verifier must reach token endpoint")
}
