package mini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProvider_GetPhoneNumber(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/cgi-bin/stable_token" {
			err := json.NewEncoder(w).Encode(&AccessTokenResp{
				AccessToken: "test-token",
				ExpiresIn:   7200,
			})
			require.NoError(t, err)
			return
		}
		if r.URL.Path == "/wxa/business/getuserphonenumber" {
			err := json.NewEncoder(w).Encode(&PhoneNumberResp{
				PhoneInfo: &PhoneInfo{
					PhoneNumber:     "+86 13800138000",
					PurePhoneNumber: "13800138000",
					CountryCode:     "86",
				},
			})
			require.NoError(t, err)
			return
		}
	}))
	defer srv.Close()

	mgr := newTestManager(srv.URL)
	prov := NewProvider("wx123", mgr)

	rc, phone, err := prov.GetPhoneNumber(context.Background(), "phone-code")
	require.NoError(t, err)
	require.Equal(t, "CN", rc)
	require.Equal(t, "13800138000", phone)
}

func TestProvider_GetPhoneNumber_NoPhoneInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/cgi-bin/stable_token" {
			err := json.NewEncoder(w).Encode(&AccessTokenResp{
				AccessToken: "test-token",
				ExpiresIn:   7200,
			})
			require.NoError(t, err)
			return
		}
		// getuserphonenumber returns a response with no PhoneInfo.
		err := json.NewEncoder(w).Encode(&PhoneNumberResp{})
		require.NoError(t, err)
	}))
	defer srv.Close()

	mgr := newTestManager(srv.URL)
	prov := NewProvider("wx123", mgr)

	rc, phone, err := prov.GetPhoneNumber(context.Background(), "phone-code")
	require.Error(t, err)
	require.Equal(t, "", rc)
	require.Equal(t, "", phone)
	require.Contains(t, err.Error(), "no phone info")
}
