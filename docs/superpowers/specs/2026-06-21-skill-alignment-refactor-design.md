# user-service 按 Skills 对齐重构设计

**日期**：2026-06-21
**状态**：approved（待 writing-plans 转化为实施计划）
**作者**：moss + Claude（brainstorming 协作产出）

## 背景

user-service 当前代码结构与 `.claude/skills/` 下的 5 个 skill（golang-service-development、gorm-cli-development、golang-development、proto-development、go-common-usage）定义的目标架构存在系统性偏差。本设计文档定义一次全量重构的范围、策略和分阶段方案。

## 当前状态 vs skill 要求的关键差距

| 维度 | 当前 | skill 要求 |
|------|------|-----------|
| gRPC 入口 | `internal/service/user_service.go` 同时 embed `UnimplementedUserServiceServer` + 业务委托 | `pkg/handler/` 薄壳一行委托 + `internal/service/service.go` facade |
| service 结构 | 扁平 6 个文件（auth.go/rbac.go/session.go/social.go/user.go/user_service.go） | `service.go` facade + 领域子包（`auth/`、`user/`、`session/`、`rbac/`、`social/`） |
| store 数据层 | `repository/` 用 struct + 方法（`UserRepository.Create`） | `dal/` 用函数式 API + 表名前缀方法名（`dal.CreateUser`） |
| Model 字段 | 嵌入 `SnowflakeModelWithDeleted`/`BaseModelWithDeleted`/`CreatedAtModel` | 显式声明 ID/CreatedAt/UpdatedAt/DeletedAt |
| Model 命名 | `LoginLog`、`Session`、`Identity` 等 | 业务表加 `User` 前缀（`UserLoginLog`、`UserSession`、`UserIdentity`）；RBAC 表用 `rbac_` 表前缀，model 名按业务语义 |
| 资源管理 | `ownDB`/`ownRedis` bool + 手工 cleanup 列表 | `lifecycle.Manager` 统一注册 Stopper |
| cron/jobs | 无 | `internal/jobs/` + `setupJobs()` 骨架（即便当前无任务也预留位置） |
| pkg/module.go | 返回 `*service.UserService` | 返回 `*handler.Handler` |

## 约束与决策

通过 brainstorming 对话确认的约束：

1. **范围**：全量重构，覆盖所有 8 个维度的差距
2. **策略**：增量推进，每个 phase 结束后项目可编译、可运行、gRPC 接口不变
3. **行为**：严格保持业务行为不变（gRPC 接口、参数、响应、错误码都不动）
4. **DB schema**：model 名和表名都按 skill 规范重命名（开发库允许 drop & recreate）
5. **测试**：边重构边补集成测试，每个 phase 完成时该层有测试覆盖
6. **分支**：直接在 main 上提交，每个 phase 一个或几个小 commit
7. **RBAC 表名**：model 名按业务语义（`Group`/`Role`/`UserGroup`/`UserRole`），表名加 `rbac_` 前缀
8. **审计表字段**：所有表（含审计表）统一显式 ID/CreatedAt/UpdatedAt/DeletedAt

## 目标终态目录结构

```
user-service/
├── api/proto/user/v1/user.proto
├── cmd/
│   ├── migrate/main.go
│   └── server/main.go
├── gen/user/v1/
├── internal/
│   ├── cache/
│   ├── identity/
│   ├── jobs/                          # 新增（骨架）
│   │   └── jobs.go
│   ├── middleware/
│   ├── service/
│   │   ├── service.go                 # facade：Service struct + New + Start/Stop + 30 个委托
│   │   ├── auth/auth.go
│   │   ├── user/user.go
│   │   ├── session/session.go
│   │   ├── rbac/rbac.go
│   │   └── social/social.go
│   ├── session/                       # 技术组件，保留独立
│   ├── store/
│   │   ├── generated/
│   │   ├── models/                    # 重写
│   │   └── dal/                       # 由 repository/ 改名 + 函数式 API
│   └── thirdcall/gid_service/
├── pkg/
│   ├── client.go
│   ├── config/
│   ├── handler/                       # 新增：薄壳
│   │   └── user.go
│   ├── module.go                      # 返回 *handler.Handler
│   ├── option/
│   ├── server.go                      # 调 handler 而非 service
│   ├── thirdcall/
│   └── xcodes/
├── buf.yaml / buf.gen.yaml
├── Makefile
├── .golangci.yml                      # 新增
└── config.example.yaml
```

## Phase 序列（自上而下，5 个 phase）

| Phase | 主要改动 | 可验证里程碑 |
|-------|---------|-------------|
| 1 | `pkg/handler/` 薄壳 + `pkg/server.go` `pkg/module.go` 切换到 handler | grpcurl 调用全 RPC 通过 |
| 2 | `internal/service/` 拆 5 个子包 + `service.go` facade；删除 `user_service.go` 和 5 个旧扁平文件 | grpcurl + 已有集成测试通过 |
| 3 | `repository/` → `dal/`，函数式 API；`BaseRepo` 删除 | 编译通过 + dal 单测 |
| 4 | `models/` 重写：显式 ID/CreatedAt/UpdatedAt/DeletedAt + UserXxx 前缀；regen generated | AutoMigrate + dal 集成测试通过 |
| 5 | `lifecycle.Manager` 替换 `ownDB`/`ownRedis`；新增 `internal/jobs/` 骨架 + `setupJobs()` | in-process module 测试 + graceful shutdown 验证 |

**贯穿所有 phase**：
- 每个 phase 一个或几个 commit，直接在 main 上
- 每个 phase 完成后跑：`go build ./... && golangci-lint run ./... && 现有测试 && grpcurl smoke test`
- 每个 phase 补该层的关键路径集成测试（仅 phase 1 后才能跑端到端）

## Phase 1：pkg/handler 薄壳 + server/module 切换

### 目标

引入 `pkg/handler/` 薄壳层，把 gRPC stub 责任从 `internal/service.UserService` 剥离。Phase 1 结束时 `UserService` 还在原位继续承担业务委托，但 gRPC 注册入口已经切到 handler。

### 新增 `pkg/handler/user.go`

```go
package handler

import (
    "context"
    "github.com/servekit/go-common/signalx"
    pb "user-service/gen/user/v1"
    "user-service/internal/service"
    "google.golang.org/protobuf/types/known/emptypb"
)

type Handler struct {
    pb.UnimplementedUserServiceServer
    svc *service.UserService
}

func New(svc *service.UserService) *Handler { return &Handler{svc: svc} }

var (
    _ pb.UserServiceServer = (*Handler)(nil)
    _ signalx.Service      = (*Handler)(nil)
)

// Phase 5 改为委托到 svc.Start/Stop
func (h *Handler) Start() error { return nil }
func (h *Handler) Stop()  error { return nil }

// 一行委托 × 30 个 RPC
func (h *Handler) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
    return h.svc.Register(ctx, req)
}
// ... 其余 RPC 同样模式
```

### 修改 `pkg/server.go`

- `Server` 持有 `*handler.Handler` 而非 `*service.UserService`
- `NewServer` 内部：先 `service.New` 得到 `*UserService`，再 `handler.New(svc)` 包一层
- gRPC 注册：`pb.RegisterUserServiceServer(s, hdl)` 而非 `(s, svc)`
- middleware 拿 session manager：handler 暴露 `Svc()` 方法（临时桥接，Phase 2 后改）

### 修改 `pkg/module.go`

- `NewModule` 返回 `*handler.Handler`
- in-process 调用方拿 handler 后直接调 RPC 方法

### 不动

- `internal/service/user_service.go`：Phase 2 才拆

### 临时桥接

`pkg/handler.Handler` 暴露 `Svc() *service.UserService` 方法返回底层 svc，供 middleware 获取 `SessionMgr()`。Phase 2 service 拆完后去掉。

### 验证

- `go build ./...` 通过
- grpcurl smoke test：Login、GetProfile、CreateGroup 等代表 RPC 通过
- in-process module：`hdl, _ := userservice.NewModule(...); hdl.Login(ctx, req)` 能跑

## Phase 2：internal/service 拆子包 + service.go facade

### 目标

把 5 个扁平文件按领域拆到子包，建立 `service.go` facade 模式。Phase 2 结束时 `pkg/handler` 委托目标从 `*UserService` 变为 `*service.Service`。

### 当前扁平结构 → 目标子包对应

| 当前 | 目标子包 | 内容 |
|------|---------|------|
| `internal/service/auth.go` (689 行) | `internal/service/auth/auth.go` | `type Service` + New + Register/Login/Logout/SendVerificationCode + `xxxToProto` |
| `internal/service/social.go` (424 行) | `internal/service/social/social.go` | `type Service` + New + GetOAuthURL/SocialLogin/MiniProgramLogin/MiniProgramPhoneLogin |
| `internal/service/user.go` (280 行) | `internal/service/user/user.go` | `type Service` + New + GetProfile/UpdateProfile/ChangePassword/ResetPassword/ListIdentities/BindIdentity/UnbindIdentity/GetUser/ListUsers/DisableUser/GetLoginLogs |
| `internal/service/session.go` (126 行) | `internal/service/session/session.go` | `type Service` + New + RefreshSession/ListSessions/RevokeSession/RevokeAllSessions |
| `internal/service/rbac.go` (624 行) | `internal/service/rbac/rbac.go` | `type Service` + New + 所有 Group/Role/Permission/Member 相关 RPC |

### `internal/service/service.go`（facade）

```go
package service

import (
    "github.com/servekit/go-common/lifecycle"
    domainSession "user-service/internal/service/session"  // 别名解决包名冲突
    "user-service/internal/service/auth"
    "user-service/internal/service/rbac"
    "user-service/internal/service/social"
    "user-service/internal/service/user"
    "user-service/internal/session"  // 技术组件
    // ...
)

type Service struct {
    cfg *config.Config
    mgr *lifecycle.Manager  // Phase 5 才真正使用

    db  *gorm.DB
    rdb *redis.Client
    gid thirdcall.GIDService
    sessionMgr *session.Manager  // 技术组件

    auth   *auth.Service
    user   *user.Service
    social *social.Service
    sess   *domainSession.Service  // 业务子包
    rbacH  *rbac.Service
}

func New(cfg *config.Config, opts ...option.Option) (*Service, error) {
    // resolveDB / resolveRedis / resolveGID（保留现有 ownDB/ownRedis 逻辑，Phase 5 改 lifecycle）
    // 初始化各子包 Service（注入 db / repos / sessionMgr / limiter / gid 等）
    // 返回 facade
}

// SessionMgr 暴露给 middleware
func (s *Service) SessionMgr() *session.Manager { return s.sessionMgr }

// gRPC 委托 × 30（每个 RPC 一行）
func (s *Service) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
    return s.auth.Register(ctx, req)
}
// ...
```

### 子包构造函数签名

通过参数注入所有依赖（repo、sessionMgr、limiter、gid 等），**不持有父 `*Service` 引用**（避免循环依赖）。

```go
// internal/service/auth/auth.go
package auth

type Service struct {
    db          *gorm.DB  // Phase 3 改为直接持有 db
    // Phase 3 之前仍持有 repo struct
    userRepo     *repository.UserRepository
    identityRepo *repository.IdentityRepository
    // ...
}

func New(/* deps */) *Service { return &Service{...} }

func (s *Service) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
    // 从当前 auth.go 的 AuthHandler.Register 整体搬过来
}
```

### 包名冲突处理

`internal/service/session/` 子包名 vs `internal/session/`（session.Manager 所在）—— 包名都是 `session`，但路径不同。

父 `service.Service` 同时持有：
- `*session.Manager`（技术组件，来自 `internal/session`）
- `*domainSession.Service`（业务子包，来自 `internal/service/session`）

父 `service.go` 用别名 import：`domainSession "user-service/internal/service/session"`，字段名用 `sess *domainSession.Service` 区分。

### 删除

- `internal/service/user_service.go`
- `internal/service/{auth,user,session,social,rbac}.go`（5 个旧扁平文件）

### pkg/handler 更新

`Handler` 持有 `*service.Service`（不再持有 `*UserService`）。删除 `Svc()` 临时方法，改为 `handler.New(svc *service.Service)`。

### 验证

- `go build ./...` 通过
- grpcurl 全 RPC smoke test 通过
- in-process module 测试通过
- 子包级集成测试：每个子包至少 1 个 happy path

## Phase 3：repository/ → dal/ 重命名 + 函数式 API

### 目标

`internal/store/repository/` 改名为 `dal/`，从 struct + 方法模式改为函数式 API（`dal.CreateUser` 而非 `userRepo.Create`），符合 gorm-cli-development §6。

### 目录变化

```
internal/store/
├── generated/        # 不动
├── models/           # Phase 4 才动
└── repository/       ❌ 删除
    ├── base.go       ❌ 删除（BaseRepo 不再需要）
    ├── user.go
    ├── identity.go
    ├── session.go
    ├── login_log.go
    ├── message.go
    ├── group.go
    └── rbac.go

↓ 改为 ↓

internal/store/
├── generated/
├── models/
└── dal/              ✅ 新建
    ├── common.go     # 跨表辅助（分页 helper、错误变量等）
    ├── user.go       # 函数：CreateUser, GetUserByID, GetUserByEmail ...
    ├── identity.go
    ├── session.go
    ├── login_log.go
    ├── message.go
    ├── group.go
    └── rbac.go
```

### API 风格转换

```go
// 之前：repository struct + 方法
type UserRepository struct { *BaseRepo }
func NewUserRepository(db *gorm.DB) *UserRepository { ... }
func (r *UserRepository) Create(ctx context.Context, user *models.User) error { ... }
func (r *UserRepository) FindByID(ctx context.Context, id int64) (*models.User, error) { ... }

// 之后：包级函数 + 表名前缀
package dal

func CreateUser(ctx context.Context, tx *gorm.DB, user *models.User) error {
    if err := gorm.G[models.User](tx).Create(ctx, user); err != nil {
        return xcodes.ErrInternal.Wrap(err)
    }
    return nil
}

func GetUserByID(ctx context.Context, tx *gorm.DB, id int64) (*models.User, error) {
    user, err := gorm.G[models.User](tx).
        Where(generated.SnowflakeModelWithDeleted.ID.Eq(id)).  // Phase 4 后改为 generated.User.ID.Eq(id)
        Take(ctx)
    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, xcodes.ErrUserNotFound.New()
        }
        return nil, xcodes.ErrInternal.Wrap(err)
    }
    return &user, nil
}
```

### 调用方变化（service 层）

```go
// 之前
type authHandler struct {
    userRepo     *repository.UserRepository
    identityRepo *repository.IdentityRepository
}
authHandler := NewAuthHandler(userRepo, identityRepo, ...)
user, err := h.userRepo.FindByID(ctx, id)

// 之后
type Service struct {
    db *gorm.DB  // 直接持有 db，dal 函数接收 tx
}
user, err := dal.GetUserByID(ctx, s.db, id)
```

子包 Service 构造函数瘦身：
- 之前：注入 ~10 个 repo struct
- 之后：只注入 `db *gorm.DB`，需要时直接调 `dal.Xxx(ctx, db, ...)`

### 事务边界明确

- service 方法内 `s.db.Transaction(func(tx *gorm.DB) error { ... })` 开事务
- 事务里把 `tx` 传给 dal 函数：`dal.CreateLoginLog(ctx, tx, log)`
- 单条操作不开事务，直接传 `s.db`

### message.go 的定位

当前 `repository/message.go` 含邮件/SMS 日志记录逻辑（不是发送本身）—— **留在 dal 里**，语义是"消息日志记录"。发送本身已经在 service 层调 email/sms provider，不属于 dal。

### 验证

- `go build ./...` 通过
- dal 包单测：每个 dal 文件至少 1 个 happy path + 1 个 NotFound 测试（用 `dbx.SetupTestDB`）
- service 层关键流程集成测试通过
- grpcurl smoke test 通过

### 工作量估算

- dal 文件数：8 个（user/identity/session/login_log/message/group/rbac/common）
- 转换模式机械但量大（~1500 行 repository 代码 → 类似行数 dal）

## Phase 4：models 重写 + regen generated

### 目标

把 `internal/store/models/` 重写为显式 ID/CreatedAt/UpdatedAt/DeletedAt 字段 + 服务前缀命名（UserLoginLog 而非 LoginLog），重新生成 generated/。改动面最大的一个 phase。

### Model 变化对照

| 当前 | 目标 model 名 | 目标表名 |
|------|--------------|---------|
| `User` (嵌入 `SnowflakeModelWithDeleted`) | `User` | `users` |
| `Identity` | `UserIdentity` | `user_identities` |
| `Session` | `UserSession` | `user_sessions` |
| `LoginLog` | `UserLoginLog` | `user_login_logs` |
| `VerificationCode` | `UserVerificationCode` | `user_verification_codes` |
| `Email` | `UserEmail` | `user_emails` |
| `SMSLog` | `UserSMSLog` | `user_sms_logs` |
| `Group` | `Group` | `rbac_groups` |
| `UserGroup` | `UserGroup` | `rbac_user_groups` |
| `Role` | `Role` | `rbac_roles` |
| `Permission` | `Permission` | `rbac_permissions` |
| `PermissionGroup` | `PermissionGroup` | `rbac_permission_groups` |
| `RolePermission` | `RolePermission` | `rbac_role_permissions` |
| `RolePermissionGroup` | `RolePermissionGroup` | `rbac_role_permission_groups` |
| `GroupRole` | `GroupRole` | `rbac_group_roles` |
| `UserRole` | `UserRole` | `rbac_user_roles` |

### RBAC 表前缀实现

RBAC model 名按业务语义（`Group`/`Role`/`UserGroup` 等），通过显式 `TableName()` 方法实现表前缀：

```go
func (Group) TableName() string { return "rbac_groups" }
func (Role) TableName() string { return "rbac_roles" }
// ... 每个 RBAC model 一个 TableName()
```

### 显式字段模板

所有 model 统一 4 字段：

```go
type User struct {
    ID        int64          `gorm:"primaryKey"`     // 雪花 ID（应用层生成）
    // ...业务字段...
    CreatedAt time.Time
    UpdatedAt time.Time
    DeletedAt gorm.DeletedAt `gorm:"index"`
}
```

不再嵌入 `SnowflakeModelWithDeleted` / `BaseModelWithDeleted` / `CreatedAtModel`。

**审计表（LoginLog、Email、SMSLog、VerificationCode）也加 UpdatedAt/DeletedAt**，统一所有 model 字段集合（即便业务上不会更新/软删，结构上保持一致，避免 gorm gen 生成的辅助器类型不齐）。

### 修改文件

1. `internal/store/models/*.go` — 全部重写
2. `internal/store/models/register.go` — 更新 `AllModels()` 列表
3. `internal/store/models/base.go` — 删除（不再需要嵌入类型）
4. `internal/store/models/genconfig.go` — 重建（含 OAuthData 等自定义类型的 FieldTypeMap）
5. `internal/store/generated/` — 重新 `gorm gen`
6. `internal/store/dal/*.go` — 全部更新（`generated.SnowflakeModelWithDeleted.ID` → `generated.User.ID` 等）
7. `internal/service/*/*.go` — 全部更新（model 字段访问、表名引用）

### 验证

- `go build ./...` 通过
- `cmd/migrate/main.go` 跑通，新表结构创建（开发库允许 drop & recreate）
- dal 层集成测试全部通过（testcontainer PostgreSQL）
- service 层关键流程集成测试通过
- grpcurl smoke test 通过
- RBAC 表名测试：验证 `rbac_groups` 等表名生效

### 潜在坑

- `gorm gen` 对 `*string`、`*time.Time`、自定义类型（`OAuthData`）需要 `genconfig.Config` 里的 `FieldTypeMap` 映射 —— 当前 `models/genconfig.go` 已删除，需要重建
- 改名后所有 service 代码里的 `&models.LoginLog{...}` → `&models.UserLoginLog{...}` 这种引用要全改
- `dal` 包里 `generated.SnowflakeModelWithDeleted.ID` 这种通过嵌入类型访问的字段辅助器，重写后改为 `generated.User.ID`

## Phase 5：lifecycle.Manager + internal/jobs/

### 目标

把 `ownDB`/`ownRedis` 替换为 `lifecycle.Manager`；新增 `internal/jobs/` 骨架（即便目前没有周期任务，按 skill §5 要求预留位置）。

### A. lifecycle.Manager 替换

当前 `internal/service/service.go` 的 New：

```go
// 现状
var cleanup []func()
db, ownDB, err := resolveDB(&o, cfg)
if ownDB { cleanup = append(cleanup, func() { _ = sqlDB.Close() }) }
// ... 手工维护 cleanup 列表
```

改为：

```go
// 之后
mgr := lifecycle.NewManager()

db, err := resolveDB(cfg, o.DB, mgr)  // 注入 → 不注册；自建 → 注册为 Stopper
if err != nil {
    return nil, errors.Join(err, mgr.Stop())  // 已注册的反序回滚
}
rdb, err := resolveRedis(cfg, o.RDB, mgr)
if err != nil {
    return nil, errors.Join(err, mgr.Stop())
}
gid, err := resolveGID(cfg, o.GIDService, mgr)
if err != nil {
    return nil, errors.Join(err, mgr.Stop())
}
```

`resolveDB` 内部（参考 skill §5）：

```go
func resolveDB(cfg *config.Config, injected *gorm.DB, mgr *lifecycle.Manager) (*gorm.DB, error) {
    if injected != nil {
        return injected, nil  // 调用方注入，不注册
    }
    db, err := dbx.New(cfg.Database)
    if err != nil {
        return nil, fmt.Errorf("database: %w", err)
    }
    mgr.AddStopper("db", lifecycle.StopFunc(func() {
        sqlDB, err := db.DB()
        if err != nil { slog.Warn("get sql db for close", "error", err); return }
        if err := sqlDB.Close(); err != nil {
            slog.Warn("close db", "error", err)
        }
    }))
    return db, nil
}
```

`Service.Start()` / `Stop()`：

```go
func (s *Service) Start() error { return s.mgr.Start() }
func (s *Service) Stop() error  { return s.mgr.Stop() }
```

`pkg/handler.Handler` 的 Start/Stop 委托到 service：

```go
func (h *Handler) Start() error { return h.svc.Start() }
func (h *Handler) Stop() error  { return h.svc.Stop() }
```

`pkg/server.go` 的 Start 加 rollback：

```go
func (s *Server) Start() error {
    if err := s.svc.Start(); err != nil { return err }
    if err := s.grpcSrv.Start(); err != nil {
        return errors.Join(err, s.svc.Stop())
    }
    return nil
}
```

### B. internal/jobs/ 骨架

```
internal/jobs/
└── jobs.go    # Scheduler + New + AddFunc + Start/Stop
```

`Scheduler` 实现 `lifecycle.Service`，包内代码 ~100 行。`ownsCron` 字段控制是否真管 cron lifecycle。

`internal/service/service.go` 加 `setupJobs()` receiver 方法，签名恒定：

```go
func (s *Service) setupJobs() error {
    scheduler, err := jobs.New(&jobs.Deps{
        Config: &cronx.Config{Timezone: s.cfg.Cron.Timezone},
    })
    if err != nil { return fmt.Errorf("init jobs: %w", err) }
    s.mgr.Add("jobs", scheduler)
    // 当前无任务；未来在这里加 scheduler.AddFunc(...)
    return nil
}
```

`New()` 末尾：

```go
if err := svc.setupJobs(); err != nil {
    if cerr := mgr.Stop(); cerr != nil {
        err = errors.Join(err, fmt.Errorf("rollback: %w", cerr))
    }
    return nil, err
}
```

`pkg/config/config.go` 加 `CronConfig`：

```go
type CronConfig struct {
    Timezone string `default:"Asia/Shanghai"`
}
```

### C. main 入口加 logging.Setup

skill §3 要求入口必须：

```go
logging.Setup(&cfg.Log)
```

如果当前 `cmd/server/main.go` 没调用，Phase 5 补上。

### 验证

- `go build ./...` 通过
- `golangci-lint run ./...` 无 error
- 启动 server，发 SIGTERM，确认 graceful shutdown（关闭 db pool、redis、gRPC server）
- in-process module 测试：`hdl.Start(); defer hdl.Stop(); hdl.Login(...)` 能跑
- 跑 cron 配置但不加任务，确认 scheduler.Start/Stop 无 panic

### 潜在坑

- 当前 `UserService.Close()` 是手工 cleanup；改 lifecycle 后 `Close()` 删除，全部走 `Stop()`
- `pkg/server.go` 现在的 `Stop()` 只调 `grpcSrv.Stop()`，要加 `svc.Stop()`
- `internal/middleware/auth.go` 用 `svc.SessionMgr()` —— Phase 5 后 `*Service.SessionMgr()` 仍要保留（facade）

## 测试策略

### 集成测试覆盖

每个 phase 补的关键测试（统一放在 `*_test.go` 旁边，用 `dbx.SetupTestDB` + `redisx.NewTestClient`）：

| Phase | 新增测试 | 文件位置 |
|-------|---------|---------|
| 1 | handler 端到端：`NewModule` → 调 1 个 RPC → 校验返回 | `pkg/handler/user_test.go` |
| 2 | service facade：每个子包至少 1 个 happy path | `internal/service/{auth,user,session,rbac,social}/*_test.go` |
| 3 | dal 层：每个 dal 文件至少 1 个 Create + 1 个 Get-by-X + 1 个 NotFound | `internal/store/dal/*_test.go` |
| 4 | models 重生成后，dal 测试全部跑通；新增 RBAC 表名测试（验证 `rbac_groups` 等表名生效） | 同上 |
| 5 | lifecycle 测试：注入 DB 时不注册、自建时注册；Start/Stop 幂等；jobs.Scheduler Start/Stop | `internal/service/service_test.go` + `internal/jobs/jobs_test.go` |

### 每个 Phase 必跑的 smoke test

```bash
# 1. 静态检查
gofmt -l ./
goimports -l ./
go vet ./...
golangci-lint run ./...

# 2. 构建 + 单元测试 + 集成测试
go build ./...
go test -race -cover ./...

# 3. 启动 server，跑 grpcurl smoke test（写入 scripts/smoke.sh）
make run &
sleep 2
grpcurl -plaintext -d '{...}' localhost:9000 user.UserService/Login
grpcurl -plaintext localhost:9000 user.UserService/ListPermissions
kill %1
```

### .golangci.yml 配置

从 `ai-kit-studio/skills/golang-development/.golangci.yml` 模板复制，`local-prefixes` 改成 `user-service,github.com/servekit/go-common`。启用 `decorder` 强制声明顺序。

### Makefile 目标核对

按 skill §7 要求，必须提供：`build`、`run`、`test`、`lint`、`fmt`、`vet`、`generate`、`proto`、`migrate`、`tidy`。当前 Makefile 已有部分，phase 5 末尾核对补齐。

## 最终验收清单

完成所有 5 个 phase 后确认：

- [ ] `go build ./...` 通过
- [ ] `golangci-lint run ./...` 无 error
- [ ] `make proto && git diff --exit-code` — buf 生成与 committed 一致
- [ ] `make generate && git diff --exit-code` — gorm gen 与 committed 一致
- [ ] `make migrate` 在干净 DB 上跑通
- [ ] grpcurl 全 RPC smoke test 通过
- [ ] HTTP gateway smoke test 通过
- [ ] in-process module 测试通过（`pkg.NewModule`）
- [ ] 每个 RPC 在 `service.go` 都有对应 facade 方法
- [ ] 每个领域在 `internal/service/<domain>/` 子包，`internal/service/` 根目录只有 `service.go`
- [ ] 周期任务入口在 `internal/jobs/` + `svc.setupJobs()` 内注册
- [ ] model 全部用显式 ID/CreatedAt/UpdatedAt/DeletedAt，无嵌入类型
- [ ] model 命名遵循 `User*` 前缀（业务表）或 RBAC 业务语义 + `rbac_` 表前缀
- [ ] 资源管理走 `lifecycle.Manager`，无 `ownXxx bool` 残留
- [ ] `pkg/handler.Handler` 同时满足 `pb.UserServiceServer` 和 `signalx.Service`

## 不在本次范围

重构过程中遇到但**不在本次范围**的问题：

1. `go-common/cronx` 通过 `gid-service` 间接导致 `go.sum` 缺 `github.com/robfig/cron/v3` —— go-common 或 gid-service 的问题，本次不修
2. 现有 `migrations/` 目录已删除（前一次提交），迁移走 GORM AutoMigrate —— 已成事实
3. proto 字段的 protovalidate 规则补全 —— 留待后续单独迭代

## 关联

**实现计划**：待 writing-plans skill 产出 `docs/superpowers/plans/2026-06-21-skill-alignment-refactor-plan.md`

**相关 skills**：
- `.claude/skills/golang-service-development/golang-service-development.md`
- `.claude/skills/gorm-cli-development/gorm-cli-development.md`
- `.claude/skills/golang-development/golang-development.md`
- `.claude/skills/proto-development/proto-development.md`
- `.claude/skills/go-common-usage/SKILL.md`

**参考实现**：
- `gid-service/`（canonical 实现，pkg/handler + service.go facade）
- `ai-kit-studio/skills/golang-service-development/demo-service/`（scaffold 生成的 demo）
