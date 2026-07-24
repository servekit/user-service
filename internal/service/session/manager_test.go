package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/servekit/go-common/redisx"
	"github.com/servekit/user-service/internal/service/session"
	"github.com/servekit/user-service/pkg/config"
)

// newTestManager builds a Manager backed by miniredis with a 1h TTL.
// Returns the underlying *redis.Client so tests can inspect keys directly.
func newTestManager(t *testing.T) (*session.Manager, *redis.Client) {
	t.Helper()
	rdb := redisx.NewTestClient(t)
	cfg := &config.SessionConfig{
		TTL:                time.Hour,
		KeyPrefix:          "test:sess",
		UserSessionsPrefix: "test:u",
	}
	return session.NewManager(rdb, cfg), rdb
}

// TestManager_Get_DoesNotRefreshTTL verifies the pure-read contract: Get
// returns session data but must not slide the TTL window forward. Any caller
// that wants renewal must call Validate explicitly.
func TestManager_Get_DoesNotRefreshTTL(t *testing.T) {
	m, rdb := newTestManager(t)
	ctx := context.Background()

	const sessionID = "sid-1"
	data := &session.Data{UserID: 42, LoginAt: time.Now()}
	if err := m.Create(ctx, sessionID, data); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Pin TTL to a small value. If Get refreshes, TTL snaps back to ~1h.
	if err := rdb.Expire(ctx, "test:sess:"+sessionID, 10*time.Second).Err(); err != nil {
		t.Fatalf("set short TTL: %v", err)
	}

	got, err := m.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UserID != 42 {
		t.Fatalf("Get returned wrong user: got %d, want 42", got.UserID)
	}

	ttl, err := rdb.TTL(ctx, "test:sess:"+sessionID).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl > 30*time.Second {
		t.Fatalf("Get refreshed TTL: got %v, want <= 30s (pure read should not slide window)", ttl)
	}
}

// TestManager_Validate_RefreshesTTL verifies the validate-on-use contract:
// Validate reads session data AND resets the TTL to the configured value.
// This is the sliding-window mechanism for authenticated requests.
func TestManager_Validate_RefreshesTTL(t *testing.T) {
	m, rdb := newTestManager(t)
	ctx := context.Background()

	const sessionID = "sid-2"
	data := &session.Data{UserID: 7, LoginAt: time.Now()}
	if err := m.Create(ctx, sessionID, data); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := rdb.Expire(ctx, "test:sess:"+sessionID, 10*time.Second).Err(); err != nil {
		t.Fatalf("set short TTL: %v", err)
	}

	got, err := m.Validate(ctx, sessionID)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.UserID != 7 {
		t.Fatalf("Validate returned wrong user: got %d, want 7", got.UserID)
	}

	ttl, err := rdb.TTL(ctx, "test:sess:"+sessionID).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl < 30*time.Minute {
		t.Fatalf("Validate did not refresh TTL: got %v, want >= 30m (validate must slide window)", ttl)
	}
}
