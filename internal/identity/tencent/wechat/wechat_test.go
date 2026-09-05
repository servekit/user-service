package wechat

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
	p := New("wxappid", "secret", "http://localhost/cb")
	require.Equal(t, "wxappid", p.config.ClientID)
	require.Equal(t, "secret", p.config.ClientSecret)
	require.Equal(t, "http://localhost/cb", p.config.RedirectURL)
	require.Equal(t, []string{"snsapi_login"}, p.config.Scopes)
	require.Equal(t, WeChatEndpoint, p.config.Endpoint)
}

func TestProvider_Provider(t *testing.T) {
	p := New("id", "secret", "http://cb")
	require.Equal(t, userv1.IdentityProvider_IDENTITY_PROVIDER_WECHAT, p.Provider())
}

func TestProvider_GetAuthURL(t *testing.T) {
	p := New("id", "secret", "http://default/cb")
	url, err := p.GetAuthURL(context.Background(), "", "we-chat-state", "")
	require.NoError(t, err)
	require.Contains(t, url, "state=we-chat-state")
	// oauth2.Config emits "client_id" (standard) rather than WeChat's "appid".
	// The AuthCodeURL output is what's actually returned to clients today.
	require.Contains(t, url, "client_id=id")
}

func TestProvider_ExchangeCode_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sns/oauth2/access_token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "wx-code", r.PostForm.Get("code"))
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "wechat_access_tok",
				"expires_in":   "7200",
				"openid":       "o_test_openid",
				"unionid":      "o_test_unionid",
			})
		case "/sns/userinfo":
			require.Equal(t, "wechat_access_tok", r.URL.Query().Get("access_token"))
			require.Equal(t, "o_test_openid", r.URL.Query().Get("openid"))
			require.Equal(t, "application/json", r.Header.Get("Accept"))
			_ = json.NewEncoder(w).Encode(map[string]string{
				"openid":     "o_test_openid",
				"nickname":   "WeChat User",
				"headimgurl": "https://wx.qlogo.cn/avatar.png",
				"unionid":    "o_test_unionid",
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := New("wxappid", "secret", "http://cb")
	p.config.Endpoint = oauth2.Endpoint{
		AuthURL:  srv.URL + "/connect/qrconnect",
		TokenURL: srv.URL + "/sns/oauth2/access_token",
	}
	p.apiBase = srv.URL

	result, err := p.ExchangeCode(context.Background(), "wx-code", "")
	require.NoError(t, err)
	require.Equal(t, userv1.IdentityProvider_IDENTITY_PROVIDER_WECHAT, result.Provider)
	require.Equal(t, "o_test_openid", result.ProviderUID)
	require.Equal(t, "WeChat User", result.Nickname)
	require.Equal(t, "https://wx.qlogo.cn/avatar.png", result.AvatarURL)
	require.Equal(t, "wechat_access_tok", result.AccessToken)
	require.Equal(t, "o_test_unionid", result.UnionID)
}

func TestProvider_ExchangeCode_TokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errcode": 40029,
			"errmsg":  "invalid code",
		})
	}))
	defer srv.Close()

	p := New("wxappid", "secret", "http://cb")
	p.config.Endpoint = oauth2.Endpoint{TokenURL: srv.URL + "/sns/oauth2/access_token"}

	_, err := p.ExchangeCode(context.Background(), "bad-code", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "OAUTH_EXCHANGE_FAILED")
}

func TestExtraString(t *testing.T) {
	tok := &oauth2.Token{}
	require.Empty(t, extraString(tok, "missing"))
	tok = tok.WithExtra(map[string]any{"openid": "o_123"})
	require.Equal(t, "o_123", extraString(tok, "openid"))
}
