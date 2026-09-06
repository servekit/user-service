package service_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	pb "github.com/servekit/api/gen/go/user/v1"
	"github.com/servekit/user-service/internal/service"
	"github.com/servekit/user-service/internal/store/dal"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/clientinfo"
	"github.com/servekit/user-service/pkg/option"

	"github.com/servekit/go-common/captcha"
	"github.com/servekit/go-common/dbx"
	"github.com/servekit/go-common/redisx"

	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"
)

// envCtx attaches client-info metadata exactly like the HTTP edge does.
func envCtx(ctx context.Context) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs(
		clientinfo.XClientIP, "203.0.113.7",
		clientinfo.XClientUA, "Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) Version/17.0 Mobile Safari/604.1",
		clientinfo.XClientDevice, "iPhone",
	))
}

// newEnvTestService builds the facade on a fresh Postgres + Redis. The captcha
// covers the register purpose (the numeric key Register verifies against);
// CodeFormat must be non-nil or go-common's generator dereferences a nil
// format on Generate (same reason resolveCaptcha backfills FormatDigit6).
func newEnvTestService(t *testing.T) (*service.Service, *captcha.Captcha) {
	t.Helper()
	db := dbx.SetupTestDB(t, dbx.DriverPostgres)
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	rdb := redisx.NewTestClient(t)
	cap, err := captcha.New(&captcha.Config{
		MaxAttempts: 3,
		Purposes: map[string]*captcha.PurposeConfig{
			strconv.Itoa(int(pb.VerificationPurpose_VERIFICATION_PURPOSE_REGISTER)): {CodeFormat: captcha.FormatDigit6},
		},
	}, captcha.WithRedisClient(rdb))
	if err != nil {
		t.Fatalf("captcha.New: %v", err)
	}
	gidHdl := testGIDHandler(t)
	msgHdl := testMessageHandler(t, db, rdb, gidHdl)
	svc, _, err := service.New(testConfig(),
		option.WithDB(db), option.WithRedis(rdb),
		option.WithGIDHandler(gidHdl), option.WithMessageHandler(msgHdl),
		option.WithCaptcha(cap))
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	return svc, cap
}

// seedRegisterCaptcha pre-generates the code Register will verify. Purpose
// and channel are the enum ints as strings — mirrors auth.go purposeKey/
// channelKey (auth.go:799-807). Check Register's own Verify call
// (auth.go:91-94) if this ever drifts and adjust both enums to match.
func seedRegisterCaptcha(t *testing.T, ctx context.Context, cap *captcha.Captcha, email string) (captchaID, code string) {
	t.Helper()
	purpose := strconv.Itoa(int(pb.VerificationPurpose_VERIFICATION_PURPOSE_REGISTER))
	channel := strconv.Itoa(int(pb.VerificationChannel_VERIFICATION_CHANNEL_EMAIL))
	id, code, err := cap.Generate(ctx, email, purpose, channel)
	if err != nil {
		t.Fatalf("captcha.Generate: %v", err)
	}
	return id, code
}

// svcDB returns the datastore behind the facade for direct row assertions.
func svcDB(t *testing.T, svc *service.Service) *gorm.DB {
	t.Helper()
	return svc.DB()
}

func TestService_Register_CapturesRegisterEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	ctx := envCtx(context.Background())
	svc, cap := newEnvTestService(t)

	const email = "env-test@example.com"
	captchaID, code := seedRegisterCaptcha(t, ctx, cap, email)

	resp, err := svc.Register(ctx, &pb.RegisterRequest{
		Provider:  pb.IdentityProvider_IDENTITY_PROVIDER_EMAIL,
		Email:     email,
		Code:      code,
		CaptchaId: captchaID,
		Password:  "S3cure-pass!",
		Nickname:  "envtest",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	prof, perr := dal.GetRegisterProfileByUserID(ctx, svcDB(t, svc), resp.GetUser().GetId())
	if perr != nil {
		t.Fatalf("GetRegisterProfileByUserID: %v", perr)
	}
	if prof == nil {
		t.Fatal("register did not create a profile row")
	}
	if prof.IP != "203.0.113.7" || prof.Device != "iPhone" {
		t.Fatalf("profile env = %q/%q, want 203.0.113.7/iPhone", prof.IP, prof.Device)
	}
	if !strings.Contains(prof.UserAgent, "iPhone") || prof.UserAgentHash == "" {
		t.Fatalf("profile UA/hash = %q/%q", prof.UserAgent, prof.UserAgentHash)
	}
}

func TestService_CreateUser_EmptyRegisterEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	ctx := context.Background()
	svc, _ := newEnvTestService(t)

	resp, err := svc.CreateUser(ctx, &pb.CreateUserRequest{
		UserType: pb.UserType_USER_TYPE_NORMAL,
		Email:    "admin-made@example.com",
		Password: "S3cure-pass!",
		Nickname: "adminmade",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	prof, perr := dal.GetRegisterProfileByUserID(ctx, svcDB(t, svc), resp.GetUser().GetId())
	if perr != nil {
		t.Fatalf("GetRegisterProfileByUserID: %v", perr)
	}
	// Admin-created users get the invariant-preserving row with an empty
	// environment — the admin's own environment is NOT the user's.
	if prof == nil || prof.IP != "" || prof.UserAgent != "" || prof.UserAgentHash != "" {
		t.Fatalf("admin-create profile = %+v, want empty-env row", prof)
	}
}

func TestService_GetUser_ExposesRegisterEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode (requires Docker)")
	}
	ctx := envCtx(context.Background())
	svc, cap := newEnvTestService(t)

	const email = "env-detail@example.com"
	captchaID, code := seedRegisterCaptcha(t, ctx, cap, email)
	regResp, err := svc.Register(ctx, &pb.RegisterRequest{
		Provider: pb.IdentityProvider_IDENTITY_PROVIDER_EMAIL, Email: email,
		Code: code, CaptchaId: captchaID, Password: "S3cure-pass!", Nickname: "envdetail",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := svc.GetUser(ctx, &pb.GetUserRequest{UserId: regResp.GetUser().GetId()})
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.GetRegisterIp() != "203.0.113.7" || got.GetRegisterAgent() == "" {
		t.Fatalf("GetUser env = %q/%q, want captured register environment", got.GetRegisterIp(), got.GetRegisterAgent())
	}
	if got.GetRegisterDevice() != pb.DeviceType_DEVICE_TYPE_IOS {
		t.Fatalf("GetUser register_device = %v, want derived IOS from iPhone UA", got.GetRegisterDevice())
	}

	// GetProfile (self-view) must NOT leak the environment.
	prof, err := svc.GetProfile(ctx, &pb.GetProfileRequest{UserId: regResp.GetUser().GetId()})
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if prof.GetRegisterIp() != "" || prof.GetRegisterAgent() != "" || prof.GetRegisterDevice() != pb.DeviceType_DEVICE_TYPE_UNSPECIFIED {
		t.Fatalf("GetProfile leaked register env: %q/%q/%v", prof.GetRegisterIp(), prof.GetRegisterAgent(), prof.GetRegisterDevice())
	}
}
