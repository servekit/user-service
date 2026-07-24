package mini

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/servekit/user-service/pkg/xcodes"

	"golang.org/x/sync/singleflight"
)

// Manager manages WeChat Mini Program API clients with access token caching.
// Supports multiple appids and auto-renews tokens before expiration.
type Manager struct {
	clients        map[string]*Client
	tokens         map[string]*cachedToken
	mu             sync.RWMutex
	sf             singleflight.Group
	nowFunc        func() time.Time
	onRefreshError func(appID string, err error)
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
		clients:        clients,
		tokens:         make(map[string]*cachedToken),
		nowFunc:        time.Now,
		onRefreshError: cfg.OnRefreshError,
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
		return nil, xcodes.ErrInternal.New(fmt.Sprintf("mini: appid %s not configured", appID))
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
	s, ok := v.(string)
	if !ok {
		return "", xcodes.ErrInternal.New(fmt.Sprintf("mini: unexpected access-token type %T", v))
	}
	return s, nil
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
	if err != nil && m.onRefreshError != nil {
		m.onRefreshError(appID, err)
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
