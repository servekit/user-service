package dal_test

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/servekit/user-service/internal/store/dal"
	"github.com/servekit/user-service/internal/store/models"
)

func newProfileTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.UserRegisterProfile{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestCreateRegisterProfile_HashAutoFill(t *testing.T) {
	db := newProfileTestDB(t)
	ctx := context.Background()

	p := &models.UserRegisterProfile{
		UserID:    42,
		IP:        "203.0.113.7",
		UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) Version/17.0 Mobile Safari/604.1",
		Device:    "iPhone",
	}
	if err := dal.CreateRegisterProfile(ctx, db, p); err != nil {
		t.Fatalf("CreateRegisterProfile: %v", err)
	}
	if p.UserAgentHash == "" {
		t.Fatal("UserAgentHash not auto-filled for non-empty UA")
	}

	got, err := dal.GetRegisterProfileByUserID(ctx, db, 42)
	if err != nil {
		t.Fatalf("GetRegisterProfileByUserID: %v", err)
	}
	if got == nil || got.IP != "203.0.113.7" || got.Device != "iPhone" || got.UserAgentHash != p.UserAgentHash {
		t.Fatalf("roundtrip = %+v, want env fields preserved", got)
	}
}

func TestCreateRegisterProfile_EmptyUAStaysEmptyHash(t *testing.T) {
	db := newProfileTestDB(t)
	ctx := context.Background()

	if err := dal.CreateRegisterProfile(ctx, db, &models.UserRegisterProfile{UserID: 7}); err != nil {
		t.Fatalf("CreateRegisterProfile: %v", err)
	}
	got, err := dal.GetRegisterProfileByUserID(ctx, db, 7)
	if err != nil {
		t.Fatalf("GetRegisterProfileByUserID: %v", err)
	}
	if got == nil || got.UserAgentHash != "" {
		t.Fatalf("empty-UA profile hash = %q, want empty", got.UserAgentHash)
	}
}

func TestGetRegisterProfileByUserID_AbsentReturnsNilNil(t *testing.T) {
	db := newProfileTestDB(t)
	got, err := dal.GetRegisterProfileByUserID(context.Background(), db, 999)
	if err != nil || got != nil {
		t.Fatalf("absent profile = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestHashUserAgent(t *testing.T) {
	if dal.HashUserAgent("") != "" {
		t.Fatal("empty UA must hash to empty string")
	}
	// Stable md5 hex, 32 chars.
	h := dal.HashUserAgent("curl/8.4.0")
	if len(h) != 32 {
		t.Fatalf("hash len = %d, want 32", len(h))
	}
	if dal.HashUserAgent("curl/8.4.0") != h {
		t.Fatal("hash must be deterministic")
	}
}

func TestBackfillRegisterProfiles(t *testing.T) {
	db := newBackfillTestDB(t)
	ctx := context.Background()

	// User 1: register log exists (two rows — earliest wins) -> env backfilled.
	// User 2: no register log -> empty-env row.
	// User 3: already has a profile row -> untouched on rerun.
	older := time.Now().Add(-48 * time.Hour)
	uid1, uid2, uid3 := int64(1), int64(2), int64(3)
	seed := []models.UserAuthLog{
		{UserID: &uid1, Action: 2, Success: true, IP: "198.51.100.1",
			UserAgent: "curl/8.4.0", CreatedAt: older, UpdatedAt: older},
		{UserID: &uid1, Action: 4, Success: true, IP: "198.51.100.2",
			UserAgent: "Mozilla/5.0", CreatedAt: older.Add(time.Hour), UpdatedAt: older.Add(time.Hour)},
		{UserID: &uid1, Action: 1, Success: true, IP: "203.0.113.9", // login, ignored
			UserAgent: "Mozilla/5.0", CreatedAt: older.Add(-time.Hour), UpdatedAt: older.Add(-time.Hour)},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed auth log: %v", err)
		}
	}
	users := []models.UserUser{{ID: uid1, Nickname: "a"}, {ID: uid2, Nickname: "b"}, {ID: uid3, Nickname: "c"}}
	for i := range users {
		if err := db.Create(&users[i]).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	if err := dal.CreateRegisterProfile(ctx, db, &models.UserRegisterProfile{UserID: uid3, IP: "keep-me"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	created, err := dal.BackfillRegisterProfiles(ctx, db)
	if err != nil {
		t.Fatalf("BackfillRegisterProfiles: %v", err)
	}
	if created != 2 {
		t.Fatalf("created = %d, want 2 (users 1 and 2)", created)
	}

	p1, _ := dal.GetRegisterProfileByUserID(ctx, db, uid1)
	if p1 == nil || p1.IP != "198.51.100.1" || p1.UserAgent != "curl/8.4.0" || p1.UserAgentHash == "" {
		t.Fatalf("user1 profile = %+v, want earliest register log env", p1)
	}
	p2, _ := dal.GetRegisterProfileByUserID(ctx, db, uid2)
	if p2 == nil || p2.IP != "" || p2.UserAgent != "" {
		t.Fatalf("user2 profile = %+v, want empty-env row", p2)
	}
	p3, _ := dal.GetRegisterProfileByUserID(ctx, db, uid3)
	if p3 == nil || p3.IP != "keep-me" {
		t.Fatalf("user3 profile = %+v, want untouched", p3)
	}

	// Idempotent rerun.
	created, err = dal.BackfillRegisterProfiles(ctx, db)
	if err != nil || created != 0 {
		t.Fatalf("rerun = (%d, %v), want (0, nil)", created, err)
	}
}

func TestListUsers_RegisterIPFilterHitsProfileTable(t *testing.T) {
	db := newBackfillTestDB(t)
	ctx := context.Background()

	mk := func(id int64, ip string) {
		t.Helper()
		if err := db.Create(&models.UserUser{ID: id, Nickname: "u"}).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
		if err := dal.CreateRegisterProfile(ctx, db, &models.UserRegisterProfile{UserID: id, IP: ip}); err != nil {
			t.Fatalf("seed profile: %v", err)
		}
	}
	mk(1, "198.51.100.1")
	mk(2, "198.51.100.2")

	users, err := dal.ListUsers(ctx, db, dal.UserFilter{UserFilterCore: dal.UserFilterCore{RegisterIP: "198.51.100.1"}})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0].ID != 1 {
		t.Fatalf("register_ip filter returned %v, want only user 1", users)
	}
}

func newBackfillTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.UserUser{}, &models.UserAuthLog{}, &models.UserRegisterProfile{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}
