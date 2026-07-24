package service_test

import (
	"context"
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
			Apple:  &config.OAuthAppleConfig{RedirectURL: "https://auth.example.com/oauth/callback"},
		},
		ThirdParty: &config.ThirdPartyConfig{},
		RateLimit:  &config.RateLimitConfig{},
		Log:        &logging.Config{Level: "error"},
	}
}

// --- test helpers ---

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
