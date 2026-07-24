# Plan 3: Proto & gRPC Skeleton

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 创建完整的 Proto 定义文件，生成 gRPC + grpc-gateway 代码，搭建可启动的服务骨架。

**Architecture:** Proto 文件定义所有 RPC 和消息类型。protoc 生成 Go 代码到 gen/ 目录。internal/service/ 提供 UserService 的桩实现（返回 Unimplemented）。cmd/server/ 启动 gRPC server + grpc-gateway。

**Tech Stack:** Go, protobuf, gRPC, grpc-gateway, protoc-gen-go, protoc-gen-go-grpc

**Depends on:** Plan 1 (Foundation)

**Produces:** 可启动的 gRPC + HTTP 服务，所有 RPC 都有桩实现，健康检查可用。

**Spec:** `docs/superpowers/specs/2026-05-22-user-service-design.md` §5 (Proto), §3 (Project Structure)

---

## File Structure

```
api/proto/user/
  user.proto                        # 完整 Proto 定义
gen/
  user/                             # protoc 生成输出
    user.pb.go
    user_grpc.pb.go
    user.pb.gw.go
internal/service/
  server.go                         # gRPC server + gateway 启动/停止
  user_service.go                   # UserService 桩实现
cmd/server/
  main.go                           # 最小启动入口（后续 Plan 6 完善）
Makefile                            # 更新 proto target
```

---

### Task 1: Add Proto Dependencies

- [ ] **Step 1: Install protoc toolchain (macOS)**

```bash
brew install protobuf
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@latest
```

- [ ] **Step 2: Add Go dependencies**

```bash
go get google.golang.org/grpc
go get google.golang.org/protobuf
go get github.com/grpc-ecosystem/grpc-gateway/v2
go get google.golang.org/genproto/googleapis/api
go get google.golang.org/genproto/googleapis/rpc
```

- [ ] **Step 3: Verify tools are available**

Run: `which protoc && protoc --version && which protoc-gen-go`
Expected: all found

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "feat: add proto and gRPC dependencies"
```

---

### Task 2: Create Proto File

**Files:**
- Create: `api/proto/user/user.proto`

- [ ] **Step 1: Create directory**

```bash
mkdir -p api/proto/user
```

- [ ] **Step 2: Write user.proto**

完整 proto 定义，基于设计文档 §5，包含以下变更：
- User message 移除 `registered_at`（field 15）
- Identity message `bound_at` → `created_at`（field 5）
- GroupMember message `joined_at` → `created_at`（field 5）

从设计文档 §5 复制完整 proto 内容，应用上述变更。包含：
- 3 个枚举：UserStatus, Gender, IdentityProvider
- UserService service（~35 RPCs）
- 所有 message 类型（认证、用户、身份、OAuth、验证码、会话、RBAC）

**注意：proto 文件内容直接取自设计文档 §5（第 452-1038 行），只需做上述 3 个字段变更。**

- [ ] **Step 3: Verify proto compiles**

Run: `protoc --go_out=. --go_opt=paths=source_relative api/proto/user/user.proto`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add api/proto/user/user.proto
git commit -m "feat: add user service proto definition"
```

---

### Task 3: Generate Code & Update Makefile

**Files:**
- Generate: `gen/user/` (所有生成文件)
- Modify: `Makefile`

- [ ] **Step 1: Create gen output directory**

```bash
mkdir -p gen/user
```

- [ ] **Step 2: Update Makefile proto target**

```makefile
## proto: Generate protobuf code
proto:
	protoc \
		-I api/proto/user \
		-I $(shell go env GOMODCACHE)/github.com/grpc-ecosystem/grpc-gateway/v2@$(shell go list -m -f '{{.Version}}' github.com/grpc-ecosystem/grpc-gateway/v2) \
		--go_out=gen/user --go_opt=paths=source_relative \
		--go-grpc_out=gen/user --go-grpc_opt=paths=source_relative \
		--grpc-gateway_out=gen/user --grpc-gateway_opt=paths=source_relative \
		api/proto/user/user.proto
```

- [ ] **Step 3: Run make proto**

Run: `make proto`
Expected: `gen/user/user.pb.go`, `gen/user/user_grpc.pb.go`, `gen/user/user.pb.gw.go` created

- [ ] **Step 4: Verify generated code compiles**

Run: `go build ./gen/user/...`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add gen/ Makefile
git commit -m "feat: generate protobuf code and update Makefile"
```

---

### Task 4: gRPC Server + Gateway Skeleton

**Files:**
- Create: `internal/service/server.go`
- Create: `internal/service/user_service.go`
- Create: `cmd/server/main.go`

- [ ] **Step 1: Write internal/service/server.go**

```go
package service

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"go-common/xerr/xcodes" // used in error handler below
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// Server wraps gRPC server and grpc-gateway HTTP proxy.
type Server struct {
	grpcAddr   string
	gatewayAddr string
	grpcServer  *grpc.Server
	healthServer *health.Server
	logger      *slog.Logger
}

// NewServer creates a new Server.
func NewServer(grpcAddr, gatewayAddr string, logger *slog.Logger) *Server {
	return &Server{
		grpcAddr:    grpcAddr,
		gatewayAddr: gatewayAddr,
		logger:      logger,
	}
}

// Start starts the gRPC server and grpc-gateway.
func (s *Server) Start(ctx context.Context) error {
	s.grpcServer = grpc.NewServer()
	s.healthServer = health.NewServer()
	grpc_health_v1.RegisterHealthServer(s.grpcServer, s.healthServer)
	s.healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	// Register services here (done in Plan 6 wiring)
	// user.RegisterUserServiceServer(s.grpcServer, userService)

	// Start gRPC listener
	lis, err := net.Listen("tcp", s.grpcAddr)
	if err != nil {
		return fmt.Errorf("listen gRPC %s: %w", s.grpcAddr, err)
	}

	go func() {
		s.logger.Info("gRPC server listening", "addr", s.grpcAddr)
		if err := s.grpcServer.Serve(lis); err != nil {
			s.logger.Error("gRPC server stopped", "error", err)
		}
	}()

	// Start grpc-gateway
	go func() {
		if err := s.startGateway(ctx); err != nil {
			s.logger.Error("gateway stopped", "error", err)
		}
	}()

	return nil
}

func (s *Server) startGateway(ctx context.Context) error {
	mux := runtime.NewServeMux(
		runtime.WithErrorHandler(func(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
			// Convert xerr errors to proper HTTP status codes via xcodes
			_ = xcodes.ErrInternal // reference to avoid unused import
			s.logger.Debug("gateway error", "error", err)
			runtime.DefaultHTTPErrorHandler(ctx, mux, marshaler, w, r, err)
		}),
	)

	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	// Register gateway handlers here (done in Plan 6)
	// err := user.RegisterUserServiceHandlerFromEndpoint(ctx, mux, s.grpcAddr, opts)

	conn, err := grpc.DialContext(ctx, s.grpcAddr, opts...)
	if err != nil {
		return fmt.Errorf("dial gRPC for gateway: %w", err)
	}
	defer conn.Close()

	s.logger.Info("gateway listening", "addr", s.gatewayAddr)
	return http.ListenAndServe(s.gatewayAddr, mux)
}

// Stop gracefully stops the server.
func (s *Server) Stop() {
	s.logger.Info("stopping server...")
	s.healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	s.grpcServer.GracefulStop()
}
```

- [ ] **Step 2: Write internal/service/user_service.go**

桩实现，所有 RPC 返回 Unimplemented：

```go
package service

import (
	"context"

	pb "user-service/gen/user"
)

// UserService implements pb.UserServiceServer.
// Methods will be implemented in Plans 4-5.
type UserService struct {
	pb.UnimplementedUserServiceServer
}

func NewUserService() *UserService {
	return &UserService{}
}

// Ensure UserService implements the interface at compile time.
var _ pb.UserServiceServer = (*UserService)(nil)
```

- [ ] **Step 3: Write cmd/server/main.go (minimal skeleton)**

```go
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"user-service/internal/config"
	"user-service/internal/service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load("config.yaml")
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}

	srv := service.NewServer(cfg.Server.GRPC.Addr, cfg.Server.Gateway.Addr, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		logger.Error("start server", "error", err)
		os.Exit(1)
	}

	// Wait for interrupt signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	logger.Info("received signal, shutting down", "signal", sig)

	srv.Stop()
	logger.Info("server stopped")
}
```

- [ ] **Step 4: Verify compilation**

Run: `go build ./cmd/server/`
Expected: binary builds successfully

- [ ] **Step 5: Commit**

```bash
git add internal/service/ cmd/server/
git commit -m "feat: add gRPC server and gateway skeleton with health check"
```

---

### Task 5: Verify End-to-End

- [ ] **Step 1: Start server**

Run: `go run ./cmd/server/ &`
Expected: logs show "gRPC server listening :9000" and "gateway listening :8080"

- [ ] **Step 2: Health check via gRPC**

Run: `grpcurl -plaintext localhost:9000 grpc.health.v1.Health/Check`
Expected: `{"status": "SERVING"}`

- [ ] **Step 3: Health check via HTTP gateway**

Run: `curl http://localhost:8080/health`
Expected: health check response

- [ ] **Step 4: Stop server**

Run: `kill %1`

- [ ] **Step 5: Run go vet and lint**

```bash
go vet ./...
golangci-lint run ./...
```
Expected: no errors

- [ ] **Step 6: Commit any fixes**

```bash
git add -A
git commit -m "chore: verify server starts and health check works"
```

---

## Self-Review

### Spec Coverage
| Spec Section | Task |
|---|---|
| §5 Proto definition | Task 2 |
| §3 gen/ output | Task 3 |
| gRPC :9000 + gateway :8080 | Task 4 |
| Health check | Task 5 |

### Placeholder Scan
Task 2 references the proto from design doc §5 — the executor should copy and modify. All Go code is complete.

### Type Consistency
Server constructor takes (grpcAddr, gatewayAddr, logger) matching config.yaml fields. UserService embeds UnimplementedUserServiceServer.
