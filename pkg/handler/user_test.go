package handler_test

import (
	"context"
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

	pb "github.com/servekit/user-service/gen/user/v1"
	"github.com/servekit/user-service/internal/store/models"
	userservice "github.com/servekit/user-service/pkg"
	"github.com/servekit/user-service/pkg/config"
	"github.com/servekit/user-service/pkg/option"

	"gorm.io/gorm"
)

// TestHandler_Login_NoUsers verifies that Handler is constructed via
// NewModule and dispatches to the underlying service. Login fails because
// no users exist in the freshly-migrated DB.
func TestHandler_Login_NoUsers(t *testing.T) {
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
	hdl, err := userservice.NewModule(testConfig(),
		option.WithDB(db),
		option.WithRedis(rdb),
		option.WithGIDHandler(gidHdl),
		option.WithMessageHandler(msgHdl),
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
		Database: &dbx.Config{Postgres: &dbx.PostgresConfig{Host: "unused"}},
		Redis:    &redisx.Config{Addr: "unused"},
		Session:  &config.SessionConfig{TTL: time.Hour, KeyPrefix: "test:", UserSessionsPrefix: "test:u:"},
		RBAC:     &config.RBACConfig{},
		OAuth: &config.OAuthConfig{
			GitHub: &config.OAuthGitHubConfig{},
			Google: &config.OAuthGoogleConfig{},
			WeChat: &config.OAuthWeChatConfig{},
			Apple:  &config.OAuthAppleConfig{},
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
