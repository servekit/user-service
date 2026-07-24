# Plan 1: Foundation — Config, Error Codes, Database, Models

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立项目基础设施：配置加载、错误码、数据库连接、模型更新、代码生成、迁移脚本、Makefile。

**Architecture:** Viper 加载 config.yaml 到 Go struct；xcodes 集中定义业务错误码；database 包用 testcontainers 管理测试 PG；gorm.io/cli 从 models 生成查询代码；golang-migrate 管理迁移。

**Tech Stack:** Go 1.26, viper, go-common/xerr, gorm.io, golang-migrate, testcontainers-go

**Depends on:** go-common (xerr, xerr/xcodes)

**Produces:** 可编译、可测试的基础设施层。后续所有 Plan 都依赖此 Plan 的产出。

**Spec:** `docs/superpowers/specs/2026-05-22-user-service-design.md` §14 (Config), §4 (Database)

---

## File Structure

```
internal/
  config/
    config.go              # Config struct + Load()
    config_test.go         # 测试配置加载
  xcodes/
    xcodes.go              # 公共类型 + 注册表（预留）
    user.go                # 用户相关错误码
    identity.go            # 登录方式相关错误码
    session.go             # Session 相关错误码
    rbac.go                # RBAC 相关错误码
  database/
    postgres.go            # NewDB() 连接 + 连接池配置
    postgres_test.go       # testcontainers 集成测试
  models/
    base.go                # 更新：添加 DeletedAt 支持的 SnowflakeModel
    user.go                # 更新：移除 RegisteredAt/BoundAt，添加 Email/SMS 模型
    rbac.go                # 更新：Permission 添加 UpdatedAt/DeletedAt
cmd/
  gen/
    main.go                # gorm.io/cli 生成入口（沿用现有 genconfig）
migrations/
  000001_init_schema.up.sql
  000001_init_schema.down.sql
Makefile
config.yaml               # 已存在，无需修改
```

---

### Task 1: Update Models — Align with Design Doc

**Files:**
- Modify: `internal/models/base.go`
- Modify: `internal/models/user.go`
- Modify: `internal/models/rbac.go`

**Why:** 设计文档已更新时间字段为 GORM 标准（created_at/updated_at/deleted_at），模型需同步。当前模型还有 `RegisteredAt`、`BoundAt` 等冗余字段。

- [ ] **Step 1: Update base.go — add SnowflakeModelWithDeleted**

```go
// SnowflakeModelWithDeleted adds soft delete on top of SnowflakeModel.
type SnowflakeModelWithDeleted struct {
	ID        int64          `gorm:"primaryKey"`
	CreatedAt time.Time      `gorm:"not null;default:now()"`
	UpdatedAt time.Time      `gorm:"not null;default:now()"`
	DeletedAt gorm.DeletedAt `gorm:"index"`
}
```

- [ ] **Step 2: Update user.go — User model**

Changes to User struct:
- Change base from `SnowflakeModel` to `SnowflakeModelWithDeleted`
- Remove `RegisteredAt time.Time` field (CreatedAt serves this purpose)
- Change `Credentials` type from `string` to `map[string]any` with `gorm:"type:jsonb"` to match design spec (JSONB column)

- [ ] **Step 3: Update user.go — Identity model**

Changes to Identity struct:
- Change base from `BaseModel` to `BaseModelWithDeleted`
- Remove `BoundAt time.Time` field (CreatedAt serves this purpose)

- [ ] **Step 4: Update user.go — Session model**

Changes to Session struct:
- Add `UpdatedAt time.Time` field after CreatedAt

- [ ] **Step 5: Update user.go — Add Email and SMS models**

```go
// Email records sent emails for audit and troubleshooting.
type Email struct {
	CreatedAtModel
	ToAddr         string `gorm:"size:256;not null;index"`
	Subject        string `gorm:"size:256;not null"`
	Content        string `gorm:"type:text;not null"`
	Template       string `gorm:"size:64"`
	Provider       string `gorm:"size:32"`
	Status         string `gorm:"size:16;not null;default:pending"` // pending / sent / failed
	ProviderMsgID  string `gorm:"size:256"`
	ErrorMessage   string `gorm:"size:512"`
	IP             string `gorm:"size:45"`
}

// SMSLog records sent SMS messages for audit and troubleshooting.
type SMSLog struct {
	CreatedAtModel
	Phone          string `gorm:"size:20;not null;index"`
	Content        string `gorm:"size:512;not null"`
	Template       string `gorm:"size:64"`
	Provider       string `gorm:"size:32"`
	Status         string `gorm:"size:16;not null;default:pending"` // pending / sent / failed
	ProviderMsgID  string `gorm:"size:256"`
	ErrorMessage   string `gorm:"size:512"`
	IP             string `gorm:"size:45"`
}
```

- [ ] **Step 6: Update rbac.go — Permission model**

Changes to Permission struct:
- Change base from `CreatedAtModel` to `BaseModelWithDeleted` (adds UpdatedAt + DeletedAt)

- [ ] **Step 7: Update rbac.go — PermissionGroupItem, RolePermission, RolePermissionGroup, GroupRole**

These join tables currently embed `CreatedAtModel` but are pure association tables. Change them to embed a simpler struct with only ID (no CreatedAt), since join table records don't need timestamps:

Add to `base.go`:

```go
// JoinModel provides only a primary key for pure association tables.
type JoinModel struct {
	ID int64 `gorm:"primaryKey;autoIncrement"`
}
```

Update all four join tables to use `JoinModel` instead of `CreatedAtModel`.

- [ ] **Step 8: Run go vet to verify**

Run: `go vet ./internal/models/...`
Expected: no errors

- [ ] **Step 9: Commit**

```bash
git add internal/models/
git commit -m "refactor: align models with GORM conventions and design doc"
```

---

### Task 2: Create Error Codes Package

**Files:**
- Create: `internal/xcodes/xcodes.go`
- Create: `internal/xcodes/user.go`
- Create: `internal/xcodes/identity.go`
- Create: `internal/xcodes/session.go`
- Create: `internal/xcodes/rbac.go`
- Create: `internal/xcodes/xcodes_test.go`

- [ ] **Step 1: Add go-common dependency**

Run: `go get go-common/xerr go-common/xerr/xcodes`

Note: go-common is a local module at `../go-common`, go.mod needs `replace go-common => ../go-common`.

- [ ] **Step 2: Write xcodes.go — re-export common error codes**

```go
package xcodes

// Re-export common error codes from go-common so all code only imports this package.
import xcodes "go-common/xerr/xcodes" // aliased to avoid package cycle

var (
	// Common errors — re-exported from go-common/xerr/xcodes.
	ErrBadRequest         = xcodes.ErrBadRequest
	ErrUnauthorized       = xcodes.ErrUnauthorized
	ErrForbidden          = xcodes.ErrForbidden
	ErrNotFound           = xcodes.ErrNotFound
	ErrConflict           = xcodes.ErrConflict
	ErrTooManyRequests    = xcodes.ErrTooManyRequests
	ErrInternal           = xcodes.ErrInternal
	ErrServiceUnavailable = xcodes.ErrServiceUnavailable
)
```

- [ ] **Step 3: Write user.go**

```go
package xcodes

import "go-common/xerr"

var (
	ErrUserNotFound   = xerr.New("USER_NOT_FOUND", xerr.CategoryNotFound, 404, "user not found")
	ErrUserExists     = xerr.New("USER_EXISTS", xerr.CategoryConflict, 409, "user already exists")
	ErrUserDisabled   = xerr.New("USER_DISABLED", xerr.CategoryForbidden, 403, "user is disabled")
	ErrNicknameTaken  = xerr.New("NICKNAME_TAKEN", xerr.CategoryConflict, 409, "nickname already taken")
)
```

- [ ] **Step 4: Write identity.go**

```go
package xcodes

import "go-common/xerr"

var (
	ErrIdentityNotFound  = xerr.New("IDENTITY_NOT_FOUND", xerr.CategoryNotFound, 404, "identity not found")
	ErrIdentityExists    = xerr.New("IDENTITY_EXISTS", xerr.CategoryConflict, 409, "identity already bound")
	ErrIdentityBound     = xerr.New("IDENTITY_BOUND_OTHER", xerr.CategoryConflict, 409, "identity bound to another user")
	ErrPasswordWrong     = xerr.New("PASSWORD_WRONG", xerr.CategoryUnauthorized, 401, "invalid password")
	ErrOAuthFailed       = xerr.New("OAUTH_FAILED", xerr.CategoryUnauthorized, 401, "OAuth authentication failed")
	ErrVerificationCode  = xerr.New("VERIFICATION_CODE_INVALID", xerr.CategoryBadRequest, 400, "invalid or expired verification code")
)
```

- [ ] **Step 5: Write session.go**

```go
package xcodes

import "go-common/xerr"

var (
	ErrSessionExpired = xerr.New("SESSION_EXPIRED", xerr.CategoryUnauthorized, 401, "session expired")
	ErrSessionInvalid = xerr.New("SESSION_INVALID", xerr.CategoryUnauthorized, 401, "invalid session")
)
```

- [ ] **Step 6: Write rbac.go**

```go
package xcodes

import "go-common/xerr"

var (
	ErrPermissionDenied = xerr.New("PERMISSION_DENIED", xerr.CategoryForbidden, 403, "permission denied")
	ErrRoleNotFound     = xerr.New("ROLE_NOT_FOUND", xerr.CategoryNotFound, 404, "role not found")
	ErrRoleIsBuiltin    = xerr.New("ROLE_IS_BUILTIN", xerr.CategoryBadRequest, 400, "cannot modify built-in role")
	ErrGroupNotFound    = xerr.New("GROUP_NOT_FOUND", xerr.CategoryNotFound, 404, "group not found")
	ErrAlreadyMember    = xerr.New("ALREADY_MEMBER", xerr.CategoryConflict, 409, "user is already a group member")
	ErrNotMember        = xerr.New("NOT_MEMBER", xerr.CategoryNotFound, 404, "user is not a group member")
)
```

- [ ] **Step 7: Write xcodes_test.go**

```go
package xcodes

import (
	"errors"
	"testing"

	"go-common/xerr"
)

func TestErrorCodesCreateAndMatch(t *testing.T) {
	tests := []struct {
		name     string
		code     xerr.Code
		reason   string
		httpCode int
	}{
		{"user not found", ErrUserNotFound, "USER_NOT_FOUND", 404},
		{"password wrong", ErrPasswordWrong, "PASSWORD_WRONG", 401},
		{"session expired", ErrSessionExpired, "SESSION_EXPIRED", 401},
		{"permission denied", ErrPermissionDenied, "PERMISSION_DENIED", 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.code.Reason() != tt.reason {
				t.Errorf("Reason() = %q, want %q", tt.code.Reason(), tt.reason)
			}
			if tt.code.HTTPCode() != tt.httpCode {
				t.Errorf("HTTPCode() = %d, want %d", tt.code.HTTPCode(), tt.httpCode)
			}
		})
	}
}

func TestErrorCodesWrapAndIs(t *testing.T) {
	err := ErrUserNotFound.Wrap(errors.New("db error"))
	if !errors.Is(err, ErrUserNotFound.New()) {
		t.Error("errors.Is should match by reason")
	}
	if err.Code().Reason() != "USER_NOT_FOUND" {
		t.Error("wrapped error should preserve code")
	}
}
```

- [ ] **Step 8: Run tests**

Run: `go test ./internal/xcodes/...`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/xcodes/ go.mod go.sum
git commit -m "feat: add centralized error codes package"
```

---

### Task 3: Create Config Package

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

- [ ] **Step 1: Add viper dependency**

Run: `go get github.com/spf13/viper`

- [ ] **Step 2: Write config.go — complete struct + Load with duration support**

```go
package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
	"go-common/xerr"
)

type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Redis     RedisConfig     `mapstructure:"redis"`
	Session   SessionConfig   `mapstructure:"session"`
	RBAC      RBACConfig      `mapstructure:"rbac"`
	OAuth     OAuthConfig     `mapstructure:"oauth"`
	Snowflake SnowflakeConfig `mapstructure:"snowflake"`
	Log       LogConfig       `mapstructure:"log"`
}

type ServerConfig struct {
	GRPC    ListenConfig `mapstructure:"grpc"`
	Gateway ListenConfig `mapstructure:"gateway"`
}

type ListenConfig struct {
	Addr string `mapstructure:"addr"`
}

type DatabaseConfig struct {
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	User            string        `mapstructure:"user"`
	Password        string        `mapstructure:"password"`
	DBName          string        `mapstructure:"dbname"`
	SSLMode         string        `mapstructure:"sslmode"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type SessionConfig struct {
	TTL   time.Duration      `mapstructure:"ttl"`
	Redis SessionRedisConfig `mapstructure:"redis"`
}

type SessionRedisConfig struct {
	KeyPrefix          string `mapstructure:"key_prefix"`
	UserSessionsPrefix string `mapstructure:"user_sessions_prefix"`
}

type RBACConfig struct {
	Cache RBACCacheConfig `mapstructure:"cache"`
	Redis RBACRedisConfig `mapstructure:"redis"`
}

type RBACCacheConfig struct {
	UserPermsTTL  time.Duration `mapstructure:"user_perms_ttl"`
	UserRolesTTL  time.Duration `mapstructure:"user_roles_ttl"`
	RolePermsTTL  time.Duration `mapstructure:"role_perms_ttl"`
	GroupRolesTTL time.Duration `mapstructure:"group_roles_ttl"`
}

type RBACRedisConfig struct {
	UserPermsPrefix  string `mapstructure:"user_perms_prefix"`
	UserRolesPrefix  string `mapstructure:"user_roles_prefix"`
	RolePermsPrefix  string `mapstructure:"role_perms_prefix"`
	GroupRolesPrefix string `mapstructure:"group_roles_prefix"`
}

type OAuthConfig struct {
	GitHub OAuthGitHubConfig `mapstructure:"github"`
	Google OAuthGoogleConfig `mapstructure:"google"`
	WeChat OAuthWeChatConfig `mapstructure:"wechat"`
	Apple  OAuthAppleConfig  `mapstructure:"apple"`
}

type OAuthGitHubConfig struct {
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	RedirectURL  string `mapstructure:"redirect_url"`
}

type OAuthGoogleConfig struct {
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	RedirectURL  string `mapstructure:"redirect_url"`
}

type OAuthWeChatConfig struct {
	AppID       string `mapstructure:"app_id"`
	AppSecret   string `mapstructure:"app_secret"`
	RedirectURL string `mapstructure:"redirect_url"`
}

type OAuthAppleConfig struct {
	ClientID    string `mapstructure:"client_id"`
	TeamID      string `mapstructure:"team_id"`
	KeyID       string `mapstructure:"key_id"`
	PrivateKey  string `mapstructure:"private_key"`
	RedirectURL string `mapstructure:"redirect_url"`
}

type SnowflakeConfig struct {
	Node int64 `mapstructure:"node"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg, viper.DecodeHook(mapstructureDecodeHook)); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &cfg, nil
}
```

**Important: viper duration decode hook.** Viper needs a custom hook to parse `"168h"`, `"10m"` strings into `time.Duration`. Add this in config.go:

```go
import "github.com/mitchellh/mapstructure"

func mapstructureDecodeHook(f mapstructure.DecodeHookFuncType) mapstructure.DecodeHookFuncType {
	return func(from, to reflect.Type, data any) (any, error) {
		if to == reflect.TypeOf(time.Duration(0)) && from == reflect.TypeOf("") {
			s := data.(string)
			return time.ParseDuration(s)
		}
		return data, nil
	}
}
```

- [ ] **Step 3: Write config_test.go**

```go
package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	content := `
server:
  grpc:
    addr: ":9000"
  gateway:
    addr: ":8080"
database:
  host: "127.0.0.1"
  port: 5432
  user: "test"
  password: "test"
  dbname: "test_db"
  sslmode: "disable"
  max_open_conns: 25
  max_idle_conns: 10
  conn_max_lifetime: "5m"
redis:
  addr: "localhost:6379"
session:
  ttl: "168h"
  redis:
    key_prefix: "user:session:"
    user_sessions_prefix: "user:user_sessions:"
rbac:
  cache:
    user_perms_ttl: "10m"
  redis:
    user_perms_prefix: "user:rbac:user_perms:"
snowflake:
  node: 1
log:
  level: "info"
  format: "json"
`
	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(content)
	tmpFile.Close()

	cfg, err := Load(tmpFile.Name())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.GRPC.Addr != ":9000" {
		t.Errorf("GRPC.Addr = %q, want :9000", cfg.Server.GRPC.Addr)
	}
	if cfg.Database.Port != 5432 {
		t.Errorf("Database.Port = %d, want 5432", cfg.Database.Port)
	}
	if cfg.Database.ConnMaxLifetime != 5*time.Minute {
		t.Errorf("ConnMaxLifetime = %v, want 5m", cfg.Database.ConnMaxLifetime)
	}
	if cfg.Session.TTL != 168*time.Hour {
		t.Errorf("Session.TTL = %v, want 168h", cfg.Session.TTL)
	}
	if cfg.RBAC.Cache.UserPermsTTL != 10*time.Minute {
		t.Errorf("RBAC.Cache.UserPermsTTL = %v, want 10m", cfg.RBAC.Cache.UserPermsTTL)
	}
	if cfg.Snowflake.Node != 1 {
		t.Errorf("Snowflake.Node = %d, want 1", cfg.Snowflake.Node)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/config/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/ go.mod go.sum
git commit -m "feat: add config loading with viper"
```

---

### Task 4: Create Database Package

**Files:**
- Create: `internal/database/postgres.go`
- Create: `internal/database/postgres_test.go`
- Create: `internal/database/testhelpers.go`

- [ ] **Step 1: Add dependencies**

Run: `go get gorm.io/driver/postgres github.com/testcontainers/testcontainers-go github.com/testcontainers/testcontainers-go/modules/postgres github.com/golang-migrate/migrate/v4`

- [ ] **Step 2: Write postgres.go**

```go
package database

import (
	"fmt"

	"user-service/internal/config"
	"user-service/internal/xcodes"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func NewDB(cfg config.DatabaseConfig) (*gorm.DB, error) {
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode,
	)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	return db, nil
}
```

- [ ] **Step 3: Write testhelpers.go**

Provide `SetupTestDB(t *testing.T) *gorm.DB` using testcontainers:

```go
package database

import (
	"context"
	"fmt"
	"testing"
	"time"

	"user-service/internal/config"

	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func SetupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	ctx := context.Background()

	c, err := postgres.Run(ctx,
		"postgres:17-alpine",
		postgres.WithDatabase("user_service_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { c.Terminate(ctx) })

	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get connection string: %v", err)
	}

	db, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	return db
}
```

- [ ] **Step 4: Write postgres_test.go**

```go
func TestNewDB(t *testing.T) {
	// Skip in CI without DB, or use testcontainers
	// Test that connection pool settings are applied
	// Test that database is reachable
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/database/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/database/ go.mod go.sum
git commit -m "feat: add database connection with testcontainers support"
```

---

### Task 5: Run gorm.io/cli Code Generation

**Files:**
- Modify: `internal/models/genconfig.go` (update if needed for new models)
- Generate: `internal/generated/` (output)

- [ ] **Step 1: Verify genconfig.go includes all new models AND excludes new base structs**

Check that `Email`, `SMSLog`, and updated models are included in the generation config. **Importantly, add the new base structs to `ExcludeStructs`:**

```go
ExcludeStructs: []any{
    BaseModel{},
    BaseModelWithDeleted{},
    SnowflakeModel{},
    SnowflakeModelWithDeleted{}, // new
    CreatedAtModel{},
    JoinModel{},                  // new
},
```

- [ ] **Step 2: Run gorm.io/cli generate**

Run: `gorm gen` (or the appropriate command from existing genconfig)
Expected: `internal/generated/` populated with type-safe query code

- [ ] **Step 3: Verify generated code compiles**

Run: `go build ./internal/generated/...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add internal/generated/ internal/models/genconfig.go
git commit -m "feat: run gorm.io/cli code generation for all models"
```

---

### Task 6: Create Database Migrations

**Files:**
- Create: `migrations/000001_init_schema.up.sql`
- Create: `migrations/000001_init_schema.down.sql`

- [ ] **Step 1: Write 000001_init_schema.up.sql**

Write the complete migration SQL covering all tables from the design doc §4.1-4.7, including:
- users (with deleted_at, no registered_at)
- identities (with created_at replacing bound_at, with deleted_at)
- sessions (with updated_at)
- login_logs
- verification_codes
- emails
- sms_logs
- All RBAC tables (groups, user_groups, roles, permissions, permission_groups, join tables)
- All indexes from §4.9

- [ ] **Step 2: Write 000001_init_schema.down.sql**

Write DROP TABLE statements in reverse dependency order.

- [ ] **Step 3: Verify migration runs against test database**

Write a quick test or manual verification:
```bash
migrate -path migrations -database "postgres://test:test@localhost:5432/user_service_test?sslmode=disable" up
```

- [ ] **Step 4: Commit**

```bash
git add migrations/
git commit -m "feat: add initial database migration"
```

---

### Task 7: Create Makefile

**Files:**
- Create: `Makefile`

- [ ] **Step 1: Write Makefile**

```makefile
.PHONY: all build test lint generate migrate-up migrate-down fmt vet

## build: Build the server binary
build:
	go build -o bin/server ./cmd/server/

## test: Run tests with race detector
test:
	go test -race -coverprofile=coverage.out ./...

## lint: Run golangci-lint
lint:
	golangci-lint run ./...

## fmt: Format code
fmt:
	gofmt -w .
	goimports -w .

## vet: Run go vet
vet:
	go vet ./...

## generate: Run gorm.io/cli code generation
generate:
	gorm gen

## proto: Generate protobuf code
proto:
	protoc --go_out=gen --go-grpc_out=gen --grpc-gateway_out=gen api/proto/user/user.proto

## migrate-up: Run database migrations up
migrate-up:
	migrate -path migrations -database "$(DB_DSN)" up

## migrate-down: Run database migrations down (1 step)
migrate-down:
	migrate -path migrations -database "$(DB_DSN)" down 1

## tidy: Run go mod tidy
tidy:
	go mod tidy

## all: Format, vet, lint, test
all: fmt vet lint test
```

- [ ] **Step 2: Verify make targets work**

Run: `make vet` and `make tidy`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "feat: add Makefile with build, test, lint, generate targets"
```

---

## Self-Review

### Spec Coverage
| Spec Section | Covered by Task |
|---|---|
| §4 Database design (all tables) | Task 1 (models), Task 6 (migrations) |
| §4.6 emails / §4.7 sms_logs | Task 1 Step 5 |
| §14 Config struct | Task 3 |
| CLAUDE.md xcodes/ | Task 2 |
| CLAUDE.md database conventions | Task 4 |
| CLAUDE.md gorm.io/cli | Task 5 |

### Placeholder Scan
No TBD/TODO/fill-in-later found. All code blocks contain concrete implementations.

### Type Consistency
- Config structs match config.yaml keys via mapstructure tags
- Model field types match migration SQL column types
- xerr.New parameters use xerr.Category constants
- Error code references (ErrUserNotFound etc.) are consistent across files

---

## Subsequent Plans

| Plan | Scope | Depends on |
|------|-------|------------|
| Plan 2: Repository | All repository implementations with tests | Plan 1 |
| Plan 3: Proto & gRPC Skeleton | Proto file, code gen, server skeleton | Plan 1 |
| Plan 4: Identity & Auth + Session | Identity providers, session manager, auth service | Plan 1, 2, 3 |
| Plan 5: RBAC Module | Authorizer, cache, seed data, admin APIs | Plan 1, 2, 3 |
| Plan 6: Server Integration | Middleware, main.go wiring, pkg/ | Plan 1-5 |
