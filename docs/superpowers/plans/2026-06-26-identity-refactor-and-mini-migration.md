# Identity 包重构 + tencent/mini 迁移 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `github.com/servekit/go-common/tencent/mini` 整体迁移到本项目，并把 `internal/identity/` 包拆分为 `provider/`（公共接口）+ 各 provider 独立子包（`github/`、`google/`、`apple/`、`tencent/wechat/`、`tencent/mini/`）。

**Architecture:** 纯迁移/重构，业务逻辑零修改。先在并行存在的新子包里把代码搬好（旧文件原样保留，保证每一步都能编译），然后一次性把使用方切到新子包，最后删除旧文件。`tencent/` 是分类目录（无 .go 文件），归集所有腾讯系登录方式。

**Tech Stack:** Go 1.x，`golang.org/x/oauth2`，`golang.org/x/sync/singleflight`，`net/http`，`crypto/hmac`/`sha256`。

**对应 spec:** `docs/superpowers/specs/2026-06-26-identity-refactor-and-mini-migration-design.md`

---

## 文件结构

新建文件（按创建顺序）：

```
internal/identity/
├── provider/
│   └── provider.go                          # Task 1
├── tencent/
│   ├── mini/
│   │   ├── types.go                         # Task 2
│   │   ├── client.go                        # Task 2
│   │   ├── manager.go                       # Task 2
│   │   ├── provider.go                      # Task 2
│   │   ├── client_test.go                   # Task 2
│   │   └── manager_test.go                  # Task 2
│   └── wechat/
│       └── wechat.go                        # Task 6
├── github/
│   └── github.go                            # Task 3
├── google/
│   └── google.go                            # Task 4
└── apple/
    └── apple.go                             # Task 5
```

修改文件：

```
internal/service/service.go                  # Task 7
internal/service/social/social.go            # Task 7
```

删除文件：

```
internal/identity/provider.go                # Task 8
internal/identity/github.go                  # Task 8
internal/identity/google.go                  # Task 8
internal/identity/wechat.go                  # Task 8
internal/identity/apple.go                   # Task 8
internal/identity/wechat_miniprogram.go      # Task 8
```

保留文件（不动）：

```
internal/identity/credentials.go             # HashPassword / VerifyPassword
internal/service/auth/auth.go                # identity.HashPassword / VerifyPassword
internal/service/user/user.go                # identity.HashPassword / VerifyPassword
```

---

## Task 1: 创建 provider 公共子包

**Files:**
- Create: `internal/identity/provider/provider.go`

- [ ] **Step 1: 创建子包目录与文件**

创建 `internal/identity/provider/provider.go`：

```go
// Package provider defines the common contracts for social login providers.
package provider

import (
	"context"

	userv1 "user-service/gen/user/v1"
)

// SocialProvider is the core interface for social login providers.
// All social providers must implement ExchangeCode to exchange a code for user info.
type SocialProvider interface {
	// Provider returns the identity provider enum value.
	Provider() userv1.IdentityProvider

	// ExchangeCode exchanges a code for user info.
	// The code comes from: OAuth callback (web), platform SDK (mini program, mobile), etc.
	ExchangeCode(ctx context.Context, code string) (*SocialResult, error)
}

// RedirectProvider is an optional interface for providers that require redirect-based OAuth.
// Providers like GitHub, Google, WeChat (web) implement this in addition to SocialProvider.
type RedirectProvider interface {
	SocialProvider

	// GetAuthURL returns the OAuth authorization URL for redirect-based login.
	GetAuthURL(ctx context.Context, redirectURL, state string) (string, error)
}

// SocialResult is returned after a successful code exchange.
type SocialResult struct {
	Provider    userv1.IdentityProvider
	ProviderUID string // user's unique ID from the social provider (openid, sub, etc.)
	Email       string
	Phone       string
	Nickname    string
	AvatarURL   string
	AccessToken string // OAuth access token (google, github, wechat)
	SessionKey  string // WeChat Mini Program session key
	UnionID     string // WeChat UnionID (cross-app user identifier)
}
```

- [ ] **Step 2: 验证编译**

Run: `go build ./internal/identity/provider/`
Expected: 无输出（编译通过）

- [ ] **Step 3: 提交**

```bash
git add internal/identity/provider/provider.go
git commit -m "refactor(identity): extract provider interfaces into provider subpackage"
```

---

## Task 2: 迁移 tencent/mini 包（API client + Manager + Provider）

**Files:**
- Create: `internal/identity/tencent/mini/types.go`
- Create: `internal/identity/tencent/mini/client.go`
- Create: `internal/identity/tencent/mini/manager.go`
- Create: `internal/identity/tencent/mini/provider.go`
- Create: `internal/identity/tencent/mini/client_test.go`
- Create: `internal/identity/tencent/mini/manager_test.go`

源：`github.com/servekit/go-common/tencent/mini/{types,client,manager,client_test,manager_test}.go` + `internal/identity/wechat_miniprogram.go`。

- [ ] **Step 1: 创建 types.go**

创建 `internal/identity/tencent/mini/types.go`：

```go
// Package mini provides a WeChat Mini Program API client with access token management,
// plus a SocialProvider implementation for mini-program login.
package mini

// Config holds configuration for the Mini Program Manager.
type Config struct {
	// Credentials maps appid to secret. Supports multiple mini programs.
	Credentials map[string]string
}

// LoginResp is the response from jscode2session.
type LoginResp struct {
	ErrCode    int    `json:"errcode"`
	ErrMsg     string `json:"errmsg"`
	OpenID     string `json:"openid"`
	SessionKey string `json:"session_key"`
	UnionID    string `json:"unionid"`
}

// AccessTokenResp is the response from getStableAccessToken.
type AccessTokenResp struct {
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

// PhoneNumberResp is the response from getPhoneNumber.
type PhoneNumberResp struct {
	ErrCode   int        `json:"errcode"`
	ErrMsg    string     `json:"errmsg"`
	PhoneInfo *PhoneInfo `json:"phone_info"`
}

// PhoneInfo contains the user's phone number details.
type PhoneInfo struct {
	PhoneNumber     string `json:"phoneNumber"`
	PurePhoneNumber string `json:"purePhoneNumber"`
	CountryCode     string `json:"countryCode"`
}
```

- [ ] **Step 2: 创建 client.go**

创建 `internal/identity/tencent/mini/client.go`（package 改为 `mini`，去掉无关 import；其余 1:1 复制）：

```go
package mini

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client calls WeChat Mini Program APIs for a single appid.
type Client struct {
	appID      string
	secret     string
	baseURL    string
	httpClient *http.Client
}

const (
	defaultBaseURL           = "https://api.weixin.qq.com"
	code2SessionPath         = "/sns/jscode2session"
	checkSessionKeyPath      = "/wxa/checksession"
	getStableAccessTokenPath = "/cgi-bin/stable_token"
	getPhoneNumberPath       = "/wxa/business/getuserphonenumber"

	loginGrantType       = "authorization_code"
	accessTokenGrantType = "client_credential"
	sigMethodHMACSHA256  = "hmac_sha256"
)

// NewClient creates a new WeChat Mini Program API client.
func NewClient(appID, secret string) *Client {
	return &Client{
		appID:   appID,
		secret:  secret,
		baseURL: defaultBaseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// NewClientWithBaseURL creates a client with a custom base URL (for testing).
func NewClientWithBaseURL(appID, secret, baseURL string) *Client {
	c := NewClient(appID, secret)
	c.baseURL = baseURL
	return c
}

// SignIn calls jscode2session to exchange a wx.login() code for openid + session_key.
func (c *Client) SignIn(ctx context.Context, code string) (*LoginResp, error) {
	params := url.Values{}
	params.Add("appid", c.appID)
	params.Add("secret", c.secret)
	params.Add("js_code", code)
	params.Add("grant_type", loginGrantType)

	body, err := c.get(ctx, code2SessionPath, params)
	if err != nil {
		return nil, fmt.Errorf("jscode2session: %w", err)
	}

	resp := &LoginResp{}
	if err := json.Unmarshal(body, resp); err != nil {
		return nil, fmt.Errorf("parse jscode2session response: %w", err)
	}
	if err := checkErr(resp.ErrCode, resp.ErrMsg); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetStableAccessToken returns a stable access token.
func (c *Client) GetStableAccessToken(ctx context.Context, forceRefresh bool) (*AccessTokenResp, error) {
	reqBody := map[string]any{
		"grant_type":    accessTokenGrantType,
		"appid":         c.appID,
		"secret":        c.secret,
		"force_refresh": forceRefresh,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	body, err := c.post(ctx, getStableAccessTokenPath, nil, bodyBytes)
	if err != nil {
		return nil, fmt.Errorf("getStableAccessToken: %w", err)
	}

	resp := &AccessTokenResp{}
	if err := json.Unmarshal(body, resp); err != nil {
		return nil, fmt.Errorf("parse getStableAccessToken response: %w", err)
	}
	if err := checkErr(resp.ErrCode, resp.ErrMsg); err != nil {
		return nil, err
	}
	return resp, nil
}

// CheckLoginStatus checks if the session key is still valid.
func (c *Client) CheckLoginStatus(ctx context.Context, accessToken, sessionKey, openID string) (bool, error) {
	signature := signSessionKey(sessionKey)
	params := url.Values{}
	params.Add("openid", openID)
	params.Add("access_token", accessToken)
	params.Add("signature", signature)
	params.Add("sig_method", sigMethodHMACSHA256)

	body, err := c.get(ctx, checkSessionKeyPath, params)
	if err != nil {
		return false, fmt.Errorf("checkSessionKey: %w", err)
	}

	var resp struct {
		ErrCode int `json:"errcode"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return false, fmt.Errorf("parse checkSessionKey response: %w", err)
	}
	return resp.ErrCode == 0, nil
}

// GetPhoneNumber exchanges a phone-number code for the user's phone number.
func (c *Client) GetPhoneNumber(ctx context.Context, accessToken, code string) (*PhoneNumberResp, error) {
	reqBody := map[string]string{"code": code}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Add("access_token", accessToken)

	body, err := c.post(ctx, getPhoneNumberPath, params, bodyBytes)
	if err != nil {
		return nil, fmt.Errorf("getPhoneNumber: %w", err)
	}

	resp := &PhoneNumberResp{}
	if err := json.Unmarshal(body, resp); err != nil {
		return nil, fmt.Errorf("parse getPhoneNumber response: %w", err)
	}
	if err := checkErr(resp.ErrCode, resp.ErrMsg); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) get(ctx context.Context, path string, params url.Values) ([]byte, error) {
	reqURL := c.baseURL + path
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (c *Client) post(ctx context.Context, path string, params url.Values, body []byte) ([]byte, error) {
	reqURL := c.baseURL + path
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// --- internal helpers ---

func signSessionKey(sessionKey string) string {
	h := hmac.New(sha256.New, []byte(sessionKey))
	h.Write([]byte(sessionKey))
	return hex.EncodeToString(h.Sum(nil))
}

func checkErr(errCode int, errMsg string) error {
	if errCode == 0 {
		return nil
	}
	return fmt.Errorf("wechat error: code=%d msg=%s", errCode, errMsg)
}
```

- [ ] **Step 3: 创建 manager.go**

创建 `internal/identity/tencent/mini/manager.go`（保留原 `slog.Error`，1:1 迁移）：

```go
package mini

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Manager manages WeChat Mini Program API clients with access token caching.
// Supports multiple appids and auto-renews tokens before expiration.
type Manager struct {
	clients map[string]*Client
	tokens  map[string]*cachedToken
	mu      sync.RWMutex
	sf      singleflight.Group
	nowFunc func() time.Time // for testing
}

// cachedToken holds a cached access token and its expiration time.
type cachedToken struct {
	accessToken string
	expiresAt   time.Time
}

const (
	// Renew token when it expires within this many seconds.
	renewBeforeSeconds = 240
	// If token expires within this many seconds, block and refresh synchronously.
	syncRefreshPoint = 60
)

// NewManager creates a new Manager from the given Config.
func NewManager(cfg *Config) *Manager {
	if cfg == nil || len(cfg.Credentials) == 0 {
		return nil
	}
	clients := make(map[string]*Client, len(cfg.Credentials))
	for appID, secret := range cfg.Credentials {
		clients[appID] = NewClient(appID, secret)
	}
	return &Manager{
		clients: clients,
		tokens:  make(map[string]*cachedToken),
		nowFunc: time.Now,
	}
}

// SignIn calls jscode2session via the client for the given appid.
func (m *Manager) SignIn(ctx context.Context, appID, code string) (*LoginResp, error) {
	client, err := m.getClient(appID)
	if err != nil {
		return nil, err
	}
	return client.SignIn(ctx, code)
}

// GetPhoneNumber retrieves a user's phone number.
func (m *Manager) GetPhoneNumber(ctx context.Context, appID, code string) (*PhoneNumberResp, error) {
	client, err := m.getClient(appID)
	if err != nil {
		return nil, err
	}
	accessToken, err := m.getAccessToken(ctx, appID)
	if err != nil {
		return nil, err
	}
	return client.GetPhoneNumber(ctx, accessToken, code)
}

// CheckLoginStatus checks if a session key is still valid.
func (m *Manager) CheckLoginStatus(ctx context.Context, appID, sessionKey, openID string) (bool, error) {
	client, err := m.getClient(appID)
	if err != nil {
		return false, err
	}
	accessToken, err := m.getAccessToken(ctx, appID)
	if err != nil {
		return false, err
	}
	return client.CheckLoginStatus(ctx, accessToken, sessionKey, openID)
}

// --- internal helpers ---

func (m *Manager) getClient(appID string) (*Client, error) {
	client, ok := m.clients[appID]
	if !ok {
		return nil, fmt.Errorf("mini: appid %s not configured", appID)
	}
	return client, nil
}

// getAccessToken returns a cached access token, refreshing if needed.
func (m *Manager) getAccessToken(ctx context.Context, appID string) (string, error) {
	if token := m.getCachedToken(appID); token != nil {
		if m.needsAsyncRefresh(token) {
			go m.backgroundRefresh(appID)
		}
		return token.accessToken, nil
	}

	v, err, _ := m.sf.Do("token:"+appID, func() (any, error) {
		return m.refreshToken(ctx, appID)
	})
	m.sf.Forget("token:" + appID)
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

func (m *Manager) getCachedToken(appID string) *cachedToken {
	m.mu.RLock()
	defer m.mu.RUnlock()
	token, ok := m.tokens[appID]
	if !ok {
		return nil
	}
	now := m.nowFunc()
	if token.expiresAt.Sub(now) < time.Duration(syncRefreshPoint)*time.Second {
		return nil
	}
	return token
}

func (m *Manager) needsAsyncRefresh(token *cachedToken) bool {
	remaining := token.expiresAt.Sub(m.nowFunc()).Seconds()
	return remaining <= float64(renewBeforeSeconds)
}

func (m *Manager) backgroundRefresh(appID string) {
	defer m.sf.Forget("bg:" + appID)
	_, err, _ := m.sf.Do("bg:"+appID, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return m.refreshToken(ctx, appID)
	})
	if err != nil {
		slog.Error("mini: background token refresh failed", "appid", appID, "error", err)
	}
}

func (m *Manager) refreshToken(ctx context.Context, appID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if token, ok := m.tokens[appID]; ok && !m.needsAsyncRefresh(token) {
		return token.accessToken, nil
	}

	client, err := m.getClient(appID)
	if err != nil {
		return "", err
	}
	resp, err := client.GetStableAccessToken(ctx, false)
	if err != nil {
		return "", err
	}

	m.tokens[appID] = &cachedToken{
		accessToken: resp.AccessToken,
		expiresAt:   m.nowFunc().Add(time.Duration(resp.ExpiresIn) * time.Second),
	}
	return resp.AccessToken, nil
}
```

- [ ] **Step 4: 创建 provider.go**

创建 `internal/identity/tencent/mini/provider.go`（从 `internal/identity/wechat_miniprogram.go` 迁移；类型 `WeChatMiniProgramProvider` → `Provider`；构造函数 `NewWeChatMiniProgramProvider` → `NewProvider`）：

```go
package mini

import (
	"context"
	"fmt"

	userv1 "user-service/gen/user/v1"
	"user-service/internal/identity/provider"
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

// ExchangeCode calls jscode2session to exchange the wx.login() code for openid.
func (p *Provider) ExchangeCode(ctx context.Context, code string) (*provider.SocialResult, error) {
	resp, err := p.mgr.SignIn(ctx, p.appID, code)
	if err != nil {
		return nil, fmt.Errorf("wechat miniprogram login: %w", err)
	}

	return &provider.SocialResult{
		Provider:    userv1.IdentityProvider_IDENTITY_PROVIDER_WECHAT_MINIPROGRAM,
		ProviderUID: resp.OpenID,
		SessionKey:  resp.SessionKey,
		UnionID:     resp.UnionID,
	}, nil
}

// GetPhoneNumber exchanges a getPhoneNumber button code for the user's phone number.
func (p *Provider) GetPhoneNumber(ctx context.Context, phoneCode string) (string, error) {
	resp, err := p.mgr.GetPhoneNumber(ctx, p.appID, phoneCode)
	if err != nil {
		return "", fmt.Errorf("wechat get phone number: %w", err)
	}
	if resp.PhoneInfo == nil {
		return "", fmt.Errorf("wechat get phone number: no phone info in response")
	}
	return resp.PhoneInfo.PhoneNumber, nil
}
```

- [ ] **Step 5: 创建 client_test.go**

创建 `internal/identity/tencent/mini/client_test.go`（1:1 复制）：

```go
package mini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	require.NoError(t, checkErr(0, ""))
	require.Error(t, checkErr(40001, "invalid credential"))
}
```

- [ ] **Step 6: 创建 manager_test.go**

创建 `internal/identity/tencent/mini/manager_test.go`（1:1 复制）：

```go
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
```

- [ ] **Step 7: 验证编译与测试**

Run: `go build ./internal/identity/tencent/mini/`
Expected: 无输出

Run: `go test ./internal/identity/tencent/mini/`
Expected: `ok  user-service/internal/identity/tencent/mini  <duration>`

- [ ] **Step 8: 提交**

```bash
git add internal/identity/tencent/mini/
git commit -m "refactor(identity): pull tencent/mini package into the project"
```

---

## Task 3: 迁移 github 子包

**Files:**
- Create: `internal/identity/github/github.go`

源：`internal/identity/github.go`。

- [ ] **Step 1: 创建 github.go**

创建 `internal/identity/github/github.go`（package 改为 `github`；`GitHubProvider` → `Provider`；`NewGitHubProvider` → `New`；`SocialResult` 改为 `provider.SocialResult`）：

```go
// Package github implements the GitHub OAuth2 social login provider.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	userv1 "user-service/gen/user/v1"
	"user-service/internal/identity/provider"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

// Provider handles GitHub OAuth2 authentication.
type Provider struct {
	config *oauth2.Config
}

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

// GetAuthURL returns the OAuth authorization URL.
func (p *Provider) GetAuthURL(_ context.Context, redirectURL, state string) (string, error) {
	if redirectURL != "" {
		p.config.RedirectURL = redirectURL
	}
	return p.config.AuthCodeURL(state), nil
}

// ExchangeCode exchanges the OAuth code for user info.
func (p *Provider) ExchangeCode(ctx context.Context, code string) (*provider.SocialResult, error) {
	token, err := p.config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oauth exchange: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	token.SetAuthHeader(req)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github user info: %w", err)
	}
	defer resp.Body.Close()

	var user struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode github user: %w", err)
	}

	nickname := user.Name
	if nickname == "" {
		nickname = user.Login
	}

	return &provider.SocialResult{
		Provider:    userv1.IdentityProvider_IDENTITY_PROVIDER_GITHUB,
		ProviderUID: strconv.FormatInt(user.ID, 10),
		Nickname:    nickname,
		Email:       user.Email,
		AvatarURL:   user.AvatarURL,
		AccessToken: token.AccessToken,
	}, nil
}
```

- [ ] **Step 2: 验证编译**

Run: `go build ./internal/identity/github/`
Expected: 无输出

- [ ] **Step 3: 提交**

```bash
git add internal/identity/github/github.go
git commit -m "refactor(identity): extract github provider into its own subpackage"
```

---

## Task 4: 迁移 google 子包

**Files:**
- Create: `internal/identity/google/google.go`

源：`internal/identity/google.go`。

- [ ] **Step 1: 创建 google.go**

创建 `internal/identity/google/google.go`（package 改为 `google`；`GoogleProvider` → `Provider`；`NewGoogleProvider` → `New`；`SocialResult` 改为 `provider.SocialResult`）：

```go
// Package google implements the Google OAuth2 social login provider.
package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	userv1 "user-service/gen/user/v1"
	"user-service/internal/identity/provider"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Provider handles Google OAuth2 authentication.
type Provider struct {
	config *oauth2.Config
}

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

// GetAuthURL returns the OAuth authorization URL.
func (p *Provider) GetAuthURL(_ context.Context, redirectURL, state string) (string, error) {
	if redirectURL != "" {
		p.config.RedirectURL = redirectURL
	}
	return p.config.AuthCodeURL(state), nil
}

// ExchangeCode exchanges the OAuth code for user info.
func (p *Provider) ExchangeCode(ctx context.Context, code string) (*provider.SocialResult, error) {
	token, err := p.config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oauth exchange: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	token.SetAuthHeader(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google user info: %w", err)
	}
	defer resp.Body.Close()

	var user struct {
		Sub     string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode google user: %w", err)
	}

	return &provider.SocialResult{
		Provider:    userv1.IdentityProvider_IDENTITY_PROVIDER_GOOGLE,
		ProviderUID: user.Sub,
		Nickname:    user.Name,
		Email:       user.Email,
		AvatarURL:   user.Picture,
		AccessToken: token.AccessToken,
	}, nil
}
```

- [ ] **Step 2: 验证编译**

Run: `go build ./internal/identity/google/`
Expected: 无输出

- [ ] **Step 3: 提交**

```bash
git add internal/identity/google/google.go
git commit -m "refactor(identity): extract google provider into its own subpackage"
```

---

## Task 5: 迁移 apple 子包

**Files:**
- Create: `internal/identity/apple/apple.go`

源：`internal/identity/apple.go`。

- [ ] **Step 1: 创建 apple.go**

创建 `internal/identity/apple/apple.go`（package 改为 `apple`；`AppleProvider` → `Provider`；`NewAppleProvider` → `New`；`SocialResult` 改为 `provider.SocialResult`；其余 1:1，占位 TODO 保留）：

```go
// Package apple implements the Apple Sign-In social login provider.
package apple

import (
	"context"
	"fmt"

	userv1 "user-service/gen/user/v1"
	"user-service/internal/identity/provider"
)

// Provider handles Apple Sign-In authentication.
// Apple requires JWT-based client_secret generation.
type Provider struct {
	clientID    string
	teamID      string
	keyID       string
	redirectURL string
}

// New creates a new Apple Provider.
func New(clientID, teamID, keyID, redirectURL string) *Provider {
	return &Provider{
		clientID:    clientID,
		teamID:      teamID,
		keyID:       keyID,
		redirectURL: redirectURL,
	}
}

// Provider returns the identity provider enum value.
func (Provider) Provider() userv1.IdentityProvider {
	return userv1.IdentityProvider_IDENTITY_PROVIDER_APPLE
}

// GetAuthURL returns the OAuth authorization URL.
func (Provider) GetAuthURL(_ context.Context, _, _ string) (string, error) {
	// TODO: implement Apple auth URL with JWT client_secret
	return "", fmt.Errorf("apple provider not yet implemented")
}

// ExchangeCode processes the authorization code.
func (Provider) ExchangeCode(_ context.Context, _ string) (*provider.SocialResult, error) {
	// TODO: implement Apple code exchange with JWT verification
	return nil, fmt.Errorf("apple provider not yet implemented")
}
```

- [ ] **Step 2: 验证编译**

Run: `go build ./internal/identity/apple/`
Expected: 无输出

- [ ] **Step 3: 提交**

```bash
git add internal/identity/apple/apple.go
git commit -m "refactor(identity): extract apple provider into its own subpackage"
```

---

## Task 6: 迁移 tencent/wechat 子包

**Files:**
- Create: `internal/identity/tencent/wechat/wechat.go`

源：`internal/identity/wechat.go` + `internal/identity/provider.go` 中的 `extraString` helper。

- [ ] **Step 1: 创建 wechat.go**

创建 `internal/identity/tencent/wechat/wechat.go`（package 改为 `wechat`；`WeChatProvider` → `Provider`；`NewWeChatProvider` → `New`；`SocialResult` 改为 `provider.SocialResult`；`extraString` helper 一并搬入此文件，作为 unexported helper）：

```go
// Package wechat implements the WeChat web QR-code (qrconnect) social login provider.
package wechat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	userv1 "user-service/gen/user/v1"
	"user-service/internal/identity/provider"

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
}

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

// GetAuthURL returns the OAuth authorization URL.
func (p *Provider) GetAuthURL(_ context.Context, redirectURL, state string) (string, error) {
	if redirectURL != "" {
		p.config.RedirectURL = redirectURL
	}
	return p.config.AuthCodeURL(state), nil
}

// ExchangeCode exchanges the OAuth code for user info.
func (p *Provider) ExchangeCode(ctx context.Context, code string) (*provider.SocialResult, error) {
	token, err := p.config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oauth exchange: %w", err)
	}

	openid := extraString(token, "openid")
	unionid := extraString(token, "unionid")

	userInfoURL := fmt.Sprintf(
		"https://api.weixin.qq.com/sns/userinfo?access_token=%s&openid=%s",
		url.QueryEscape(token.AccessToken),
		url.QueryEscape(openid),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wechat user info: %w", err)
	}
	defer resp.Body.Close()

	var user struct {
		OpenID     string `json:"openid"`
		Nickname   string `json:"nickname"`
		HeadImgURL string `json:"headimgurl"`
		UnionID    string `json:"unionid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode wechat user: %w", err)
	}

	return &provider.SocialResult{
		Provider:    userv1.IdentityProvider_IDENTITY_PROVIDER_WECHAT,
		ProviderUID: openid,
		Nickname:    user.Nickname,
		AvatarURL:   user.HeadImgURL,
		AccessToken: token.AccessToken,
		UnionID:     unionid,
	}, nil
}

// --- internal helpers ---

func extraString(t *oauth2.Token, key string) string {
	if v, ok := t.Extra(key).(string); ok {
		return v
	}
	return ""
}
```

- [ ] **Step 2: 验证编译**

Run: `go build ./internal/identity/tencent/wechat/`
Expected: 无输出

- [ ] **Step 3: 提交**

```bash
git add internal/identity/tencent/wechat/wechat.go
git commit -m "refactor(identity): extract wechat (web QR) provider under tencent/ subtree"
```

---

## Task 7: 更新使用方（service.go + social.go）

**Files:**
- Modify: `internal/service/service.go`
- Modify: `internal/service/social/social.go`

至此旧 `internal/identity/{provider,github,google,wechat,apple,wechat_miniprogram}.go` 仍在原位，旧调用与新子包并存编译，可正常工作。本 task 一次性把使用方切到新子包；旧文件留到 Task 8 删除。

- [ ] **Step 1: 更新 service.go 的 imports**

在 `internal/service/service.go` 中：

把 import 块里的：

```go
	"user-service/internal/identity"
```

替换为 6 个子包 import（`identity` 包本身在 service.go 中不再被引用，必须移除以免 unused）：

```go
	"user-service/internal/identity/apple"
	"user-service/internal/identity/github"
	"user-service/internal/identity/google"
	"user-service/internal/identity/provider"
	"user-service/internal/identity/tencent/mini"
	"user-service/internal/identity/tencent/wechat"
```

把 import 块里的：

```go
	"github.com/servekit/go-common/tencent/mini"
```

整行删除。

- [ ] **Step 2: 更新 service.go 的 socialProviders 构造**

把 `newWithDeps` 函数中的：

```go
	// WeChat Mini Program Manager (access token caching + multi-appid)
	wechatMgr := mini.NewManager(&mini.Config{
		Credentials: map[string]string{
			cfg.OAuth.WeChat.AppID: cfg.OAuth.WeChat.AppSecret,
		},
	})

	// Social providers
	socialProviders := map[pb.IdentityProvider]identity.SocialProvider{
		pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB: identity.NewGitHubProvider(
			cfg.OAuth.GitHub.ClientID, cfg.OAuth.GitHub.ClientSecret, cfg.OAuth.GitHub.RedirectURL,
		),
		pb.IdentityProvider_IDENTITY_PROVIDER_GOOGLE: identity.NewGoogleProvider(
			cfg.OAuth.Google.ClientID, cfg.OAuth.Google.ClientSecret, cfg.OAuth.Google.RedirectURL,
		),
		pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT: identity.NewWeChatProvider(
			cfg.OAuth.WeChat.AppID, cfg.OAuth.WeChat.AppSecret, cfg.OAuth.WeChat.RedirectURL,
		),
		pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT_MINIPROGRAM: identity.NewWeChatMiniProgramProvider(
			cfg.OAuth.WeChat.AppID, wechatMgr,
		),
		pb.IdentityProvider_IDENTITY_PROVIDER_APPLE: identity.NewAppleProvider(
			cfg.OAuth.Apple.ClientID, cfg.OAuth.Apple.TeamID, cfg.OAuth.Apple.KeyID, cfg.OAuth.Apple.RedirectURL,
		),
	}
```

替换为：

```go
	// WeChat Mini Program Manager (access token caching + multi-appid)
	wechatMgr := mini.NewManager(&mini.Config{
		Credentials: map[string]string{
			cfg.OAuth.WeChat.AppID: cfg.OAuth.WeChat.AppSecret,
		},
	})

	// Social providers
	socialProviders := map[pb.IdentityProvider]provider.SocialProvider{
		pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB: github.New(
			cfg.OAuth.GitHub.ClientID, cfg.OAuth.GitHub.ClientSecret, cfg.OAuth.GitHub.RedirectURL,
		),
		pb.IdentityProvider_IDENTITY_PROVIDER_GOOGLE: google.New(
			cfg.OAuth.Google.ClientID, cfg.OAuth.Google.ClientSecret, cfg.OAuth.Google.RedirectURL,
		),
		pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT: wechat.New(
			cfg.OAuth.WeChat.AppID, cfg.OAuth.WeChat.AppSecret, cfg.OAuth.WeChat.RedirectURL,
		),
		pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT_MINIPROGRAM: mini.NewProvider(
			cfg.OAuth.WeChat.AppID, wechatMgr,
		),
		pb.IdentityProvider_IDENTITY_PROVIDER_APPLE: apple.New(
			cfg.OAuth.Apple.ClientID, cfg.OAuth.Apple.TeamID, cfg.OAuth.Apple.KeyID, cfg.OAuth.Apple.RedirectURL,
		),
	}
```

注：`service.go` 此后不再使用 `identity` 包本身（不再有 `identity.Xxx` 引用），其 import 已在 Step 1 中替换为 6 个子包。

- [ ] **Step 3: 更新 social.go 的 imports**

在 `internal/service/social/social.go` 中：

把 import 块里的：

```go
	"user-service/internal/identity"
```

替换为：

```go
	"user-service/internal/identity/provider"
	"user-service/internal/identity/tencent/mini"
```

- [ ] **Step 4: 更新 social.go 的类型引用**

把 `internal/service/social/social.go` 中所有：

- `identity.SocialProvider` → `provider.SocialProvider`
- `identity.RedirectProvider` → `provider.RedirectProvider`
- `identity.SocialResult` → `provider.SocialResult`
- `*identity.WeChatMiniProgramProvider` → `*mini.Provider`

具体落点：

```go
// 第 26 行
socialProviders map[pb.IdentityProvider]provider.SocialProvider
// 第 34 行
socialProviders map[pb.IdentityProvider]provider.SocialProvider,
// 第 51 行
redirectProv, ok := prov.(provider.RedirectProvider)
// 第 162 行
mpProv, ok := prov.(*mini.Provider)
// 第 304 行
result *provider.SocialResult
```

- [ ] **Step 5: 验证编译**

Run: `go build ./internal/service/...`
Expected: 无输出

- [ ] **Step 6: 验证测试**

Run: `go test ./internal/service/...`
Expected: 全部 PASS（若有依赖外部 DB/Redis 的集成测试，预期 FAIL/跳过也行，只要不是编译错误）

- [ ] **Step 7: 提交**

```bash
git add internal/service/service.go internal/service/social/social.go
git commit -m "refactor(service): switch to new identity subpackages"
```

---

## Task 8: 删除旧的 identity 包内 provider 文件

**Files:**
- Delete: `internal/identity/provider.go`
- Delete: `internal/identity/github.go`
- Delete: `internal/identity/google.go`
- Delete: `internal/identity/wechat.go`
- Delete: `internal/identity/apple.go`
- Delete: `internal/identity/wechat_miniprogram.go`

保留：`internal/identity/credentials.go`（`HashPassword` / `VerifyPassword` 仍被 `auth.go`、`user.go` 使用）。

- [ ] **Step 1: 删除文件**

```bash
git rm internal/identity/provider.go \
       internal/identity/github.go \
       internal/identity/google.go \
       internal/identity/wechat.go \
       internal/identity/apple.go \
       internal/identity/wechat_miniprogram.go
```

- [ ] **Step 2: 验证全项目编译**

Run: `go build ./...`
Expected: 无输出

- [ ] **Step 3: 验证全项目测试**

Run: `go test ./...`
Expected: 所有单元测试 PASS；依赖 testcontainer/Redis 的集成测试如本地无环境，FAIL/跳过可接受，但不应该出现"undefined"类编译错误。

- [ ] **Step 4: 提交**

```bash
git commit -m "refactor(identity): remove old provider files (now split into subpackages)"
```

注：`git rm` 已经把删除暂存，所以这里只需 commit。

---

## Task 9: 最终验证（格式化 + lint + test + build）

**Files:** 无修改（除非 lint 报错需要修复）。

- [ ] **Step 1: 格式化全项目**

Run: `gofmt -w .`
Expected: 无输出

Run: `goimports -w .`
Expected: 无输出

- [ ] **Step 2: lint**

Run: `golangci-lint run ./...`
Expected: 无 error。如有 unused import 等提示，按提示修复后回到 Step 1 重跑。

- [ ] **Step 3: 测试**

Run: `go test -race ./internal/identity/... ./internal/service/...`
Expected: 全部 PASS

- [ ] **Step 4: 构建**

Run: `go build ./...`
Expected: 无输出

- [ ] **Step 5: 提交（如有改动）**

Run: `git status`
Expected: 大概率 working tree clean（前面 task 已经按子包 commit 完）。

如果 gofmt/goimports 改了某些文件（`git status` 列出 modified），按文件名精确添加：

```bash
git add <具体的 .go 文件路径>
git commit -m "style: apply gofmt/goimports after identity refactor"
```

如果 `git status` 显示 working tree clean，跳过本步。

- [ ] **Step 6: 确认 go.mod 不需要清理**

Run: `go mod tidy && git diff go.mod go.sum`
Expected: 不应有变化。`github.com/servekit/go-common` 仍被其他子包（captcha/cronx/dbx/lifecycle/ratelimit/redisx/ptr）使用，replace 行必须保留。

如果 `go.mod` / `go.sum` 有变更，提交：

```bash
git add go.mod go.sum
git commit -m "chore: go mod tidy after dropping tencent/mini dep"
```

---

## 完成判据

- `grep -rn "go-common/tencent/mini" --include="*.go"` 在本仓库内无任何命中。
- `internal/identity/` 根目录只剩 `credentials.go`（+ 可能的 doc.go）。
- 各 provider 子包目录结构与 spec 一致。
- `go build ./...`、`go test -race ./...`、`golangci-lint run ./...` 均通过。
