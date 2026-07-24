package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/servekit/go-common/redisx"
	"github.com/servekit/user-service/internal/service/session"
	"github.com/servekit/user-service/pkg/config"
)

// newCodeTestManager builds a Manager backed by miniredis with a short
// codeTTL. Returns the *miniredis.Miniredis so tests can FastForward to
// simulate TTL expiry (not exposed via redisx.NewTestClient).
func newCodeTestManager(t *testing.T) (*session.Manager, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cfg := &config.SessionConfig{
		TTL:                time.Hour,
		KeyPrefix:          "test:sess",
		UserSessionsPrefix: "test:u",
		SessionCodeTTL:     5 * time.Minute,
	}
	return session.NewManager(rdb, cfg), mr
}

// newCodeTestManagerDefault uses the project's standard redisx test client
// for tests that don't need to manipulate miniredis time directly.
func newCodeTestManagerDefault(t *testing.T) *session.Manager {
	t.Helper()
	rdb := redisx.NewTestClient(t)
	cfg := &config.SessionConfig{
		TTL:                time.Hour,
		KeyPrefix:          "test:sess",
		UserSessionsPrefix: "test:u",
		SessionCodeTTL:     5 * time.Minute,
	}
	return session.NewManager(rdb, cfg)
}

func TestIssueAndExchangeSessionCode_RoundTrip(t *testing.T) {
	m := newCodeTestManagerDefault(t)
	ctx := context.Background()

	code, err := m.IssueSessionCode(ctx, "sess-123")
	require.NoError(t, err)
	require.Len(t, code, 43, "base64url(32 bytes) = 43 chars")

	sid, err := m.ExchangeSessionCode(ctx, code)
	require.NoError(t, err)
	require.Equal(t, "sess-123", sid)
}

func TestExchangeSessionCode_OneTimeUse(t *testing.T) {
	m := newCodeTestManagerDefault(t)
	ctx := context.Background()

	code, err := m.IssueSessionCode(ctx, "sess-123")
	require.NoError(t, err)

	_, err = m.ExchangeSessionCode(ctx, code)
	require.NoError(t, err)

	_, err = m.ExchangeSessionCode(ctx, code)
	require.Error(t, err, "second exchange must fail — one-time use")
}

func TestExchangeSessionCode_Expired(t *testing.T) {
	m, mr := newCodeTestManager(t)
	ctx := context.Background()

	code, err := m.IssueSessionCode(ctx, "sess-123")
	require.NoError(t, err)

	mr.FastForward(6 * time.Minute)

	_, err = m.ExchangeSessionCode(ctx, code)
	require.Error(t, err, "expired code must fail")
}

func TestIssueSessionCode_Random(t *testing.T) {
	m := newCodeTestManagerDefault(t)
	ctx := context.Background()

	seen := make(map[string]bool, 50)
	for i := 0; i < 50; i++ {
		code, err := m.IssueSessionCode(ctx, "sess")
		require.NoError(t, err)
		require.False(t, seen[code], "collision at iter %d", i)
		seen[code] = true
	}
}

// TestExchangeSessionCode_Empty ensures the defensive empty-code check
// fails before hitting Redis with an empty key.
func TestExchangeSessionCode_Empty(t *testing.T) {
	m := newCodeTestManagerDefault(t)
	ctx := context.Background()

	_, err := m.ExchangeSessionCode(ctx, "")
	require.Error(t, err)
}
