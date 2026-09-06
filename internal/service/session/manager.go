// Package session provides Redis-backed session management.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/servekit/user-service/pkg/config"
	"github.com/servekit/user-service/pkg/xcodes"

	"github.com/redis/go-redis/v9"
)

// Data stored in Redis for each active session.
type Data struct {
	UserID      int64     `json:"user_id"`
	LoginIP     string    `json:"login_ip"`
	LoginAt     time.Time `json:"login_at"`
	LoginMethod string    `json:"login_method"` // email, phone, github, google, wechat, apple
	UserAgent   string    `json:"user_agent"`   // raw User-Agent header
	OS          string    `json:"os"`           // e.g. "iOS 17.2", "Windows 11", "macOS 14.1"
	Browser     string    `json:"browser"`      // e.g. "Chrome 120", "Safari 17", "WeChat 8.0"
	Device      string    `json:"device"`       // e.g. "iPhone 15 Pro", "Samsung Galaxy S24", "MacBook Pro"
}

// Manager manages sessions in Redis.
type Manager struct {
	client             *redis.Client
	ttl                time.Duration
	maxSessions        int
	keyPrefix          string
	userSessionsPrefix string
	codeTTL            time.Duration // TTL for one-time session short codes (IssueSessionCode)
}

// defaultSessionCodeTTL is the fallback TTL for IssueSessionCode short codes
// when SessionConfig.SessionCodeTTL is not set.
const defaultSessionCodeTTL = 5 * time.Minute

// NewManager creates a new session Manager.
func NewManager(client *redis.Client, cfg *config.SessionConfig) *Manager {
	codeTTL := defaultSessionCodeTTL
	if cfg.SessionCodeTTL > 0 {
		codeTTL = cfg.SessionCodeTTL
	}
	return &Manager{
		client:             client,
		ttl:                cfg.TTL,
		maxSessions:        cfg.MaxSessions,
		keyPrefix:          cfg.KeyPrefix,
		userSessionsPrefix: cfg.UserSessionsPrefix,
		codeTTL:            codeTTL,
	}
}

// Create stores a new session in Redis.
// If maxSessions > 0 and the user exceeds the limit, the least recently active sessions are revoked.
func (m *Manager) Create(ctx context.Context, sessionID string, data *Data) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal session data: %w", err)
	}
	expiry := float64(time.Now().Add(m.ttl).Unix())
	uKey := m.userSessionsKey(data.UserID)
	pipe := m.client.Pipeline()
	pipe.Set(ctx, m.sessionKey(sessionID), b, m.ttl)
	pipe.ZAdd(ctx, uKey, redis.Z{Score: expiry, Member: sessionID})
	pipe.Expire(ctx, uKey, m.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	if m.maxSessions > 0 {
		if err := m.evictOldest(ctx, data.UserID); err != nil {
			return xcodes.ErrInternal.Wrap(err)
		}
	}
	return nil
}

// Get returns session data without affecting TTL. Use for read-only lookups
// (admin views, locating a session before Revoke, background jobs). Any caller
// that wants sliding-window renewal must call Validate explicitly.
func (m *Manager) Get(ctx context.Context, sessionID string) (*Data, error) {
	b, err := m.client.Get(ctx, m.sessionKey(sessionID)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, xcodes.ErrSessionInvalid.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	var data Data
	if err := json.Unmarshal([]byte(b), &data); err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &data, nil
}

// Validate reads session data and atomically refreshes the session key TTL and
// ZSET score via Lua (single Redis round trip). This is the sliding-window
// mechanism: every authenticated request extends the session by TTL. Only call
// this from validate-on-use paths (gateway interceptor, GetSession RPC); pure
// reads should use Get to avoid the renewal side effect.
func (m *Manager) Validate(ctx context.Context, sessionID string) (*Data, error) {
	key := m.sessionKey(sessionID)
	expiry := time.Now().Add(m.ttl).Unix()

	result, err := luaGetAndRefresh.Run(ctx, m.client,
		[]string{key},
		int64(m.ttl.Seconds()), sessionID, expiry, m.userSessionsPrefix,
	).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, xcodes.ErrSessionInvalid.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	b, ok := result.(string)
	if !ok {
		return nil, xcodes.ErrSessionInvalid.New()
	}

	var data Data
	if err := json.Unmarshal([]byte(b), &data); err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &data, nil
}

// Revoke removes a session.
func (m *Manager) Revoke(ctx context.Context, sessionID string, userID int64) error {
	pipe := m.client.Pipeline()
	pipe.Del(ctx, m.sessionKey(sessionID))
	pipe.ZRem(ctx, m.userSessionsKey(userID), sessionID)
	if _, err := pipe.Exec(ctx); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// RevokeAll removes all sessions for a user.
func (m *Manager) RevokeAll(ctx context.Context, userID int64) error {
	uKey := m.userSessionsKey(userID)
	sessionIDs, err := m.client.ZRange(ctx, uKey, 0, -1).Result()
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	if len(sessionIDs) == 0 {
		return nil
	}
	pipe := m.client.Pipeline()
	for _, sid := range sessionIDs {
		pipe.Del(ctx, m.sessionKey(sid))
	}
	pipe.Del(ctx, uKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// TTL returns the configured session TTL.
func (m *Manager) TTL() time.Duration {
	return m.ttl
}

// RemainingTTL returns the remaining TTL of a session key. Mirrors Redis PTTL
// semantics: -2 if the key does not exist, -1 if it exists without expiry,
// otherwise the duration until expiry. Used by read-only RPCs (e.g. GetSession)
// that surface expires_at without changing the write side effects of Get.
func (m *Manager) RemainingTTL(ctx context.Context, sessionID string) (time.Duration, error) {
	ttl, err := m.client.PTTL(ctx, m.sessionKey(sessionID)).Result()
	if err != nil {
		return 0, xcodes.ErrInternal.Wrap(err)
	}
	return ttl, nil
}

// GetMulti fetches multiple sessions in one Redis round trip via MGet. Returns
// a map keyed by session ID; sessions whose key has expired (or never existed)
// are silently omitted — callers should treat absence as "not in Redis".
//
// Trade-off vs. Get: GetMulti does NOT refresh TTL. List views do not slide
// the session window forward — only the validate-on-use path (Get / GetSession
// ) does. This matches "list ≠ validate" semantics: an admin
// browsing active sessions should not extend them. If a caller does need TTL
// refresh, use Get instead.
func (m *Manager) GetMulti(ctx context.Context, sessionIDs []string) (map[string]*Data, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	keys := make([]string, len(sessionIDs))
	for i, sid := range sessionIDs {
		keys[i] = m.sessionKey(sid)
	}
	results, err := m.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	out := make(map[string]*Data, len(sessionIDs))
	for i, r := range results {
		if r == nil {
			continue
		}
		b, ok := r.(string)
		if !ok || b == "" {
			continue
		}
		var data Data
		if err := json.Unmarshal([]byte(b), &data); err != nil {
			continue
		}
		out[sessionIDs[i]] = &data
	}
	return out, nil
}

// ListByUserID returns all active session IDs for a user, each with its ZSET
// expiry score (unix seconds). Create and the validate-on-use refresh both
// set score = last-activity + TTL, so score - TTL yields the session's last
// activity time without touching the session keys themselves.
func (m *Manager) ListByUserID(ctx context.Context, userID int64) ([]string, map[string]float64, error) {
	if err := m.cleanExpired(ctx, userID); err != nil {
		return nil, nil, err
	}
	uKey := m.userSessionsKey(userID)
	zs, err := m.client.ZRangeWithScores(ctx, uKey, 0, -1).Result()
	if err != nil {
		return nil, nil, xcodes.ErrInternal.Wrap(err)
	}
	if len(zs) == 0 {
		return nil, nil, nil
	}
	ids := make([]string, 0, len(zs))
	scores := make(map[string]float64, len(zs))
	for _, z := range zs {
		sid, ok := z.Member.(string)
		if !ok {
			continue
		}
		ids = append(ids, sid)
		scores[sid] = z.Score
	}
	return ids, scores, nil
}

// --- internal helpers ---

// luaGetAndRefresh gets session data and refreshes TTL + ZSET score in one round trip.
// Uses string.match instead of cjson.decode to avoid Lua float precision loss on snowflake IDs.
var luaGetAndRefresh = redis.NewScript(`
local session_key = KEYS[1]
local ttl = tonumber(ARGV[1])
local session_id = ARGV[2]
local expiry = tonumber(ARGV[3])
local zset_prefix = ARGV[4]

local data = redis.call('GET', session_key)
if not data then
	return nil
end

local user_id = string.match(data, '"user_id":%s*(%d+)')
if not user_id then
	return nil
end

local zset_key = zset_prefix .. ":" .. user_id
redis.call('EXPIRE', session_key, ttl)
redis.call('ZADD', zset_key, expiry, session_id)
redis.call('EXPIRE', zset_key, ttl)

return data
`)

// sessionKey returns the Redis key for a session.
func (m *Manager) sessionKey(sessionID string) string {
	return fmt.Sprintf("%s:%s", m.keyPrefix, sessionID)
}

// userSessionsKey returns the Redis key for a user's session ZSET.
func (m *Manager) userSessionsKey(userID int64) string {
	return fmt.Sprintf("%s:%d", m.userSessionsPrefix, userID)
}

// evictOldest removes the least recently active sessions when a user exceeds the session limit.
func (m *Manager) evictOldest(ctx context.Context, userID int64) error {
	if err := m.cleanExpired(ctx, userID); err != nil {
		return err
	}
	uKey := m.userSessionsKey(userID)
	count, err := m.client.ZCard(ctx, uKey).Result()
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	if int(count) <= m.maxSessions {
		return nil
	}
	toEvict := int(count) - m.maxSessions
	// ZRange ascending: lowest score = expires soonest = least recently active
	ids, err := m.client.ZRange(ctx, uKey, 0, int64(toEvict-1)).Result()
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	pipe := m.client.Pipeline()
	for _, sid := range ids {
		pipe.Del(ctx, m.sessionKey(sid))
		pipe.ZRem(ctx, uKey, sid)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// cleanExpired removes expired session entries from the user's ZSET.
func (m *Manager) cleanExpired(ctx context.Context, userID int64) error {
	uKey := m.userSessionsKey(userID)
	now := time.Now().Unix()
	if err := m.client.ZRemRangeByScore(ctx, uKey, "-inf", fmt.Sprintf("%d", now)).Err(); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}
