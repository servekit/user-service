package mini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/servekit/user-service/pkg/xcodes"

	"github.com/stretchr/testify/require"
)

func TestClient_SignIn(t *testing.T) {
	want := &LoginResp{
		OpenID:     "o-test-openid",
		SessionKey: "test-session-key",
		UnionID:    "o-test-unionid",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, code2SessionPath)
		require.Equal(t, "test-code", r.URL.Query().Get("js_code"))
		require.Equal(t, "wx123", r.URL.Query().Get("appid"))
		require.Equal(t, "secret456", r.URL.Query().Get("secret"))

		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(want)
		require.NoError(t, err)
	}))
	defer srv.Close()

	client := NewClientWithBaseURL("wx123", "secret456", srv.URL)
	resp, err := client.SignIn(context.Background(), "test-code")
	require.NoError(t, err)
	require.Equal(t, "o-test-openid", resp.OpenID)
	require.Equal(t, "test-session-key", resp.SessionKey)
	require.Equal(t, "o-test-unionid", resp.UnionID)
}

func TestClient_SignIn_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(LoginResp{ErrCode: 40029, ErrMsg: "invalid code"})
		require.NoError(t, err)
	}))
	defer srv.Close()

	client := NewClientWithBaseURL("wx123", "secret456", srv.URL)
	_, err := client.SignIn(context.Background(), "bad-code")
	require.Error(t, err)
	require.Contains(t, err.Error(), "code=40029")
}

func TestClient_GetStableAccessToken(t *testing.T) {
	want := &AccessTokenResp{
		AccessToken: "test-access-token",
		ExpiresIn:   7200,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Contains(t, r.URL.Path, getStableAccessTokenPath)

		var body map[string]any
		err := json.NewDecoder(r.Body).Decode(&body)
		require.NoError(t, err)
		require.Equal(t, "wx123", body["appid"])

		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(want)
		require.NoError(t, err)
	}))
	defer srv.Close()

	client := NewClientWithBaseURL("wx123", "secret456", srv.URL)
	resp, err := client.GetStableAccessToken(context.Background(), false)
	require.NoError(t, err)
	require.Equal(t, "test-access-token", resp.AccessToken)
	require.Equal(t, int64(7200), resp.ExpiresIn)
}

func TestClient_GetPhoneNumber(t *testing.T) {
	want := &PhoneNumberResp{
		PhoneInfo: &PhoneInfo{
			PhoneNumber:     "+86 13800138000",
			PurePhoneNumber: "13800138000",
			CountryCode:     "86",
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Contains(t, r.URL.Path, getPhoneNumberPath)
		require.Equal(t, "test-token", r.URL.Query().Get("access_token"))

		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(want)
		require.NoError(t, err)
	}))
	defer srv.Close()

	client := NewClientWithBaseURL("wx123", "secret456", srv.URL)
	resp, err := client.GetPhoneNumber(context.Background(), "test-token", "phone-code")
	require.NoError(t, err)
	require.Equal(t, "13800138000", resp.PhoneInfo.PurePhoneNumber)
	require.Equal(t, "86", resp.PhoneInfo.CountryCode)
}

func TestClient_CheckLoginStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, checkSessionKeyPath)
		require.NotEmpty(t, r.URL.Query().Get("signature"))
		require.Equal(t, sigMethodHMACSHA256, r.URL.Query().Get("sig_method"))

		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(map[string]int{"errcode": 0})
		require.NoError(t, err)
	}))
	defer srv.Close()

	client := NewClientWithBaseURL("wx123", "secret456", srv.URL)
	ok, err := client.CheckLoginStatus(context.Background(), "token", "session-key", "openid")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestClient_CheckLoginStatus_Expired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(map[string]int{"errcode": 40001})
		require.NoError(t, err)
	}))
	defer srv.Close()

	client := NewClientWithBaseURL("wx123", "secret456", srv.URL)
	ok, err := client.CheckLoginStatus(context.Background(), "token", "session-key", "openid")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestSignSessionKey(t *testing.T) {
	// HMAC-SHA256 of "testkey" using "testkey" as key
	result := signSessionKey("testkey")
	require.NotEmpty(t, result)
	// Result should be deterministic
	require.Equal(t, result, signSessionKey("testkey"))
}

func TestCheckErr(t *testing.T) {
	require.NoError(t, checkErr(xcodes.ErrOAuthFailed, 0, "", "test"))
	err := checkErr(xcodes.ErrOAuthFailed, 40001, "invalid credential", "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "OAUTH_FAILED")
	require.Contains(t, err.Error(), "errcode=40001")
}
