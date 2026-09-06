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
//   - ACTIVE rows from Redis first (os/browser/device type derive from UA,
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
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/120.0.0.0",
	}); err != nil {
		t.Fatalf("Create web: %v", err)
	}

	revoked := time.Now().Add(-30 * time.Minute)
	seed := []models.UserSession{
		{ID: "pg-revoked", UserID: 7, IP: "198.51.100.1",
			UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) Version/17.0 Mobile Safari/604.1",
			CreatedAt: loginAt.Add(-2 * time.Hour), RevokedAt: &revoked,
			Method: int32(pb.LoginMethod_LOGIN_METHOD_PHONE_CODE), Target: "17000000000", Device: "iPhone"},
		{ID: "pg-lapsed", UserID: 7, IP: "198.51.100.2",
			UserAgent: "Mozilla/5.0 (Linux; Android 14; Pixel 8 Pro) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Mobile Safari/537.36",
			CreatedAt: loginAt.Add(-3 * time.Hour),
			Provider:  int32(pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB), Target: "octocat", Device: "Pixel 8"},
		{ID: "sess-web", UserID: 7, CreatedAt: loginAt}, // live row also in PG — must be skipped
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].ID, err)
		}
	}

	resp, err := svc.ListSessions(ctx, &pb.ListSessionsRequest{UserId: 7, PageSize: 20})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.GetSessions()) != 3 {
		t.Fatalf("sessions = %d, want 3 (1 active + 2 history)", len(resp.GetSessions()))
	}
	if resp.GetNextCursor() != "" {
		t.Errorf("next_cursor = %q, want empty (history exhausted)", resp.GetNextCursor())
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
	// Revoked rows carry revoked_at as their final activity time…
	if got := second.GetLastActiveAt(); got == nil || got.AsTime().Sub(revoked) > time.Minute {
		t.Errorf("revoked last-active = %v, want ≈ revoked_at %v", got, revoked)
	}

	third := resp.GetSessions()[2]
	if third.GetId() != "pg-lapsed" || third.GetStatus() != pb.SessionStatus_SESSION_STATUS_EXPIRED {
		t.Errorf("third row = %s/%v, want pg-lapsed/EXPIRED", third.GetId(), third.GetStatus())
	}
	// History rows carry how they authenticated — from the PG columns.
	if third.GetLoginProvider() != pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB || third.GetLoginTarget() != "octocat" || third.GetDevice() != "Pixel 8" {
		t.Errorf("history login fields = %v/%s/%s, want github/octocat/Pixel 8",
			third.GetLoginProvider(), third.GetLoginTarget(), third.GetDevice())
	}
	// …lapsed rows stay unset (no knowable moment).
	if third.GetLastActiveAt() != nil {
		t.Errorf("lapsed last-active should be unset, got %v", third.GetLastActiveAt())
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

// TestListSessions_HistoryPaging pins cursor paging over history: page one
// carries the live row + first history batch and a cursor; page two returns
// strictly older history only (no live rows repeated).
func TestListSessions_HistoryPaging(t *testing.T) {
	m, _ := newTestManager(t)
	db := newTestDB(t)
	svc := session.New(db, m)
	ctx := context.Background()

	if err := m.Create(ctx, "sess-live", &session.Data{UserID: 11, LoginAt: time.Now()}); err != nil {
		t.Fatalf("Create live: %v", err)
	}
	base := time.Now().Add(-time.Hour)
	seed := []models.UserSession{
		{ID: "h-new", UserID: 11, CreatedAt: base},
		{ID: "h-mid", UserID: 11, CreatedAt: base.Add(-time.Minute)},
		{ID: "h-old", UserID: 11, CreatedAt: base.Add(-2 * time.Minute)},
		{ID: "sess-live", UserID: 11, CreatedAt: base.Add(-3 * time.Minute)}, // live, must be skipped
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].ID, err)
		}
	}

	page1, err := svc.ListSessions(ctx, &pb.ListSessionsRequest{UserId: 11, PageSize: 2})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if got := len(page1.GetSessions()); got != 3 { // live + 2 history
		t.Fatalf("page1 = %d rows, want 3", got)
	}
	if page1.GetSessions()[0].GetId() != "sess-live" {
		t.Errorf("page1 first = %s, want live row", page1.GetSessions()[0].GetId())
	}
	if page1.GetNextCursor() == "" {
		t.Fatal("page1 next_cursor empty, want a page 2")
	}

	page2, err := svc.ListSessions(ctx, &pb.ListSessionsRequest{UserId: 11, PageSize: 2, Cursor: page1.GetNextCursor()})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if got := len(page2.GetSessions()); got != 1 { // h-old only, no live repeat
		t.Fatalf("page2 = %d rows, want 1", got)
	}
	if page2.GetSessions()[0].GetId() != "h-old" {
		t.Errorf("page2 row = %s, want h-old", page2.GetSessions()[0].GetId())
	}
	if page2.GetNextCursor() != "" {
		t.Errorf("page2 next_cursor = %q, want empty", page2.GetNextCursor())
	}
}

// TestListSessions_StatusFilter pins filter semantics: ACTIVE returns live
// rows only (no history, no cursor); REVOKED returns only explicit-logout
// tombstones (live rows never leak into a filtered history page).
func TestListSessions_StatusFilter(t *testing.T) {
	m, _ := newTestManager(t)
	db := newTestDB(t)
	svc := session.New(db, m)
	ctx := context.Background()

	if err := m.Create(ctx, "sess-live", &session.Data{UserID: 21, LoginAt: time.Now()}); err != nil {
		t.Fatalf("Create live: %v", err)
	}
	revoked := time.Now().Add(-time.Minute)
	seed := []models.UserSession{
		{ID: "h-revoked", UserID: 21, CreatedAt: time.Now().Add(-time.Hour), RevokedAt: &revoked},
		{ID: "h-lapsed", UserID: 21, CreatedAt: time.Now().Add(-2 * time.Hour)},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].ID, err)
		}
	}

	active, err := svc.ListSessions(ctx, &pb.ListSessionsRequest{UserId: 21, Status: pb.SessionStatus_SESSION_STATUS_ACTIVE})
	if err != nil {
		t.Fatalf("ACTIVE: %v", err)
	}
	if len(active.GetSessions()) != 1 || active.GetSessions()[0].GetId() != "sess-live" {
		t.Fatalf("ACTIVE = %+v, want live row only", active.GetSessions())
	}
	if active.GetNextCursor() != "" {
		t.Errorf("ACTIVE next_cursor = %q, want empty", active.GetNextCursor())
	}

	rev, err := svc.ListSessions(ctx, &pb.ListSessionsRequest{UserId: 21, Status: pb.SessionStatus_SESSION_STATUS_REVOKED})
	if err != nil {
		t.Fatalf("REVOKED: %v", err)
	}
	if len(rev.GetSessions()) != 1 || rev.GetSessions()[0].GetId() != "h-revoked" {
		t.Fatalf("REVOKED = %+v, want h-revoked only", rev.GetSessions())
	}

	exp, err := svc.ListSessions(ctx, &pb.ListSessionsRequest{UserId: 21, Status: pb.SessionStatus_SESSION_STATUS_EXPIRED})
	if err != nil {
		t.Fatalf("EXPIRED: %v", err)
	}
	if len(exp.GetSessions()) != 1 || exp.GetSessions()[0].GetId() != "h-lapsed" {
		t.Fatalf("EXPIRED = %+v, want h-lapsed only", exp.GetSessions())
	}
}

// TestRevokeAllSessions_OnlyLiveRowsStamped pins the audit semantics of
// "log out everywhere": only sessions still live in Redis become REVOKED.
// Tombstones that already lapsed (Redis evicted, RevokedAt NULL) must keep
// reading EXPIRED — the old user-scoped UPDATE flipped them to REVOKED too.
func TestRevokeAllSessions_OnlyLiveRowsStamped(t *testing.T) {
	m, _ := newTestManager(t)
	db := newTestDB(t)
	svc := session.New(db, m)
	ctx := context.Background()

	// Live session, present in both Redis and PG.
	if err := m.Create(ctx, "sess-live", &session.Data{UserID: 33, LoginAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatalf("Create live: %v", err)
	}
	revoked := time.Now().Add(-2 * time.Hour)
	seed := []models.UserSession{
		{ID: "sess-live", UserID: 33, CreatedAt: time.Now().Add(-time.Hour)},
		{ID: "h-lapsed", UserID: 33, CreatedAt: time.Now().Add(-3 * time.Hour)}, // TTL已过，Redis 无此会话
		{ID: "h-revoked", UserID: 33, CreatedAt: revoked.Add(-time.Hour), RevokedAt: &revoked},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].ID, err)
		}
	}

	if _, err := svc.RevokeAllSessions(ctx, &pb.RevokeAllSessionsRequest{UserId: 33}); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}

	resp, err := svc.ListSessions(ctx, &pb.ListSessionsRequest{UserId: 33, PageSize: 20})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	statuses := make(map[string]pb.SessionStatus, len(resp.GetSessions()))
	for _, s := range resp.GetSessions() {
		statuses[s.GetId()] = s.GetStatus()
	}
	if len(statuses) != 3 {
		t.Fatalf("sessions = %d, want 3 (all history after revoke-all)", len(statuses))
	}
	if got := statuses["sess-live"]; got != pb.SessionStatus_SESSION_STATUS_REVOKED {
		t.Errorf("sess-live status = %v, want REVOKED", got)
	}
	if got := statuses["h-lapsed"]; got != pb.SessionStatus_SESSION_STATUS_EXPIRED {
		t.Errorf("h-lapsed status = %v, want EXPIRED (already lapsed before revoke-all)", got)
	}
	if got := statuses["h-revoked"]; got != pb.SessionStatus_SESSION_STATUS_REVOKED {
		t.Errorf("h-revoked status = %v, want REVOKED (unchanged)", got)
	}
}

// TestRevokeAllSessions_SparesCurrentSession pins "log out other devices":
// the excluded session (the caller's current one) must survive intact —
// still ACTIVE in the list, its Redis key and PG row untouched — while every
// other live session is revoked.
func TestRevokeAllSessions_SparesCurrentSession(t *testing.T) {
	m, _ := newTestManager(t)
	db := newTestDB(t)
	svc := session.New(db, m)
	ctx := context.Background()

	for _, id := range []string{"sess-current", "sess-other"} {
		if err := m.Create(ctx, id, &session.Data{UserID: 44, LoginAt: time.Now().Add(-time.Hour)}); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	seed := []models.UserSession{
		{ID: "sess-current", UserID: 44, CreatedAt: time.Now().Add(-time.Hour)},
		{ID: "sess-other", UserID: 44, CreatedAt: time.Now().Add(-40 * time.Minute)},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].ID, err)
		}
	}

	if _, err := svc.RevokeAllSessions(ctx, &pb.RevokeAllSessionsRequest{UserId: 44, ExcludeSessionId: "sess-current"}); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}

	// The spared session stays live in Redis; the other one is gone.
	if _, err := m.Get(ctx, "sess-current"); err != nil {
		t.Errorf("spared session Get: %v, want alive", err)
	}
	if _, err := m.Get(ctx, "sess-other"); err == nil {
		t.Errorf("other session Get: want invalid, got alive")
	}

	resp, err := svc.ListSessions(ctx, &pb.ListSessionsRequest{UserId: 44, PageSize: 20})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	statuses := make(map[string]pb.SessionStatus, len(resp.GetSessions()))
	for _, s := range resp.GetSessions() {
		statuses[s.GetId()] = s.GetStatus()
	}
	if got := statuses["sess-current"]; got != pb.SessionStatus_SESSION_STATUS_ACTIVE {
		t.Errorf("sess-current status = %v, want ACTIVE (spared)", got)
	}
	if got := statuses["sess-other"]; got != pb.SessionStatus_SESSION_STATUS_REVOKED {
		t.Errorf("sess-other status = %v, want REVOKED", got)
	}
}
