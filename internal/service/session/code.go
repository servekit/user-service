package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"github.com/redis/go-redis/v9"

	"github.com/servekit/user-service/pkg/xcodes"
)

// sessionCodeKeyPrefix is the Redis namespace for one-time short codes
// minted by IssueSessionCode. Distinct from the session-data keyspace
// (keyPrefix) so TTLs and access patterns don't interfere.
const sessionCodeKeyPrefix = "session:shortcode:"

// IssueSessionCode mints a one-time short code referencing sessionID.
// The code is 32 random bytes base64url-encoded (~43 chars). Stored in
// Redis under sessionCodeKeyPrefix with TTL = m.codeTTL. One-time use —
// exchange via ExchangeSessionCode consumes it atomically (GETDEL).
//
// Returns the code; caller (callback service) puts it in the URL query
// when 302'ing to return_to instead of leaking session_id.
func (m *Manager) IssueSessionCode(ctx context.Context, sessionID string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", xcodes.ErrInternal.Wrapf(err, "read random bytes for session code")
	}
	code := base64.RawURLEncoding.EncodeToString(raw[:])
	key := sessionCodeKeyPrefix + code
	if err := m.client.Set(ctx, key, sessionID, m.codeTTL).Err(); err != nil {
		return "", xcodes.ErrInternal.Wrapf(err, "redis set session code")
	}
	return code, nil
}

// ExchangeSessionCode trades a one-time code for the underlying session_id.
// Atomic GETDEL — replay returns an error. Empty code is rejected at the
// service layer (validation), but defended here too.
func (m *Manager) ExchangeSessionCode(ctx context.Context, code string) (string, error) {
	if code == "" {
		return "", xcodes.ErrBadRequest.New("empty session code")
	}
	key := sessionCodeKeyPrefix + code
	sid, err := m.client.GetDel(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", xcodes.ErrSessionInvalid.New("session code not found or already used")
		}
		return "", xcodes.ErrInternal.Wrapf(err, "redis getdel session code")
	}
	return sid, nil
}
