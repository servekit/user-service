# user-service Skill Alignment Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor user-service to match the architecture defined in `.claude/skills/` (golang-service-development, gorm-cli-development, golang-development, proto-development, go-common-usage) in 5 incremental phases.

**Architecture:** Top-down refactor: introduce `pkg/handler` first (Phase 1), split `internal/service` into domain subpackages (Phase 2), rename `repository` → `dal` with functional API (Phase 3), rewrite models with explicit fields + service prefix (Phase 4), replace `ownDB`/`ownRedis` with `lifecycle.Manager` and add `internal/jobs/` skeleton (Phase 5). Each phase compiles, runs, and preserves all gRPC behavior.

**Tech Stack:** Go 1.26, gRPC + grpc-gateway, GORM + gorm-cli (typed API), buf v2 + protovalidate, PostgreSQL, Redis, `github.com/servekit/go-common` (configx, dbx, redisx, lifecycle, signalx, xerr/xcodes, cronx, captcha, ratelimit).

**Spec:** `docs/superpowers/specs/2026-06-21-skill-alignment-refactor-design.md`

**Branch strategy:** Direct commits on `main`. Each task ends with a commit (or a few small commits per task).

**Pre-flight check (do once before starting):**

- [ ] **Step 0.1: Verify current build status**

```bash
go build ./...
```

Note: there is a known pre-existing error: `cronx.go:13:2: missing go.sum entry for github.com/robfig/cron/v3 (imported by gid-service/internal/jobs)` triggered via `replace gid-service => ../gid-service`. This is **not in scope** for this refactor — Phase 5 will work around it by ensuring user-service's own `go.sum` is complete. If the build fails for OTHER reasons, fix before starting Phase 1.

- [ ] **Step 0.2: Snapshot current behavior**

Run the existing smoke test and record baseline:

```bash
go test -run TestSmoke ./cmd/server/... -v
```

Record output as "pre-refactor baseline". Phase 1 will replace this with real grpcurl smoke tests.

---

## Phase 1: pkg/handler 薄壳 + server/module 切换

**Goal:** Introduce `pkg/handler/` as a thin shell implementing `pb.UserServiceServer`. After Phase 1, `internal/service.UserService` still owns business logic, but gRPC registration flows through `*handler.Handler`.

### Task 1.1: Set up test infrastructure

**Files:**
- Create: `scripts/smoke.sh`
- Create: `internal/testutil/testutil.go`

- [ ] **Step 1: Create `internal/testutil/testutil.go`**

This package provides shared helpers for integration tests: testcontainer DB setup, miniredis, snowflake ID generator stub.

```go
// Package testutil provides shared helpers for user-service integration tests.
package testutil

import (
    "context"
    "testing"

    "github.com/servekit/go-common/dbx"
    "github.com/servekit/go-common/redisx"
    "github.com/servekit/go-common/captcha"
    "github.com/servekit/go-common/ratelimit"
    "gorm.io/gorm"

    "user-service/internal/store/models"
    "user-service/pkg/thirdcall"
)

// SetupDB returns a testcontainer PostgreSQL with all user-service tables migrated.
func SetupDB(t *testing.T) *gorm.DB {
    t.Helper()
    db := dbx.SetupTestDB(t)
    if err := dbx.AutoMigrate(db, models.AllModels()...); err != nil {
        t.Fatalf("migrate: %v", err)
    }
    return db
}

// SetupRedis returns a miniredis-backed client.
func SetupRedis(t *testing.T) *redis.Client {
    t.Helper()
    return redisx.NewTestClient(t)
}

// StubGID returns a thirdcall.GIDService that issues sequential IDs starting at 1000.
func StubGID() thirdcall.GIDService {
    return &stubGID{next: 1000}
}

type stubGID struct{ next int64 }

func (s *stubGID) NextID() (int64, error) {
    s.next++
    return s.next, nil
}

// SetupCaptcha returns a Captcha instance backed by the test Redis.
func SetupCaptcha(t *testing.T, rdb *redis.Client) *captcha.Captcha {
    t.Helper()
    cap, err := captcha.New(&captcha.Config{
        Prefix: "captcha",
        Redis:  &redisx.Config{Addr: "unused"}, // overridden by WithRedisClient
        Purposes: map[string]*captcha.PurposeConfig{
            "register": {CodeFormat: captcha.FormatDigit6},
            "login":    {CodeFormat: captcha.FormatDigit6},
        },
    }, captcha.WithRedisClient(rdb))
    if err != nil {
        t.Fatalf("init captcha: %v", err)
    }
    return cap
}
```

Note: actual signatures depend on the `go-common` Captcha/redisx API. Verify against `github.com/servekit/go-common/captcha` and `redisx` packages before running.

- [ ] **Step 2: Create `scripts/smoke.sh`**

```bash
#!/usr/bin/env bash
set -euo pipefail

# Starts user-service in background, exercises key RPCs via grpcurl, kills server.
# Requires: config.yaml in repo root, postgres + redis running on localhost.

ADDR="${USER_SERVICE_ADDR:-localhost:9000}"
BINARY="${USER_SERVICE_BIN:-./bin/server}"

echo "Building server..."
go build -o "$BINARY" ./cmd/server/

echo "Starting server..."
"$BINARY" &
SERVER_PID=$!
trap "kill $SERVER_PID 2>/dev/null || true" EXIT

# Wait for server
for i in $(seq 1 30); do
    if grpcurl -plaintext "$ADDR" list > /dev/null 2>&1; then break; fi
    sleep 0.5
done

echo "Server up. Running smoke tests..."

echo "→ grpcurl list services"
grpcurl -plaintext "$ADDR" list

echo "→ grpcurl UserService method list"
grpcurl -plaintext "$ADDR" list user.UserService

echo "Smoke tests passed."
```

- [ ] **Step 3: Make script executable**

```bash
chmod +x scripts/smoke.sh
```

- [ ] **Step 4: Commit**

```bash
git add scripts/smoke.sh internal/testutil/testutil.go
git commit -m "test: add integration test helpers and smoke script"
```

### Task 1.2: Write failing handler test

**Files:**
- Create: `pkg/handler/user_test.go`

- [ ] **Step 1: Write failing test**

```go
package handler_test

import (
    "context"
    "testing"

    pb "user-service/gen/user/v1"
    "user-service/internal/testutil"
    "user-service/pkg/handler"
    "user-service/pkg/option"
)

// TestHandler_Login_NoUsers verifies that Handler is constructed and dispatches
// to the underlying service. The actual login fails (no users), but the test
// confirms the dispatch path works end-to-end.
func TestHandler_Login_NoUsers(t *testing.T) {
    db := testutil.SetupDB(t)
    rdb := testutil.SetupRedis(t)
    gid := testutil.StubGID()
    cap := testutil.SetupCaptcha(t, rdb)

    hdl, err := userservice.NewModule(testConfig(),
        option.WithDB(db),
        option.WithRedis(rdb),
        option.WithGIDService(gid),
        option.WithCaptcha(cap),
    )
    if err != nil {
        t.Fatalf("NewModule: %v", err)
    }

    resp, err := hdl.Login(context.Background(), &pb.LoginRequest{
        Method:   pb.LoginMethod_LOGIN_METHOD_EMAIL,
        Target:   "nobody@example.com",
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
// Real values don't matter — DB/Redis/GID are injected via options.
func testConfig() *config.Config {
    return &config.Config{
        Server:     &config.ServerConfig{GRPC: &config.ListenConfig{Addr: ":0"}, Gateway: &config.ListenConfig{Addr: ":0"}},
        Database:   &dbx.Config{Host: "unused"},
        Redis:      &redisx.Config{Addr: "unused"},
        Session:    &config.SessionConfig{TTL: time.Hour, KeyPrefix: "test:", UserSessionsPrefix: "test:u:"},
        RBAC:       &config.RBACConfig{},
        OAuth:      &config.OAuthConfig{},
        ThirdParty: &config.ThirdPartyConfig{},
        RateLimit:  &config.RateLimitConfig{},
        Log:        &logging.Config{Level: "error"},
    }
}
```

Note: import paths and config struct literal fields need to match the actual `pkg/config` and `go-common` types. Verify against current `config.Config` definition before running.

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./pkg/handler/... -run TestHandler_Login -v
```

Expected: FAIL with "package user-service/pkg/handler is not in stdlib" / "no Go files" — handler package doesn't exist yet.

- [ ] **Step 3: Commit test**

```bash
git add pkg/handler/user_test.go
git commit -m "test: add failing handler integration test"
```

### Task 1.3: Create pkg/handler/user.go

**Files:**
- Create: `pkg/handler/user.go`

- [ ] **Step 1: Create handler with full RPC set**

The handler is a thin shell. Each RPC = one delegation line. Generate the full list by mirroring `internal/service/user_service.go` delegations, just changing the receiver to `*Handler`:

```go
// Package handler implements pb.UserServiceServer as a thin shim over
// internal/service. Each RPC method is a one-line delegation — service takes
// the proto request directly. Handler holds NO business logic.
//
// Handler also implements signalx.Service (Start/Stop). In Phase 1 these are
// no-ops; Phase 5 will delegate to the underlying *service.Service.
package handler

import (
    "context"

    "github.com/servekit/go-common/signalx"
    "google.golang.org/protobuf/types/known/emptypb"

    pb "user-service/gen/user/v1"
    "user-service/internal/service"
)

// Handler implements pb.UserServiceServer.
type Handler struct {
    pb.UnimplementedUserServiceServer

    svc *service.UserService
}

// New constructs a Handler wrapping svc.
func New(svc *service.UserService) *Handler { return &Handler{svc: svc} }

// Svc returns the underlying service. TEMPORARY: used by middleware in Phase 1
// to access SessionMgr; removed in Phase 2 once middleware is rewired.
func (h *Handler) Svc() *service.UserService { return h.svc }

// Compile-time assertions.
var (
    _ pb.UserServiceServer = (*Handler)(nil)
    _ signalx.Service      = (*Handler)(nil)
)

// Start is a no-op in Phase 1. Phase 5 wires it to h.svc.Start().
func (h *Handler) Start() error { return nil }

// Stop is a no-op in Phase 1. Phase 5 wires it to h.svc.Stop().
func (h *Handler) Stop() error { return nil }

// --- gRPC method delegations (30 RPCs) ---

func (h *Handler) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
    return h.svc.Register(ctx, req)
}

func (h *Handler) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
    return h.svc.Login(ctx, req)
}

func (h *Handler) Logout(ctx context.Context, req *pb.LogoutRequest) (*emptypb.Empty, error) {
    return h.svc.Logout(ctx, req)
}

func (h *Handler) RefreshSession(ctx context.Context, req *pb.RefreshSessionRequest) (*emptypb.Empty, error) {
    return h.svc.RefreshSession(ctx, req)
}

func (h *Handler) GetOAuthURL(ctx context.Context, req *pb.GetOAuthURLRequest) (*pb.GetOAuthURLResponse, error) {
    return h.svc.GetOAuthURL(ctx, req)
}

func (h *Handler) SocialLogin(ctx context.Context, req *pb.SocialLoginRequest) (*pb.LoginResponse, error) {
    return h.svc.SocialLogin(ctx, req)
}

func (h *Handler) MiniProgramLogin(ctx context.Context, req *pb.MiniProgramLoginRequest) (*pb.LoginResponse, error) {
    return h.svc.MiniProgramLogin(ctx, req)
}

func (h *Handler) MiniProgramPhoneLogin(ctx context.Context, req *pb.MiniProgramPhoneLoginRequest) (*pb.LoginResponse, error) {
    return h.svc.MiniProgramPhoneLogin(ctx, req)
}

func (h *Handler) GetProfile(ctx context.Context, req *emptypb.Empty) (*pb.User, error) {
    return h.svc.GetProfile(ctx, req)
}

func (h *Handler) UpdateProfile(ctx context.Context, req *pb.UpdateProfileRequest) (*pb.User, error) {
    return h.svc.UpdateProfile(ctx, req)
}

func (h *Handler) ChangePassword(ctx context.Context, req *pb.ChangePasswordRequest) (*emptypb.Empty, error) {
    return h.svc.ChangePassword(ctx, req)
}

func (h *Handler) ResetPassword(ctx context.Context, req *pb.ResetPasswordRequest) (*emptypb.Empty, error) {
    return h.svc.ResetPassword(ctx, req)
}

func (h *Handler) ListIdentities(ctx context.Context, req *emptypb.Empty) (*pb.ListIdentitiesResponse, error) {
    return h.svc.ListIdentities(ctx, req)
}

func (h *Handler) BindIdentity(ctx context.Context, req *pb.BindIdentityRequest) (*pb.Identity, error) {
    return h.svc.BindIdentity(ctx, req)
}

func (h *Handler) UnbindIdentity(ctx context.Context, req *pb.UnbindIdentityRequest) (*emptypb.Empty, error) {
    return h.svc.UnbindIdentity(ctx, req)
}

func (h *Handler) SendVerificationCode(ctx context.Context, req *pb.SendVerificationCodeRequest) (*emptypb.Empty, error) {
    return h.svc.SendVerificationCode(ctx, req)
}

func (h *Handler) ListSessions(ctx context.Context, req *emptypb.Empty) (*pb.ListSessionsResponse, error) {
    return h.svc.ListSessions(ctx, req)
}

func (h *Handler) RevokeSession(ctx context.Context, req *pb.RevokeSessionRequest) (*emptypb.Empty, error) {
    return h.svc.RevokeSession(ctx, req)
}

func (h *Handler) RevokeAllSessions(ctx context.Context, req *emptypb.Empty) (*emptypb.Empty, error) {
    return h.svc.RevokeAllSessions(ctx, req)
}

func (h *Handler) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.User, error) {
    return h.svc.GetUser(ctx, req)
}

func (h *Handler) ListUsers(ctx context.Context, req *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
    return h.svc.ListUsers(ctx, req)
}

func (h *Handler) DisableUser(ctx context.Context, req *pb.DisableUserRequest) (*pb.User, error) {
    return h.svc.DisableUser(ctx, req)
}

func (h *Handler) GetLoginLogs(ctx context.Context, req *pb.GetLoginLogsRequest) (*pb.GetLoginLogsResponse, error) {
    return h.svc.GetLoginLogs(ctx, req)
}

func (h *Handler) CreateGroup(ctx context.Context, req *pb.CreateGroupRequest) (*pb.Group, error) {
    return h.svc.CreateGroup(ctx, req)
}

func (h *Handler) GetGroup(ctx context.Context, req *pb.GetGroupRequest) (*pb.Group, error) {
    return h.svc.GetGroup(ctx, req)
}

func (h *Handler) UpdateGroup(ctx context.Context, req *pb.UpdateGroupRequest) (*pb.Group, error) {
    return h.svc.UpdateGroup(ctx, req)
}

func (h *Handler) ListGroups(ctx context.Context, req *pb.ListGroupsRequest) (*pb.ListGroupsResponse, error) {
    return h.svc.ListGroups(ctx, req)
}

func (h *Handler) DeleteGroup(ctx context.Context, req *pb.DeleteGroupRequest) (*emptypb.Empty, error) {
    return h.svc.DeleteGroup(ctx, req)
}

func (h *Handler) AddGroupMember(ctx context.Context, req *pb.AddGroupMemberRequest) (*emptypb.Empty, error) {
    return h.svc.AddGroupMember(ctx, req)
}

func (h *Handler) RemoveGroupMember(ctx context.Context, req *pb.RemoveGroupMemberRequest) (*emptypb.Empty, error) {
    return h.svc.RemoveGroupMember(ctx, req)
}

func (h *Handler) ListGroupMembers(ctx context.Context, req *pb.ListGroupMembersRequest) (*pb.ListGroupMembersResponse, error) {
    return h.svc.ListGroupMembers(ctx, req)
}

func (h *Handler) CreateRole(ctx context.Context, req *pb.CreateRoleRequest) (*pb.Role, error) {
    return h.svc.CreateRole(ctx, req)
}

func (h *Handler) UpdateRole(ctx context.Context, req *pb.UpdateRoleRequest) (*pb.Role, error) {
    return h.svc.UpdateRole(ctx, req)
}

func (h *Handler) DeleteRole(ctx context.Context, req *pb.DeleteRoleRequest) (*emptypb.Empty, error) {
    return h.svc.DeleteRole(ctx, req)
}

func (h *Handler) ListRoles(ctx context.Context, req *pb.ListRolesRequest) (*pb.ListRolesResponse, error) {
    return h.svc.ListRoles(ctx, req)
}

func (h *Handler) ListPermissions(ctx context.Context, req *emptypb.Empty) (*pb.ListPermissionsResponse, error) {
    return h.svc.ListPermissions(ctx, req)
}

func (h *Handler) ListPermissionGroups(ctx context.Context, req *emptypb.Empty) (*pb.ListPermissionGroupsResponse, error) {
    return h.svc.ListPermissionGroups(ctx, req)
}

func (h *Handler) AddGroupRole(ctx context.Context, req *pb.AddGroupRoleRequest) (*emptypb.Empty, error) {
    return h.svc.AddGroupRole(ctx, req)
}

func (h *Handler) RemoveGroupRole(ctx context.Context, req *pb.RemoveGroupRoleRequest) (*emptypb.Empty, error) {
    return h.svc.RemoveGroupRole(ctx, req)
}

func (h *Handler) ListGroupRoles(ctx context.Context, req *pb.ListGroupRolesRequest) (*pb.ListGroupRolesResponse, error) {
    return h.svc.ListGroupRoles(ctx, req)
}

func (h *Handler) AssignRole(ctx context.Context, req *pb.AssignRoleRequest) (*emptypb.Empty, error) {
    return h.svc.AssignRole(ctx, req)
}

func (h *Handler) RevokeRole(ctx context.Context, req *pb.RevokeRoleRequest) (*emptypb.Empty, error) {
    return h.svc.RevokeRole(ctx, req)
}

func (h *Handler) ListUserRoles(ctx context.Context, req *pb.ListUserRolesRequest) (*pb.ListUserRolesResponse, error) {
    return h.svc.ListUserRoles(ctx, req)
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./pkg/handler/...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/handler/user.go
git commit -m "feat: add pkg/handler thin shell implementing UserServiceServer"
```

### Task 1.4: Update pkg/server.go and pkg/module.go to use handler

**Files:**
- Modify: `pkg/server.go`
- Modify: `pkg/module.go`

- [ ] **Step 1: Update `pkg/server.go`**

Replace the `Server` struct, `NewServer`, and gRPC registration to use `*handler.Handler`:

```go
// Package userservice provides in-process and gRPC client access to user-service.
package userservice

import (
    "github.com/servekit/go-common/grpcx"

    pb "user-service/gen/user/v1"
    "user-service/internal/middleware"
    "user-service/internal/service"
    "user-service/pkg/config"
    "user-service/pkg/handler"
    "user-service/pkg/option"

    "buf.build/go/protovalidate"
    protovalidate_middleware "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/protovalidate"
    "google.golang.org/grpc"
)

// ServerOption configures a Server.
type ServerOption func(*serverOptions)

type serverOptions struct {
    serviceOpts []option.Option
}

// WithServiceOptions passes through options to the underlying service.New call.
func WithServiceOptions(opts ...option.Option) ServerOption {
    return func(o *serverOptions) { o.serviceOpts = append(o.serviceOpts, opts...) }
}

// Server wraps a gRPC server for user-service.
type Server struct {
    grpcSrv *grpcx.Server
    svc     *service.UserService
    hdl     *handler.Handler
}

// NewServer creates a Server with all dependencies.
func NewServer(cfg *config.Config, opts ...ServerOption) (*Server, error) {
    var o serverOptions
    for _, opt := range opts {
        opt(&o)
    }

    svc, err := service.New(cfg, o.serviceOpts...)
    if err != nil {
        return nil, err
    }
    hdl := handler.New(svc)

    validator, err := protovalidate.New()
    if err != nil {
        return nil, err
    }

    authInterceptor := middleware.AuthInterceptor(svc.SessionMgr())
    grpcSrv := grpcx.New(
        grpcx.ServerConfig{
            GRPCAddr:    cfg.Server.GRPC.Addr,
            GatewayAddr: cfg.Server.Gateway.Addr,
        },
        func(s *grpc.Server) { pb.RegisterUserServiceServer(s, hdl) },
        pb.RegisterUserServiceHandlerFromEndpoint,
        grpcx.ErrorInterceptor,
        protovalidate_middleware.UnaryServerInterceptor(validator),
        authInterceptor,
    )

    return &Server{grpcSrv: grpcSrv, svc: svc, hdl: hdl}, nil
}

// Run starts gRPC + HTTP gateway and blocks until shutdown signal.
func (s *Server) Run() { s.grpcSrv.Run() }

// Stop gracefully stops all transports.
// Phase 5 will add svc.Stop() here; for now grpcSrv.Stop() is sufficient
// because Phase 1 service has no Start/Stop.
func (s *Server) Stop() { s.grpcSrv.Stop() }
```

Note: keep `svc` field — Phase 5 needs it for `svc.Stop()`.

- [ ] **Step 2: Update `pkg/module.go`**

Change return type from `*service.UserService` to `*handler.Handler`:

```go
// Package userservice provides client, server, and in-process module access to user-service.
package userservice

import (
    "user-service/internal/service"
    "user-service/pkg/config"
    "user-service/pkg/handler"
    "user-service/pkg/option"
)

// NewModule creates a Handler from config. Returns *handler.Handler so callers
// interact directly with the gRPC service interface.
// Pass option.WithDB/WithRedis to inject external connections.
func NewModule(cfg *config.Config, opts ...option.Option) (*handler.Handler, error) {
    svc, err := service.New(cfg, opts...)
    if err != nil {
        return nil, err
    }
    return handler.New(svc), nil
}
```

- [ ] **Step 3: Build everything**

```bash
go build ./...
```

Expected: PASS. If `cmd/server/main_test.go` references `userservice.NewModule` returning `*service.UserService`, update it to use handler API (call `hdl.Login(...)` directly).

- [ ] **Step 4: Run handler test**

```bash
go test ./pkg/handler/... -run TestHandler_Login -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/server.go pkg/module.go
git commit -m "refactor: swap pkg/server and pkg/module to use pkg/handler"
```

### Task 1.5: Phase 1 acceptance — smoke test

**Files:**
- Modify (if needed): `scripts/smoke.sh`

- [ ] **Step 1: Run full build + vet + test**

```bash
go build ./...
go vet ./...
go test -short ./...
```

Expected: all PASS.

- [ ] **Step 2: Run grpcurl smoke test against running server**

```bash
# Start postgres + redis first (via docker-compose, etc.)
./scripts/smoke.sh
```

Expected: output shows `user.UserService` in service list and method list completes.

- [ ] **Step 3: Phase 1 commit (any final tweaks)**

If smoke test surfaced issues, fix them in a follow-up commit:

```bash
git add -A
git commit -m "fix: phase 1 smoke test adjustments"
```

---

## Phase 2: internal/service 拆子包 + service.go facade

**Goal:** Split the 5 flat files into domain subpackages and create `service.go` facade. After Phase 2, `pkg/handler` delegates to `*service.Service` (not `*UserService`), and `internal/service/user_service.go` plus the 5 flat files are deleted.

### Task 2.1: Write failing facade test

**Files:**
- Create: `internal/service/service_test.go`

- [ ] **Step 1: Write failing test**

```go
package service_test

import (
    "context"
    "testing"

    pb "user-service/gen/user/v1"
    "user-service/internal/service"
    "user-service/internal/testutil"
    "user-service/pkg/config"
    "user-service/pkg/option"
)

// TestService_Login_UnknownUser verifies that the facade Service dispatches
// to the auth subpackage and returns an error for unknown users.
func TestService_Login_UnknownUser(t *testing.T) {
    db := testutil.SetupDB(t)
    rdb := testutil.SetupRedis(t)
    gid := testutil.StubGID()
    cap := testutil.SetupCaptcha(t, rdb)

    svc, err := service.New(testConfig(), option.WithDB(db), option.WithRedis(rdb), option.WithGIDService(gid), option.WithCaptcha(cap))
    if err != nil {
        t.Fatalf("service.New: %v", err)
    }

    resp, err := svc.Login(context.Background(), &pb.LoginRequest{
        Method:   pb.LoginMethod_LOGIN_METHOD_EMAIL,
        Target:   "nobody@example.com",
        Password: "anything",
    })
    if err == nil {
        t.Fatalf("Login: expected error, got resp=%+v", resp)
    }
}
```

Note: `testConfig()` — reuse the helper from `pkg/handler/user_test.go` (move it to `internal/testutil` to avoid duplication).

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./internal/service/... -run TestService_Login -v
```

Expected: FAIL — no `service.New` returning `*Service` exists yet (only `*UserService`).

- [ ] **Step 3: Commit failing test**

```bash
git add internal/service/service_test.go
git commit -m "test: add failing facade Service integration test"
```

### Task 2.2: Create internal/service/auth/auth.go

**Files:**
- Create: `internal/service/auth/auth.go`

This task moves `internal/service/auth.go` (689 lines) into `internal/service/auth/auth.go` with structural changes. No logic changes — pure mechanical move + rename.

- [ ] **Step 1: Create the auth subpackage file**

Transformation rules applied to every line of `internal/service/auth.go`:

1. `package service` → `package auth`
2. `type AuthHandler struct {...}` → `type Service struct {...}`
3. `func NewAuthHandler(...)` → `func New(...)`
4. `func (h *AuthHandler) Register(...)` → `func (s *Service) Register(...)`
5. All `h.field` references → `s.field` (receiver name change)
6. Private helpers `func (h *AuthHandler) verifyCredentials(...)` → `func (s *Service) verifyCredentials(...)`
7. Package-level helpers (`providerToChannel`, `verificationPurposeString`, etc.) — keep package-level, change nothing
8. Imports unchanged (still uses `repository`, `models`, etc.)

Move the entire 689 lines into `internal/service/auth/auth.go` with these substitutions. Use sed or manual edit.

- [ ] **Step 2: Delete original**

```bash
git rm internal/service/auth.go
```

- [ ] **Step 3: Verify auth subpackage builds**

```bash
go build ./internal/service/auth/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/service/auth/auth.go
git commit -m "refactor: move auth.go to internal/service/auth/auth.go subpackage"
```

### Task 2.3: Create internal/service/user/user.go

**Files:**
- Create: `internal/service/user/user.go`

Same transformation pattern as Task 2.2, applied to `internal/service/user.go` (280 lines).

- [ ] **Step 1: Move user.go into user subpackage**

Apply transformation:
1. `package service` → `package user`
2. `type UserHandler struct {...}` → `type Service struct {...}`
3. `func NewUserHandler(...)` → `func New(...)`
4. `func (h *UserHandler) X(...)` → `func (s *Service) X(...)`
5. `h.field` → `s.field`

```bash
git rm internal/service/user.go
```

- [ ] **Step 2: Verify build**

```bash
go build ./internal/service/user/...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/service/user/user.go
git commit -m "refactor: move user.go to internal/service/user/user.go subpackage"
```

### Task 2.4: Create internal/service/session/session.go

**Files:**
- Create: `internal/service/session/session.go`

Apply transformation to `internal/service/session.go` (126 lines).

**⚠️ Package name collision:** `internal/service/session` (subpackage) and `internal/session` (technical component) both have package name `session`. They are in different import paths, so this works at the language level — but any file that imports both must alias one.

- [ ] **Step 1: Move session.go into subpackage**

Apply standard transformation (same as 2.2).

```bash
git rm internal/service/session.go
```

- [ ] **Step 2: Verify build**

```bash
go build ./internal/service/session/...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/service/session/session.go
git commit -m "refactor: move session.go to internal/service/session/session.go subpackage"
```

### Task 2.5: Create internal/service/social/social.go

**Files:**
- Create: `internal/service/social/social.go`

Apply transformation to `internal/service/social.go` (424 lines).

- [ ] **Step 1: Move social.go into subpackage**

Standard transformation.

```bash
git rm internal/service/social.go
```

- [ ] **Step 2: Verify build**

```bash
go build ./internal/service/social/...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/service/social/social.go
git commit -m "refactor: move social.go to internal/service/social/social.go subpackage"
```

### Task 2.6: Create internal/service/rbac/rbac.go

**Files:**
- Create: `internal/service/rbac/rbac.go`

Apply transformation to `internal/service/rbac.go` (624 lines).

- [ ] **Step 1: Move rbac.go into subpackage**

Standard transformation:
1. `package service` → `package rbac`
2. `type RBACHandler struct {...}` → `type Service struct {...}`
3. `func NewRBACHandler(...)` → `func New(...)`
4. `func (h *RBACHandler) X(...)` → `func (s *Service) X(...)`
5. Local types like `type UserGroupEntry struct{...}` — keep in subpackage (still package-level type, no change needed)

```bash
git rm internal/service/rbac.go
```

- [ ] **Step 2: Verify build**

```bash
go build ./internal/service/rbac/...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/service/rbac/rbac.go
git commit -m "refactor: move rbac.go to internal/service/rbac/rbac.go subpackage"
```

### Task 2.7: Create internal/service/service.go (facade)

**Files:**
- Create: `internal/service/service.go`

- [ ] **Step 1: Write the facade**

This replaces the responsibility of `user_service.go` (467 lines) but with the new subpackage structure. The bulk of the construction code (resolveDB, resolveRedis, etc., and repo initialization) is preserved from `user_service.go` — only the field types and constructor calls change.

```go
// Package service contains user-service business logic.
//
// Layering contract (see golang-service-development skill §2):
//   - This is the SERVICE ROOT. It holds the Service struct + New + Start/Stop
//     + resource resolve helpers + one-line facade methods (one per RPC).
//   - Business logic lives in SUBPACKAGES (internal/service/<domain>/). This
//     file does NOT contain RPC implementations — only delegations.
//   - handler calls service.X; service.X is a one-line facade that calls
//     s.<domain>.X in the subpackage.
//   - Service methods take proto types DIRECTLY and return proto types.
//
// Lifecycle:
//   - Phase 2 still uses ownDB/ownRedis bool pattern. Phase 5 swaps to
//     lifecycle.Manager.
package service

import (
    "context"
    "errors"
    "fmt"
    "time"

    "github.com/servekit/go-common/captcha"
    "github.com/servekit/go-common/dbx"
    "github.com/servekit/go-common/redisx"
    "github.com/servekit/go-common/ratelimit"
    "github.com/servekit/go-common/tencent/mini"
    "github.com/redis/go-redis/v9"
    "google.golang.org/protobuf/types/known/emptypb"
    "gorm.io/gorm"

    pb "user-service/gen/user/v1"
    "user-service/internal/cache"
    "user-service/internal/identity"
    "user-service/internal/service/auth"
    "user-service/internal/service/rbac"
    domainSession "user-service/internal/service/session"
    "user-service/internal/service/social"
    "user-service/internal/service/user"
    "user-service/internal/session"
    "user-service/internal/store/repository"  // Phase 3 changes to dal
    "user-service/pkg/config"
    "user-service/pkg/option"
    "user-service/pkg/thirdcall"
)

// Service holds user-service business state. Each domain field is a subpackage
// *Service instance constructed in New() from resolved resources.
type Service struct {
    cfg *config.Config

    // Owned resources (Phase 5 migrates to lifecycle.Manager)
    db       *gorm.DB
    rdb      *redis.Client
    ownDB    bool
    ownRedis bool

    gid       thirdcall.GIDService
    sessionMgr *session.Manager  // technical component

    // Domain subpackages
    auth   *auth.Service
    user   *user.Service
    social *social.Service
    sess   *domainSession.Service
    rbacSvc *rbac.Service
}

// New constructs a Service from config and functional options.
func New(cfg *config.Config, opts ...option.Option) (*Service, error) {
    o := option.Apply(opts...)

    var cleanup []func()

    db, ownDB, err := resolveDB(&o, cfg)
    if err != nil {
        return nil, err
    }
    if ownDB {
        cleanup = append(cleanup, func() {
            if sqlDB, e := db.DB(); e == nil && sqlDB != nil {
                _ = sqlDB.Close()
            }
        })
    }

    rdb, ownRedis, err := resolveRedis(&o, cfg)
    if err != nil {
        runCleanup(cleanup)
        return nil, err
    }
    if ownRedis {
        cleanup = append(cleanup, func() { _ = rdb.Close() })
    }

    gid, err := resolveGID(&o, cfg)
    if err != nil {
        runCleanup(cleanup)
        return nil, err
    }

    // Repositories (Phase 3: switch to dal functions, remove these)
    userRepo := repository.NewUserRepository(db)
    identityRepo := repository.NewIdentityRepository(db)
    sessionRepo := repository.NewSessionRepository(db)
    loginLogRepo := repository.NewLoginLogRepo(db)
    groupRepo := repository.NewGroupRepo(db)
    userGroupRepo := repository.NewUserGroupRepo(db)
    roleRepo := repository.NewRoleRepo(db)
    permissionRepo := repository.NewPermissionRepo(db)
    permissionGroupRepo := repository.NewPermissionGroupRepo(db)
    rolePermissionRepo := repository.NewRolePermissionRepo(db)
    rolePermGroupRepo := repository.NewRolePermissionGroupRepo(db)
    groupRoleRepo := repository.NewGroupRoleRepo(db)
    userRoleRepo := repository.NewUserRoleRepo(db)

    sessionMgr := session.NewManager(rdb, cfg.Session)

    wechatMgr := mini.NewManager(&mini.Config{
        Credentials: map[string]string{
            cfg.OAuth.WeChat.AppID: cfg.OAuth.WeChat.AppSecret,
        },
    })

    socialProviders := map[pb.IdentityProvider]identity.SocialProvider{
        pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB: identity.NewGitHubProvider(
            cfg.OAuth.GitHub.ClientID, cfg.OAuth.GitHub.ClientSecret, cfg.OAuth.GitHub.RedirectURL,
        ),
        pb.IdentityProvider_IDENTITY_PROVIDER_GOOGLE: identity.NewGoogleProvider(
            cfg.OAuth.Google.ClientID, cfg.OAuth.Google.ClientSecret, cfg.OAuth.Google.RedirectURL,
        ),
        pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT: identity.NewWeChatProvider(
            cfg.OAuth.WeChat.AppID, cfg.OAuth.WeChat.AppSecret, cfg.OAuth.WeChat.RedirectURL,
        ),
        pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT_MINIPROGRAM: identity.NewWeChatMiniProgramProvider(
            cfg.OAuth.WeChat.AppID, wechatMgr,
        ),
        pb.IdentityProvider_IDENTITY_PROVIDER_APPLE: identity.NewAppleProvider(
            cfg.OAuth.Apple.ClientID, cfg.OAuth.Apple.TeamID, cfg.OAuth.Apple.KeyID, cfg.OAuth.Apple.RedirectURL,
        ),
    }

    rbacCache := cache.NewRBACCache(rdb, cfg.RBAC)

    loginRateLimit := resolveLoginRateLimit(cfg)
    loginLimiter := ratelimit.NewRedisLimiter(rdb, loginRateLimit)

    return &Service{
        cfg:        cfg,
        db:         db,
        rdb:        rdb,
        ownDB:      ownDB,
        ownRedis:   ownRedis,
        gid:        gid,
        sessionMgr: sessionMgr,
        auth:       auth.New(userRepo, identityRepo, sessionRepo, loginLogRepo, sessionMgr, o.Captcha, loginLimiter, gid),
        user:       user.New(userRepo, identityRepo, loginLogRepo),
        social:     social.New(userRepo, identityRepo, sessionRepo, loginLogRepo, sessionMgr, socialProviders, gid),
        sess:       domainSession.New(sessionMgr, sessionRepo),
        rbacSvc:    rbac.New(groupRepo, userGroupRepo, roleRepo, permissionRepo, permissionGroupRepo,
            rolePermissionRepo, rolePermGroupRepo, groupRoleRepo, userRoleRepo, rbacCache, gid),
    }, nil
}

// SessionMgr exposes the session manager for middleware (e.g., auth interceptor).
func (s *Service) SessionMgr() *session.Manager { return s.sessionMgr }

// Close cleans up resources owned by this instance.
// Phase 5 will replace this with Stop() via lifecycle.Manager.
func (s *Service) Close() {
    if s.ownRedis && s.rdb != nil {
        _ = s.rdb.Close()
    }
    if s.ownDB && s.db != nil {
        if sqlDB, err := s.db.DB(); err == nil && sqlDB != nil {
            _ = sqlDB.Close()
        }
    }
}

// --- internal helpers (preserved from user_service.go) ---

func resolveDB(o *option.Options, cfg *config.Config) (db *gorm.DB, own bool, err error) {
    if o.DB != nil {
        return o.DB, false, nil
    }
    db, err = dbx.New(cfg.Database)
    if err != nil {
        return nil, false, fmt.Errorf("database: %w", err)
    }
    return db, true, nil
}

func resolveRedis(o *option.Options, cfg *config.Config) (rdb *redis.Client, own bool, err error) {
    if o.RDB != nil {
        return o.RDB, false, nil
    }
    rdb, err = redisx.New(cfg.Redis)
    if err != nil {
        return nil, false, fmt.Errorf("redis: %w", err)
    }
    return rdb, true, nil
}

func resolveGID(o *option.Options, cfg *config.Config) (thirdcall.GIDService, error) {
    if o.GIDService != nil {
        return o.GIDService, nil
    }
    if cfg.ThirdParty == nil || cfg.ThirdParty.GID == nil {
        return nil, fmt.Errorf("third_party.gid: not configured")
    }
    svc, err := thirdcall.NewGIDService(cfg.ThirdParty.GID)
    if err != nil {
        return nil, fmt.Errorf("init gid-service: %w", err)
    }
    return svc, nil
}

func runCleanup(fns []func()) {
    for i := len(fns) - 1; i >= 0; i-- {
        fns[i]()
    }
}

func resolveLoginRateLimit(cfg *config.Config) ratelimit.Config {
    if cfg.RateLimit != nil && cfg.RateLimit.Login != nil {
        return *cfg.RateLimit.Login
    }
    return ratelimit.Config{
        Prefix: "login:rate",
        Global: []ratelimit.Rule{
            {Window: 5 * time.Minute, Max: 20},
        },
        Rules: map[string][]ratelimit.Rule{
            "fail": {
                {Window: 5 * time.Minute, Max: 5},
                {Window: time.Hour, Max: 15},
            },
        },
    }
}

// --- gRPC facade delegations (30 RPCs) ---

func (s *Service) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
    return s.auth.Register(ctx, req)
}

func (s *Service) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
    return s.auth.Login(ctx, req)
}

func (s *Service) Logout(ctx context.Context, req *pb.LogoutRequest) (*emptypb.Empty, error) {
    return s.auth.Logout(ctx, req)
}

func (s *Service) RefreshSession(ctx context.Context, req *pb.RefreshSessionRequest) (*emptypb.Empty, error) {
    return s.sess.RefreshSession(ctx, req)
}

func (s *Service) GetOAuthURL(ctx context.Context, req *pb.GetOAuthURLRequest) (*pb.GetOAuthURLResponse, error) {
    return s.social.GetOAuthURL(ctx, req)
}

func (s *Service) SocialLogin(ctx context.Context, req *pb.SocialLoginRequest) (*pb.LoginResponse, error) {
    return s.social.SocialLogin(ctx, req)
}

func (s *Service) MiniProgramLogin(ctx context.Context, req *pb.MiniProgramLoginRequest) (*pb.LoginResponse, error) {
    return s.social.MiniProgramLogin(ctx, req)
}

func (s *Service) MiniProgramPhoneLogin(ctx context.Context, req *pb.MiniProgramPhoneLoginRequest) (*pb.LoginResponse, error) {
    return s.social.MiniProgramPhoneLogin(ctx, req)
}

func (s *Service) GetProfile(ctx context.Context, req *emptypb.Empty) (*pb.User, error) {
    return s.user.GetProfile(ctx, req)
}

func (s *Service) UpdateProfile(ctx context.Context, req *pb.UpdateProfileRequest) (*pb.User, error) {
    return s.user.UpdateProfile(ctx, req)
}

func (s *Service) ChangePassword(ctx context.Context, req *pb.ChangePasswordRequest) (*emptypb.Empty, error) {
    return s.user.ChangePassword(ctx, req)
}

func (s *Service) ResetPassword(ctx context.Context, req *pb.ResetPasswordRequest) (*emptypb.Empty, error) {
    return s.user.ResetPassword(ctx, req)
}

func (s *Service) ListIdentities(ctx context.Context, req *emptypb.Empty) (*pb.ListIdentitiesResponse, error) {
    return s.user.ListIdentities(ctx, req)
}

func (s *Service) BindIdentity(ctx context.Context, req *pb.BindIdentityRequest) (*pb.Identity, error) {
    return s.user.BindIdentity(ctx, req)
}

func (s *Service) UnbindIdentity(ctx context.Context, req *pb.UnbindIdentityRequest) (*emptypb.Empty, error) {
    return s.user.UnbindIdentity(ctx, req)
}

func (s *Service) SendVerificationCode(ctx context.Context, req *pb.SendVerificationCodeRequest) (*emptypb.Empty, error) {
    return s.auth.SendVerificationCode(ctx, req)
}

func (s *Service) ListSessions(ctx context.Context, req *emptypb.Empty) (*pb.ListSessionsResponse, error) {
    return s.sess.ListSessions(ctx, req)
}

func (s *Service) RevokeSession(ctx context.Context, req *pb.RevokeSessionRequest) (*emptypb.Empty, error) {
    return s.sess.RevokeSession(ctx, req)
}

func (s *Service) RevokeAllSessions(ctx context.Context, req *emptypb.Empty) (*emptypb.Empty, error) {
    return s.sess.RevokeAllSessions(ctx, req)
}

func (s *Service) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.User, error) {
    return s.user.GetUser(ctx, req)
}

func (s *Service) ListUsers(ctx context.Context, req *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
    return s.user.ListUsers(ctx, req)
}

func (s *Service) DisableUser(ctx context.Context, req *pb.DisableUserRequest) (*pb.User, error) {
    return s.user.DisableUser(ctx, req)
}

func (s *Service) GetLoginLogs(ctx context.Context, req *pb.GetLoginLogsRequest) (*pb.GetLoginLogsResponse, error) {
    return s.user.GetLoginLogs(ctx, req)
}

func (s *Service) CreateGroup(ctx context.Context, req *pb.CreateGroupRequest) (*pb.Group, error) {
    return s.rbacSvc.CreateGroup(ctx, req)
}

func (s *Service) GetGroup(ctx context.Context, req *pb.GetGroupRequest) (*pb.Group, error) {
    return s.rbacSvc.GetGroup(ctx, req)
}

func (s *Service) UpdateGroup(ctx context.Context, req *pb.UpdateGroupRequest) (*pb.Group, error) {
    return s.rbacSvc.UpdateGroup(ctx, req)
}

func (s *Service) ListGroups(ctx context.Context, req *pb.ListGroupsRequest) (*pb.ListGroupsResponse, error) {
    return s.rbacSvc.ListGroups(ctx, req)
}

func (s *Service) DeleteGroup(ctx context.Context, req *pb.DeleteGroupRequest) (*emptypb.Empty, error) {
    return s.rbacSvc.DeleteGroup(ctx, req)
}

func (s *Service) AddGroupMember(ctx context.Context, req *pb.AddGroupMemberRequest) (*emptypb.Empty, error) {
    return s.rbacSvc.AddGroupMember(ctx, req)
}

func (s *Service) RemoveGroupMember(ctx context.Context, req *pb.RemoveGroupMemberRequest) (*emptypb.Empty, error) {
    return s.rbacSvc.RemoveGroupMember(ctx, req)
}

func (s *Service) ListGroupMembers(ctx context.Context, req *pb.ListGroupMembersRequest) (*pb.ListGroupMembersResponse, error) {
    return s.rbacSvc.ListGroupMembers(ctx, req)
}

func (s *Service) CreateRole(ctx context.Context, req *pb.CreateRoleRequest) (*pb.Role, error) {
    return s.rbacSvc.CreateRole(ctx, req)
}

func (s *Service) UpdateRole(ctx context.Context, req *pb.UpdateRoleRequest) (*pb.Role, error) {
    return s.rbacSvc.UpdateRole(ctx, req)
}

func (s *Service) DeleteRole(ctx context.Context, req *pb.DeleteRoleRequest) (*emptypb.Empty, error) {
    return s.rbacSvc.DeleteRole(ctx, req)
}

func (s *Service) ListRoles(ctx context.Context, req *pb.ListRolesRequest) (*pb.ListRolesResponse, error) {
    return s.rbacSvc.ListRoles(ctx, req)
}

func (s *Service) ListPermissions(ctx context.Context, req *emptypb.Empty) (*pb.ListPermissionsResponse, error) {
    return s.rbacSvc.ListPermissions(ctx, req)
}

func (s *Service) ListPermissionGroups(ctx context.Context, req *emptypb.Empty) (*pb.ListPermissionGroupsResponse, error) {
    return s.rbacSvc.ListPermissionGroups(ctx, req)
}

func (s *Service) AddGroupRole(ctx context.Context, req *pb.AddGroupRoleRequest) (*emptypb.Empty, error) {
    return s.rbacSvc.AddGroupRole(ctx, req)
}

func (s *Service) RemoveGroupRole(ctx context.Context, req *pb.RemoveGroupRoleRequest) (*emptypb.Empty, error) {
    return s.rbacSvc.RemoveGroupRole(ctx, req)
}

func (s *Service) ListGroupRoles(ctx context.Context, req *pb.ListGroupRolesRequest) (*pb.ListGroupRolesResponse, error) {
    return s.rbacSvc.ListGroupRoles(ctx, req)
}

func (s *Service) AssignRole(ctx context.Context, req *pb.AssignRoleRequest) (*emptypb.Empty, error) {
    return s.rbacSvc.AssignRole(ctx, req)
}

func (s *Service) RevokeRole(ctx context.Context, req *pb.RevokeRoleRequest) (*emptypb.Empty, error) {
    return s.rbacSvc.RevokeRole(ctx, req)
}

func (s *Service) ListUserRoles(ctx context.Context, req *pb.ListUserRolesRequest) (*pb.ListUserRolesResponse, error) {
    return s.rbacSvc.ListUserRoles(ctx, req)
}
```

Note: `runCleanup`, `resolveDB`, `resolveRedis`, `resolveGID`, `resolveLoginRateLimit` are preserved verbatim from `user_service.go`. Phase 5 will rewrite the resource handling.

- [ ] **Step 2: Delete user_service.go**

```bash
git rm internal/service/user_service.go
```

- [ ] **Step 3: Verify build**

```bash
go build ./internal/service/...
```

Expected: PASS. Note that `internal/service` no longer has flat files — only `service.go` and the 5 subpackage dirs.

- [ ] **Step 4: Commit**

```bash
git add internal/service/service.go
git commit -m "refactor: introduce service.go facade with 5 domain subpackages"
```

### Task 2.8: Update pkg/handler to use *service.Service

**Files:**
- Modify: `pkg/handler/user.go`
- Modify: `pkg/server.go`
- Modify: `pkg/module.go`

- [ ] **Step 1: Update handler to hold *service.Service**

In `pkg/handler/user.go`:

1. Change import: `"user-service/internal/service"` (unchanged path, but now `service.Service` not `service.UserService`)
2. Change field: `svc *service.Service` (was `*service.UserService`)
3. Change `New(svc *service.UserService)` → `New(svc *service.Service)`
4. Change `Svc()` return type → `*service.Service`
5. Change compile-time assertion `_ signalx.Service = (*Handler)(nil)` — still works because `Service` doesn't yet have `Start()`/`Stop()` methods. **Wait — this means Handler.Stop() can't be a no-op delegation if assertion requires it.**

The Handler still has its own Start/Stop (no-ops in Phase 2). The assertion `_ signalx.Service = (*Handler)(nil)` checks Handler, not service. Handler satisfies signalx.Service because it has Start() and Stop(). So the assertion holds.

- [ ] **Step 2: Update pkg/server.go**

Change `svc *service.UserService` → `svc *service.Service`. No other changes.

- [ ] **Step 3: Update pkg/module.go**

Change `*service.UserService` → `*service.Service` in `NewModule` internals (handler.New takes care of the rest).

- [ ] **Step 4: Build + test**

```bash
go build ./...
go test ./internal/service/... -run TestService_Login -v
go test ./pkg/handler/... -run TestHandler_Login -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/handler/user.go pkg/server.go pkg/module.go
git commit -m "refactor: switch handler/server/module to *service.Service facade"
```

### Task 2.9: Add per-subpackage happy-path tests

**Files:**
- Create: `internal/service/auth/auth_test.go`
- Create: `internal/service/user/user_test.go`
- Create: `internal/service/session/session_test.go`
- Create: `internal/service/rbac/rbac_test.go`
- Create: `internal/service/social/social_test.go`

- [ ] **Step 1: Write one test per subpackage**

Each test constructs the subpackage `Service` directly with test dependencies, calls one happy-path RPC, and asserts the response. Example for auth:

```go
package auth_test

import (
    "context"
    "testing"

    pb "user-service/gen/user/v1"
    "user-service/internal/service/auth"
    "user-service/internal/testutil"
)

// TestService_Register_HappyPath verifies that the auth subpackage can register
// a user via email/password and return a session.
func TestService_Register_HappyPath(t *testing.T) {
    db := testutil.SetupDB(t)
    rdb := testutil.SetupRedis(t)
    gid := testutil.StubGID()
    cap := testutil.SetupCaptcha(t, rdb)
    // ... construct repos, sessionMgr, limiter ...
    // svc := auth.New(...)
    // resp, err := svc.Register(context.Background(), &pb.RegisterRequest{...})
    // assert resp.UserId > 0, resp.SessionId != "", err == nil
}
```

Note: actual setup depends on the subpackage `New` signature. Verify against the subpackage's `auth.New` signature in `internal/service/auth/auth.go`. The test should exercise one real RPC end-to-end through the subpackage.

Skip if subpackage `New` is complex to construct standalone — instead test via the facade `service.New` (covered by `internal/service/service_test.go`).

- [ ] **Step 2: Run tests**

```bash
go test ./internal/service/... -v
```

Expected: all PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/service/auth/auth_test.go internal/service/user/user_test.go internal/service/session/session_test.go internal/service/rbac/rbac_test.go internal/service/social/social_test.go
git commit -m "test: add per-subpackage happy-path tests"
```

### Task 2.10: Phase 2 acceptance

- [ ] **Step 1: Full build + vet**

```bash
go build ./...
go vet ./...
```

Expected: PASS.

- [ ] **Step 2: Smoke test**

```bash
./scripts/smoke.sh
```

Expected: all RPC methods listed, server stays up.

- [ ] **Step 3: Final phase 2 commit if any cleanup**

```bash
git add -A
git commit -m "refactor: phase 2 complete — service split into 5 subpackages" --allow-empty
```

---

## Phase 3: repository/ → dal/ 重命名 + 函数式 API

**Goal:** Rename `internal/store/repository/` → `dal/` and convert from struct+method API to functional API (`dal.CreateUser` instead of `userRepo.Create`). Delete `BaseRepo`.

### Task 3.1: Set up dal test infrastructure

**Files:**
- Create: `internal/store/dal/common.go`

- [ ] **Step 1: Create common.go**

```go
// Package dal provides type-safe data access for user-service tables.
//
// Conventions (gorm-cli-development skill §6):
//   - One file per table (user.go, identity.go, ...); cross-table helpers here.
//   - Method names are table-prefixed (CreateUser, GetUserByID).
//   - Functions accept (ctx, tx *gorm.DB) so callers control transactions.
//   - Errors are wrapped via pkg/xcodes.
package dal

import (
    "context"

    "user-service/pkg/xcodes"

    "gorm.io/gorm"
)

// paginate is a helper for cursor-based pagination. Returns a function that
// applies LIMIT to a *gorm.DB query.
// (Phase 3: add helper as needed; for now placeholder for cross-table use.)
```

- [ ] **Step 2: Commit**

```bash
git add internal/store/dal/common.go
git commit -m "feat: add dal package skeleton with common helpers"
```

### Task 3.2: Create dal/user.go + test

**Files:**
- Create: `internal/store/dal/user.go`
- Create: `internal/store/dal/user_test.go`

- [ ] **Step 1: Write failing test**

```go
package dal_test

import (
    "context"
    "errors"
    "testing"

    "user-service/internal/store/dal"
    "user-service/internal/store/models"
    "user-service/internal/testutil"
    "user-service/pkg/xcodes"

    "gorm.io/gorm"
)

func TestCreateUser_AndGetByID(t *testing.T) {
    db := testutil.SetupDB(t)
    ctx := context.Background()

    u := &models.User{
        ID:       1001,
        Username: "alice",
        Email:    strPtr("alice@example.com"),
        Status:   "active",
    }
    if err := dal.CreateUser(ctx, db, u); err != nil {
        t.Fatalf("CreateUser: %v", err)
    }

    got, err := dal.GetUserByID(ctx, db, 1001)
    if err != nil {
        t.Fatalf("GetUserByID: %v", err)
    }
    if got.Username != "alice" {
        t.Errorf("Username = %q, want %q", got.Username, "alice")
    }
}

func TestGetUserByID_NotFound(t *testing.T) {
    db := testutil.SetupDB(t)
    ctx := context.Background()

    _, err := dal.GetUserByID(ctx, db, 99999)
    if !errors.Is(err, xcodes.ErrUserNotFound) {
        t.Errorf("GetUserByID(99999): expected ErrUserNotFound, got %v", err)
    }
}

func strPtr(s string) *string { return &s }
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./internal/store/dal/... -v
```

Expected: FAIL — `dal.CreateUser` undefined.

- [ ] **Step 3: Implement dal/user.go**

Convert `internal/store/repository/user.go` (149 lines) into function-style. Pattern:

```go
package dal

import (
    "context"
    "errors"
    "fmt"
    "strconv"

    "user-service/internal/store/generated"
    "user-service/internal/store/models"
    "user-service/pkg/xcodes"

    "gorm.io/gorm"
)

// CreateUser inserts a new user record.
func CreateUser(ctx context.Context, tx *gorm.DB, user *models.User) error {
    if err := gorm.G[models.User](tx).Create(ctx, user); err != nil {
        return xcodes.ErrInternal.Wrap(err)
    }
    return nil
}

// GetUserByID returns a user by ID. Returns xcodes.ErrUserNotFound if not found.
func GetUserByID(ctx context.Context, tx *gorm.DB, id int64) (*models.User, error) {
    user, err := gorm.G[models.User](tx).
        Where(generated.SnowflakeModelWithDeleted.ID.Eq(id)).
        Take(ctx)
    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, xcodes.ErrUserNotFound.New()
        }
        return nil, xcodes.ErrInternal.Wrap(err)
    }
    return &user, nil
}

// GetUserByEmail returns a user by email address.
func GetUserByEmail(ctx context.Context, tx *gorm.DB, email string) (*models.User, error) {
    user, err := gorm.G[models.User](tx).
        Where(generated.User.Email.Eq(email)).
        Take(ctx)
    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, xcodes.ErrUserNotFound.New()
        }
        return nil, xcodes.ErrInternal.Wrap(err)
    }
    return &user, nil
}

// GetUserByPhone returns a user by phone number.
func GetUserByPhone(ctx context.Context, tx *gorm.DB, phone string) (*models.User, error) {
    user, err := gorm.G[models.User](tx).
        Where(generated.User.Phone.Eq(phone)).
        Take(ctx)
    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, xcodes.ErrUserNotFound.New()
        }
        return nil, xcodes.ErrInternal.Wrap(err)
    }
    return &user, nil
}

// GetUserByUsername returns a user by username.
func GetUserByUsername(ctx context.Context, tx *gorm.DB, username string) (*models.User, error) {
    user, err := gorm.G[models.User](tx).
        Where(generated.User.Username.Eq(username)).
        Take(ctx)
    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, xcodes.ErrUserNotFound.New()
        }
        return nil, xcodes.ErrInternal.Wrap(err)
    }
    return &user, nil
}

// UpdateUser saves all fields of user (including zero values).
func UpdateUser(ctx context.Context, tx *gorm.DB, user *models.User) error {
    result := tx.WithContext(ctx).Save(user)
    if result.Error != nil {
        return xcodes.ErrInternal.Wrap(result.Error)
    }
    if result.RowsAffected == 0 {
        return xcodes.ErrUserNotFound.New()
    }
    return nil
}

// UpdateUserLastLogin updates the LastLoginAt and LastLoginIP fields.
func UpdateUserLastLogin(ctx context.Context, tx *gorm.DB, id int64, ip string) error {
    rowsAffected, err := gorm.G[models.User](tx).
        Where(generated.SnowflakeModelWithDeleted.ID.Eq(id)).
        Set(
            generated.User.LastLoginAt.Now(),
            generated.User.LastLoginIP.Set(ip),
        ).
        Update(ctx)
    if err != nil {
        return xcodes.ErrInternal.Wrap(err)
    }
    if rowsAffected == 0 {
        return xcodes.ErrUserNotFound.New()
    }
    return nil
}

// ListUsers returns a page of users ordered by ID. cursor is the last user ID
// from the previous page; pass "" for the first page. Returns the next cursor
// (or "" if no more pages).
func ListUsers(ctx context.Context, tx *gorm.DB, cursor string, pageSize int32) ([]*models.User, string, error) {
    q := gorm.G[models.User](tx).Order(generated.SnowflakeModelWithDeleted.ID)

    if cursor != "" {
        cursorID, err := strconv.ParseInt(cursor, 10, 64)
        if err != nil {
            return nil, "", xcodes.ErrBadRequest.Wrapf(err, "invalid cursor: %s", cursor)
        }
        q = q.Where(generated.SnowflakeModelWithDeleted.ID.Gt(cursorID))
    }

    results, err := q.Limit(int(pageSize) + 1).Find(ctx)
    if err != nil {
        return nil, "", xcodes.ErrInternal.Wrap(err)
    }

    users := make([]*models.User, len(results))
    for i := range results {
        users[i] = &results[i]
    }

    var nextCursor string
    if len(users) > int(pageSize) {
        nextCursor = fmt.Sprintf("%d", users[pageSize].ID)
        users = users[:pageSize]
    }
    return users, nextCursor, nil
}
```

Note: every method from `repository/user.go` gets converted. Function name = `Verb + TableName` (`Create`, `GetUserByID`, `UpdateUser`, `ListUsers`, etc.).

- [ ] **Step 4: Run test**

```bash
go test ./internal/store/dal/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/dal/user.go internal/store/dal/user_test.go
git commit -m "feat: add dal/user.go with function-style CRUD"
```

### Task 3.3: Create dal/identity.go + test

**Files:**
- Create: `internal/store/dal/identity.go`
- Create: `internal/store/dal/identity_test.go`

- [ ] **Step 1: Write failing test**

```go
package dal_test

import (
    "context"
    "testing"

    "user-service/internal/store/dal"
    "user-service/internal/store/models"
    "user-service/internal/testutil"
)

func TestCreateIdentity_AndFindByProviderUID(t *testing.T) {
    db := testutil.SetupDB(t)
    ctx := context.Background()

    // First create a user (FK-like; no constraint since we disabled FK)
    if err := dal.CreateUser(ctx, db, &models.User{ID: 2001, Username: "bob", Status: "active"}); err != nil {
        t.Fatalf("CreateUser: %v", err)
    }

    ident := &models.Identity{
        UserID:      2001,
        Provider:    "email",
        ProviderUID: "bob@example.com",
        Credentials: `{"hash":"..."}`,
        Verified:    true,
    }
    if err := dal.CreateIdentity(ctx, db, ident); err != nil {
        t.Fatalf("CreateIdentity: %v", err)
    }

    got, err := dal.GetIdentityByProviderUID(ctx, db, "email", "bob@example.com")
    if err != nil { t.Fatalf("GetIdentityByProviderUID: %v", err) }
    if got.UserID != 2001 { t.Errorf("UserID = %d, want 2001", got.UserID) }
}
```

- [ ] **Step 2: Run test, verify fails**

```bash
go test ./internal/store/dal/... -run TestCreateIdentity -v
```

- [ ] **Step 3: Implement dal/identity.go**

Convert `repository/identity.go` (103 lines) to function-style. Methods to convert:
- `Create` → `CreateIdentity`
- `FindByID` → `GetIdentityByID`
- `FindByProviderUID` → `GetIdentityByProviderUID`
- `FindByUserID` → `ListIdentitiesByUserID`
- `UpdateCredentials` → `UpdateIdentityCredentials`
- `Delete` → `DeleteIdentity`

- [ ] **Step 4: Run test, commit**

```bash
go test ./internal/store/dal/... -v
git add internal/store/dal/identity.go internal/store/dal/identity_test.go
git commit -m "feat: add dal/identity.go"
```

### Task 3.4: Create dal/session.go + test

**Files:**
- Create: `internal/store/dal/session.go`
- Create: `internal/store/dal/session_test.go`

Convert `repository/session.go` (100 lines). Same pattern.

- [ ] **Step 1-4: Write test, verify fails, implement, verify passes, commit**

```bash
git add internal/store/dal/session.go internal/store/dal/session_test.go
git commit -m "feat: add dal/session.go"
```

### Task 3.5: Create dal/login_log.go + test

**Files:**
- Create: `internal/store/dal/login_log.go`
- Create: `internal/store/dal/login_log_test.go`

Convert `repository/login_log.go` (93 lines). Includes both `LoginLogRepo` and `VerificationCodeRepo` types — both become dal functions in the same file (or split into `dal/verification_code.go` if it grows past ~150 lines).

Methods to convert:
- `LoginLogRepo`: `Create` → `CreateLoginLog`, `ListByUserID` → `ListLoginLogsByUserID`
- `VerificationCodeRepo`: `Create` → `CreateVerificationCode`, `MarkUsed` → `MarkVerificationCodeUsed`

- [ ] **Step 1-4: TDD, commit**

```bash
git add internal/store/dal/login_log.go internal/store/dal/login_log_test.go
git commit -m "feat: add dal/login_log.go"
```

### Task 3.6: Create dal/message.go + test

**Files:**
- Create: `internal/store/dal/message.go`
- Create: `internal/store/dal/message_test.go`

Convert `repository/message.go` (113 lines). Contains both `EmailRepo` and `SMSLogRepo` types.

- [ ] **Step 1-4: TDD, commit**

```bash
git add internal/store/dal/message.go internal/store/dal/message_test.go
git commit -m "feat: add dal/message.go"
```

### Task 3.7: Create dal/group.go + test

**Files:**
- Create: `internal/store/dal/group.go`
- Create: `internal/store/dal/group_test.go`

Convert `repository/group.go` (215 lines). Contains `GroupRepo` and `UserGroupRepo`.

- [ ] **Step 1-4: TDD, commit**

```bash
git add internal/store/dal/group.go internal/store/dal/group_test.go
git commit -m "feat: add dal/group.go"
```

### Task 3.8: Create dal/rbac.go + test

**Files:**
- Create: `internal/store/dal/rbac.go`
- Create: `internal/store/dal/rbac_test.go`

Convert `repository/rbac.go` (523 lines). This is the largest — contains many join queries. Preserve all join logic verbatim.

Methods to convert (from `RoleRepo`, `PermissionRepo`, `PermissionGroupRepo`, `RolePermissionRepo`, `RolePermissionGroupRepo`, `GroupRoleRepo`, `UserRoleRepo`):

Apply standard naming: `RoleRepo.Create` → `CreateRole`, `RolePermissionRepo.FindByRoleID` → `ListRolePermissionsByRoleID`, etc. All join queries preserved.

- [ ] **Step 1-4: TDD, commit**

```bash
git add internal/store/dal/rbac.go internal/store/dal/rbac_test.go
git commit -m "feat: add dal/rbac.go with join queries"
```

### Task 3.9: Update service subpackages to use dal

**Files:**
- Modify: `internal/service/auth/auth.go`
- Modify: `internal/service/user/user.go`
- Modify: `internal/service/session/session.go`
- Modify: `internal/service/social/social.go`
- Modify: `internal/service/rbac/rbac.go`
- Modify: `internal/service/service.go`

- [ ] **Step 1: Update service.go to remove repo initialization**

In `internal/service/service.go`, remove all `repository.NewXxxRepo(db)` lines. Subpackage `New` signatures change to take `db *gorm.DB` instead of repos:

```go
// Before (Phase 2):
auth.New(userRepo, identityRepo, sessionRepo, loginLogRepo, sessionMgr, captchaSvc, loginLimiter, gid)

// After (Phase 3):
auth.New(db, sessionMgr, captchaSvc, loginLimiter, gid)
```

- [ ] **Step 2: Update each subpackage**

For each subpackage (`auth/auth.go`, `user/user.go`, etc.):
1. Remove `repository` import, add `dal` import
2. Remove all repo struct fields from `Service`, add `db *gorm.DB`
3. Update `New()` signature to take `db` and other deps (no repos)
4. Update each method body: `h.userRepo.FindByID(ctx, id)` → `dal.GetUserByID(ctx, s.db, id)`
5. Update receiver `h` → `s` if not already done in Phase 2

For transactions:
- Replace `s.db.Transaction(func(tx *gorm.DB) error { ... })` blocks. Inside the tx body, pass `tx` to dal functions: `dal.CreateLoginLog(ctx, tx, log)`.

- [ ] **Step 3: Update service.go construction**

```go
svc := &Service{
    cfg:        cfg,
    db:         db,
    rdb:        rdb,
    ownDB:      ownDB,
    ownRedis:   ownRedis,
    gid:        gid,
    sessionMgr: sessionMgr,
    auth:       auth.New(db, sessionMgr, o.Captcha, loginLimiter, gid),
    user:       user.New(db),
    social:     social.New(db, sessionMgr, socialProviders, gid),
    sess:       domainSession.New(sessionMgr, db),
    rbacSvc:    rbac.New(db, rbacCache, gid),
}
```

- [ ] **Step 4: Build + test**

```bash
go build ./...
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/
git commit -m "refactor: switch service subpackages from repos to dal functions"
```

### Task 3.10: Delete repository/ directory

**Files:**
- Delete: `internal/store/repository/` (all 8 files)

- [ ] **Step 1: Delete the directory**

```bash
git rm -r internal/store/repository/
```

- [ ] **Step 2: Verify nothing references it**

```bash
grep -r "internal/store/repository" --include="*.go" .
```

Expected: no matches.

- [ ] **Step 3: Build + test**

```bash
go build ./...
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git commit -m "refactor: remove internal/store/repository (replaced by dal)"
```

### Task 3.11: Phase 3 acceptance

- [ ] **Step 1: Full build + vet + lint**

```bash
go build ./...
go vet ./...
go test -race ./...
```

Expected: PASS.

- [ ] **Step 2: Smoke test**

```bash
./scripts/smoke.sh
```

Expected: PASS.

---

## Phase 4: models 重写 + regen generated

**Goal:** Rewrite all models with explicit ID/CreatedAt/UpdatedAt/DeletedAt fields. Add `User` prefix to business tables (`UserLoginLog`, `UserSession`, etc.). RBAC tables keep business-semantic model names but get `rbac_` table prefix via `TableName()`. Regenerate `internal/store/generated/`.

### Task 4.1: Rewrite models/user.go

**Files:**
- Modify: `internal/store/models/user.go`

- [ ] **Step 1: Rewrite all model definitions**

Replace embedded base types with explicit fields. Apply renames.

```go
package models

import (
    "database/sql"
    "time"

    "gorm.io/gorm"
)

// User represents the core user record with snowflake ID.
type User struct {
    ID            int64          `gorm:"primaryKey"`
    Username      string         `gorm:"size:64;uniqueIndex"`
    Nickname      string         `gorm:"size:64"`
    RealName      string         `gorm:"size:64"`
    AvatarURL     string         `gorm:"size:512"`
    Email         *string        `gorm:"size:256;uniqueIndex"`
    Phone         *string        `gorm:"size:20;uniqueIndex"`
    Gender        string         `gorm:"size:8;default:unknown"`
    Birthday      *time.Time
    Timezone      string         `gorm:"size:64;default:Asia/Shanghai"`
    Locale        string         `gorm:"size:16;default:zh-CN"`
    Bio           string         `gorm:"size:512"`
    Status        string         `gorm:"size:16;not null;default:active"`
    RegisterSource string        `gorm:"size:32"`
    RegisterIP    string         `gorm:"size:45"`
    RegisterDevice string       `gorm:"size:16"`
    LastLoginAt   *time.Time
    LastLoginIP   string         `gorm:"size:45"`
    CreatedAt     time.Time
    UpdatedAt     time.Time
    DeletedAt     gorm.DeletedAt `gorm:"index"`
}

// UserIdentity represents a login method bound to a user.
type UserIdentity struct {
    ID          int64          `gorm:"primaryKey"`
    UserID      int64          `gorm:"not null;index"`
    Provider    string         `gorm:"size:32;not null;uniqueIndex:uq_user_identity_provider"`
    ProviderUID string         `gorm:"size:256;not null;uniqueIndex:uq_user_identity_provider"`
    Credentials string         `gorm:"type:jsonb"`
    Verified    bool           `gorm:"not null;default:false"`
    OAuthData   OAuthData      `gorm:"type:jsonb"`
    CreatedAt   time.Time
    UpdatedAt   time.Time
    DeletedAt   gorm.DeletedAt `gorm:"index"`
}

// UserSession represents a user login session. Redis is primary storage; PG is for persistence.
type UserSession struct {
    ID           string         `gorm:"size:64;primaryKey"`
    UserID       int64          `gorm:"not null;index"`
    IP           string         `gorm:"size:45"`
    UserAgent    string         `gorm:"size:512"`
    DeviceType   string         `gorm:"size:16"`
    OS           string         `gorm:"size:32"`
    Browser      string         `gorm:"size:32"`
    Country      string         `gorm:"size:4"`
    City         string         `gorm:"size:64"`
    ExpiresAt    time.Time      `gorm:"not null;index:idx_user_sessions_expires,where:deleted_at IS NULL"`
    LastActiveAt time.Time
    RevokedAt    *time.Time
    CreatedAt    time.Time
    UpdatedAt    time.Time
    DeletedAt    gorm.DeletedAt `gorm:"index"`
}

// UserLoginLog records login attempts (append-only audit table).
type UserLoginLog struct {
    ID         int64          `gorm:"primaryKey"`
    UserID     *int64         `gorm:"index"`
    IdentityID *int64
    Provider   string         `gorm:"size:32;not null"`
    Action     string         `gorm:"size:16;not null"`
    Success    bool           `gorm:"not null"`
    FailReason string         `gorm:"size:128"`
    IP         string         `gorm:"size:45"`
    UserAgent  string         `gorm:"size:512"`
    DeviceType string         `gorm:"size:16"`
    OS         string         `gorm:"size:32"`
    Browser    string         `gorm:"size:32"`
    Country    string         `gorm:"size:4"`
    City       string         `gorm:"size:64"`
    CreatedAt  time.Time
    UpdatedAt  time.Time
    DeletedAt  gorm.DeletedAt `gorm:"index"`
}

// UserVerificationCode records sent verification codes.
type UserVerificationCode struct {
    ID        int64          `gorm:"primaryKey"`
    Target    string         `gorm:"size:256;not null;index:idx_user_verification_target_type"`
    Code      string         `gorm:"size:16;not null"`
    Channel   string         `gorm:"size:16;not null"`
    Type      string         `gorm:"size:32;not null;index:idx_user_verification_target_type"`
    ExpiresAt time.Time      `gorm:"not null"`
    UsedAt    sql.NullTime
    IP        string         `gorm:"size:45"`
    CreatedAt time.Time
    UpdatedAt time.Time
    DeletedAt gorm.DeletedAt `gorm:"index"`
}

// UserEmail records sent emails for audit.
type UserEmail struct {
    ID            int64          `gorm:"primaryKey"`
    ToAddr        string         `gorm:"size:256;not null;index"`
    Subject       string         `gorm:"size:256;not null"`
    Content       string         `gorm:"type:text;not null"`
    Template      string         `gorm:"size:64"`
    Provider      string         `gorm:"size:32"`
    Status        string         `gorm:"size:16;not null;default:pending;index"`
    ProviderMsgID string         `gorm:"size:256"`
    ErrorMessage  string         `gorm:"size:512"`
    IP            string         `gorm:"size:45"`
    CreatedAt     time.Time
    UpdatedAt     time.Time
    DeletedAt     gorm.DeletedAt `gorm:"index"`
}

// UserSMSLog records sent SMS messages for audit.
type UserSMSLog struct {
    ID            int64          `gorm:"primaryKey"`
    Phone         string         `gorm:"size:20;not null;index"`
    Content       string         `gorm:"size:512;not null"`
    Template      string         `gorm:"size:64"`
    Provider      string         `gorm:"size:32"`
    Status        string         `gorm:"size:16;not null;default:pending;index"`
    ProviderMsgID string         `gorm:"size:256"`
    ErrorMessage  string         `gorm:"size:512"`
    IP            string         `gorm:"size:45"`
    CreatedAt     time.Time
    UpdatedAt     time.Time
    DeletedAt     gorm.DeletedAt `gorm:"index"`
}

// OAuthData stores provider-specific token data for social login identities.
type OAuthData struct {
    AccessToken string `json:"access_token,omitempty"`
    SessionKey  string `json:"session_key,omitempty"`
    UnionID     string `json:"unionid,omitempty"`
}
```

Note the following changes from original:
- All models have explicit `ID int64 gorm:"primaryKey"`, `CreatedAt time.Time`, `UpdatedAt time.Time`, `DeletedAt gorm.DeletedAt gorm:"index"`
- Renamed: `Identity` → `UserIdentity`, `Session` → `UserSession`, `LoginLog` → `UserLoginLog`, `VerificationCode` → `UserVerificationCode`, `Email` → `UserEmail`, `SMSLog` → `UserSMSLog`
- Index names updated to reflect new table names (e.g., `idx_user_sessions_expires` instead of `idx_sessions_expires`)

- [ ] **Step 2: Verify build**

```bash
go build ./internal/store/models/...
```

Expected: FAIL — references to old model names throughout codebase. That's expected; we fix in Task 4.5.

- [ ] **Step 3: Commit**

```bash
git add internal/store/models/user.go
git commit -m "refactor: rewrite models/user.go with explicit fields and User prefix"
```

### Task 4.2: Rewrite models/rbac.go with rbac_ table prefix

**Files:**
- Modify: `internal/store/models/rbac.go`

- [ ] **Step 1: Rewrite all RBAC models**

Each RBAC model gets:
1. Explicit ID/CreatedAt/UpdatedAt/DeletedAt
2. `TableName()` method returning `rbac_<plural>`

```go
package models

import (
    "time"

    "gorm.io/gorm"
)

// Group represents a user group in RBAC.
type Group struct {
    ID          int64          `gorm:"primaryKey"`
    Name        string         `gorm:"size:64;not null;uniqueIndex"`
    DisplayName string         `gorm:"size:128"`
    Description string         `gorm:"size:512"`
    Status      string         `gorm:"size:16;not null;default:active"`
    CreatedBy   int64
    CreatedAt   time.Time
    UpdatedAt   time.Time
    DeletedAt   gorm.DeletedAt `gorm:"index"`
}

func (Group) TableName() string { return "rbac_groups" }

// UserGroup is the join table between User and Group.
type UserGroup struct {
    ID       int64          `gorm:"primaryKey"`
    UserID   int64          `gorm:"not null;uniqueIndex:uq_user_group"`
    GroupID  int64          `gorm:"not null;uniqueIndex:uq_user_group"`
    Role     string         `gorm:"size:32;default:member"`
    JoinedAt time.Time
    CreatedAt time.Time
    UpdatedAt time.Time
    DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (UserGroup) TableName() string { return "rbac_user_groups" }

// Role represents a named role in RBAC.
type Role struct {
    ID          int64          `gorm:"primaryKey"`
    Name        string         `gorm:"size:64;not null;uniqueIndex"`
    DisplayName string         `gorm:"size:128"`
    Description string         `gorm:"size:512"`
    CreatedAt   time.Time
    UpdatedAt   time.Time
    DeletedAt   gorm.DeletedAt `gorm:"index"`
}

func (Role) TableName() string { return "rbac_roles" }

// Permission represents an atomic permission in RBAC.
type Permission struct {
    ID               int64          `gorm:"primaryKey"`
    Name             string         `gorm:"size:128;not null;uniqueIndex"`
    DisplayName      string         `gorm:"size:256"`
    PermissionGroupID int64         `gorm:"index"`
    CreatedAt        time.Time
    UpdatedAt        time.Time
    DeletedAt        gorm.DeletedAt `gorm:"index"`
}

func (Permission) TableName() string { return "rbac_permissions" }

// PermissionGroup groups permissions for display.
type PermissionGroup struct {
    ID          int64          `gorm:"primaryKey"`
    Name        string         `gorm:"size:64;not null;uniqueIndex"`
    DisplayName string         `gorm:"size:128"`
    CreatedAt   time.Time
    UpdatedAt   time.Time
    DeletedAt   gorm.DeletedAt `gorm:"index"`
}

func (PermissionGroup) TableName() string { return "rbac_permission_groups" }

// PermissionGroupItem is the join table for Permission ↔ PermissionGroup.
type PermissionGroupItem struct {
    ID                int64          `gorm:"primaryKey"`
    PermissionID      int64          `gorm:"not null;uniqueIndex:uq_perm_group_item"`
    PermissionGroupID int64          `gorm:"not null;uniqueIndex:uq_perm_group_item"`
    CreatedAt         time.Time
    UpdatedAt         time.Time
    DeletedAt         gorm.DeletedAt `gorm:"index"`
}

func (PermissionGroupItem) TableName() string { return "rbac_permission_group_items" }

// RolePermission is the join table for Role ↔ Permission.
type RolePermission struct {
    ID           int64          `gorm:"primaryKey"`
    RoleID       int64          `gorm:"not null;uniqueIndex:uq_role_permission"`
    PermissionID int64          `gorm:"not null;uniqueIndex:uq_role_permission"`
    CreatedAt    time.Time
    UpdatedAt    time.Time
    DeletedAt    gorm.DeletedAt `gorm:"index"`
}

func (RolePermission) TableName() string { return "rbac_role_permissions" }

// RolePermissionGroup is the join table for Role ↔ PermissionGroup.
type RolePermissionGroup struct {
    ID                int64          `gorm:"primaryKey"`
    RoleID            int64          `gorm:"not null;uniqueIndex:uq_role_perm_group"`
    PermissionGroupID int64          `gorm:"not null;uniqueIndex:uq_role_perm_group"`
    CreatedAt         time.Time
    UpdatedAt         time.Time
    DeletedAt         gorm.DeletedAt `gorm:"index"`
}

func (RolePermissionGroup) TableName() string { return "rbac_role_permission_groups" }

// GroupRole is the join table for Group ↔ Role.
type GroupRole struct {
    ID       int64          `gorm:"primaryKey"`
    GroupID  int64          `gorm:"not null;uniqueIndex:uq_group_role"`
    RoleID   int64          `gorm:"not null;uniqueIndex:uq_group_role"`
    CreatedAt time.Time
    UpdatedAt time.Time
    DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (GroupRole) TableName() string { return "rbac_group_roles" }

// UserRole is the join table for User ↔ Role.
type UserRole struct {
    ID       int64          `gorm:"primaryKey"`
    UserID   int64          `gorm:"not null;uniqueIndex:uq_user_role"`
    RoleID   int64          `gorm:"not null;uniqueIndex:uq_user_role"`
    CreatedAt time.Time
    UpdatedAt time.Time
    DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (UserRole) TableName() string { return "rbac_user_roles" }
```

Note: actual field details for each RBAC model — match the existing `models/rbac.go` (preserve column names and types). The above is a template — verify against current file before applying.

- [ ] **Step 2: Verify the existing fields are preserved**

```bash
git diff internal/store/models/rbac.go  # confirm only renames + field additions, no field deletions
```

- [ ] **Step 3: Commit**

```bash
git add internal/store/models/rbac.go
git commit -m "refactor: rewrite models/rbac.go with explicit fields and rbac_ table prefix"
```

### Task 4.3: Update register.go AllModels()

**Files:**
- Modify: `internal/store/models/register.go`

- [ ] **Step 1: Update the AllModels slice**

```go
package models

// AllModels returns all model types for GORM AutoMigrate.
// Order matters: parent tables before join tables.
func AllModels() []any {
    return []any{
        &User{},
        &UserIdentity{},
        &UserSession{},
        &UserLoginLog{},
        &UserVerificationCode{},
        &UserEmail{},
        &UserSMSLog{},
        &Group{},
        &UserGroup{},
        &Role{},
        &Permission{},
        &PermissionGroup{},
        &PermissionGroupItem{},
        &RolePermission{},
        &RolePermissionGroup{},
        &GroupRole{},
        &UserRole{},
    }
}
```

- [ ] **Step 2: Delete base.go**

`internal/store/models/base.go` had the embedded types (`BaseModel`, `SnowflakeModel`, etc.) — no longer needed.

```bash
git rm internal/store/models/base.go
```

- [ ] **Step 3: Commit**

```bash
git add internal/store/models/register.go
git commit -m "refactor: update AllModels for new model names, remove base.go"
```

### Task 4.4: Recreate genconfig.go with FieldTypeMap

**Files:**
- Create: `internal/store/models/genconfig.go`

- [ ] **Step 1: Create genconfig.go**

`gorm gen` needs to know how to handle custom types like `OAuthData` (stored as JSONB).

```go
package models

import (
    "database/sql"

    "gorm.io/cli/field"
    "gorm.io/datatypes/json"
)

// GenConfig is consumed by gorm gen to customize field helpers.
// See https://gorm.io/cli/field_helpers.html for FieldTypeMap usage.
type GenConfig struct{}

// FieldTypeMap registers custom Go types so gorm gen generates proper helpers.
// Without this, fields like OAuthData get the generic field.Any helper.
func (GenConfig) FieldTypeMap() map[string]field.Field {
    return map[string]field.Field{
        // OAuthData is stored as JSONB but exposed as a struct.
        "OAuthData": field.Field{Type: "json", JSONType: json.Type[OAuthData]()},
    }
}
```

Note: the exact API for `gorm.io/cli/field` and `gorm.io/datatypes/json` needs verification against the current go.mod versions. Check `github.com/servekit/go-common` for any prior usage. If the gen API is different, adapt accordingly. This file may need iteration.

- [ ] **Step 2: Commit**

```bash
git add internal/store/models/genconfig.go
git commit -m "feat: add genconfig.go with FieldTypeMap for OAuthData"
```

### Task 4.5: Regenerate internal/store/generated/

- [ ] **Step 1: Run gorm gen**

```bash
make generate
# Or directly:
# gorm gen -i ./internal/store/models -o ./internal/store/generated
```

Expected: new files generated for `User`, `UserIdentity`, `UserSession`, `UserLoginLog`, etc. Old files for `Identity`, `Session`, `LoginLog` are removed by the generator.

- [ ] **Step 2: Verify generated output**

```bash
ls internal/store/generated/
```

Expected: `user.gen.go`, `user_identity.gen.go`, `user_session.gen.go`, ..., `query.gen.go`. No `identity.gen.go`, `session.gen.go`, etc.

- [ ] **Step 3: Verify diff is clean (no SnowflakeModelWithDeleted references)**

```bash
grep "SnowflakeModelWithDeleted\|BaseModelWithDeleted\|CreatedAtModel" internal/store/generated/ -r
```

Expected: no matches.

- [ ] **Step 4: Commit**

```bash
git add internal/store/generated/
git commit -m "regen: gorm gen output for renamed models"
```

### Task 4.6: Update dal references to new model names

**Files:**
- Modify: `internal/store/dal/user.go`
- Modify: `internal/store/dal/identity.go`
- Modify: `internal/store/dal/session.go`
- Modify: `internal/store/dal/login_log.go`
- Modify: `internal/store/dal/message.go`
- Modify: `internal/store/dal/group.go`
- Modify: `internal/store/dal/rbac.go`

- [ ] **Step 1: Find all references to old model names**

```bash
grep -rn "models\.\(Identity\|Session\|LoginLog\|VerificationCode\|Email\|SMSLog\)\b" internal/store/dal/
grep -rn "generated\.\(SnowflakeModelWithDeleted\|BaseModelWithDeleted\|CreatedAtModel\)" internal/store/dal/
```

- [ ] **Step 2: Apply substitutions in dal files**

For each dal file:
1. `models.Identity` → `models.UserIdentity`
2. `models.Session` → `models.UserSession`
3. `models.LoginLog` → `models.UserLoginLog`
4. `models.VerificationCode` → `models.UserVerificationCode`
5. `models.Email` → `models.UserEmail`
6. `models.SMSLog` → `models.UserSMSLog`
7. `generated.SnowflakeModelWithDeleted.ID` → `generated.User.ID` (or appropriate model)
8. `generated.Identity.X` → `generated.UserIdentity.X`
9. Similar for all generated field helper references

Use sed with care, or edit manually. Test after each file.

```bash
# Example sed for one file:
sed -i '' 's/models\.Identity/models.UserIdentity/g; s/models\.Session/models.UserSession/g; ...' internal/store/dal/user.go
```

- [ ] **Step 3: Verify build**

```bash
go build ./internal/store/dal/...
```

Expected: PASS.

- [ ] **Step 4: Run dal tests**

```bash
go test ./internal/store/dal/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/dal/
git commit -m "refactor: update dal references to renamed models"
```

### Task 4.7: Update service references to new model names

**Files:**
- Modify: `internal/service/auth/auth.go`
- Modify: `internal/service/user/user.go`
- Modify: `internal/service/session/session.go`
- Modify: `internal/service/social/social.go`
- Modify: `internal/service/rbac/rbac.go`
- Modify: `internal/cache/rbac.go` (if it references models)

- [ ] **Step 1: Find references**

```bash
grep -rn "models\.\(Identity\|Session\|LoginLog\|VerificationCode\|Email\|SMSLog\)\b" internal/
```

- [ ] **Step 2: Apply substitutions**

Same pattern as Task 4.6, applied to service files.

- [ ] **Step 3: Verify build**

```bash
go build ./...
```

Expected: PASS.

- [ ] **Step 4: Run service tests**

```bash
go test ./internal/service/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/
git commit -m "refactor: update service and cache references to renamed models"
```

### Task 4.8: Verify migrate runs cleanly

- [ ] **Step 1: Drop and recreate test DB**

The testcontainer from `dbx.SetupTestDB` starts fresh each time, so this is automatic for tests. For local dev DB:

```bash
# Use psql to drop and recreate the dev database
psql -h localhost -U postgres -c "DROP DATABASE IF EXISTS user_service;"
psql -h localhost -U postgres -c "CREATE DATABASE user_service;"
```

- [ ] **Step 2: Run migrate**

```bash
make migrate
```

Expected: PASS. Tables created: `users`, `user_identities`, `user_sessions`, `user_login_logs`, `user_verification_codes`, `user_emails`, `user_sms_logs`, `rbac_groups`, `rbac_user_groups`, `rbac_roles`, `rbac_permissions`, `rbac_permission_groups`, `rbac_permission_group_items`, `rbac_role_permissions`, `rbac_role_permission_groups`, `rbac_group_roles`, `rbac_user_roles`.

- [ ] **Step 3: Verify table names**

```bash
psql -h localhost -U postgres -d user_service -c "\dt"
```

Expected: all 17 tables listed with correct prefixed names.

- [ ] **Step 4: Commit if anything needed adjusting**

```bash
git add -A
git commit -m "fix: phase 4 migrate adjustments" --allow-empty
```

### Task 4.9: Phase 4 acceptance

- [ ] **Step 1: Full build + vet + tests**

```bash
go build ./...
go vet ./...
go test -race ./...
```

Expected: PASS.

- [ ] **Step 2: Smoke test**

```bash
./scripts/smoke.sh
```

Expected: PASS.

- [ ] **Step 3: Verify gen output is committed**

```bash
make generate
git diff --exit-code internal/store/generated/
```

Expected: no diff (gen output is in sync).

---

## Phase 5: lifecycle.Manager + internal/jobs/

**Goal:** Replace `ownDB`/`ownRedis` bool with `lifecycle.Manager`. Add `internal/jobs/` skeleton + `setupJobs()` receiver method. Verify graceful shutdown via SIGTERM.

### Task 5.1: Replace ownDB/ownRedis with lifecycle.Manager in service.go

**Files:**
- Modify: `internal/service/service.go`

- [ ] **Step 1: Update Service struct**

```go
import (
    "log/slog"

    "github.com/servekit/go-common/lifecycle"
    // ... other imports
)

type Service struct {
    cfg *config.Config
    mgr *lifecycle.Manager  // NEW: tracks every owned resource

    db  *gorm.DB
    rdb *redis.Client
    gid thirdcall.GIDService
    sessionMgr *session.Manager

    auth   *auth.Service
    user   *user.Service
    social *social.Service
    sess   *domainSession.Service
    rbacSvc *rbac.Service
}
```

Remove `ownDB`, `ownRedis` fields. Remove `Close()` method (replaced by `Stop()`).

- [ ] **Step 2: Update New() to use mgr**

```go
func New(cfg *config.Config, opts ...option.Option) (*Service, error) {
    o := option.Apply(opts...)
    mgr := lifecycle.NewManager()

    db, err := resolveDB(cfg, o.DB, mgr)
    if err != nil {
        return nil, errors.Join(err, mgr.Stop())
    }
    rdb, err := resolveRedis(cfg, o.RDB, mgr)
    if err != nil {
        return nil, errors.Join(err, mgr.Stop())
    }
    gid, err := resolveGID(cfg, o.GIDService, mgr)
    if err != nil {
        return nil, errors.Join(err, mgr.Stop())
    }

    sessionMgr := session.NewManager(rdb, cfg.Session)
    // ... (rest of construction unchanged from Phase 3)

    svc := &Service{
        cfg: cfg, mgr: mgr,
        db: db, rdb: rdb, gid: gid,
        sessionMgr: sessionMgr,
        auth:  auth.New(db, sessionMgr, o.Captcha, loginLimiter, gid),
        // ...
    }

    // Phase 5 Task 5.5 adds: if err := svc.setupJobs(); err != nil { ... }
    return svc, nil
}

func (s *Service) Start() error { return s.mgr.Start() }
func (s *Service) Stop() error  { return s.mgr.Stop() }
```

- [ ] **Step 3: Update resolveDB/resolveRedis/resolveGID**

```go
func resolveDB(cfg *config.Config, injected *gorm.DB, mgr *lifecycle.Manager) (*gorm.DB, error) {
    if injected != nil {
        return injected, nil
    }
    db, err := dbx.New(cfg.Database)
    if err != nil {
        return nil, fmt.Errorf("database: %w", err)
    }
    mgr.AddStopper("db", lifecycle.StopFunc(func() {
        sqlDB, err := db.DB()
        if err != nil {
            slog.Warn("get sql db for close", "error", err)
            return
        }
        if err := sqlDB.Close(); err != nil {
            slog.Warn("close db", "error", err)
        }
    }))
    return db, nil
}

func resolveRedis(cfg *config.Config, injected *redis.Client, mgr *lifecycle.Manager) (*redis.Client, error) {
    if injected != nil {
        return injected, nil
    }
    rdb, err := redisx.New(cfg.Redis)
    if err != nil {
        return nil, fmt.Errorf("redis: %w", err)
    }
    mgr.AddStopper("redis", lifecycle.StopFunc(func() {
        if err := rdb.Close(); err != nil {
            slog.Warn("close redis", "error", err)
        }
    }))
    return rdb, nil
}

func resolveGID(cfg *config.Config, injected thirdcall.GIDService, mgr *lifecycle.Manager) (thirdcall.GIDService, error) {
    if injected != nil {
        return injected, nil
    }
    if cfg.ThirdParty == nil || cfg.ThirdParty.GID == nil {
        return nil, fmt.Errorf("third_party.gid: not configured")
    }
    svc, err := thirdcall.NewGIDService(cfg.ThirdParty.GID)
    if err != nil {
        return nil, fmt.Errorf("init gid-service: %w", err)
    }
    if closer, ok := svc.(interface{ Close() error }); ok {
        mgr.AddStopper("gid", lifecycle.StopFunc(func() {
            if err := closer.Close(); err != nil {
                slog.Warn("close gid", "error", err)
            }
        }))
    }
    return svc, nil
}
```

Delete `runCleanup` — no longer needed.

- [ ] **Step 4: Build + test**

```bash
go build ./...
go test ./internal/service/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/service.go
git commit -m "refactor: replace ownDB/ownRedis with lifecycle.Manager"
```

### Task 5.2: Wire handler Start/Stop to service

**Files:**
- Modify: `pkg/handler/user.go`

- [ ] **Step 1: Update Start/Stop**

```go
func (h *Handler) Start() error { return h.svc.Start() }
func (h *Handler) Stop() error  { return h.svc.Stop() }
```

Remove the `Svc()` method (no longer needed; middleware uses `service.SessionMgr()` directly via server wiring).

- [ ] **Step 2: Update pkg/server.go**

```go
// Start starts the service and the gRPC server. Rolls back svc.Start on
// grpcSrv.Start failure.
func (s *Server) Start() error {
    if err := s.svc.Start(); err != nil {
        return err
    }
    if err := s.grpcSrv.Start(); err != nil {
        return errors.Join(err, s.svc.Stop())
    }
    return nil
}

func (s *Server) Stop() error {
    return errors.Join(s.grpcSrv.Stop(), s.svc.Stop())
}
```

Update `Run()` to use `Start()` + signal handling. Reference `grpcx.Server.Run()` — it already does signal handling. Either:
- Option A: keep `Run()` calling `grpcSrv.Run()` and separately start/stop svc. Cleaner is to call `signalx.Run(s)` where `s` is the `*Server` (now satisfying `signalx.Service`).
- Option B: have `*Server` implement `signalx.Service` and call `signalx.RunWithForceQuit(s)`.

Go with Option B. Update Server to satisfy `signalx.Service`:

```go
var _ signalx.Service = (*Server)(nil)
```

`main.go` then calls `signalx.RunWithForceQuit(srv)`:

```go
// cmd/server/main.go
if err := signalx.RunWithForceQuit(srv); err != nil {
    slog.Error("run server", "error", err)
    os.Exit(1)
}
```

This requires `pkg/server.go` to remove its own `Run()` method or keep it as a thin wrapper. Prefer the latter for backwards compat:

```go
// Run starts the server and blocks until SIGTERM/SIGINT.
func (s *Server) Run() {
    if err := signalx.RunWithForceQuit(s); err != nil {
        slog.Error("run server", "error", err)
        os.Exit(1)
    }
}
```

- [ ] **Step 3: Build + test**

```bash
go build ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/handler/user.go pkg/server.go cmd/server/main.go
git commit -m "refactor: wire handler/server Start/Stop to service lifecycle"
```

### Task 5.3: Create internal/jobs/jobs.go

**Files:**
- Create: `internal/jobs/jobs.go`
- Create: `internal/jobs/jobs_test.go`

- [ ] **Step 1: Write the Scheduler**

```go
// Package jobs provides a cron-driven task scheduler that integrates with
// lifecycle.Manager. The scheduler itself is task-agnostic — business code
// registers jobs via AddFunc.
package jobs

import (
    "errors"
    "fmt"
    "sync"

    "github.com/servekit/go-common/cronx"
    "github.com/robfig/cron/v3"
)

// Scheduler wraps *cron.Cron and implements lifecycle.Service.
type Scheduler struct {
    cron     *cron.Cron
    ownsCron bool
    mu       sync.Mutex
    started  bool
}

// Deps configures a Scheduler. Either Config (scheduler will create cron) or
// Cron (caller-managed) must be non-nil.
type Deps struct {
    Config *cronx.Config  // used when Cron is nil
    Cron   *cron.Cron     // optional: caller-managed cron
}

// New constructs a Scheduler. When Deps.Cron is nil, the scheduler creates its
// own cron from Config and manages its lifecycle.
func New(d *Deps) (*Scheduler, error) {
    if d == nil {
        return nil, errors.New("jobs: nil deps")
    }
    if d.Cron != nil {
        return &Scheduler{cron: d.Cron, ownsCron: false}, nil
    }
    if d.Config == nil {
        return nil, errors.New("jobs: nil config when cron not injected")
    }
    c, err := cronx.New(d.Config)
    if err != nil {
        return nil, fmt.Errorf("jobs: init cron: %w", err)
    }
    return &Scheduler{cron: c, ownsCron: true}, nil
}

// AddFunc registers a cron job. Must be called before Start.
func (s *Scheduler) AddFunc(spec string, cmd func()) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.started {
        return errors.New("jobs: cannot AddFunc after Start")
    }
    if _, err := s.cron.AddFunc(spec, cmd); err != nil {
        return fmt.Errorf("jobs: add func %q: %w", spec, err)
    }
    return nil
}

// Start starts the underlying cron if owned; otherwise no-op.
func (s *Scheduler) Start() error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if !s.ownsCron || s.started {
        return nil
    }
    s.cron.Start()
    s.started = true
    return nil
}

// Stop stops the underlying cron if owned; otherwise no-op. Returns nil
// (cron.Stop runs its own wait goroutine; lifecycle.Manager doesn't propagate
// the error meaningfully).
func (s *Scheduler) Stop() error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if !s.ownsCron || !s.started {
        return nil
    }
    s.cron.Stop()
    s.started = false
    return nil
}
```

- [ ] **Step 2: Write test**

```go
package jobs_test

import (
    "testing"
    "time"

    "github.com/servekit/go-common/cronx"

    "user-service/internal/jobs"
)

func TestScheduler_StartStop(t *testing.T) {
    s, err := jobs.New(&jobs.Deps{
        Config: &cronx.Config{Timezone: "UTC", WithSeconds: true},
    })
    if err != nil { t.Fatalf("New: %v", err) }

    if err := s.Start(); err != nil { t.Fatalf("Start: %v", err) }
    // Idempotent
    if err := s.Start(); err != nil { t.Fatalf("Start (2nd): %v", err) }
    if err := s.Stop(); err != nil { t.Fatalf("Stop: %v", err) }
    if err := s.Stop(); err != nil { t.Fatalf("Stop (2nd): %v", err) }
}

func TestScheduler_AddFunc_AfterStart_Fails(t *testing.T) {
    s, _ := jobs.New(&jobs.Deps{Config: &cronx.Config{Timezone: "UTC"}})
    _ = s.Start()
    defer s.Stop()

    if err := s.AddFunc("* * * * * *", func() {}); err == nil {
        t.Error("AddFunc after Start should fail")
    }
}
```

Note: this test triggers the go.sum issue with `robfig/cron/v3`. If the build fails, this is the pre-existing `cronx` problem — Phase 5 is allowed to add the missing go.sum entry locally:

```bash
GOPROXY=https://goproxy.cn,direct go mod tidy
```

- [ ] **Step 3: Run test**

```bash
go test ./internal/jobs/... -v
```

Expected: PASS (after go.sum is fixed).

- [ ] **Step 4: Commit**

```bash
git add internal/jobs/ go.mod go.sum
git commit -m "feat: add internal/jobs scheduler with lifecycle.Service impl"
```

### Task 5.4: Add CronConfig to pkg/config

**Files:**
- Modify: `pkg/config/config.go`
- Modify: `config.example.yaml`

- [ ] **Step 1: Add CronConfig**

```go
type Config struct {
    // ... existing fields ...
    Cron *CronConfig
}

// CronConfig holds cron scheduler settings. Per-task specs live in their
// owning domain config (e.g. cfg.Session.ReapCronSpec), not here.
type CronConfig struct {
    Timezone string `default:"Asia/Shanghai"`
}
```

- [ ] **Step 2: Update config.example.yaml**

```yaml
cron:
  timezone: "Asia/Shanghai"
```

- [ ] **Step 3: Commit**

```bash
git add pkg/config/config.go config.example.yaml
git commit -m "feat: add CronConfig for jobs scheduler"
```

### Task 5.5: Wire setupJobs() into Service.New

**Files:**
- Modify: `internal/service/service.go`

- [ ] **Step 1: Add setupJobs method**

```go
import (
    "user-service/internal/jobs"
    "github.com/servekit/go-common/cronx"
    // ...
)

func (s *Service) setupJobs() error {
    tz := "Asia/Shanghai"
    if s.cfg.Cron != nil && s.cfg.Cron.Timezone != "" {
        tz = s.cfg.Cron.Timezone
    }
    scheduler, err := jobs.New(&jobs.Deps{
        Config: &cronx.Config{Timezone: tz, OverlapPolicy: "skip"},
    })
    if err != nil {
        return fmt.Errorf("init jobs: %w", err)
    }
    // Register periodic tasks here. Current state: no tasks.
    // Example (commented out):
    //   if err := scheduler.AddFunc(s.cfg.Session.ReapCronSpec, func() {
    //       ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    //       defer cancel()
    //       if err := s.sess.ReapExpired(ctx); err != nil { slog.Error("reap sessions", "error", err) }
    //   }); err != nil { return fmt.Errorf("register reap: %w", err) }

    s.mgr.Add("jobs", scheduler)
    return nil
}
```

- [ ] **Step 2: Call setupJobs in New()**

```go
svc := &Service{
    cfg: cfg, mgr: mgr,
    // ... fields ...
}

if err := svc.setupJobs(); err != nil {
    if cerr := mgr.Stop(); cerr != nil {
        err = errors.Join(err, fmt.Errorf("rollback: %w", cerr))
    }
    return nil, err
}
return svc, nil
```

- [ ] **Step 3: Build + test**

```bash
go build ./...
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/service/service.go
git commit -m "feat: wire setupJobs into Service.New"
```

### Task 5.6: Verify graceful shutdown

- [ ] **Step 1: Start server in background**

```bash
make run &
SERVER_PID=$!
sleep 2
```

- [ ] **Step 2: Send SIGTERM, verify clean exit**

```bash
kill -TERM $SERVER_PID
wait $SERVER_PID
echo "Exit code: $?"
```

Expected: exit code 0. Log output shows graceful shutdown messages (db closed, redis closed, gRPC stopped).

- [ ] **Step 3: Commit if any fixes needed**

```bash
git add -A
git commit -m "fix: phase 5 graceful shutdown adjustments" --allow-empty
```

### Task 5.7: Phase 5 acceptance

- [ ] **Step 1: Full build + vet + lint + test**

```bash
go build ./...
go vet ./...
go test -race ./...
```

Expected: PASS.

- [ ] **Step 2: Verify in-process module lifecycle**

Write a quick check in `internal/service/service_test.go`:

```go
func TestService_StartStop_Idempotent(t *testing.T) {
    svc, err := service.New(testConfig(), option.WithDB(testutil.SetupDB(t)), option.WithRedis(testutil.SetupRedis(t)), option.WithGIDService(testutil.StubGID()))
    if err != nil { t.Fatalf("New: %v", err) }
    if err := svc.Start(); err != nil { t.Fatalf("Start: %v", err) }
    if err := svc.Start(); err != nil { t.Fatalf("Start (2nd): %v", err) }
    if err := svc.Stop(); err != nil { t.Fatalf("Stop: %v", err) }
    if err := svc.Stop(); err != nil { t.Fatalf("Stop (2nd): %v", err) }
}
```

Expected: PASS.

- [ ] **Step 3: Final smoke test**

```bash
./scripts/smoke.sh
```

Expected: PASS.

---

## Final Phase: Project-level acceptance

### Task F.1: Add .golangci.yml

**Files:**
- Create: `.golangci.yml`

- [ ] **Step 1: Copy template, customize local-prefixes**

```bash
cp /Users/moss/code/base/ai-kit-studio/skills/golang-development/.golangci.yml .golangci.yml
```

Edit `.golangci.yml`:
- Set `issues.exclude-dirs` if needed
- Set `linters-settings.decorder` to enforce declaration order (per skill §7)

- [ ] **Step 2: Run lint**

```bash
golangci-lint run ./...
```

Expected: PASS (fix any issues surfaced).

- [ ] **Step 3: Commit**

```bash
git add .golangci.yml
git commit -m "feat: add .golangci.yml with decorder rule"
```

### Task F.2: Verify Makefile targets

- [ ] **Step 1: Confirm all required targets exist**

Required per skill §7: `build`, `run`, `test`, `lint`, `fmt`, `vet`, `generate`, `proto`, `migrate`, `tidy`.

```bash
make -n build run test lint fmt vet generate proto migrate tidy
```

Expected: all targets print their commands without "No rule to make target" errors.

- [ ] **Step 2: Commit if Makefile needed updates**

```bash
git add Makefile
git commit -m "build: ensure Makefile has all required targets" --allow-empty
```

### Task F.3: Final acceptance checklist

Run through each item from spec §最终验收清单:

- [ ] `go build ./...` passes
- [ ] `golangci-lint run ./...` clean
- [ ] `make proto && git diff --exit-code` (gen is committed in sync)
- [ ] `make generate && git diff --exit-code` (gen is committed in sync)
- [ ] `make migrate` runs on clean DB
- [ ] grpcurl smoke test passes for all RPCs
- [ ] HTTP gateway smoke test passes
- [ ] in-process module test passes
- [ ] Every RPC has a corresponding facade method in `service.go`
- [ ] Every domain is in `internal/service/<domain>/` subpackage; root has only `service.go`
- [ ] Periodic task entry points are in `internal/jobs/` + `setupJobs()`
- [ ] Models all use explicit ID/CreatedAt/UpdatedAt/DeletedAt, no embedded types
- [ ] Model naming follows `User*` prefix (business tables) or RBAC business semantic + `rbac_` table prefix
- [ ] Resource management uses `lifecycle.Manager`, no `ownXxx bool` remaining

Verify each with concrete command:

```bash
# No ownDB/ownRedis remains
grep -rn "ownDB\|ownRedis" internal/ pkg/ cmd/
# Expected: no matches

# No embedded base types in models
grep -rn "SnowflakeModelWithDeleted\|BaseModelWithDeleted\|CreatedAtModel" internal/store/
# Expected: no matches

# No flat service files in internal/service/
ls internal/service/
# Expected: only service.go and subpackage dirs

# Every RPC has facade method
grep -c "^func (s \*Service)" internal/service/service.go
# Expected: 30+ (one per RPC, plus SessionMgr, Start, Stop)
```

- [ ] **Step 1: Final commit**

```bash
git add -A
git commit -m "refactor: complete skill alignment — all 5 phases done" --allow-empty
```

### Task F.4: Sync design + plan to Obsidian

Per `~/CLAUDE.md` doc-sync rules, sync the design spec and plan to Obsidian vault.

- [ ] **Step 1: Sync design spec**

```bash
# Create directory structure in vault
obsidian-cli vault=only create name="skill-alignment-refactor" content="$(cat docs/superpowers/specs/2026-06-21-skill-alignment-refactor-design.md)"
obsidian-cli vault=only move file="skill-alignment-refactor" to="services/user-service/design/v1/"
```

- [ ] **Step 2: Sync plan**

```bash
obsidian-cli vault=only create name="skill-alignment-refactor-plan" content="$(cat docs/superpowers/plans/2026-06-21-skill-alignment-refactor-plan.md)"
obsidian-cli vault=only move file="skill-alignment-refactor-plan" to="services/user-service/plan/v1/"
```

- [ ] **Step 3: Update index.md and changes.md**

Append entries to `services/index.md` and `services/changes.md` per the global CLAUDE.md format rules.

---

## Summary

This plan implements the full skill-alignment refactor of user-service in 5 phases plus a final cleanup phase:

| Phase | Tasks | Description |
|-------|-------|-------------|
| 1 | 1.1 – 1.5 | pkg/handler thin shell + server/module swap |
| 2 | 2.1 – 2.10 | Split internal/service into 5 domain subpackages + service.go facade |
| 3 | 3.1 – 3.11 | Rename repository → dal with functional API |
| 4 | 4.1 – 4.9 | Rewrite models with explicit fields + User prefix; regen generated |
| 5 | 5.1 – 5.7 | lifecycle.Manager + internal/jobs skeleton |
| F | F.1 – F.4 | .golangci.yml, Makefile targets, final acceptance, Obsidian sync |

Each task is a self-contained, commit-able unit. Most follow the TDD pattern: write failing test → verify it fails → implement → verify it passes → commit. Pure code moves (Phase 2 subpackages) use the existing smoke test as regression. Each phase ends with `go build && go vet && go test && grpcurl smoke`.

Total commit count: ~30-35 commits across 5 phases.
