# Plan 4: Identity Providers, Session & Auth Services

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现完整的认证流程：Identity Provider 抽象、Redis Session 管理、注册/登录/登出、用户 CRUD、OAuth、密码重置。

**Architecture:** Session Manager 管理 Redis 中的会话。Identity Provider 接口抽象不同登录方式（邮箱、手机、OAuth）。Service 层组合 Provider + Session Manager + Repository 完成业务逻辑。

**Tech Stack:** Go, gRPC, Redis (go-redis/v9), bcrypt, golang.org/x/oauth2, go-common/captcha, go-common/message

**Depends on:** Plan 1 (Foundation), Plan 2 (Repository), Plan 3 (Proto & gRPC)

**Produces:** 完整的认证和用户管理功能，可通过 gRPC 调用。

**Spec:** `docs/superpowers/specs/2026-05-22-user-service-design.md` §8 (Session), §9 (Identity Provider), §10 (Core Flows)

---

## File Structure

```
internal/session/
  manager.go                # Session Manager 接口 + Redis 实现
  manager_test.go           # miniredis 测试
internal/identity/
  provider.go               # Provider 接口 + Request/Result 类型
  email.go                  # EmailProvider
  email_test.go
  phone.go                  # PhoneProvider
  phone_test.go
  github.go                 # GitHub OAuth
  google.go                 # Google OAuth
  wechat.go                 # WeChat OAuth
  apple.go                  # Apple Sign-In
internal/service/
  auth.go                   # Register, Login, Logout, RefreshSession
  auth_test.go
  user.go                   # GetProfile, UpdateProfile, ChangePassword, ResetPassword, Admin CRUD
  user_test.go
  oauth.go                  # GetOAuthURL, OAuthCallback
  session.go                # ListSessions, RevokeSession, RevokeAllSessions
```

---

### Task 1: Redis Session Manager

**Files:**
- Create: `internal/session/manager.go`
- Create: `internal/session/manager_test.go`

- [ ] **Step 1: Add Redis dependency**

```bash
go get github.com/redis/go-redis/v9
go get github.com/alicebob/miniredis/v2
```

- [ ] **Step 2: Write manager.go**

```go
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"user-service/internal/config"
	"user-service/internal/xcodes"

	"github.com/redis/go-redis/v9"
)

// SessionData stored in Redis.
type SessionData struct {
	UserID    int64  `json:"user_id"`
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatar_url"`
	IP        string `json:"ip"`
	DeviceType string `json:"device_type"`
}

// Manager manages sessions in Redis.
type Manager struct {
	client              *redis.Client
	ttl                 time.Duration
	keyPrefix           string // e.g. "user:session:"
	userSessionsPrefix  string // e.g. "user:user_sessions:"
}

func NewManager(client *redis.Client, cfg config.SessionConfig) *Manager {
	return &Manager{
		client:             client,
		ttl:                cfg.TTL,
		keyPrefix:          cfg.Redis.KeyPrefix,
		userSessionsPrefix: cfg.Redis.UserSessionsPrefix,
	}
}

// Create stores a new session in Redis.
func (m *Manager) Create(ctx context.Context, sessionID string, data *SessionData) error {
	key := m.keyPrefix + sessionID
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal session data: %w", err)
	}
	if err := m.client.Set(ctx, key, b, m.ttl).Err(); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	// Add to user's session set
	userKey := fmt.Sprintf("%s%d", m.userSessionsPrefix, data.UserID)
	if err := m.client.SAdd(ctx, userKey, sessionID).Err(); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// Get retrieves session data. Returns error if not found or expired.
func (m *Manager) Get(ctx context.Context, sessionID string) (*SessionData, error) {
	key := m.keyPrefix + sessionID
	b, err := m.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, xcodes.ErrSessionInvalid.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	var data SessionData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	// Refresh TTL on access
	m.client.Expire(ctx, key, m.ttl)
	return &data, nil
}

// Refresh extends the session TTL.
func (m *Manager) Refresh(ctx context.Context, sessionID string) error {
	key := m.keyPrefix + sessionID
	if err := m.client.Expire(ctx, key, m.ttl).Err(); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// Revoke removes a session.
func (m *Manager) Revoke(ctx context.Context, sessionID string, userID int64) error {
	key := m.keyPrefix + sessionID
	if err := m.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("redis del session: %w", err)
	}
	userKey := fmt.Sprintf("%s%d", m.userSessionsPrefix, userID)
	m.client.SRem(ctx, userKey, sessionID)
	return nil
}

// RevokeAll removes all sessions for a user.
func (m *Manager) RevokeAll(ctx context.Context, userID int64) error {
	userKey := fmt.Sprintf("%s%d", m.userSessionsPrefix, userID)
	sessionIDs, err := m.client.SMembers(ctx, userKey).Result()
	if err != nil {
		return fmt.Errorf("redis smembers: %w", err)
	}
	for _, sid := range sessionIDs {
		m.client.Del(ctx, m.keyPrefix+sid)
	}
	m.client.Del(ctx, userKey)
	return nil
}

// ListByUserID returns all active session IDs for a user.
func (m *Manager) ListByUserID(ctx context.Context, userID int64) ([]string, error) {
	userKey := fmt.Sprintf("%s%d", m.userSessionsPrefix, userID)
	ids, err := m.client.SMembers(ctx, userKey).Result()
	if err != nil {
		return nil, fmt.Errorf("redis smembers: %w", err)
	}
	// Filter out expired sessions
	var active []string
	for _, sid := range ids {
		exists := m.client.Exists(ctx, m.keyPrefix+sid).Val()
		if exists == 1 {
			active = append(active, sid)
		} else {
			m.client.SRem(ctx, userKey, sid)
		}
	}
	return active, nil
}
```

- [ ] **Step 3: Write manager_test.go**

```go
package session

import (
	"context"
	"testing"
	"time"

	"user-service/internal/config"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestManager(t *testing.T) (*Manager, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := config.SessionConfig{
		TTL: 7 * 24 * time.Hour,
		Redis: config.SessionRedisConfig{
			KeyPrefix:          "user:session:",
			UserSessionsPrefix: "user:user_sessions:",
		},
	}
	return NewManager(client, cfg), mr
}

func TestManager_CreateAndGet(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	data := &SessionData{UserID: 1, Nickname: "alice"}
	err := mgr.Create(ctx, "sess-1", data)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := mgr.Get(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UserID != 1 || got.Nickname != "alice" {
		t.Errorf("got %+v, want UserID=1 Nickname=alice", got)
	}
}

func TestManager_Get_NotFound(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	_, err := mgr.Get(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestManager_Revoke(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	mgr.Create(ctx, "sess-1", &SessionData{UserID: 1})
	mgr.Revoke(ctx, "sess-1", 1)

	_, err := mgr.Get(ctx, "sess-1")
	if err == nil {
		t.Fatal("expected error after revoke")
	}
}

func TestManager_RevokeAll(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	mgr.Create(ctx, "s1", &SessionData{UserID: 1})
	mgr.Create(ctx, "s2", &SessionData{UserID: 1})

	err := mgr.RevokeAll(ctx, 1)
	if err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}

	_, err1 := mgr.Get(ctx, "s1")
	_, err2 := mgr.Get(ctx, "s2")
	if err1 == nil || err2 == nil {
		t.Fatal("expected both sessions to be revoked")
	}
}

func TestManager_ListByUserID(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	mgr.Create(ctx, "s1", &SessionData{UserID: 1})
	mgr.Create(ctx, "s2", &SessionData{UserID: 1})

	ids, err := mgr.ListByUserID(ctx, 1)
	if err != nil {
		t.Fatalf("ListByUserID: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("got %d sessions, want 2", len(ids))
	}
}

func TestManager_TTL_Expired(t *testing.T) {
	mgr, mr := newTestManager(t)
	ctx := context.Background()

	mgr.Create(ctx, "s1", &SessionData{UserID: 1})
	mgr.ttl = 1 * time.Second
	mgr.Create(ctx, "s2", &SessionData{UserID: 1})

	// Fast-forward past TTL
	mr.FastForward(2 * time.Second)

	_, err := mgr.Get(ctx, "s1")
	if err == nil {
		t.Fatal("expected session to be expired")
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/session/... -v -count=1`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/session/ go.mod go.sum
git commit -m "feat: add Redis session manager with miniredis tests"
```

---

### Task 2: Identity Provider Interface

**Files:**
- Create: `internal/identity/provider.go`

- [ ] **Step 1: Write provider.go**

```go
package identity

import "context"

// Provider abstracts a login method (email, phone, OAuth).
type Provider interface {
	// Name returns the provider identifier: email, phone, github, google, wechat, apple.
	Name() string

	// Register creates a new identity for a user.
	Register(ctx context.Context, req *RegisterRequest) (*ProviderResult, error)

	// Authenticate verifies credentials and returns provider user info.
	Authenticate(ctx context.Context, req *AuthRequest) (*ProviderResult, error)

	// GetAuthURL returns the OAuth authorization URL (only for OAuth providers).
	// Returns ("", nil) for non-OAuth providers.
	GetAuthURL(ctx context.Context, redirectURL, state string) (string, error)

	// HandleCallback processes the OAuth callback (only for OAuth providers).
	// Returns (nil, nil) for non-OAuth providers.
	HandleCallback(ctx context.Context, code string) (*ProviderResult, error)
}

// RegisterRequest for creating a new identity.
type RegisterRequest struct {
	Target   string // email address / phone number
	Password string // bcrypt hash will be stored
	Code     string // verification code (already verified by caller)
	Nickname string // optional, for auto-register scenarios
}

// AuthRequest for login.
type AuthRequest struct {
	Target   string // email / phone
	Password string // plain password (for email provider)
	Code     string // verification code (for phone provider)
}

// ProviderResult is returned after successful register or authenticate.
type ProviderResult struct {
	ProviderUID string         // provider-side user ID (email address / phone / OAuth ID)
	Email       string         // OAuth-returned email (if any)
	Phone       string         // OAuth-returned phone (if any)
	Nickname    string         // OAuth-returned nickname
	AvatarURL   string         // OAuth-returned avatar
	RawData     map[string]any // OAuth raw response
	Credentials string        // stored credential (bcrypt hash etc.)
}
```

- [ ] **Step 2: Commit**

```bash
git add internal/identity/provider.go
git commit -m "feat: add identity provider interface"
```

---

### Task 3: Email Provider

**Files:**
- Create: `internal/identity/email.go`
- Create: `internal/identity/email_test.go`

- [ ] **Step 1: Add bcrypt dependency**

```bash
go get golang.org/x/crypto
```

- [ ] **Step 2: Write email.go**

EmailProvider 负责：
- Register: 创建 identity（bcrypt 哈希密码）
- Authenticate: 查找 identity，验证 bcrypt 密码

不负责：验证码校验（由 service 层调用 captcha.Verify 后传入 RegisterRequest）、用户创建（由 service 层编排）。

```go
package identity

import (
	"context"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// EmailProvider handles email+password authentication.
type EmailProvider struct{}

func NewEmailProvider() *EmailProvider {
	return &EmailProvider{}
}

func (p *EmailProvider) Name() string { return "email" }

func (p *EmailProvider) Register(ctx context.Context, req *RegisterRequest) (*ProviderResult, error) {
	if req.Target == "" {
		return nil, fmt.Errorf("email is required")
	}
	if req.Password == "" {
		return nil, fmt.Errorf("password is required for email registration")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("bcrypt hash: %w", err)
	}

	return &ProviderResult{
		ProviderUID: req.Target,
		Email:       req.Target,
		Credentials: string(hash),
	}, nil
}

func (p *EmailProvider) Authenticate(ctx context.Context, req *AuthRequest) (*ProviderResult, error) {
	// Authenticate only validates that required fields are present.
	// Actual bcrypt verification is done by the caller (AuthService) via VerifyPassword,
	// because the stored hash is in the Identity record which the caller looks up.
	if req.Target == "" {
		return nil, fmt.Errorf("email is required")
	}
	if req.Password == "" {
		return nil, fmt.Errorf("password is required")
	}
	return &ProviderResult{
		ProviderUID: req.Target,
	}, nil
}

func (p *EmailProvider) GetAuthURL(_ context.Context, _, _ string) (string, error) {
	return "", nil // Not an OAuth provider
}

func (p *EmailProvider) HandleCallback(_ context.Context, _ string) (*ProviderResult, error) {
	return nil, nil // Not an OAuth provider
}

// VerifyPassword checks a plain password against a bcrypt hash.
func VerifyPassword(hash, password string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return fmt.Errorf("wrong password")
	}
	return nil
}
```

- [ ] **Step 3: Write email_test.go**

```go
package identity

import (
	"context"
	"testing"
)

func TestEmailProvider_Register(t *testing.T) {
	p := NewEmailProvider()
	ctx := context.Background()

	result, err := p.Register(ctx, &RegisterRequest{
		Target:   "alice@example.com",
		Password: "secret123",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if result.ProviderUID != "alice@example.com" {
		t.Errorf("ProviderUID = %q, want alice@example.com", result.ProviderUID)
	}
	if result.Credentials == "" {
		t.Error("expected credentials to be set")
	}
	if result.Credentials == "secret123" {
		t.Error("credentials should be hashed, not plaintext")
	}
}

func TestEmailProvider_Register_NoPassword(t *testing.T) {
	p := NewEmailProvider()
	_, err := p.Register(context.Background(), &RegisterRequest{Target: "a@b.com"})
	if err == nil {
		t.Fatal("expected error when password is empty")
	}
}

func TestVerifyPassword(t *testing.T) {
	result, _ := NewEmailProvider().Register(context.Background(), &RegisterRequest{
		Target: "a@b.com", Password: "secret",
	})

	if err := VerifyPassword(result.Credentials, "secret"); err != nil {
		t.Errorf("VerifyPassword correct: %v", err)
	}
	if err := VerifyPassword(result.Credentials, "wrong"); err == nil {
		t.Error("expected error for wrong password")
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/identity/... -v -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/identity/email.go internal/identity/email_test.go go.mod go.sum
git commit -m "feat: add email identity provider with bcrypt"
```

---

### Task 4: Phone Provider

**Files:**
- Create: `internal/identity/phone.go`
- Create: `internal/identity/phone_test.go`

PhoneProvider 模式简单：不存储密码，只验证验证码（验证码由 service 层校验后传入）。

```go
package identity

import (
	"context"
	"fmt"
)

type PhoneProvider struct{}

func NewPhoneProvider() *PhoneProvider { return &PhoneProvider{} }
func (p *PhoneProvider) Name() string  { return "phone" }

func (p *PhoneProvider) Register(ctx context.Context, req *RegisterRequest) (*ProviderResult, error) {
	if req.Target == "" {
		return nil, fmt.Errorf("phone is required")
	}
	return &ProviderResult{
		ProviderUID: req.Target,
		Phone:       req.Target,
	}, nil
}

func (p *PhoneProvider) Authenticate(ctx context.Context, req *AuthRequest) (*ProviderResult, error) {
	if req.Target == "" {
		return nil, fmt.Errorf("phone is required")
	}
	return &ProviderResult{
		ProviderUID: req.Target,
		Phone:       req.Target,
	}, nil
}

func (p *PhoneProvider) GetAuthURL(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func (p *PhoneProvider) HandleCallback(_ context.Context, _ string) (*ProviderResult, error) {
	return nil, nil
}
```

TDD: 测试 Register 和 Authenticate 的基本场景和空输入校验。

```go
package identity

import (
	"context"
	"testing"
)

func TestPhoneProvider_Register(t *testing.T) {
	p := NewPhoneProvider()
	result, err := p.Register(context.Background(), &RegisterRequest{Target: "13800138000"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if result.ProviderUID != "13800138000" {
		t.Errorf("ProviderUID = %q, want 13800138000", result.ProviderUID)
	}
	if result.Phone != "13800138000" {
		t.Errorf("Phone = %q, want 13800138000", result.Phone)
	}
}

func TestPhoneProvider_Register_Empty(t *testing.T) {
	_, err := NewPhoneProvider().Register(context.Background(), &RegisterRequest{})
	if err == nil {
		t.Fatal("expected error for empty phone")
	}
}

func TestPhoneProvider_Authenticate(t *testing.T) {
	p := NewPhoneProvider()
	result, err := p.Authenticate(context.Background(), &AuthRequest{Target: "13800138000"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if result.ProviderUID != "13800138000" {
		t.Errorf("ProviderUID = %q, want 13800138000", result.ProviderUID)
	}
}
```

- [ ] **Commit**

```bash
git add internal/identity/phone.go internal/identity/phone_test.go
git commit -m "feat: add phone identity provider"
```

---

### Task 5: OAuth Providers

**Files:**
- Create: `internal/identity/github.go`
- Create: `internal/identity/google.go`
- Create: `internal/identity/wechat.go`
- Create: `internal/identity/apple.go`

每个 OAuth provider 实现 `GetAuthURL` 和 `HandleCallback`，使用 `golang.org/x/oauth2`。

通用模式：

```go
package identity

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

type GitHubProvider struct {
	config *oauth2.Config
}

func NewGitHubProvider(clientID, clientSecret, redirectURL string) *GitHubProvider {
	return &GitHubProvider{
		config: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     github.Endpoint,
			RedirectURL:  redirectURL,
			Scopes:       []string{"user:email"},
		},
	}
}

func (p *GitHubProvider) Name() string { return "github" }

func (p *GitHubProvider) GetAuthURL(_ context.Context, redirectURL, state string) (string, error) {
	if redirectURL != "" {
		p.config.RedirectURL = redirectURL
	}
	return p.config.AuthCodeURL(state), nil
}

func (p *GitHubProvider) HandleCallback(ctx context.Context, code string) (*ProviderResult, error) {
	token, err := p.config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oauth exchange: %w", err)
	}
	// Fetch user info from GitHub API
	// GET https://api.github.com/user with token
	// Parse id, login, email, avatar_url
	// Return ProviderResult
	// ... (implement with net/http + json.Unmarshal)
	return &ProviderResult{}, nil
}

func (p *GitHubProvider) Register(_ context.Context, _ *RegisterRequest) (*ProviderResult, error) {
	return nil, fmt.Errorf("github provider does not support direct registration")
}

func (p *GitHubProvider) Authenticate(_ context.Context, _ *AuthRequest) (*ProviderResult, error) {
	return nil, fmt.Errorf("github provider does not support direct authentication")
}
```

Google、WeChat、Apple 同样模式，差异在 endpoint、scopes、user info API。WeChat 用 `oauth2.Endpoint` 自定义，Apple 需要 JWT client_secret。

每个 provider 都有单元测试验证 GetAuthURL 返回合法 URL。

- [ ] **Commit**

```bash
git add internal/identity/github.go internal/identity/google.go \
        internal/identity/wechat.go internal/identity/apple.go
git commit -m "feat: add OAuth identity providers (GitHub, Google, WeChat, Apple)"
```

---

### Task 6: Auth Handler

**Files:**
- Create: `internal/service/auth.go`
- Create: `internal/service/auth_test.go`

Auth Handler 编排注册/登录流程，是整个认证的核心。
注意：Proto 中所有 RPC 都属于 `UserService`，所以 Handler 只实现自己负责的 RPC 方法，不 embed `UnimplementedUserServiceServer`。

```go
package service

import (
	"context"
	"fmt"

	pb "user-service/gen/user"
	"user-service/internal/identity"
	"user-service/internal/models"
	"user-service/internal/repository"
	"user-service/internal/session"
	"user-service/internal/xcodes"

	"github.com/google/uuid"
	"go-common/xerr"
)

// AuthHandler handles authentication RPCs (Register, Login, Logout, RefreshSession).
type AuthHandler struct {
	userRepo     *repository.UserRepository
	identityRepo *repository.IdentityRepository
	sessionRepo  *repository.SessionRepository
	loginLogRepo *repository.LoginLogRepo
	sessionMgr   *session.Manager
	providers    map[string]identity.Provider
}

func NewAuthHandler(
	userRepo *repository.UserRepository,
	identityRepo *repository.IdentityRepository,
	sessionRepo *repository.SessionRepository,
	loginLogRepo *repository.LoginLogRepo,
	sessionMgr *session.Manager,
	providers map[string]identity.Provider,
) *AuthHandler {
	return &AuthHandler{
		userRepo:     userRepo,
		identityRepo: identityRepo,
		sessionRepo:  sessionRepo,
		loginLogRepo: loginLogRepo,
		sessionMgr:   sessionMgr,
		providers:    providers,
	}
}
```

**Register 流程**（设计文档 §10.1）：
1. 获取对应 provider
2. 调用 provider.Register() 获取 ProviderResult
3. 检查 identity 是否已存在（provider + providerUID）
4. 创建 User（status=active, register_source=provider）
5. 创建 Identity（关联 user, 存储 credentials）
6. 创建 Session
7. 写 LoginLog（action=register, success=true）
8. 返回 User + session_id

**Login 流程**（设计文档 §10.2）：
1. 获取对应 provider
2. 查找 identity by provider + providerUID
3. 未找到 + phone provider → 自动注册
4. 未找到 + 其他 provider → 返回 IDENTITY_NOT_FOUND
5. 验证凭证（bcrypt / captcha）
6. 创建 Session
7. 写 LoginLog（action=login, success=true）
8. 更新 User.LastLoginAt
9. 返回 User + session_id

**Logout**: Revoke session from Redis + PG
**RefreshSession**: Refresh TTL in Redis

TDD: 用 mock repository 和 miniredis 测试各流程。测试正常路径、用户已存在、密码错误、自动注册等场景。

- [ ] **Commit**

```bash
git add internal/service/auth.go internal/service/auth_test.go
git commit -m "feat: add auth service with register/login/logout"
```

---

### Task 7: User Handler

**Files:**
- Create: `internal/service/user.go`
- Create: `internal/service/user_test.go`

Handler 只实现自己负责的 RPC 方法（GetProfile, UpdateProfile, ChangePassword, ResetPassword, admin CRUD），不 embed Unimplemented。

- **GetProfile**: 从 context 获取 userID（由 interceptor 注入），查 User + Identities
- **UpdateProfile**: 更新 User 可编辑字段（nickname, real_name, avatar_url, gender, birthday, timezone, locale, bio）
- **ChangePassword**: 验证旧密码，bcrypt 新密码，更新 identity.credentials
- **ResetPassword**: 验证 captcha，查找 identity by email，更新 credentials
- **GetUser** (admin): 按 ID 查用户
- **ListUsers** (admin): cursor 分页，支持 status/keyword 筛选
- **DisableUser** (admin): 更新 user.status

构造函数注入：UserRepo, IdentityRepo, LoginLogRepo, Captcha（go-common/captcha）。

- [ ] **Commit**

```bash
git add internal/service/user.go internal/service/user_test.go
git commit -m "feat: add user service with profile and admin operations"
```

---

### Task 8: OAuth Handler + Session Handler

**Files:**
- Create: `internal/service/oauth.go`
- Create: `internal/service/session.go`

**OAuth Handler**：
- GetOAuthURL: 调用对应 provider.GetAuthURL()，返回 URL + state
- OAuthCallback: 调用 provider.HandleCallback()，查找或创建 user + identity，创建 session

**Session Handler**：
- ListSessions: 列出当前用户的所有活跃 session（从 Redis + PG 获取详情）
- RevokeSession: 踢下线指定 session
- RevokeAllSessions: 踢下线所有 session

- [ ] **Commit**

```bash
git add internal/service/oauth.go internal/service/session.go
git commit -m "feat: add OAuth and session management services"
```

---

### Task 9: Run All Tests

- [ ] **Step 1: Run all tests**

Run: `go test ./internal/... -v -count=1 -race`
Expected: all PASS

- [ ] **Step 2: Run linter**

Run: `golangci-lint run ./internal/...`
Expected: no errors

- [ ] **Step 3: Final commit if fixes needed**

---

## Self-Review

### Spec Coverage
| Spec Section | Task |
|---|---|
| §8 Session (Redis) | Task 1 |
| §9 Identity Provider interface | Task 2 |
| Email provider | Task 3 |
| Phone provider | Task 4 |
| OAuth providers | Task 5 |
| §10.1 Email register | Task 6 |
| §10.2 Phone login + auto-register | Task 6 |
| §10.2.1 Password reset | Task 7 |
| §10.3 OAuth flow | Task 8 |
| §10.4 Bind identity | Task 7 |
| §10.5 Kick-off | Task 8 |

### Placeholder Scan
OAuth providers (Task 5) show the GitHub pattern and describe Google/WeChat/Apple differences. Service methods (Tasks 6-8) show constructor and method signatures with flow descriptions. Each task specifies test requirements.

### Type Consistency
Session Manager uses config.SessionConfig fields. Providers map uses string key matching Provider.Name(). Service constructors inject repos + session manager + providers.
