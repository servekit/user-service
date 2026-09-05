package service_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	gidservice "github.com/servekit/gid-service/pkg"
	gidconfig "github.com/servekit/gid-service/pkg/config"

	"github.com/servekit/go-common/captcha"
	"github.com/servekit/go-common/dbx"
	"github.com/servekit/go-common/logging"
	"github.com/servekit/go-common/redisx"

	messageservice "github.com/servekit/message-service/pkg"
	messageconfig "github.com/servekit/message-service/pkg/config"
	messageoption "github.com/servekit/message-service/pkg/option"

	pb "github.com/servekit/api/gen/go/user/v1"
	"github.com/servekit/user-service/internal/service"
	"github.com/servekit/user-service/internal/store/dal"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/config"
	"github.com/servekit/user-service/pkg/option"

	"gorm.io/gorm"
)

// TestService_Login_UnknownUser verifies that the facade Service dispatches
// to the auth subpackage and returns an error for unknown users.
func TestService_Login_UnknownUser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	db := dbx.SetupTestDB(t, dbx.DriverPostgres)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	rdb := redisx.NewTestClient(t)
	cap, err := captcha.New(&captcha.Config{
		MaxAttempts: 3,
		Purposes:    map[string]*captcha.PurposeConfig{"test": {}},
	}, captcha.WithRedisClient(rdb))
	if err != nil {
		t.Fatalf("captcha.New: %v", err)
	}

	gidHdl := testGIDHandler(t)
	msgHdl := testMessageHandler(t, db, rdb, gidHdl)
	svc, _, err := service.New(testConfig(), option.WithDB(db), option.WithRedis(rdb), option.WithGIDHandler(gidHdl), option.WithMessageHandler(msgHdl), option.WithCaptcha(cap))
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}

	resp, err := svc.Login(context.Background(), &pb.LoginRequest{
		Method:   pb.LoginMethod_LOGIN_METHOD_EMAIL_PASSWORD,
		Email:    "nobody@example.com",
		Password: "anything",
	})
	if err == nil {
		t.Fatalf("Login: expected error, got resp=%+v", resp)
	}
}

// testConfig returns a minimal valid Config for in-process module tests.
// DB/Redis/GID are injected via options, so the values here don't matter.
func testConfig() *config.Config {
	return &config.Config{
		Server: &config.ServerConfig{
			GRPC: ":0",
		},
		Database: &dbx.Config{Postgres: &dbx.PostgresConfig{Host: "unused"}},
		Redis:    &redisx.Config{Addr: "unused"},
		Session:  &config.SessionConfig{TTL: time.Hour, KeyPrefix: "test:", UserSessionsPrefix: "test:u:"},
		RBAC:     &config.RBACConfig{},
		OAuth: &config.OAuthConfig{
			GitHub: &config.OAuthGitHubConfig{RedirectURL: "https://auth.example.com/oauth/callback"},
			Google: &config.OAuthGoogleConfig{RedirectURL: "https://auth.example.com/oauth/callback"},
			WeChat: &config.OAuthWeChatConfig{RedirectURL: "https://auth.example.com/oauth/callback"},
			Apple: &config.OAuthAppleConfig{
				RedirectURL: "https://auth.example.com/oauth/callback",
				PrivateKey:  testAppleKeyPEM,
			},
		},
		ThirdParty: &config.ThirdPartyConfig{
			GID:     &config.RemoteServiceConfig[*gidconfig.Config]{Mode: "module"},
			Message: &config.RemoteServiceConfig[*messageconfig.Config]{Mode: "module"},
		},
		RateLimit: &config.RateLimitConfig{},
		Log:       &logging.Config{Level: "error"},
	}
}

// --- test helpers ---

// testAppleKeyPEM is a valid in-memory P-256 PKCS#8 key used to satisfy
// apple.New during service construction in tests. Generated once per package.
var testAppleKeyPEM = func() string {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}()

// newTestService builds a Service wired to a fresh test DB + Redis, migrated.
// Returns the Service and the underlying *gorm.DB so callers can inspect join
// rows directly when validating cascade-cleanup behavior.
func newTestService(t *testing.T) (*service.Service, *gorm.DB) {
	t.Helper()
	db := dbx.SetupTestDB(t, dbx.DriverPostgres)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	rdb := redisx.NewTestClient(t)
	cap, err := captcha.New(&captcha.Config{MaxAttempts: 3, Purposes: map[string]*captcha.PurposeConfig{"test": {}}}, captcha.WithRedisClient(rdb))
	if err != nil {
		t.Fatalf("captcha.New: %v", err)
	}
	gidHdl := testGIDHandler(t)
	msgHdl := testMessageHandler(t, db, rdb, gidHdl)
	svc, _, err := service.New(testConfig(), option.WithDB(db), option.WithRedis(rdb), option.WithGIDHandler(gidHdl), option.WithMessageHandler(msgHdl), option.WithCaptcha(cap))
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	return svc, db
}

// testGIDHandler builds a real in-process gid-service Handler for tests. The
// raw Handler is what option.WithGIDHandler injects; user-service wraps it
// internally. Snowflake only needs MachineID + StartTime.
func testGIDHandler(t *testing.T) *gidservice.Handler {
	t.Helper()
	hdl, err := gidservice.NewModule(&gidconfig.Config{
		Snowflake: &gidconfig.SnowflakeConfig{
			MachineID: 1,
			StartTime: time.Now().Add(-time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("gid handler: %v", err)
	}
	return hdl
}

// testMessageHandler builds a real in-process message-service Handler for tests.
// In module mode the Handler must be injected by the caller
// (option.WithMessageHandler); this builds one sharing the test DB/Redis and gid.
func testMessageHandler(t *testing.T, db *gorm.DB, rdb *redis.Client, gidHdl *gidservice.Handler) *messageservice.Handler {
	t.Helper()
	hdl, err := messageservice.NewModule(
		&messageconfig.Config{
			Email: &messageconfig.EmailConfig{IdempotencyTTL: "5m"},
			SMS:   &messageconfig.SMSConfig{IdempotencyTTL: "5m"},
		},
		messageoption.WithDB(db),
		messageoption.WithRedis(rdb),
		messageoption.WithGIDHandler(gidHdl),
	)
	if err != nil {
		t.Fatalf("message handler: %v", err)
	}
	return hdl
}

// TestService_Permission_CRUD exercises the full-stack Permission CRUD chain:
// service.Service facade → rbac.Service → dal. Covers create/get/duplicate
// conflict/update/not-found/list/delete and the builtin guard via dal.
func TestService_Permission_CRUD(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	svc, _ := newTestService(t)

	created, err := svc.CreatePermission(context.Background(), &pb.CreatePermissionRequest{Resource: "document", Action: "read", Description: "read docs"})
	if err != nil {
		t.Fatalf("CreatePermission: %v", err)
	}
	if created.GetId() == 0 || created.GetIsBuiltin() {
		t.Fatalf("unexpected created permission: %+v", created)
	}

	got, err := svc.GetPermission(context.Background(), &pb.GetPermissionRequest{PermissionId: created.GetId()})
	if err != nil {
		t.Fatalf("GetPermission: %v", err)
	}
	if got.GetResource() != "document" || got.GetAction() != "read" {
		t.Fatalf("GetPermission mismatch: %+v", got)
	}

	if _, err := svc.CreatePermission(context.Background(), &pb.CreatePermissionRequest{Resource: "document", Action: "read"}); err == nil {
		t.Fatal("expected ErrPermissionExists, got nil")
	}

	upd, err := svc.UpdatePermission(context.Background(), &pb.UpdatePermissionRequest{PermissionId: created.GetId(), Description: "read all docs"})
	if err != nil {
		t.Fatalf("UpdatePermission: %v", err)
	}
	if upd.GetDescription() != "read all docs" {
		t.Fatalf("update not applied: %+v", upd)
	}

	if _, err := svc.GetPermission(context.Background(), &pb.GetPermissionRequest{PermissionId: 999999}); err == nil {
		t.Fatal("expected ErrPermissionNotFound, got nil")
	}

	list, err := svc.ListPermissions(context.Background(), &pb.ListPermissionsRequest{PageSize: 10})
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	if len(list.GetPermissions()) == 0 {
		t.Fatal("ListPermissions returned empty")
	}

	if _, err := svc.DeletePermission(context.Background(), &pb.DeletePermissionRequest{PermissionId: created.GetId()}); err != nil {
		t.Fatalf("DeletePermission: %v", err)
	}
	if _, err := svc.GetPermission(context.Background(), &pb.GetPermissionRequest{PermissionId: created.GetId()}); err == nil {
		t.Fatal("expected ErrPermissionNotFound after delete, got nil")
	}
}

// TestService_PermissionGroup_CRUD exercises the full-stack PermissionGroup CRUD
// chain: handler → service.Service facade → rbac.Service → dal. Covers
// create/get-with-items/duplicate-name-conflict/update-full-replace-to-empty/delete.
func TestService_PermissionGroup_CRUD(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	svc, _ := newTestService(t)

	perm, err := svc.CreatePermission(context.Background(), &pb.CreatePermissionRequest{Resource: "doc", Action: "read"})
	if err != nil {
		t.Fatalf("CreatePermission: %v", err)
	}

	pg, err := svc.CreatePermissionGroup(context.Background(), &pb.CreatePermissionGroupRequest{Name: "docs-readonly", PermissionIds: []int64{perm.GetId()}})
	if err != nil {
		t.Fatalf("CreatePermissionGroup: %v", err)
	}

	got, err := svc.GetPermissionGroup(context.Background(), &pb.GetPermissionGroupRequest{PermissionGroupId: pg.GetId()})
	if err != nil {
		t.Fatalf("GetPermissionGroup: %v", err)
	}
	if len(got.GetPermissions()) != 1 || got.GetPermissions()[0].GetId() != perm.GetId() {
		t.Fatalf("group permissions not populated: %+v", got.GetPermissions())
	}

	if _, err := svc.CreatePermissionGroup(context.Background(), &pb.CreatePermissionGroupRequest{Name: "docs-readonly"}); err == nil {
		t.Fatal("expected ErrPermissionGroupExists, got nil")
	}

	upd, err := svc.UpdatePermissionGroup(context.Background(), &pb.UpdatePermissionGroupRequest{PermissionGroupId: pg.GetId(), Name: "docs-empty", PermissionIds: nil})
	if err != nil {
		t.Fatalf("UpdatePermissionGroup: %v", err)
	}
	if upd.GetName() != "docs-empty" {
		t.Fatalf("name not updated: %+v", upd)
	}
	got2, _ := svc.GetPermissionGroup(context.Background(), &pb.GetPermissionGroupRequest{PermissionGroupId: pg.GetId()})
	if len(got2.GetPermissions()) != 0 {
		t.Fatalf("expected items fully replaced to empty, got %d", len(got2.GetPermissions()))
	}

	if _, err := svc.DeletePermissionGroup(context.Background(), &pb.DeletePermissionGroupRequest{PermissionGroupId: pg.GetId()}); err != nil {
		t.Fatalf("DeletePermissionGroup: %v", err)
	}
}

// TestService_PermissionGroup_UpdateWithOverlap verifies UpdatePermissionGroup's
// full-replace works when the new permission set overlaps the old one (regression
// for the soft-delete + unique-index conflict on join tables).
func TestService_PermissionGroup_UpdateWithOverlap(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	svc, _ := newTestService(t)

	p1, err := svc.CreatePermission(context.Background(), &pb.CreatePermissionRequest{Resource: "doc", Action: "read"})
	if err != nil {
		t.Fatalf("p1: %v", err)
	}
	p2, err := svc.CreatePermission(context.Background(), &pb.CreatePermissionRequest{Resource: "doc", Action: "write"})
	if err != nil {
		t.Fatalf("p2: %v", err)
	}
	p3, err := svc.CreatePermission(context.Background(), &pb.CreatePermissionRequest{Resource: "doc", Action: "delete"})
	if err != nil {
		t.Fatalf("p3: %v", err)
	}

	// Group starts with [p1, p2].
	pg, err := svc.CreatePermissionGroup(context.Background(), &pb.CreatePermissionGroupRequest{Name: "overlap", PermissionIds: []int64{p1.GetId(), p2.GetId()}})
	if err != nil {
		t.Fatalf("CreatePermissionGroup: %v", err)
	}

	// Replace with [p2, p3] — p2 is retained, p1 dropped, p3 added. Must not conflict.
	if _, err := svc.UpdatePermissionGroup(context.Background(), &pb.UpdatePermissionGroupRequest{PermissionGroupId: pg.GetId(), PermissionIds: []int64{p2.GetId(), p3.GetId()}}); err != nil {
		t.Fatalf("UpdatePermissionGroup with overlap: %v", err)
	}

	got, err := svc.GetPermissionGroup(context.Background(), &pb.GetPermissionGroupRequest{PermissionGroupId: pg.GetId()})
	if err != nil {
		t.Fatalf("GetPermissionGroup: %v", err)
	}
	gotIDs := map[int64]bool{}
	for _, p := range got.GetPermissions() {
		gotIDs[p.GetId()] = true
	}
	if !gotIDs[p2.GetId()] || !gotIDs[p3.GetId()] || gotIDs[p1.GetId()] {
		t.Fatalf("expected {p2,p3}, got %v", gotIDs)
	}
}

// TestService_GetRole_WithPermissions verifies GetRole returns a role with its
// permissions populated, and returns ErrRoleNotFound for missing IDs.
func TestService_GetRole_WithPermissions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	svc, _ := newTestService(t)

	perm, err := svc.CreatePermission(context.Background(), &pb.CreatePermissionRequest{Resource: "doc", Action: "read"})
	if err != nil {
		t.Fatalf("CreatePermission: %v", err)
	}
	role, err := svc.CreateRole(context.Background(), &pb.CreateRoleRequest{Name: "reader", PermissionIds: []int64{perm.GetId()}})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	got, err := svc.GetRole(context.Background(), &pb.GetRoleRequest{RoleId: role.GetId()})
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if got.GetName() != "reader" {
		t.Fatalf("name mismatch: %s", got.GetName())
	}
	if len(got.GetPermissions()) != 1 || got.GetPermissions()[0].GetId() != perm.GetId() {
		t.Fatalf("GetRole did not populate permissions: %+v", got.GetPermissions())
	}

	if _, err := svc.GetRole(context.Background(), &pb.GetRoleRequest{RoleId: 999999}); err == nil {
		t.Fatal("expected ErrRoleNotFound, got nil")
	}
}

// TestService_Permission_UpdateConflict verifies UpdatePermission returns
// ErrPermissionExists (409) when renaming to a resource:action another permission owns.
func TestService_Permission_UpdateConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	svc, _ := newTestService(t)

	if _, err := svc.CreatePermission(context.Background(), &pb.CreatePermissionRequest{Resource: "doc", Action: "read"}); err != nil {
		t.Fatalf("create1: %v", err)
	}
	p2, err := svc.CreatePermission(context.Background(), &pb.CreatePermissionRequest{Resource: "doc", Action: "write"})
	if err != nil {
		t.Fatalf("create2: %v", err)
	}
	// Rename p2 to "doc:read" which p1 already owns → should be ErrPermissionExists, not 500.
	if _, err := svc.UpdatePermission(context.Background(), &pb.UpdatePermissionRequest{PermissionId: p2.GetId(), Action: "read"}); err == nil {
		t.Fatal("expected ErrPermissionExists, got nil")
	}
}

// TestService_PermissionGroup_UpdateConflict verifies UpdatePermissionGroup returns
// ErrPermissionGroupExists when renaming to a name another group owns.
func TestService_PermissionGroup_UpdateConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	svc, _ := newTestService(t)

	if _, err := svc.CreatePermissionGroup(context.Background(), &pb.CreatePermissionGroupRequest{Name: "g1"}); err != nil {
		t.Fatalf("create1: %v", err)
	}
	pg2, err := svc.CreatePermissionGroup(context.Background(), &pb.CreatePermissionGroupRequest{Name: "g2"})
	if err != nil {
		t.Fatalf("create2: %v", err)
	}
	if _, err := svc.UpdatePermissionGroup(context.Background(), &pb.UpdatePermissionGroupRequest{PermissionGroupId: pg2.GetId(), Name: "g1"}); err == nil {
		t.Fatal("expected ErrPermissionGroupExists, got nil")
	}
}

// TestService_UpdateRole_WithOverlap verifies UpdateRole's full-replace works
// when the new permission set overlaps the old (regression for the soft-delete +
// unique-index conflict on RemoveRolePermissionMapping).
func TestService_UpdateRole_WithOverlap(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	svc, _ := newTestService(t)
	ctx := context.Background()

	p1, err := svc.CreatePermission(ctx, &pb.CreatePermissionRequest{Resource: "doc", Action: "read"})
	if err != nil {
		t.Fatalf("p1: %v", err)
	}
	p2, err := svc.CreatePermission(ctx, &pb.CreatePermissionRequest{Resource: "doc", Action: "write"})
	if err != nil {
		t.Fatalf("p2: %v", err)
	}
	p3, err := svc.CreatePermission(ctx, &pb.CreatePermissionRequest{Resource: "doc", Action: "delete"})
	if err != nil {
		t.Fatalf("p3: %v", err)
	}

	role, err := svc.CreateRole(ctx, &pb.CreateRoleRequest{Name: "editor", PermissionIds: []int64{p1.GetId(), p2.GetId()}})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	// Replace [p1,p2] with [p2,p3] — p2 retained. Must not hit unique conflict.
	if _, err := svc.UpdateRole(ctx, &pb.UpdateRoleRequest{RoleId: role.GetId(), PermissionIds: []int64{p2.GetId(), p3.GetId()}}); err != nil {
		t.Fatalf("UpdateRole with overlap: %v", err)
	}

	got, err := svc.GetRole(ctx, &pb.GetRoleRequest{RoleId: role.GetId()})
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	ids := map[int64]bool{}
	for _, p := range got.GetPermissions() {
		ids[p.GetId()] = true
	}
	if !ids[p2.GetId()] || !ids[p3.GetId()] || ids[p1.GetId()] {
		t.Fatalf("expected {p2,p3}, got %v", ids)
	}
}

// TestService_DeletePermission_ClearsMappings verifies that deleting a permission
// also removes the role_permission_mapping and permission_group_item_mapping rows
// referencing it (no orphan rows left behind).
func TestService_DeletePermission_ClearsMappings(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	svc, db := newTestService(t)
	ctx := context.Background()

	perm, err := svc.CreatePermission(ctx, &pb.CreatePermissionRequest{Resource: "doc", Action: "read"})
	if err != nil {
		t.Fatalf("CreatePermission: %v", err)
	}
	if _, err := svc.CreateRole(ctx, &pb.CreateRoleRequest{Name: "r", PermissionIds: []int64{perm.GetId()}}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if _, err := svc.CreatePermissionGroup(ctx, &pb.CreatePermissionGroupRequest{Name: "g", PermissionIds: []int64{perm.GetId()}}); err != nil {
		t.Fatalf("CreatePermissionGroup: %v", err)
	}

	if rps, err := dal.ListRolePermissionMappingsByPermissionID(ctx, db, perm.GetId()); err != nil || len(rps) != 1 {
		t.Fatalf("pre-delete: expected 1 role-permission mapping, got %d (err %v)", len(rps), err)
	}

	if _, err := svc.DeletePermission(ctx, &pb.DeletePermissionRequest{PermissionId: perm.GetId()}); err != nil {
		t.Fatalf("DeletePermission: %v", err)
	}

	if rps, err := dal.ListRolePermissionMappingsByPermissionID(ctx, db, perm.GetId()); err != nil || len(rps) != 0 {
		t.Errorf("post-delete: expected 0 role-permission mappings, got %d (err %v)", len(rps), err)
	}
	if gids, err := dal.ListPermissionGroupIDsByItemPermissionID(ctx, db, perm.GetId()); err != nil || len(gids) != 0 {
		t.Errorf("post-delete: expected 0 group-item mappings, got %d (err %v)", len(gids), err)
	}
}

// TestService_DeletePermissionGroup_ClearsMappings verifies that deleting a
// permission group also removes its item rows and role references.
func TestService_DeletePermissionGroup_ClearsMappings(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	svc, db := newTestService(t)
	ctx := context.Background()

	pg, err := svc.CreatePermissionGroup(ctx, &pb.CreatePermissionGroupRequest{Name: "g"})
	if err != nil {
		t.Fatalf("CreatePermissionGroup: %v", err)
	}
	role, err := svc.CreateRole(ctx, &pb.CreateRoleRequest{Name: "r", PermissionGroupIds: []int64{pg.GetId()}})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	_ = role

	if rpgs, err := dal.ListRolePermissionGroupMappingsByPermissionGroupID(ctx, db, pg.GetId()); err != nil || len(rpgs) != 1 {
		t.Fatalf("pre-delete: expected 1 role-group mapping, got %d (err %v)", len(rpgs), err)
	}

	if _, err := svc.DeletePermissionGroup(ctx, &pb.DeletePermissionGroupRequest{PermissionGroupId: pg.GetId()}); err != nil {
		t.Fatalf("DeletePermissionGroup: %v", err)
	}

	if rpgs, err := dal.ListRolePermissionGroupMappingsByPermissionGroupID(ctx, db, pg.GetId()); err != nil || len(rpgs) != 0 {
		t.Errorf("post-delete: expected 0 role-group mappings, got %d (err %v)", len(rpgs), err)
	}
}
