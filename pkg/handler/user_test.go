package handler_test

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
	"github.com/servekit/user-service/internal/store/models"
	userservice "github.com/servekit/user-service/pkg"
	"github.com/servekit/user-service/pkg/config"
	"github.com/servekit/user-service/pkg/option"
	"github.com/servekit/user-service/pkg/thirdcall"
)

// TestHandler_Login_NoUsers verifies that Handler is constructed via
// NewModule and dispatches to the underlying service. Login fails because
// no users exist in the freshly-migrated DB.
func TestHandler_Login_NoUsers(t *testing.T) {
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

	hdl, err := userservice.NewModule(testConfig(),
		option.WithDB(db),
		option.WithRedis(rdb),
		option.WithGIDService(stubGID()),
		option.WithMessageService(stubMessage()),
		option.WithCaptcha(cap),
	)
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}

	resp, err := hdl.Login(context.Background(), &pb.LoginRequest{
		Method:   pb.LoginMethod_LOGIN_METHOD_EMAIL_PASSWORD,
		Email:    "nobody@example.com",
		Password: "anything",
	})
	if err == nil {
		t.Fatalf("Login: expected error for unknown user, got resp=%+v", resp)
	}
	if resp != nil {
		t.Errorf("Login: expected nil resp on error, got %+v", resp)
	}
}

// testConfig returns a minimal valid Config for in-process module tests.
// DB/Redis/GID are injected via options, so the values here don't matter —
// they just need to satisfy configx.Load / validation.
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
			GitHub: &config.OAuthGitHubConfig{},
			Google: &config.OAuthGoogleConfig{},
			WeChat: &config.OAuthWeChatConfig{},
			Apple:  &config.OAuthAppleConfig{},
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
// flows which don't actually send messages.
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
