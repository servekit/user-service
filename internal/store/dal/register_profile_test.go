package dal_test

import (
	"context"
	"testing"

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
