package mini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManager_NewManager_NilConfig(t *testing.T) {
	require.Nil(t, NewManager(nil))
}

func TestManager_SignIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(&LoginResp{
			OpenID:     "o-test-openid",
			SessionKey: "test-session-key",
		})
		require.NoError(t, err)
	}))
	defer srv.Close()

	mgr := newTestManager(srv.URL)
	resp, err := mgr.SignIn(context.Background(), "wx123", "code123")
	require.NoError(t, err)
	require.Equal(t, "o-test-openid", resp.OpenID)
}

func TestManager_SignIn_UnknownAppID(t *testing.T) {
	mgr := newTestManager("http://localhost")
	_, err := mgr.SignIn(context.Background(), "unknown-appid", "code")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not configured")
}

func TestManager_GetAccessToken_Caching(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(&AccessTokenResp{
			AccessToken: "cached-token",
			ExpiresIn:   7200,
		})
		require.NoError(t, err)
	}))
	defer srv.Close()

	mgr := newTestManager(srv.URL)

	// First call fetches from API.
	token, err := mgr.getAccessToken(context.Background(), "wx123")
	require.NoError(t, err)
	require.Equal(t, "cached-token", token)
	require.Equal(t, int32(1), calls.Load())

	// Second call uses cache.
	token, err = mgr.getAccessToken(context.Background(), "wx123")
	require.NoError(t, err)
	require.Equal(t, "cached-token", token)
	require.Equal(t, int32(1), calls.Load())
}

func TestManager_GetAccessToken_AutoRefresh(t *testing.T) {
	now := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(&AccessTokenResp{
			AccessToken: "refreshed-token",
			ExpiresIn:   7200,
		})
		require.NoError(t, err)
	}))
	defer srv.Close()

	mgr := newTestManager(srv.URL)
	mgr.nowFunc = func() time.Time { return now }

	// Seed an expiring token.
	mgr.mu.Lock()
	mgr.tokens["wx123"] = &cachedToken{
		accessToken: "old-token",
		expiresAt:   now.Add(100 * time.Second), // within renewBeforeSeconds but above syncRefreshPoint
	}
	mgr.mu.Unlock()

	token, err := mgr.getAccessToken(context.Background(), "wx123")
	require.NoError(t, err)
	require.Equal(t, "old-token", token) // still returns old token immediately

	// Give background refresh time to complete.
	time.Sleep(100 * time.Millisecond)

	mgr.mu.RLock()
	cached := mgr.tokens["wx123"]
	mgr.mu.RUnlock()
	require.Equal(t, "refreshed-token", cached.accessToken)
}

func TestManager_GetPhoneNumber(t *testing.T) {
	var reqCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
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
	resp, err := mgr.GetPhoneNumber(context.Background(), "wx123", "phone-code")
	require.NoError(t, err)
	require.Equal(t, "13800138000", resp.PhoneInfo.PurePhoneNumber)
}

// --- internal helpers ---

// newTestManager creates a Manager with test clients pointed at the given base URL.
func newTestManager(baseURL string) *Manager {
	cfg := &Config{
		Credentials: map[string]string{
			"wx123": "secret456",
		},
	}
	mgr := NewManager(cfg)
	// Replace clients with test clients pointing at the mock server.
	for appID, secret := range cfg.Credentials {
		mgr.clients[appID] = NewClientWithBaseURL(appID, secret, baseURL)
	}
	return mgr
}
