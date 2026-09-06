package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	pb "github.com/servekit/api/gen/go/user/v1"
	"gorm.io/gorm"

	"github.com/servekit/user-service/internal/service/session"
	"github.com/servekit/user-service/internal/store/models"
)

// newTestDB builds an in-memory sqlite DB with the session tombstone table.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.UserSession{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestListSessions_MergedView pins the merged session list:
//   - ACTIVE rows from Redis first (device type from stored OS/UA,
//     last-active derived from the ZSET expiry score);
//   - then REVOKED / EXPIRED tombstones from PG, newest first, live rows
//     skipped, historical last-active left unset.
func TestListSessions_MergedView(t *testing.T) {
	m, _ := newTestManager(t)
	db := newTestDB(t)
	svc := session.New(db, m)
	ctx := context.Background()

	loginAt := time.Now().Add(-time.Hour)
	if err := m.Create(ctx, "sess-web", &session.Data{
		UserID: 7, LoginAt: loginAt, LoginIP: "203.0.113.7",
		OS: "macOS 10.15.7", Browser: "Chrome 120.0.0.0",
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/120.0.0.0",
	}); err != nil {
		t.Fatalf("Create web: %v", err)
	}

	revoked := time.Now().Add(-30 * time.Minute)
	seed := []models.UserSession{
		{ID: "pg-revoked", UserID: 7, IP: "198.51.100.1", OS: "iOS 17.2", Browser: "Safari 17.0",
			DeviceType: 2, CreatedAt: loginAt.Add(-2 * time.Hour), RevokedAt: &revoked},
		{ID: "pg-lapsed", UserID: 7, IP: "198.51.100.2", OS: "Android 14", Browser: "Chrome 119",
			DeviceType: 3, CreatedAt: loginAt.Add(-3 * time.Hour)},
		{ID: "sess-web", UserID: 7, CreatedAt: loginAt}, // live row also in PG — must be skipped
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].ID, err)
		}
	}

	resp, err := svc.ListSessions(ctx, &pb.ListSessionsRequest{UserId: 7})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.GetSessions()) != 3 {
		t.Fatalf("sessions = %d, want 3 (1 active + 2 history)", len(resp.GetSessions()))
	}

	first := resp.GetSessions()[0]
	if first.GetId() != "sess-web" || first.GetStatus() != pb.SessionStatus_SESSION_STATUS_ACTIVE {
		t.Errorf("first row = %s/%v, want sess-web/ACTIVE", first.GetId(), first.GetStatus())
	}
	if got := first.GetDeviceType(); got != pb.DeviceType_DEVICE_TYPE_WEB {
		t.Errorf("active device type = %v, want WEB", got)
	}

	second := resp.GetSessions()[1]
	if second.GetId() != "pg-revoked" || second.GetStatus() != pb.SessionStatus_SESSION_STATUS_REVOKED {
		t.Errorf("second row = %s/%v, want pg-revoked/REVOKED (newest history first)", second.GetId(), second.GetStatus())
	}
	if second.GetLastActiveAt() != nil {
		t.Errorf("history last-active should be unset, got %v", second.GetLastActiveAt())
	}

	third := resp.GetSessions()[2]
	if third.GetId() != "pg-lapsed" || third.GetStatus() != pb.SessionStatus_SESSION_STATUS_EXPIRED {
		t.Errorf("third row = %s/%v, want pg-lapsed/EXPIRED", third.GetId(), third.GetStatus())
	}
}

// TestListSessions_HistoryOnly covers a user with no live sessions: the
// view degrades to tombstones only (the ZSET empty path).
func TestListSessions_HistoryOnly(t *testing.T) {
	m, _ := newTestManager(t)
	db := newTestDB(t)
	svc := session.New(db, m)
	ctx := context.Background()

	revoked := time.Now().Add(-time.Minute)
	row := models.UserSession{ID: "pg-only", UserID: 9, IP: "203.0.113.9", CreatedAt: time.Now().Add(-time.Hour), RevokedAt: &revoked}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, err := svc.ListSessions(ctx, &pb.ListSessionsRequest{UserId: 9})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.GetSessions()) != 1 || resp.GetSessions()[0].GetStatus() != pb.SessionStatus_SESSION_STATUS_REVOKED {
		t.Fatalf("want 1 REVOKED row, got %+v", resp.GetSessions())
	}
}
