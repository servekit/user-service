package service_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"sync/atomic"
	"testing"
	"time"

	"github.com/servekit/go-common/captcha"
	"github.com/servekit/go-common/dbx"
	"github.com/servekit/go-common/logging"
	"github.com/servekit/go-common/redisx"

	messagepb "github.com/servekit/message-service/gen/message/v1"

	pb "github.com/servekit/user-service/gen/user/v1"
	"github.com/servekit/user-service/internal/service"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/config"
	"github.com/servekit/user-service/pkg/option"
	"github.com/servekit/user-service/pkg/thirdcall"
)

// TestService_Login_UnknownUser verifies that the facade Service dispatches
// to the auth subpackage and returns an error for unknown users.
func TestService_Login_UnknownUser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	db := dbx.SetupTestDB(t)
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

	svc, _, err := service.New(testConfig(), option.WithDB(db), option.WithRedis(rdb), option.WithGIDService(stubGID()), option.WithCaptcha(cap), option.WithMessageService(stubMessage()))
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
			HTTP: ":0",
		},
		Database: &dbx.Config{Host: "unused"},
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
		ThirdParty: &config.ThirdPartyConfig{},
		RateLimit:  &config.RateLimitConfig{},
		Log:        &logging.Config{Level: "error"},
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
func newTestService(t *testing.T) *service.Service {
	t.Helper()
	db := dbx.SetupTestDB(t)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	rdb := redisx.NewTestClient(t)
	cap, err := captcha.New(&captcha.Config{MaxAttempts: 3, Purposes: map[string]*captcha.PurposeConfig{"test": {}}}, captcha.WithRedisClient(rdb))
	if err != nil {
		t.Fatalf("captcha.New: %v", err)
	}
	svc, _, err := service.New(testConfig(), option.WithDB(db), option.WithRedis(rdb), option.WithGIDService(stubGID()), option.WithCaptcha(cap), option.WithMessageService(stubMessage()))
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	return svc
}

// stubGIDService is a thirdcall.GIDService that issues sequential IDs starting at 1000.
type stubGIDService struct{ next int64 }

func (s *stubGIDService) NextID(_ context.Context) (int64, error) {
	return atomic.AddInt64(&s.next, 1) + 999, nil
}

func stubGID() thirdcall.GIDService { return &stubGIDService{} }

var _ thirdcall.GIDService = (*stubGIDService)(nil)

// stubMessageService is a no-op thirdcall.MessageService for tests that exercise
// flows which don't actually send messages (e.g. Login with no users).
type stubMessageService struct{}

func (stubMessageService) SendEmail(_ context.Context, _ *messagepb.SendEmailRequest) (*messagepb.SendResponse, error) {
	return &messagepb.SendResponse{}, nil
}
func (stubMessageService) SendSMS(_ context.Context, _ *messagepb.SendSMSRequest) (*messagepb.SendResponse, error) {
	return &messagepb.SendResponse{}, nil
}
func (stubMessageService) Close() error { return nil }

func stubMessage() thirdcall.MessageService { return stubMessageService{} }

var _ thirdcall.MessageService = stubMessageService{}

// TestService_Permission_CRUD exercises the full-stack Permission CRUD chain:
// service.Service facade → rbac.Service → dal. Covers create/get/duplicate
// conflict/update/not-found/list/delete and the builtin guard via dal.
func TestService_Permission_CRUD(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	svc := newTestService(t)

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
