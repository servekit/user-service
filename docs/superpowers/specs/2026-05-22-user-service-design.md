# User Service 设计文档

> 通用用户系统，支持多登录方式（邮箱/手机/OAuth2）、Session 有状态管理、验证码服务。
> 可作为 gRPC 服务独立部署，也可作为 Go 模块 in-process 使用。

## 1. 需求概要

| 项目 | 决策 |
|------|------|
| 定位 | 通用用户系统，不绑定任何业务 |
| 接入方式 | gRPC 服务 + Go 模块，两种复用同一套核心代码 |
| 协议 | grpc-gateway，一套 proto 同时生成 gRPC + HTTP |
| 核心功能 | 注册/登录（邮箱+密码、手机+验证码）、OAuth2 社交登录（GitHub/Google/微信/Apple）、Session 管理、登录设备管理、验证码服务 |
| 用户体系 | 统一身份 + 多登录方式（一个 user 可绑定多个 identity） |
| 认证方式 | Session 有状态，Redis 存储，支持主动吊销 |
| OAuth2 | 仅作为 OAuth2 Client（社交账号登录），不做 Provider |
| 权限 | RBAC：角色 + 权限 + 权限分组 + 群组作用域，第一版 DBAuthorizer，第二版集成 OPA |
| 存储 | PostgreSQL + Redis |
| 管理 | 纯 API，无 UI |

## 2. 架构

```
邮箱/手机/OAuth2 平台                         业务 App
│ 验证码/OAuth 回调                            │ gRPC / HTTP
▼                                              ▼
┌──────────────────────────────────────────────────┐
│              grpc-gateway (HTTP 代理)              │
└──────────────────────┬───────────────────────────┘
                       │ gRPC
                       ▼
┌──────────────────────────────────────────────────┐
│              user-service (gRPC Server)            │
│                                                    │
│  ┌──────────┐  ┌───────────┐  ┌───────────────┐ │
│  │ Auth     │  │ User      │  │ OAuth         │ │
│  │ Service  │  │ Service   │  │ Service       │ │
│  └────┬─────┘  └────┬──────┘  └──────┬────────┘ │
│       │              │                │           │
│  ┌────┴──────────────┴────────────────┴────────┐ │
│  │          Identity Provider Interface         │ │
│  │  ┌───────┐ ┌───────┐ ┌──────┐ ┌──────────┐ │ │
│  │  │Email  │ │Phone  │ │GitHub│ │Google/   │ │ │
│  │  │       │ │       │ │      │ │WeChat/   │ │ │
│  │  │       │ │       │ │      │ │Apple     │ │ │
│  │  └───────┘ └───────┘ └──────┘ └──────────┘ │ │
│  └─────────────────────────────────────────────┘ │
│                                                    │
│  ┌─────────────────────────────────────────────┐ │
│  │          Captcha Module (验证码)              │ │
│  │  ┌──────────┐  ┌────────┐  ┌──────────────┐ │ │
│  │  │Limiter   │  │Store   │  │Sender        │ │ │
│  │  │(Redis)   │  │(Redis) │  │(Email/SMS)   │ │ │
│  │  └──────────┘  └────────┘  └──────────────┘ │ │
│  └─────────────────────────────────────────────┘ │
│                                                    │
│  ┌─────────────────────────────────────────────┐ │
│  │          RBAC Module (权限)                   │ │
│  │  ┌──────────┐  ┌────────┐  ┌──────────────┐ │ │
│  │  │Authorizer│  │Groups  │  │Roles & Perms │ │ │
│  │  │(Cache)   │  │        │  │              │ │ │
│  │  └──────────┘  └────────┘  └──────────────┘ │ │
│  └─────────────────────────────────────────────┘ │
│                                                    │
│  ┌──────────────┐  ┌───────────────────────────┐ │
│  │ Session      │  │ PostgreSQL                │ │
│  │ (Redis)      │  │ (用户/身份/日志/RBAC)      │ │
│  └──────────────┘  └───────────────────────────┘ │
└──────────────────────────────────────────────────┘
```

gRPC server 默认监听 `:9000`，grpc-gateway 默认监听 `:8080`。OAuth2 回调通过 HTTP 进入。

## 3. 项目结构

```
user/
├── api/proto/user/
│   └── user.proto                  # Protobuf 定义
│
├── cmd/server/
│   └── main.go                     # 启动入口：gRPC server + grpc-gateway
│
├── gen/                            # protoc 自动生成
│   ├── user.pb.go
│   ├── user_grpc.pb.go
│   └── user.pb.gw.go
│
├── internal/
│   ├── service/                    # gRPC service 实现
│   │   ├── auth.go                 # 注册/登录/登出
│   │   ├── user.go                 # 用户 CRUD、资料管理
│   │   ├── oauth.go                # OAuth2 回调处理
│   │   └── session.go              # 会话管理（设备列表、踢下线）
│   ├── identity/                   # 登录方式 Provider 抽象
│   │   ├── provider.go             # Provider 接口定义
│   │   ├── email.go                # 邮箱+密码
│   │   ├── phone.go                # 手机+验证码
│   │   ├── github.go               # GitHub OAuth2
│   │   ├── google.go               # Google OAuth2
│   │   ├── wechat.go               # 微信 OAuth2
│   │   └── apple.go                # Apple Sign-In
│   ├── session/                    # Session 管理
│   │   ├── manager.go              # Manager 接口 + Redis 实现
│   │   └── manager_test.go
│   ├── repository/                 # 数据库操作
│   │   ├── user.go
│   │   ├── identity.go
│   │   ├── session.go
│   │   ├── login_log.go
│   │   ├── verification_code.go
│   │   ├── group.go
│   │   ├── role.go
│   │   ├── permission.go
│   │   ├── permission_group.go
│   │   ├── user_role.go
│   │   └── group_role.go
│   ├── rbac/                       # RBAC 权限模块
│   │   ├── authorizer.go           # Authorizer 接口 + DBAuthorizer
│   │   ├── authorizer_test.go
│   │   ├── cache.go                # 内存缓存（权限/角色/分组）
│   │   ├── checker.go              # 权限检查逻辑
│   │   ├── checker_test.go
│   │   └── seed.go                 # 系统内置角色/权限/分组种子数据
│   ├── middleware/                  # 鉴权拦截器
│   │   └── auth.go                 # Session 验证 + RBAC 权限检查 gRPC interceptor
│
├── pkg/                            # 可被外部 import 的公共包
│   ├── client.go                   # gRPC client 封装
│   └── module.go                   # 模块初始化（in-process 使用）
│
├── migrations/                     # PostgreSQL 迁移脚本
├── Makefile
├── config.yaml
├── go.mod
└── go.sum
```

## 4. 数据库设计

### 4.1 用户表

```sql
CREATE TABLE users (
    id              BIGINT       PRIMARY KEY,              -- 雪花 ID
    nickname        VARCHAR(64),                           -- 昵称
    real_name       VARCHAR(64),                           -- 真实姓名（可选）
    avatar_url      VARCHAR(512),                          -- 头像 URL
    email           VARCHAR(256),                          -- 主邮箱（唯一，验证后填入）
    phone           VARCHAR(20),                           -- 主手机号（唯一，验证后填入）
    gender          VARCHAR(8)   DEFAULT 'unknown',       -- male / female / other / unknown
    birthday        DATE,                                  -- 生日
    timezone        VARCHAR(64)  DEFAULT 'Asia/Shanghai',
    locale          VARCHAR(16)  DEFAULT 'zh-CN',
    bio             VARCHAR(512),                          -- 个人简介
    status          VARCHAR(16)  NOT NULL DEFAULT 'active', -- active / disabled / pending_review
    register_source VARCHAR(32),                           -- email / phone / github / google / wechat / apple
    register_ip     VARCHAR(45),                           -- 注册 IP
    register_device VARCHAR(16),                           -- 注册设备类型：web / ios / android / api
    last_login_at   TIMESTAMPTZ,
    last_login_ip   VARCHAR(45),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ,                           -- GORM soft delete

    -- UNIQUE 约束只对非 NULL 值生效（PostgreSQL 默认行为：多个 NULL 不冲突）
    CONSTRAINT uq_users_email UNIQUE (email),
    CONSTRAINT uq_users_phone UNIQUE (phone)
);
```

### 4.2 身份表（多登录方式）

```sql
CREATE TABLE identities (
    id              BIGSERIAL    PRIMARY KEY,
    user_id         BIGINT       NOT NULL REFERENCES users(id),
    provider        VARCHAR(32)  NOT NULL,                 -- email / phone / github / google / wechat / apple
    provider_uid    VARCHAR(256) NOT NULL,                 -- 邮箱 / 手机号 / OAuth provider 用户 ID
    credentials     JSONB,                                 -- 密码 bcrypt hash / OAuth token 等
    verified        BOOLEAN      NOT NULL DEFAULT false,
    oauth_raw_data  JSONB,                                 -- OAuth 返回的原始用户信息
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ,                           -- GORM soft delete

    CONSTRAINT uq_identity_provider UNIQUE (provider, provider_uid)
);
```

### 4.3 会话表（Redis 为主，PG 持久化备份）

```sql
CREATE TABLE sessions (
    id              VARCHAR(64)  PRIMARY KEY,              -- session UUID
    user_id         BIGINT       NOT NULL REFERENCES users(id),
    ip              VARCHAR(45),
    user_agent      VARCHAR(512),
    device_type     VARCHAR(16),                           -- web / ios / android / api
    os              VARCHAR(32),
    browser         VARCHAR(32),
    country         VARCHAR(4),                            -- ISO 3166-1
    city            VARCHAR(64),
    expires_at      TIMESTAMPTZ  NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    last_active_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    revoked_at      TIMESTAMPTZ                            -- 被踢下线时设置
);
```

Redis 存储：
- `session:{id}` → `{user_id, nickname, avatar_url, ...}` TTL 7天
- `user_sessions:{user_id}` → SET of session_ids（查看设备、踢下线）

### 4.4 登录日志表

```sql
CREATE TABLE login_logs (
    id              BIGSERIAL    PRIMARY KEY,
    user_id         BIGINT       REFERENCES users(id),    -- null 表示登录失败（用户不存在）
    identity_id     BIGINT,                               -- 通过哪个 identity 登录
    provider        VARCHAR(32)  NOT NULL,                -- email / phone / github / ...
    action          VARCHAR(16)  NOT NULL,                -- login / register / bind / unbind
    success         BOOLEAN      NOT NULL,
    fail_reason     VARCHAR(128),                         -- wrong_password / account_disabled / oauth_failed
    ip              VARCHAR(45),
    user_agent      VARCHAR(512),
    device_type     VARCHAR(16),
    os              VARCHAR(32),
    browser         VARCHAR(32),
    country         VARCHAR(4),
    city            VARCHAR(64),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
```

### 4.5 验证码表（Redis 为主，PG 用于审计）

```sql
CREATE TABLE verification_codes (
    id              BIGSERIAL    PRIMARY KEY,
    target          VARCHAR(256) NOT NULL,                -- 邮箱 / 手机号
    code            VARCHAR(16)  NOT NULL,
    channel         VARCHAR(16)  NOT NULL,                -- email / sms
    type            VARCHAR(32)  NOT NULL,                -- register / login / verify_email / verify_phone / password_reset / bind
    expires_at      TIMESTAMPTZ  NOT NULL,
    used_at         TIMESTAMPTZ,
    ip              VARCHAR(45),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
```

### 4.6 邮件发送记录表

```sql
CREATE TABLE emails (
    id              BIGSERIAL    PRIMARY KEY,
    to_addr         VARCHAR(256) NOT NULL,
    subject         VARCHAR(256) NOT NULL,
    content         TEXT         NOT NULL,                -- HTML body
    template        VARCHAR(64),                          -- 模板标识
    provider        VARCHAR(32),                          -- smtp / mailgun
    status          VARCHAR(16)  NOT NULL DEFAULT 'pending', -- pending / sent / failed
    provider_msg_id VARCHAR(256),                         -- provider 返回的消息 ID
    error_message   VARCHAR(512),                         -- 失败原因
    ip              VARCHAR(45),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
```

### 4.7 短信发送记录表

```sql
CREATE TABLE sms_logs (
    id              BIGSERIAL    PRIMARY KEY,
    phone           VARCHAR(20)  NOT NULL,
    content         VARCHAR(512) NOT NULL,
    template        VARCHAR(64),                          -- 模板标识
    provider        VARCHAR(32),                          -- aliyun / tencent / twilio
    status          VARCHAR(16)  NOT NULL DEFAULT 'pending', -- pending / sent / failed
    provider_msg_id VARCHAR(256),                         -- provider 返回的消息 ID
    error_message   VARCHAR(512),                         -- 失败原因
    ip              VARCHAR(45),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
```

### 4.8 RBAC 表

#### 4.6.1 群组表

```sql
CREATE TABLE groups (
    id              BIGINT       PRIMARY KEY,              -- 雪花 ID
    name            VARCHAR(64)  NOT NULL,
    description     VARCHAR(256),
    parent_id       BIGINT       REFERENCES groups(id),    -- 支持树形组织结构
    status          VARCHAR(16)  NOT NULL DEFAULT 'active', -- active / disabled
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ,                           -- GORM soft delete

    CONSTRAINT uq_groups_name UNIQUE (name)
);
```

#### 4.6.2 用户-群组关联表

```sql
CREATE TABLE user_groups (
    id              BIGSERIAL    PRIMARY KEY,
    user_id         BIGINT       NOT NULL REFERENCES users(id),
    group_id        BIGINT       NOT NULL REFERENCES groups(id),
    role            VARCHAR(32)  DEFAULT 'member',         -- owner / admin / member（群组内角色，与 RBAC role 区分）
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_user_groups UNIQUE (user_id, group_id)
);
```

#### 4.6.3 角色表

```sql
CREATE TABLE roles (
    id              BIGSERIAL    PRIMARY KEY,
    name            VARCHAR(64)  NOT NULL,
    description     VARCHAR(256),
    is_builtin      BOOLEAN      NOT NULL DEFAULT false,   -- 系统内置角色不可删除
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ,                           -- GORM soft delete

    CONSTRAINT uq_roles_name UNIQUE (name)
);
```

#### 4.6.4 权限表

```sql
CREATE TABLE permissions (
    id              BIGSERIAL    PRIMARY KEY,
    resource        VARCHAR(64)  NOT NULL,                 -- 资源：users / groups / roles / sessions / ...
    action          VARCHAR(32)  NOT NULL,                 -- 操作：read / write / delete / manage
    description     VARCHAR(256),
    is_builtin      BOOLEAN      NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ,                           -- GORM soft delete

    CONSTRAINT uq_permissions_resource_action UNIQUE (resource, action)
);
```

#### 4.6.5 权限分组表

```sql
CREATE TABLE permission_groups (
    id              BIGSERIAL    PRIMARY KEY,
    name            VARCHAR(64)  NOT NULL,
    description     VARCHAR(256),
    is_builtin      BOOLEAN      NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ,                           -- GORM soft delete

    CONSTRAINT uq_permission_groups_name UNIQUE (name)
);
```

#### 4.6.6 权限分组项表

```sql
CREATE TABLE permission_group_items (
    id                      BIGSERIAL    PRIMARY KEY,
    permission_group_id     BIGINT       NOT NULL REFERENCES permission_groups(id) ON DELETE CASCADE,
    permission_id           BIGINT       NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,

    CONSTRAINT uq_pgi UNIQUE (permission_group_id, permission_id)
);
```

#### 4.6.7 角色-权限关联表

```sql
CREATE TABLE role_permissions (
    id              BIGSERIAL    PRIMARY KEY,
    role_id         BIGINT       NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_id   BIGINT       NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,

    CONSTRAINT uq_role_permissions UNIQUE (role_id, permission_id)
);
```

#### 4.6.8 角色-权限分组关联表

```sql
CREATE TABLE role_permission_groups (
    id                      BIGSERIAL    PRIMARY KEY,
    role_id                 BIGINT       NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_group_id     BIGINT       NOT NULL REFERENCES permission_groups(id) ON DELETE CASCADE,

    CONSTRAINT uq_role_perm_groups UNIQUE (role_id, permission_group_id)
);
```

#### 4.6.9 群组-角色关联表

```sql
CREATE TABLE group_roles (
    id              BIGSERIAL    PRIMARY KEY,
    group_id        BIGINT       NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    role_id         BIGINT       NOT NULL REFERENCES roles(id) ON DELETE CASCADE,

    CONSTRAINT uq_group_roles UNIQUE (group_id, role_id)
);
```

#### 4.6.10 用户-角色关联表（直接角色分配）

```sql
CREATE TABLE user_roles (
    id              BIGSERIAL    PRIMARY KEY,
    user_id         BIGINT       NOT NULL REFERENCES users(id),
    role_id         BIGINT       NOT NULL REFERENCES roles(id),
    assigned_by     BIGINT,                                 -- 操作人 user_id
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_user_roles UNIQUE (user_id, role_id)
);
```

用户的所有角色 = `user_roles`（直接分配）+ `user_groups` → `group_roles`（通过群组继承）。

### 4.9 索引

```sql
CREATE INDEX idx_identities_user_id    ON identities(user_id);
CREATE INDEX idx_sessions_user_id      ON sessions(user_id);
CREATE INDEX idx_sessions_expires      ON sessions(expires_at) WHERE revoked_at IS NULL;
CREATE INDEX idx_login_logs_user_id    ON login_logs(user_id);
CREATE INDEX idx_login_logs_created    ON login_logs(created_at);
CREATE INDEX idx_verification_codes_target ON verification_codes(target, type);

-- Messages
CREATE INDEX idx_emails_to_addr     ON emails(to_addr);
CREATE INDEX idx_emails_status      ON emails(status);
CREATE INDEX idx_emails_created     ON emails(created_at);
CREATE INDEX idx_sms_logs_phone     ON sms_logs(phone);
CREATE INDEX idx_sms_logs_status    ON sms_logs(status);
CREATE INDEX idx_sms_logs_created   ON sms_logs(created_at);

-- RBAC
CREATE INDEX idx_user_groups_user_id      ON user_groups(user_id);
CREATE INDEX idx_user_groups_group_id     ON user_groups(group_id);
CREATE INDEX idx_permission_group_items_group ON permission_group_items(permission_group_id);
CREATE INDEX idx_role_permissions_role     ON role_permissions(role_id);
CREATE INDEX idx_role_perm_groups_role     ON role_permission_groups(role_id);
CREATE INDEX idx_group_roles_group         ON group_roles(group_id);
CREATE INDEX idx_group_roles_role          ON group_roles(role_id);
CREATE INDEX idx_user_roles_user           ON user_roles(user_id);
CREATE INDEX idx_user_roles_role           ON user_roles(role_id);
```

### 4.10 表关系概览

```
users (用户主表)
  ├── identities (登录方式，一对多)
  ├── sessions (登录会话，一对多)
  ├── login_logs (登录日志，一对多)
  ├── user_groups (群组成员，多对多)
  └── user_roles (直接角色，多对多)

groups (群组)
  ├── user_groups (成员，多对多)
  └── group_roles (群组角色，多对多)

roles (角色)
  ├── role_permissions (直接权限，多对多)
  ├── role_permission_groups (权限分组，多对多)
  ├── group_roles (被哪些群组引用，多对多)
  └── user_roles (被直接分配给哪些用户，多对多)

permissions (权限)
  ├── role_permissions (被哪些角色引用，多对多)
  └── permission_group_items (属于哪些分组，多对多)

permission_groups (权限分组)
  ├── permission_group_items (包含哪些权限，一对多)
  └── role_permission_groups (被哪些角色引用，多对多)

verification_codes (独立，验证码审计)
emails (独立，邮件发送记录)
sms_logs (独立，短信发送记录)

权限查询路径：
  用户 → user_roles → roles → role_permissions/role_permission_groups → permissions
  用户 → user_groups → group_roles → roles → role_permissions/role_permission_groups → permissions
```

## 5. Proto 接口定义

```protobuf
syntax = "proto3";
package user;
option go_package = "user/gen/user";

import "google/api/annotations.proto";
import "google/protobuf/timestamp.proto";
import "google/protobuf/empty.proto";

// ==================== 枚举 ====================

enum UserStatus {
  USER_STATUS_UNSPECIFIED   = 0;
  USER_STATUS_ACTIVE        = 1;
  USER_STATUS_DISABLED      = 2;
  USER_STATUS_PENDING_REVIEW = 3;
}

enum Gender {
  GENDER_UNSPECIFIED = 0;
  GENDER_MALE        = 1;
  GENDER_FEMALE      = 2;
  GENDER_OTHER       = 3;
  GENDER_UNKNOWN     = 4;
}

enum IdentityProvider {
  IDENTITY_PROVIDER_UNSPECIFIED = 0;
  IDENTITY_PROVIDER_EMAIL       = 1;
  IDENTITY_PROVIDER_PHONE       = 2;
  IDENTITY_PROVIDER_GITHUB      = 3;
  IDENTITY_PROVIDER_GOOGLE      = 4;
  IDENTITY_PROVIDER_WECHAT      = 5;
  IDENTITY_PROVIDER_APPLE       = 6;
}

// ==================== Service ====================

service UserService {
  // ---- 认证 ----
  rpc Register(RegisterRequest) returns (RegisterResponse) {
    option (google.api.http) = { post: "/v1/auth/register" body: "*" };
  }
  rpc Login(LoginRequest) returns (LoginResponse) {
    option (google.api.http) = { post: "/v1/auth/login" body: "*" };
  }
  rpc Logout(LogoutRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { post: "/v1/auth/logout" body: "*" };
  }
  rpc RefreshSession(RefreshSessionRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { post: "/v1/auth/refresh" body: "*" };
  }

  // ---- OAuth2 ----
  rpc GetOAuthURL(GetOAuthURLRequest) returns (GetOAuthURLResponse) {
    option (google.api.http) = { get: "/v1/oauth/{provider}/url" };
  }
  rpc OAuthCallback(OAuthCallbackRequest) returns (LoginResponse) {
    option (google.api.http) = { post: "/v1/oauth/{provider}/callback" body: "*" };
  }

  // ---- 用户管理 ----
  rpc GetProfile(google.protobuf.Empty) returns (User) {
    option (google.api.http) = { get: "/v1/profile" };
  }
  rpc UpdateProfile(UpdateProfileRequest) returns (User) {
    option (google.api.http) = { put: "/v1/profile" body: "*" };
  }
  rpc ChangePassword(ChangePasswordRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { post: "/v1/profile/password" body: "*" };
  }
  rpc ResetPassword(ResetPasswordRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { post: "/v1/auth/password-reset" body: "*" };
  }

  // ---- 身份绑定 ----
  rpc ListIdentities(google.protobuf.Empty) returns (ListIdentitiesResponse) {
    option (google.api.http) = { get: "/v1/identities" };
  }
  rpc BindIdentity(BindIdentityRequest) returns (Identity) {
    option (google.api.http) = { post: "/v1/identities" body: "*" };
  }
  rpc UnbindIdentity(UnbindIdentityRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { delete: "/v1/identities/{identity_id}" };
  }

  // ---- 验证码 ----
  rpc SendVerificationCode(SendVerificationCodeRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { post: "/v1/captcha/send" body: "*" };
  }

  // ---- 会话管理 ----
  rpc ListSessions(google.protobuf.Empty) returns (ListSessionsResponse) {
    option (google.api.http) = { get: "/v1/sessions" };
  }
  rpc RevokeSession(RevokeSessionRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { post: "/v1/sessions/{session_id}/revoke" body: "*" };
  }
  rpc RevokeAllSessions(google.protobuf.Empty) returns (google.protobuf.Empty) {
    option (google.api.http) = { post: "/v1/sessions/revoke-all" body: "*" };
  }

  // ---- 管理员 ----
  rpc GetUser(GetUserRequest) returns (User) {
    option (google.api.http) = { get: "/v1/admin/users/{user_id}" };
  }
  rpc ListUsers(ListUsersRequest) returns (ListUsersResponse) {
    option (google.api.http) = { get: "/v1/admin/users" };
  }
  rpc DisableUser(DisableUserRequest) returns (User) {
    option (google.api.http) = { post: "/v1/admin/users/{user_id}/disable" body: "*" };
  }
  rpc GetLoginLogs(GetLoginLogsRequest) returns (GetLoginLogsResponse) {
    option (google.api.http) = { get: "/v1/admin/login-logs" };
  }

  // ---- RBAC：群组 ----
  rpc CreateGroup(CreateGroupRequest) returns (Group) {
    option (google.api.http) = { post: "/v1/admin/groups" body: "*" };
  }
  rpc GetGroup(GetGroupRequest) returns (Group) {
    option (google.api.http) = { get: "/v1/admin/groups/{group_id}" };
  }
  rpc UpdateGroup(UpdateGroupRequest) returns (Group) {
    option (google.api.http) = { put: "/v1/admin/groups/{group_id}" body: "*" };
  }
  rpc ListGroups(ListGroupsRequest) returns (ListGroupsResponse) {
    option (google.api.http) = { get: "/v1/admin/groups" };
  }
  rpc DeleteGroup(DeleteGroupRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { delete: "/v1/admin/groups/{group_id}" };
  }
  rpc AddGroupMember(AddGroupMemberRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { post: "/v1/admin/groups/{group_id}/members" body: "*" };
  }
  rpc RemoveGroupMember(RemoveGroupMemberRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { delete: "/v1/admin/groups/{group_id}/members/{user_id}" };
  }
  rpc ListGroupMembers(ListGroupMembersRequest) returns (ListGroupMembersResponse) {
    option (google.api.http) = { get: "/v1/admin/groups/{group_id}/members" };
  }

  // ---- RBAC：角色 ----
  rpc CreateRole(CreateRoleRequest) returns (Role) {
    option (google.api.http) = { post: "/v1/admin/roles" body: "*" };
  }
  rpc UpdateRole(UpdateRoleRequest) returns (Role) {
    option (google.api.http) = { put: "/v1/admin/roles/{role_id}" body: "*" };
  }
  rpc DeleteRole(DeleteRoleRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { delete: "/v1/admin/roles/{role_id}" };
  }
  rpc ListRoles(ListRolesRequest) returns (ListRolesResponse) {
    option (google.api.http) = { get: "/v1/admin/roles" };
  }

  // ---- RBAC：权限 ----
  rpc ListPermissions(google.protobuf.Empty) returns (ListPermissionsResponse) {
    option (google.api.http) = { get: "/v1/admin/permissions" };
  }
  rpc ListPermissionGroups(google.protobuf.Empty) returns (ListPermissionGroupsResponse) {
    option (google.api.http) = { get: "/v1/admin/permission-groups" };
  }

  // ---- RBAC：群组角色 ----
  rpc AddGroupRole(AddGroupRoleRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { post: "/v1/admin/groups/{group_id}/roles" body: "*" };
  }
  rpc RemoveGroupRole(RemoveGroupRoleRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { delete: "/v1/admin/groups/{group_id}/roles/{role_id}" };
  }
  rpc ListGroupRoles(ListGroupRolesRequest) returns (ListGroupRolesResponse) {
    option (google.api.http) = { get: "/v1/admin/groups/{group_id}/roles" };
  }

  // ---- RBAC：用户角色 ----
  rpc AssignRole(AssignRoleRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { post: "/v1/admin/users/{user_id}/roles" body: "*" };
  }
  rpc RevokeRole(RevokeRoleRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { delete: "/v1/admin/users/{user_id}/roles/{role_id}" };
  }
  rpc ListUserRoles(ListUserRolesRequest) returns (ListUserRolesResponse) {
    option (google.api.http) = { get: "/v1/admin/users/{user_id}/roles" };
  }
}

// ==================== 认证 ====================

message RegisterRequest {
  string provider = 1;          // email / phone
  string target   = 2;          // 邮箱地址 / 手机号
  string password = 3;          // 密码（provider=email 时必填）
  string code     = 4;          // 验证码
  string nickname = 5;          // 可选
}

message RegisterResponse {
  User        user       = 1;
  string      session_id = 2;
}

message LoginRequest {
  string provider = 1;          // email / phone
  string target   = 2;          // 邮箱 / 手机号
  string password = 3;          // 密码（provider=email 时）
  string code     = 4;          // 验证码（provider=phone 时）
}

message LoginResponse {
  User        user       = 1;
  string      session_id = 2;
  bool        is_new     = 3;   // 是否为新注册用户
}

message LogoutRequest {
  string session_id = 1;        // 可选，不传则登出当前 session
}

message RefreshSessionRequest {}

// ==================== OAuth2 ====================

message GetOAuthURLRequest {
  string provider = 1;          // github / google / wechat / apple
  string redirect_url = 2;      // 可选，覆盖默认回调地址
  string state    = 3;          // 可选，CSRF 防护（不传则自动生成）
}

message GetOAuthURLResponse {
  string url   = 1;
  string state = 2;
}

message OAuthCallbackRequest {
  string provider = 1;          // github / google / wechat / apple
  string code     = 2;
  string state    = 3;
}

// ==================== 用户 ====================

message User {
  int64                     id              = 1;
  string                    nickname        = 2;
  string                    real_name       = 3;
  string                    avatar_url      = 4;
  string                    email           = 5;
  string                    phone           = 6;
  Gender                    gender          = 7;
  string                    birthday        = 8;   // YYYY-MM-DD
  string                    timezone        = 9;
  string                    locale          = 10;
  string                    bio             = 11;
  UserStatus                status          = 12;
  string                    register_source = 14;
  google.protobuf.Timestamp last_login_at   = 16;
  google.protobuf.Timestamp created_at      = 17;
  google.protobuf.Timestamp updated_at      = 18;
}

message UpdateProfileRequest {
  string nickname   = 1;
  string real_name  = 2;
  string avatar_url = 3;
  Gender gender     = 4;
  string birthday   = 5;
  string timezone   = 6;
  string locale     = 7;
  string bio        = 8;
}

message ChangePasswordRequest {
  string old_password = 1;
  string new_password = 2;
}

message ResetPasswordRequest {
  string target      = 1;     // 邮箱 / 手机号
  string code        = 2;     // 验证码（type=password_reset）
  string new_password = 3;
}

// ==================== 身份 ====================

message Identity {
  int64                     id            = 1;
  IdentityProvider          provider      = 2;
  string                    provider_uid  = 3;  // 脱敏显示（邮箱/手机部分遮盖）
  bool                      verified      = 4;
  google.protobuf.Timestamp created_at    = 5;
}

message ListIdentitiesResponse {
  repeated Identity identities = 1;
}

message BindIdentityRequest {
  string provider = 1;          // email / phone
  string target   = 2;          // 邮箱 / 手机号
  string code     = 3;          // 验证码
  string password = 4;          // 密码（provider=email 时必填）
}

message UnbindIdentityRequest {
  int64 identity_id = 1;
  string code       = 2;        // 验证码（安全验证）
}

// ==================== 验证码 ====================

message SendVerificationCodeRequest {
  string target  = 1;           // 邮箱 / 手机号
  string channel = 2;           // email / sms
  string type    = 3;           // register / login / verify_email / verify_phone / password_reset / bind
}

// ==================== 会话 ====================

message Session {
  string                    id             = 1;
  string                    ip             = 2;
  string                    device_type    = 3;
  string                    os             = 4;
  string                    browser        = 5;
  string                    country        = 6;
  string                    city           = 7;
  google.protobuf.Timestamp created_at     = 8;
  google.protobuf.Timestamp last_active_at = 9;
  bool                      current        = 10; // 是否为当前会话
}

message ListSessionsResponse {
  repeated Session sessions = 1;
}

message RevokeSessionRequest {
  string session_id = 1;
}

// ==================== 管理员 ====================

message GetUserRequest {
  int64 user_id = 1;
}

message ListUsersRequest {
  string status    = 1;  // 可选筛选
  string keyword   = 2;  // 可选搜索（昵称/邮箱/手机）
  int32  page_size = 3;
  string cursor    = 4;
}

message ListUsersResponse {
  repeated User users       = 1;
  string        next_cursor = 2;
  int32         total       = 3;
}

message DisableUserRequest {
  int64  user_id = 1;
  bool   disable = 2;  // true=禁用, false=启用
  string reason  = 3;
}

message GetLoginLogsRequest {
  int64  user_id   = 1;  // 可选筛选
  string provider  = 2;  // 可选筛选
  bool   success   = 3;  // 可选筛选
  int32  page_size = 4;
  string cursor    = 5;
}

message GetLoginLogsResponse {
  repeated LoginLog logs       = 1;
  string            next_cursor = 2;
  int32             total       = 3;
}

message LoginLog {
  int64                     id           = 1;
  int64                     user_id      = 2;
  string                    provider     = 3;
  string                    action       = 4;
  bool                      success      = 5;
  string                    fail_reason  = 6;
  string                    ip           = 7;
  string                    device_type  = 8;
  string                    os           = 9;
  string                    browser      = 10;
  string                    country      = 11;
  string                    city         = 12;
  google.protobuf.Timestamp created_at   = 13;
}

// ==================== RBAC：群组 ====================

message Group {
  int64                     id          = 1;
  string                    name        = 2;
  string                    description = 3;
  int64                     parent_id   = 4;
  string                    status      = 5;
  int32                     member_count = 6;
  google.protobuf.Timestamp created_at  = 7;
  google.protobuf.Timestamp updated_at  = 8;
}

message CreateGroupRequest {
  string name        = 1;
  string description = 2;
  int64  parent_id   = 3;    // 可选，父群组
}

message GetGroupRequest {
  int64 group_id = 1;
}

message UpdateGroupRequest {
  int64  group_id    = 1;
  string name        = 2;
  string description = 3;
}

message ListGroupsRequest {
  string status    = 1;  // 可选筛选
  int32  page_size = 2;
  string cursor    = 3;
}

message ListGroupsResponse {
  repeated Group groups      = 1;
  string         next_cursor = 2;
  int32          total       = 3;
}

message DeleteGroupRequest {
  int64 group_id = 1;
}

message AddGroupMemberRequest {
  int64  group_id = 1;
  int64  user_id  = 2;
  string role     = 3;      // owner / admin / member
}

message RemoveGroupMemberRequest {
  int64 group_id = 1;
  int64 user_id  = 2;
}

message ListGroupMembersRequest {
  int64  group_id  = 1;
  string role      = 2;     // 可选筛选
  int32  page_size = 3;
  string cursor    = 4;
}

message ListGroupMembersResponse {
  repeated GroupMember members     = 1;
  string               next_cursor = 2;
  int32                total       = 3;
}

message GroupMember {
  int64                     user_id    = 1;
  string                    nickname   = 2;
  string                    avatar_url = 3;
  string                    role       = 4;     // owner / admin / member
  google.protobuf.Timestamp created_at = 5;
}

// ==================== RBAC：角色 ====================

message Role {
  int64                     id          = 1;
  string                    name        = 2;
  string                    description = 3;
  bool                      is_builtin  = 5;
  repeated Permission       permissions = 6;     // 直接绑定的权限
  repeated PermissionGroup  perm_groups = 7;     // 绑定的权限分组
  google.protobuf.Timestamp created_at  = 8;
  google.protobuf.Timestamp updated_at  = 9;
}

message CreateRoleRequest {
  string name        = 1;
  string description = 2;
  repeated int64 permission_ids       = 4;  // 直接绑定的权限 ID
  repeated int64 permission_group_ids = 5;  // 绑定的权限分组 ID
}

message UpdateRoleRequest {
  int64  role_id      = 1;
  string name         = 2;
  string description  = 3;
  repeated int64 permission_ids       = 4;  // 全量替换
  repeated int64 permission_group_ids = 5;  // 全量替换
}

message DeleteRoleRequest {
  int64 role_id = 1;
}

message ListRolesRequest {
  int32  page_size = 1;
  string cursor    = 2;
}

message ListRolesResponse {
  repeated Role  roles       = 1;
  string         next_cursor = 2;
  int32          total       = 3;
}

// ==================== RBAC：权限 ====================

message Permission {
  int64  id          = 1;
  string resource    = 2;     // 资源名
  string action      = 3;     // 操作名
  string description = 4;
}

message ListPermissionsResponse {
  repeated Permission permissions = 1;
}

message PermissionGroup {
  int64              id          = 1;
  string             name        = 2;
  string             description = 3;
  repeated Permission permissions = 4;   // 分组内的权限列表
}

message ListPermissionGroupsResponse {
  repeated PermissionGroup groups = 1;
}

// ==================== RBAC：群组角色 ====================

message AddGroupRoleRequest {
  int64 group_id = 1;
  int64 role_id  = 2;
}

message RemoveGroupRoleRequest {
  int64 group_id = 1;
  int64 role_id  = 2;
}

message ListGroupRolesRequest {
  int64 group_id = 1;
}

message ListGroupRolesResponse {
  repeated Role roles = 1;
}

// ==================== RBAC：用户角色 ====================

message AssignRoleRequest {
  int64 user_id = 1;
  int64 role_id = 2;
}

message RevokeRoleRequest {
  int64 user_id = 1;
  int64 role_id = 2;
}

message ListUserRolesRequest {
  int64 user_id = 1;
}

message UserRole {
  int64                     id        = 1;
  int64                     role_id   = 2;
  string                    role_name = 3;
  string                    source    = 4;     // "direct" / "group:{group_name}"
  google.protobuf.Timestamp created_at = 5;
}

message ListUserRolesResponse {
  repeated UserRole roles = 1;
}
```

## 6. 消息模块

详见 [`go-common/docs/superpowers/specs/2026-05-22-message-design.md`](../../go-common/docs/superpowers/specs/2026-05-22-message-design.md)

位于 `go-common/message`，独立的消息发送模块。支持 email（SMTP / Mailgun）和 SMS（阿里云 / 腾讯云 / Twilio），多 provider fallback。user-service 通过 go.mod 依赖引入。

## 7. 验证码模块

详见 [`go-common/docs/superpowers/specs/2026-05-22-captcha-design.md`](../../go-common/docs/superpowers/specs/2026-05-22-captcha-design.md)

位于 `go-common/captcha`，依赖 `go-common/message`。提供限流（Redis 两层限流）、验证码生成（可配置格式）、存储（Redis TTL 5min）、校验。user-service 通过 go.mod 依赖引入。

## 8. Session 管理

### 8.1 Redis 存储

```
session:{id}             → Hash {user_id, nickname, avatar_url, ip, device_type, ...} TTL 7天
user_sessions:{user_id}  → SET {session_id1, session_id2, ...} TTL 跟随最晚的 session
```

### 8.2 传递方式

session-id 作为 opaque token，由调用方决定如何传递：
- gRPC 调用方：metadata `authorization: Bearer {session-id}`
- in-process 调用方：直接传 string 参数
- HTTP 传输细节（cookie / header）由网关或业务方处理，user-service 不关心

### 8.3 鉴权流程

1. 从 metadata `authorization` 取 Bearer token（即 session-id）
2. 查 Redis `session:{id}`
3. 不存在或已过期 → 返回 `Unauthenticated`
4. 存在 → 刷新 TTL，注入 user_id 到 context
5. 管理 API 需额外检查 RBAC 权限（通过 Authorizer 检查 user 是否拥有对应 resource:action）

### 8.4 操作

- 踢下线：删除 `session:{id}` + 从 `user_sessions:{user_id}` 移除
- 全部踢下线：遍历 `user_sessions:{user_id}` 逐个删除
- 清理过期：Redis TTL 自动过期 + 定期同步到 PG 标记 revoked_at

## 9. Identity Provider 接口

```go
// internal/identity/provider.go
package identity

type Provider interface {
    Name() string // "email" / "phone" / "github" / "google" / "wechat" / "apple"

    // Register 注册（邮箱/手机创建新 identity）
    Register(ctx context.Context, req *RegisterRequest) (*ProviderResult, error)

    // Authenticate 认证（验证密码/验证码/OAuth）
    Authenticate(ctx context.Context, req *AuthRequest) (*ProviderResult, error)

    // GetAuthURL 获取 OAuth 授权 URL（仅 OAuth provider）
    GetAuthURL(ctx context.Context, redirectURL, state string) (string, error)

    // HandleCallback 处理 OAuth 回调（仅 OAuth provider）
    HandleCallback(ctx context.Context, code string) (*ProviderResult, error)
}

type RegisterRequest struct {
    Target     string // 邮箱 / 手机号
    Password   string // 密码（provider=email 时）
    Code       string // 验证码
    Nickname   string // 可选
}

type AuthRequest struct {
    Target   string // 邮箱 / 手机号
    Password string // 密码
    Code     string // 验证码
}

type ProviderResult struct {
    ProviderUID  string         // provider 侧的用户标识
    Email        string         // OAuth 返回的邮箱（如有）
    Phone        string         // OAuth 返回的手机（如有）
    Nickname     string         // OAuth 返回的昵称
    AvatarURL    string         // OAuth 返回的头像
    RawData      map[string]any // OAuth 原始返回数据
    Credentials  string        // 需要存储的凭证（bcrypt hash 等）
}
```

### OAuth2 配置

每个 OAuth provider 的配置存储在 config.yaml：

```yaml
oauth:
  github:
    client_id: "xxx"
    client_secret: "xxx"
    redirect_url: "http://localhost:8080/v1/oauth/github/callback"
  google:
    client_id: "xxx"
    client_secret: "xxx"
    redirect_url: "http://localhost:8080/v1/oauth/google/callback"
  wechat:
    app_id: "xxx"
    app_secret: "xxx"
    redirect_url: "http://localhost:8080/v1/oauth/wechat/callback"
  apple:
    client_id: "xxx"
    team_id: "xxx"
    key_id: "xxx"
    private_key: "-----BEGIN PRIVATE KEY-----..."
    redirect_url: "http://localhost:8080/v1/oauth/apple/callback"
```

## 10. 核心流程

### 10.1 邮箱注册

```
客户端 → SendVerificationCode(target=email, channel=email, type=register)
    → captcha 检查限流（全局 + 分级）
    → 生成 6 位数字验证码
    → 存 Redis（TTL 5min）
    → messenger.Send("email", {To: email, Body: code})
    → 写 verification_codes 表（审计）

客户端 → Register(provider=email, target=email, password=xxx, code=123456)
    → captcha.Verify(email, code, "register")
    → 检查 identities 中是否已存在 provider=email, provider_uid=email
    → 生成 user_id（雪花）
    → 创建 user + identity(provider=email, credentials=bcrypt(password))
    → 创建 session
    → 写 login_log(action=register, success=true)
    → 返回 user + session_id
```

### 10.2 手机验证码登录

```
客户端 → SendVerificationCode(target=phone, channel=sms, type=login)
    → captcha 限流检查
    → 生成验证码，存 Redis，messenger.Send("sms")

客户端 → Login(provider=phone, target=phone, code=123456)
    → captcha.Verify(phone, code, "login")
    → 查找 identity(provider=phone, provider_uid=phone)
    → 找到 → 创建 session，写 login_log，返回 user + session_id
    → 未找到 → 自动注册：
        - 创建 user（nickname="用户{后4位手机号}"，phone=手机号，register_source=phone）
        - 创建 identity(provider=phone, verified=true)
        - 创建 session
        - 写 login_log(action=register, success=true)
        - 返回 is_new=true
```

### 10.2.1 密码重置

```
客户端 → SendVerificationCode(target=email, channel=email, type=password_reset)
    → captcha 限流检查 + 发送验证码

客户端 → ResetPassword(target=email, code=123456, new_password=xxx)
    → captcha.Verify(email, code, "password_reset")
    → 查找 identity(provider=email, provider_uid=email)
    → 更新 identity.credentials = bcrypt(new_password)
```

### 10.3 GitHub OAuth 登录

```
客户端 → GetOAuthURL(provider=github)
    → 返回 https://github.com/login/oauth/authorize?client_id=xxx&state=yyy

用户在 GitHub 授权后回调 → OAuthCallback(provider=github, code=xxx, state=yyy)
    → GitHub provider 用 code 换 access_token
    → 调 GitHub API 获取用户信息（id, login, email, avatar_url）
    → 查找 identity(provider=github, provider_uid=github_id)
    → 找到 → 登录，创建 session
    → 未找到 → 创建 user + identity，注册 + 登录
```

### 10.4 绑定新登录方式

```
已登录用户 → SendVerificationCode(target=new_email, type=bind)
已登录用户 → BindIdentity(provider=email, target=new_email, code=123456, password=xxx)
    → 验证 session
    → captcha.Verify(new_email, code, "bind")
    → 检查 identities 中是否已被其他用户绑定
    → 创建 identity 关联当前 user
```

### 10.5 踢下线

```
用户 → RevokeSession(session_id=xxx)
    → 验证 session 属于当前用户
    → 删除 Redis session:{xxx}
    → 从 user_sessions:{user_id} 移除
    → 更新 PG sessions 表 revoked_at
```

## 11. 技术选型

| 组件 | 选择 | 说明 |
|------|------|------|
| 语言 | Go | 与 pay-service 对齐 |
| RPC 框架 | gRPC + grpc-gateway | 一套 proto，双协议 |
| 数据库 | PostgreSQL | 用户/身份/日志 |
| 缓存/Session | Redis | Session 存储 + 验证码 + 限流 |
| DB 迁移 | golang-migrate | 与 pay-service 对齐 |
| 密码哈希 | bcrypt | Go 标准库 |
| 验证码 | go-common/captcha | Redis 限流 + 依赖 message 模块发送 |
| 消息 | go-common/message | 多渠道多 provider，fallback 机制 |
| ID 生成 | 雪花算法 | 与 pay-service 对齐 |
| 日志 | slog | Go 标准库 |
| 配置 | viper | YAML 配置 |
| 权限（第一版） | DBAuthorizer | PG + 内存缓存，第二版 OPA Rego |
| GeoIP（可选） | ip2region / MaxMind | IP → 国家/城市 |

## 12. Authorizer 接口与 DBAuthorizer 实现

### 12.1 接口定义

```go
// internal/rbac/authorizer.go
package rbac

import "context"

// Authorizer 权限检查接口。
type Authorizer interface {
    // Check 检查用户是否有权限执行操作。
    // resource: 如 "users", "groups", "roles", "sessions"
    // action: 如 "read", "write", "delete", "manage"
    Check(ctx context.Context, userID int64, resource, action string) (bool, error)

    // CheckInGroup 检查用户在特定群组内是否有权限。
    CheckInGroup(ctx context.Context, userID, groupID int64, resource, action string) (bool, error)

    // Refresh 刷新内存缓存（角色/权限变更后调用）。
    Refresh(ctx context.Context) error
}
```

### 12.2 DBAuthorizer 实现

DBAuthorizer 从 PostgreSQL 加载 RBAC 数据到内存缓存，权限检查走内存。

```go
// internal/rbac/authorizer.go

type DBAuthorizer struct {
    db    *sql.DB
    cache *permissionCache
    mu    sync.RWMutex
}

func NewDBAuthorizer(db *sql.DB) *DBAuthorizer {
    return &DBAuthorizer{
        db:    db,
        cache: newPermissionCache(),
    }
}
```

### 12.3 权限检查逻辑

`Check` 方法通过 SQL UNION 查询用户的所有权限：

```sql
-- 获取用户所有角色（直接 + 通过群组继承）
WITH all_roles AS (
    -- 路径1：用户直接分配的角色
    SELECT role_id FROM user_roles WHERE user_id = $1
    UNION
    -- 路径2：用户通过群组继承的角色
    SELECT gr.role_id
    FROM user_groups ug
    JOIN group_roles gr ON gr.group_id = ug.group_id
    WHERE ug.user_id = $1
)
-- 获取所有角色的权限（直接权限 + 权限分组中的权限）
SELECT DISTINCT p.resource, p.action
FROM all_roles ar
JOIN (
    -- 路径A：角色 → 直接权限
    SELECT rp.role_id, p.resource, p.action
    FROM role_permissions rp
    JOIN permissions p ON p.id = rp.permission_id

    UNION

    -- 路径B：角色 → 权限分组 → 权限
    SELECT rpg.role_id, p.resource, p.action
    FROM role_permission_groups rpg
    JOIN permission_group_items pgi ON pgi.permission_group_id = rpg.permission_group_id
    JOIN permissions p ON p.id = pgi.permission_id
) p ON p.role_id = ar.role_id
```

### 12.4 Redis 缓存设计

RBAC 表关系复杂（10 张表），每次权限检查都查 SQL 不现实。采用 **Redis 缓存，PG 为源头**：

```
请求 → Redis 命中？→ 返回
              ↓ miss
         查 PG → 写 Redis（TTL）→ 返回
```

#### Redis Key 设计

```
─── 用户权限（合并后的最终结果，直接查这个）───
rbac:user_perms:{user_id}
  → Hash { "users:read": "1", "groups:write": "1", ... }
  TTL 10 分钟

─── 用户角色列表（直接 + 群组继承）───
rbac:user_roles:{user_id}
  → Set { "role:1", "role:3", "role:7" }
  TTL 10 分钟

─── 角色权限 ───
rbac:role_perms:{role_id}
  → Set { "users:read", "users:write", "groups:read" }
  TTL 30 分钟

─── 群组角色 ───
rbac:group_roles:{group_id}
  → Set { "role:1", "role:3" }
  TTL 30 分钟
```

#### 缓存流程

```
Check(userID, resource, action):
  1. HGET rbac:user_perms:{userID} "{resource}:{action}"
     → 存在 → 返回 true
  2. Redis miss → 查 PG（三表 UNION 查询）
     → 写入 rbac:user_perms:{userID}（TTL 10min）
     → 返回结果

首次加载用户权限（rbac:user_perms miss 时）：
  1. 查 user_roles → 得到直接角色 ID
  2. 查 user_groups → group_roles → 得到继承角色 ID
  3. 查所有角色的 role_permissions + role_permission_groups
  4. 合并去重 → HSET rbac:user_perms:{user_id}（TTL 10min）
  5. SADD rbac:user_roles:{user_id}（TTL 10min）
```

#### 缓存失效

```
RBAC 变更 API 调用后主动失效：
  - 变更用户角色：DEL rbac:user_perms:{user_id} + DEL rbac:user_roles:{user_id}
  - 变更角色权限：DEL rbac:role_perms:{role_id} + DEL rbac:user_perms:{所有使用该角色的用户}
  - 变更群组角色：DEL rbac:group_roles:{group_id} + DEL rbac:user_perms:{群组内所有成员}

兜底：TTL 自动过期（10-30 分钟）
```

### 12.5 gRPC Interceptor 集成

```go
// internal/middleware/auth.go

// 管理 API 的权限映射
var adminPermissions = map[string]struct{ resource, action string }{
    "/user.UserService/GetUser":         {"users", "read"},
    "/user.UserService/ListUsers":       {"users", "read"},
    "/user.UserService/DisableUser":     {"users", "write"},
    "/user.UserService/GetLoginLogs":    {"login_logs", "read"},
    "/user.UserService/CreateGroup":     {"groups", "write"},
    "/user.UserService/GetGroup":        {"groups", "read"},
    "/user.UserService/ListGroups":      {"groups", "read"},
    "/user.UserService/UpdateGroup":     {"groups", "write"},
    "/user.UserService/DeleteGroup":     {"groups", "delete"},
    "/user.UserService/AddGroupMember":  {"groups", "manage"},
    "/user.UserService/AddGroupRole":    {"groups", "manage"},
    "/user.UserService/RemoveGroupRole": {"groups", "manage"},
    "/user.UserService/ListGroupRoles":  {"groups", "read"},
    "/user.UserService/CreateRole":      {"roles", "write"},
    "/user.UserService/UpdateRole":      {"roles", "write"},
    "/user.UserService/DeleteRole":      {"roles", "delete"},
    "/user.UserService/ListRoles":       {"roles", "read"},
    "/user.UserService/AssignRole":      {"user_roles", "write"},
    "/user.UserService/RevokeRole":      {"user_roles", "delete"},
    // ...
}
```

拦截流程：请求到达 → Session 验证 → 查 adminPermissions 映射 → 有映射则调 `authorizer.Check(userID, resource, action)` → 无映射则放行（用户自身操作如 GetProfile 不需权限检查）。

## 13. 系统种子数据

### 13.1 内置角色

| 角色名 | 说明 | 权限范围 |
|--------|------|----------|
| super_admin | 超级管理员 | 所有权限 |
| admin | 管理员 | 除角色管理外的所有权限 |
| member | 普通成员 | 无管理权限 |
| group_admin | 群组管理员 | 群组内管理权限 |

### 13.2 内置权限

| 资源 | 操作 | 说明 |
|------|------|------|
| users | read | 查看用户列表/详情 |
| users | write | 禁用/启用用户 |
| users | delete | 删除用户 |
| groups | read | 查看群组 |
| groups | write | 创建/编辑群组 |
| groups | delete | 删除群组 |
| groups | manage | 管理群组成员 |
| roles | read | 查看角色 |
| roles | write | 创建/编辑角色 |
| roles | delete | 删除角色 |
| permissions | read | 查看权限列表 |
| login_logs | read | 查看登录日志 |
| sessions | manage | 管理会话（踢下线） |
| user_roles | write | 分配用户角色 |
| user_roles | delete | 撤销用户角色 |

### 13.3 内置权限分组

| 分组名 | 包含权限 |
|--------|----------|
| user_management | users:read, users:write, users:delete |
| group_management | groups:read, groups:write, groups:delete, groups:manage |
| role_management | roles:read, roles:write, roles:delete, permissions:read |
| security | login_logs:read, sessions:manage |

### 13.4 种子数据初始化

服务启动时通过 migration 或初始化函数确保种子数据存在（`is_builtin=true` 的记录不可删除/修改名称）。

### 13.5 默认角色策略

**member 角色是隐式的，不需要写 `user_roles` 记录。**

- 新注册用户在 `user_roles` 表中无记录，Authorizer 自动视为 member 角色
- member 角色无任何管理权限，所有 admin API 均被拦截
- 只有分配 admin / super_admin / group_admin 等提升权限时，才向 `user_roles` 插入记录
- 加入群组时同理，只有非 member 的群组角色才写 `group_roles`

效果：`user_roles` 表只存储管理员记录，数据量远小于用户总数。

第二版将 DBAuthorizer 替换为 OPA (in-process)：
- 使用 Go SDK `github.com/open-policy-agent/opa` 嵌入 Rego 引擎
- Rego 策略编译进二进制，支持更复杂的条件判断（如时间限制、IP 白名单）
- 数据源仍为 PG，通过 OPA Data API 注入
- Authorizer 接口不变，实现切换为 `OPAAuthorizer`

## 14. 配置定义

```go
package config

import "time"

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
	ClientID     string `mapstructure:"client_id"`
	TeamID       string `mapstructure:"team_id"`
	KeyID        string `mapstructure:"key_id"`
	PrivateKey   string `mapstructure:"private_key"`
	RedirectURL  string `mapstructure:"redirect_url"`
}

type SnowflakeConfig struct {
	Node int64 `mapstructure:"node"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}
```
