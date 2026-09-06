package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	pb "github.com/servekit/api/gen/go/user/v1"

	"github.com/servekit/user-service/internal/service/session"
)

// TestListSessions_MapsDeviceTypeAndLastActive pins the enriched list view:
// DeviceType derives from the stored OS/UserAgent, LastActiveAt from the
// per-user ZSET expiry score (score - TTL = last validate-on-use). The web
// session's score is doctored to "last active 25m after login"; the legacy
// session (no captured environment, never re-validated) stays UNSPECIFIED
// with last-active == login time.
func TestListSessions_MapsDeviceTypeAndLastActive(t *testing.T) {
	m, rdb := newTestManager(t)
	svc := session.New(nil, m) // ListSessions touches only the manager
	ctx := context.Background()

	loginAt := time.Now()
	if err := m.Create(ctx, "sess-web", &session.Data{
		UserID: 7, LoginAt: loginAt, LoginIP: "203.0.113.7",
		OS: "macOS 10.15.7", Browser: "Chrome 120.0.0.0",
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/120.0.0.0",
	}); err != nil {
		t.Fatalf("Create web: %v", err)
	}
	if err := m.Create(ctx, "sess-legacy", &session.Data{
		UserID: 7, LoginAt: loginAt,
	}); err != nil {
		t.Fatalf("Create legacy: %v", err)
	}

	// Simulate activity on the web session 25 minutes after login: the
	// validate-on-use Lua sets score = last-activity + TTL.
	lastUse := loginAt.Add(25 * time.Minute)
	if err := rdb.ZAdd(ctx, "test:u:7", redis.Z{
		Score:  float64(lastUse.Add(time.Hour).Unix()),
		Member: "sess-web",
	}).Err(); err != nil {
		t.Fatalf("ZAdd: %v", err)
	}

	resp, err := svc.ListSessions(ctx, &pb.ListSessionsRequest{UserId: 7})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.GetSessions()) != 2 {
		t.Fatalf("sessions = %d, want 2", len(resp.GetSessions()))
	}
	byID := map[string]*pb.Session{}
	for _, s := range resp.GetSessions() {
		byID[s.GetId()] = s
	}

	web := byID["sess-web"]
	if got := web.GetDeviceType(); got != pb.DeviceType_DEVICE_TYPE_WEB {
		t.Errorf("web device type = %v, want WEB", got)
	}
	if got, want := web.GetLastActiveAt().AsTime(), lastUse; got.Sub(want) < -time.Minute || got.Sub(want) > time.Minute {
		t.Errorf("web last active = %v, want ≈ %v", got, want)
	}

	legacy := byID["sess-legacy"]
	if got := legacy.GetDeviceType(); got != pb.DeviceType_DEVICE_TYPE_UNSPECIFIED {
		t.Errorf("legacy device type = %v, want UNSPECIFIED", got)
	}
	// Never re-validated → score - TTL lands exactly on login (creation).
	if got, want := legacy.GetLastActiveAt().AsTime(), loginAt; got.Sub(want) < -time.Minute || got.Sub(want) > time.Minute {
		t.Errorf("legacy last active = %v, want ≈ %v", got, want)
	}
}
