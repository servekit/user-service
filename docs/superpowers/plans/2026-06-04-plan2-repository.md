# Plan 2: Repository Layer

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现所有数据库 repository，提供类型安全的数据访问层，每个 repo 用 testcontainers 集成测试覆盖。

**Architecture:** 每个 repository 封装 GORM 查询，构造函数注入 `*gorm.DB`。方法返回 xerr 包装的错误。测试用 testcontainers 启动真实 PG，确保 SQL 兼容性。

**Tech Stack:** Go, GORM, PostgreSQL, testcontainers-go, go-common/xerr

**Depends on:** Plan 1 (Foundation)

**Produces:** 完整的 repository 层，所有表均有 CRUD + 集成测试。

**Spec:** `docs/superpowers/specs/2026-05-22-user-service-design.md` §4 (Database), §4.9 (Indexes)

---

## File Structure

```
internal/repository/
  user.go                   # UserRepo
  user_test.go
  identity.go               # IdentityRepo
  identity_test.go
  session.go                # SessionRepo
  session_test.go
  login_log.go              # LoginLogRepo
  login_log_test.go
  verification_code.go      # VerificationCodeRepo
  verification_code_test.go
  message.go                # EmailRepo + SMSLogRepo
  message_test.go
  group.go                  # GroupRepo + UserGroupRepo
  group_test.go
  role.go                   # RoleRepo + PermissionRepo + PermissionGroupRepo
  role_test.go
  rbac_join.go              # RolePermission, RolePermissionGroup, GroupRole, UserRole repos
  rbac_join_test.go
  helpers_test.go           # 共用测试辅助函数（创建测试数据等）
```

---

### Task 1: Test Helpers + UserRepo

**Files:**
- Create: `internal/repository/helpers_test.go`
- Create: `internal/repository/user.go`
- Create: `internal/repository/user_test.go`

- [ ] **Step 1: Write helpers_test.go**

测试辅助函数，供所有 repo 测试复用：

```go
package repository

import (
	"testing"

	"user-service/internal/database"
	"user-service/internal/models"

	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := database.SetupTestDB(t)
	// AutoMigrate for test isolation — production uses golang-migrate
	err := db.AutoMigrate(
		&models.User{},
		&models.Identity{},
		&models.Session{},
		&models.LoginLog{},
		&models.VerificationCode{},
		&models.Email{},
		&models.SMSLog{},
		&models.Group{},
		&models.UserGroup{},
		&models.Role{},
		&models.Permission{},
		&models.PermissionGroup{},
		&models.PermissionGroupItem{},
		&models.RolePermission{},
		&models.RolePermissionGroup{},
		&models.GroupRole{},
		&models.UserRole{},
	)
	if err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

func cleanupAll(t *testing.T, db *gorm.DB) {
	t.Helper()
	tables := []string{
		"user_roles", "group_roles", "role_permission_groups", "role_permissions",
		"permission_group_items", "permission_groups", "permissions", "roles",
		"user_groups", "groups", "verification_codes", "login_logs", "sessions",
		"identities", "sms_logs", "emails", "users",
	}
	for _, tbl := range tables {
		db.Exec("TRUNCATE TABLE " + tbl + " CASCADE")
	}
}

// testUser creates and returns a minimal User for testing.
// User uses SnowflakeModel (non-autoIncrement), so we must set a unique ID.
var testUserCounter int64 = 1000

func testUser(db *gorm.DB, overrides ...func(*models.User)) *models.User {
	atomic.AddInt64(&testUserCounter, 1)
	u := &models.User{
		ID:             testUserCounter,
		Nickname:       "testuser",
		Status:         "active",
		RegisterSource: "email",
	}
	for _, fn := range overrides {
		fn(u)
	}
	if err := db.Create(u).Error; err != nil {
		panic(err)
	}
	return u
}
```

- [ ] **Step 2: Write user_test.go (failing test)**

```go
package repository

import (
	"context"
	"errors"
	"testing"

	"user-service/internal/models"

	"go-common/xerr"
)

func TestUserRepo_Create(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewUserRepository(db)
	ctx := context.Background()

	user := &models.User{
		Nickname:       "alice",
		Email:          strPtr("alice@example.com"),
		Status:         "active",
		RegisterSource: "email",
	}

	err := repo.Create(ctx, user)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if user.ID == 0 {
		t.Fatal("expected ID to be set")
	}
}

func TestUserRepo_FindByID(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewUserRepository(db)
	ctx := context.Background()

	created := testUser(db)

	found, err := repo.FindByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if found.ID != created.ID {
		t.Errorf("got ID %d, want %d", found.ID, created.ID)
	}
}

func TestUserRepo_FindByID_NotFound(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewUserRepository(db)
	ctx := context.Background()

	_, err := repo.FindByID(ctx, 999999)
	if err == nil {
		t.Fatal("expected error for non-existent user")
	}
	// Verify it's an xerr error with correct reason
	var xerrErr *xerr.Error
	if !errors.As(err, &xerrErr) {
		t.Fatalf("expected xerr.Error, got %T", err)
	}
	if xerrErr.Code().Reason() != "USER_NOT_FOUND" {
		t.Errorf("got reason %q, want USER_NOT_FOUND", xerrErr.Code().Reason())
	}
}

func TestUserRepo_FindByEmail(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewUserRepository(db)
	ctx := context.Background()

	email := "alice@example.com"
	testUser(db, func(u *models.User) {
		u.Email = &email
	})

	found, err := repo.FindByEmail(ctx, email)
	if err != nil {
		t.Fatalf("FindByEmail: %v", err)
	}
	if *found.Email != email {
		t.Errorf("got email %q, want %q", *found.Email, email)
	}
}

func TestUserRepo_Update(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewUserRepository(db)
	ctx := context.Background()

	created := testUser(db)
	created.Nickname = "updated"

	err := repo.Update(ctx, created)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	found, _ := repo.FindByID(ctx, created.ID)
	if found.Nickname != "updated" {
		t.Errorf("got nickname %q, want 'updated'", found.Nickname)
	}
}

func TestUserRepo_List(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewUserRepository(db)
	ctx := context.Background()

	testUser(db, func(u *models.User) { u.Nickname = "user1" })
	testUser(db, func(u *models.User) { u.Nickname = "user2" })

	users, nextCursor, err := repo.List(ctx, "", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2", len(users))
	}
	if nextCursor != "" {
		t.Error("expected empty cursor when all results fit in page")
	}
}

// strPtr is a test helper for nullable string fields.
func strPtr(s string) *string { return &s }
```

- [ ] **Step 3: Run tests — expect compilation failure**

Run: `go test ./internal/repository/... -run TestUserRepo`
Expected: compilation error — `NewUserRepository` not defined

- [ ] **Step 4: Write user.go**

```go
package repository

import (
	"context"
	"fmt"
	"strconv"

	"user-service/internal/models"
	"user-service/internal/xcodes"

	"go-common/xerr"
	"gorm.io/gorm"
)

type UserRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) Create(ctx context.Context, user *models.User) error {
	if err := r.db.WithContext(ctx).Create(user).Error; err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

func (r *UserRepository) FindByID(ctx context.Context, id int64) (*models.User, error) {
	var user models.User
	if err := r.db.WithContext(ctx).First(&user, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, xcodes.ErrUserNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &user, nil
}

func (r *UserRepository) FindByEmail(ctx context.Context, email string) (*models.User, error) {
	var user models.User
	if err := r.db.WithContext(ctx).Where("email = ?", email).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, xcodes.ErrUserNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &user, nil
}

func (r *UserRepository) FindByPhone(ctx context.Context, phone string) (*models.User, error) {
	var user models.User
	if err := r.db.WithContext(ctx).Where("phone = ?", phone).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, xcodes.ErrUserNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &user, nil
}

func (r *UserRepository) Update(ctx context.Context, user *models.User) error {
	result := r.db.WithContext(ctx).Save(user)
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	if result.RowsAffected == 0 {
		return xcodes.ErrUserNotFound.New()
	}
	return nil
}

func (r *UserRepository) UpdateLastLogin(ctx context.Context, id int64, ip string) error {
	result := r.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", id).
		Updates(map[string]any{
			"last_login_at":  gorm.Expr("now()"),
			"last_login_ip":  ip,
		})
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	return nil
}

// List returns a page of users. cursor is the last user ID from previous page.
// Returns users, next cursor, error. nextCursor is "" when no more results.
func (r *UserRepository) List(ctx context.Context, cursor string, pageSize int32) ([]*models.User, string, error) {
	query := r.db.WithContext(ctx).Model(&models.User{}).Order("id ASC")

	if cursor != "" {
		cursorID, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, "", xcodes.ErrBadRequest.Wrapf(err, "invalid cursor: %s", cursor)
		}
		query = query.Where("id > ?", cursorID)
	}

	var users []*models.User
	query = query.Limit(int(pageSize) + 1)
	if err := query.Find(&users).Error; err != nil {
		return nil, "", xcodes.ErrInternal.Wrap(err)
	}

	var nextCursor string
	if len(users) > int(pageSize) {
		nextCursor = fmt.Sprintf("%d", users[pageSize].ID)
		users = users[:pageSize]
	}

	return users, nextCursor, nil
}
```

- [ ] **Step 5: Run tests — expect PASS**

Run: `go test ./internal/repository/... -run TestUserRepo -v -count=1`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/repository/
git commit -m "feat: add user repository with testcontainers tests"
```

---

### Task 2: IdentityRepo

**Files:**
- Create: `internal/repository/identity.go`
- Create: `internal/repository/identity_test.go`

- [ ] **Step 1: Write identity_test.go**

```go
package repository

import (
	"context"
	"testing"

	"user-service/internal/models"
)

func TestIdentityRepo_Create(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewIdentityRepository(db)
	ctx := context.Background()

	u := testUser(db)
	identity := &models.Identity{
		UserID:      u.ID,
		Provider:    "email",
		ProviderUID: "alice@example.com",
		Credentials: "$2a$10$hash",
		Verified:    true,
	}

	err := repo.Create(ctx, identity)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if identity.ID == 0 {
		t.Fatal("expected ID to be set")
	}
}

func TestIdentityRepo_FindByProviderUID(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewIdentityRepository(db)
	ctx := context.Background()

	u := testUser(db)
	uid := "alice@example.com"
	db.Create(&models.Identity{
		UserID: u.ID, Provider: "email", ProviderUID: uid, Credentials: "hash", Verified: true,
	})

	found, err := repo.FindByProviderUID(ctx, "email", uid)
	if err != nil {
		t.Fatalf("FindByProviderUID: %v", err)
	}
	if found.ProviderUID != uid {
		t.Errorf("got ProviderUID %q, want %q", found.ProviderUID, uid)
	}
}

func TestIdentityRepo_FindByUserID(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewIdentityRepository(db)
	ctx := context.Background()

	u := testUser(db)
	db.Create(&models.Identity{UserID: u.ID, Provider: "email", ProviderUID: "a@b.com", Credentials: "h"})
	db.Create(&models.Identity{UserID: u.ID, Provider: "github", ProviderUID: "12345", Credentials: ""})

	identities, err := repo.FindByUserID(ctx, u.ID)
	if err != nil {
		t.Fatalf("FindByUserID: %v", err)
	}
	if len(identities) != 2 {
		t.Errorf("got %d identities, want 2", len(identities))
	}
}

func TestIdentityRepo_UpdateCredentials(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewIdentityRepository(db)
	ctx := context.Background()

	u := testUser(db)
	identity := &models.Identity{
		UserID: u.ID, Provider: "email", ProviderUID: "a@b.com", Credentials: "old",
	}
	db.Create(identity)

	err := repo.UpdateCredentials(ctx, identity.ID, "new_hash")
	if err != nil {
		t.Fatalf("UpdateCredentials: %v", err)
	}

	found, _ := repo.FindByProviderUID(ctx, "email", "a@b.com")
	if found.Credentials != "new_hash" {
		t.Errorf("got credentials %q, want 'new_hash'", found.Credentials)
	}
}

func TestIdentityRepo_Delete(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewIdentityRepository(db)
	ctx := context.Background()

	u := testUser(db)
	identity := &models.Identity{
		UserID: u.ID, Provider: "email", ProviderUID: "a@b.com", Credentials: "h",
	}
	db.Create(identity)

	err := repo.Delete(ctx, identity.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = repo.FindByProviderUID(ctx, "email", "a@b.com")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}
```

- [ ] **Step 2: Write identity.go**

```go
package repository

import (
	"context"

	"user-service/internal/models"
	"user-service/internal/xcodes"

	"gorm.io/gorm"
)

type IdentityRepository struct {
	db *gorm.DB
}

func NewIdentityRepository(db *gorm.DB) *IdentityRepository {
	return &IdentityRepository{db: db}
}

func (r *IdentityRepository) Create(ctx context.Context, identity *models.Identity) error {
	if err := r.db.WithContext(ctx).Create(identity).Error; err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

func (r *IdentityRepository) FindByID(ctx context.Context, id int64) (*models.Identity, error) {
	var identity models.Identity
	if err := r.db.WithContext(ctx).First(&identity, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, xcodes.ErrIdentityNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &identity, nil
}

func (r *IdentityRepository) FindByProviderUID(ctx context.Context, provider, providerUID string) (*models.Identity, error) {
	var identity models.Identity
	if err := r.db.WithContext(ctx).Where("provider = ? AND provider_uid = ?", provider, providerUID).First(&identity).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, xcodes.ErrIdentityNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &identity, nil
}

func (r *IdentityRepository) FindByUserID(ctx context.Context, userID int64) ([]*models.Identity, error) {
	var identities []*models.Identity
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Find(&identities).Error; err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return identities, nil
}

func (r *IdentityRepository) UpdateCredentials(ctx context.Context, id int64, credentials string) error {
	result := r.db.WithContext(ctx).Model(&models.Identity{}).Where("id = ?", id).
		Update("credentials", credentials)
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	if result.RowsAffected == 0 {
		return xcodes.ErrIdentityNotFound.New()
	}
	return nil
}

func (r *IdentityRepository) Delete(ctx context.Context, id int64) error {
	result := r.db.WithContext(ctx).Delete(&models.Identity{}, id)
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	if result.RowsAffected == 0 {
		return xcodes.ErrIdentityNotFound.New()
	}
	return nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/repository/... -run TestIdentityRepo -v -count=1`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/repository/identity.go internal/repository/identity_test.go
git commit -m "feat: add identity repository with tests"
```

---

### Task 3: SessionRepo

**Files:**
- Create: `internal/repository/session.go`
- Create: `internal/repository/session_test.go`

- [ ] **Step 1: Write session_test.go**

```go
package repository

import (
	"context"
	"testing"
	"time"

	"user-service/internal/models"
)

func TestSessionRepo_Create(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewSessionRepository(db)
	ctx := context.Background()

	u := testUser(db)
	session := &models.Session{
		ID: "sess-001", UserID: u.ID, ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}

	err := repo.Create(ctx, session)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestSessionRepo_FindByID(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewSessionRepository(db)
	ctx := context.Background()

	u := testUser(db)
	db.Create(&models.Session{
		ID: "sess-001", UserID: u.ID, ExpiresAt: time.Now().Add(24 * time.Hour),
	})

	found, err := repo.FindByID(ctx, "sess-001")
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if found.UserID != u.ID {
		t.Errorf("got UserID %d, want %d", found.UserID, u.ID)
	}
}

func TestSessionRepo_FindByUserID(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewSessionRepository(db)
	ctx := context.Background()

	u := testUser(db)
	db.Create(&models.Session{ID: "s1", UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)})
	db.Create(&models.Session{ID: "s2", UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)})

	sessions, err := repo.FindByUserID(ctx, u.ID)
	if err != nil {
		t.Fatalf("FindByUserID: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("got %d sessions, want 2", len(sessions))
	}
}

func TestSessionRepo_Revoke(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewSessionRepository(db)
	ctx := context.Background()

	u := testUser(db)
	db.Create(&models.Session{ID: "s1", UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)})

	err := repo.Revoke(ctx, "s1")
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	found, _ := repo.FindByID(ctx, "s1")
	if found.RevokedAt == nil {
		t.Error("expected RevokedAt to be set")
	}
}

func TestSessionRepo_RevokeByUserID(t *testing.T) {
	db := setupTestDB(t)
	defer cleanupAll(t, db)
	repo := NewSessionRepository(db)
	ctx := context.Background()

	u := testUser(db)
	db.Create(&models.Session{ID: "s1", UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)})
	db.Create(&models.Session{ID: "s2", UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)})

	err := repo.RevokeByUserID(ctx, u.ID)
	if err != nil {
		t.Fatalf("RevokeByUserID: %v", err)
	}

	sessions, _ := repo.FindByUserID(ctx, u.ID)
	for _, s := range sessions {
		if s.RevokedAt == nil {
			t.Errorf("session %s should be revoked", s.ID)
		}
	}
}
```

- [ ] **Step 2: Write session.go**

```go
package repository

import (
	"context"

	"user-service/internal/models"
	"user-service/internal/xcodes"

	"gorm.io/gorm"
)

type SessionRepository struct {
	db *gorm.DB
}

func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

func (r *SessionRepository) Create(ctx context.Context, session *models.Session) error {
	if err := r.db.WithContext(ctx).Create(session).Error; err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

func (r *SessionRepository) FindByID(ctx context.Context, id string) (*models.Session, error) {
	var session models.Session
	if err := r.db.WithContext(ctx).First(&session, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, xcodes.ErrSessionInvalid.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &session, nil
}

func (r *SessionRepository) FindByUserID(ctx context.Context, userID int64) ([]*models.Session, error) {
	var sessions []*models.Session
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Find(&sessions).Error; err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return sessions, nil
}

func (r *SessionRepository) UpdateLastActive(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Model(&models.Session{}).Where("id = ?", id).
		Update("last_active_at", gorm.Expr("now()"))
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	return nil
}

func (r *SessionRepository) Revoke(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Model(&models.Session{}).Where("id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", gorm.Expr("now()"))
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	if result.RowsAffected == 0 {
		return xcodes.ErrSessionInvalid.New()
	}
	return nil
}

func (r *SessionRepository) RevokeByUserID(ctx context.Context, userID int64) error {
	result := r.db.WithContext(ctx).Model(&models.Session{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", gorm.Expr("now()"))
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	return nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/repository/... -run TestSessionRepo -v -count=1`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/repository/session.go internal/repository/session_test.go
git commit -m "feat: add session repository with tests"
```

---

### Task 4: LoginLogRepo, VerificationCodeRepo, MessageRepos

**Files:**
- Create: `internal/repository/login_log.go`
- Create: `internal/repository/login_log_test.go`
- Create: `internal/repository/verification_code.go`
- Create: `internal/repository/verification_code_test.go`
- Create: `internal/repository/message.go`
- Create: `internal/repository/message_test.go`

这些 repo 模式相似：简单的 Create + List/Update。按照上面的 TDD 模式实现。

**LoginLogRepo** 方法：
- `Create(ctx, *models.LoginLog) error`
- `ListByUserID(ctx, userID, cursor, pageSize) ([]*models.LoginLog, string, error)`

**VerificationCodeRepo** 方法：
- `Create(ctx, *models.VerificationCode) error`
- `MarkUsed(ctx, id int64) error`

**EmailRepo** 方法：
- `Create(ctx, *models.Email) error`
- `ListByToAddr(ctx, toAddr, cursor, pageSize) ([]*models.Email, string, error)`

**SMSLogRepo** 方法：
- `Create(ctx, *models.SMSLog) error`
- `ListByPhone(ctx, phone, cursor, pageSize) ([]*models.SMSLog, string, error)`

错误处理：`gorm.ErrRecordNotFound` → 对应 xcodes 错误，其他 → `xcodes.ErrInternal.Wrap(err)`。

- [ ] **Step 5: Commit**

```bash
git add internal/repository/login_log.go internal/repository/login_log_test.go \
        internal/repository/verification_code.go internal/repository/verification_code_test.go \
        internal/repository/message.go internal/repository/message_test.go
git commit -m "feat: add login_log, verification_code, email and sms_log repositories"
```

---

### Task 5: GroupRepo + UserGroupRepo

**Files:**
- Create: `internal/repository/group.go`
- Create: `internal/repository/group_test.go`

**GroupRepo** 方法：
- `Create(ctx, *models.Group) error`
- `FindByID(ctx, id) (*models.Group, error)` — not found → `xcodes.ErrGroupNotFound`
- `FindByName(ctx, name) (*models.Group, error)`
- `Update(ctx, *models.Group) error`
- `Delete(ctx, id) error` — soft delete
- `List(ctx, status, cursor, pageSize) ([]*models.Group, string, error)` — status 可选筛选

**UserGroupRepo** 方法：
- `AddMember(ctx, *models.UserGroup) error` — duplicate → `xcodes.ErrAlreadyMember`
- `RemoveMember(ctx, userID, groupID) error` — not found → `xcodes.ErrNotMember`
- `FindByGroupID(ctx, groupID, cursor, pageSize) ([]*models.UserGroup, string, error)`
- `FindByUserID(ctx, userID) ([]*models.UserGroup, error)`
- `UpdateRole(ctx, userID, groupID, role string) error`

TDD: 每个方法一个测试函数。

- [ ] **Commit**

```bash
git add internal/repository/group.go internal/repository/group_test.go
git commit -m "feat: add group and user_group repositories with tests"
```

---

### Task 6: RoleRepo + PermissionRepo + PermissionGroupRepo

**Files:**
- Create: `internal/repository/role.go`
- Create: `internal/repository/role_test.go`

**RoleRepo** 方法：
- `Create(ctx, *models.Role) error`
- `FindByID(ctx, id) (*models.Role, error)` — not found → `xcodes.ErrRoleNotFound`
- `FindByName(ctx, name) (*models.Role, error)`
- `Update(ctx, *models.Role) error`
- `Delete(ctx, id) error` — soft delete；`is_builtin=true` → `xcodes.ErrRoleIsBuiltin`
- `List(ctx, cursor, pageSize) ([]*models.Role, string, error)`

**PermissionRepo** 方法：
- `Create(ctx, *models.Permission) error`
- `FindByID(ctx, id) (*models.Permission, error)`
- `FindByResourceAction(ctx, resource, action) (*models.Permission, error)`
- `FindAll(ctx) ([]*models.Permission, error)`

**PermissionGroupRepo** 方法：
- `Create(ctx, *models.PermissionGroup) error`
- `FindByID(ctx, id) (*models.PermissionGroup, error)`
- `FindAll(ctx) ([]*models.PermissionGroup, error)`
- `AddPermission(ctx, groupID, permissionID) error`
- `RemovePermission(ctx, groupID, permissionID) error`
- `FindPermissionsByGroupID(ctx, groupID) ([]*models.Permission, error)`

TDD: 每个方法一个测试函数。注意测试 Role.Delete 对 is_builtin 的保护。

- [ ] **Commit**

```bash
git add internal/repository/role.go internal/repository/role_test.go
git commit -m "feat: add role, permission and permission_group repositories with tests"
```

---

### Task 7: RBAC Join Table Repos

**Files:**
- Create: `internal/repository/rbac_join.go`
- Create: `internal/repository/rbac_join_test.go`

四个关联表的 repo，放在一个文件里，因为每个只有 2-3 个方法：

**RolePermissionRepo**：
- `Assign(ctx, roleID, permissionID) error`
- `Remove(ctx, roleID, permissionID) error`
- `FindByRoleID(ctx, roleID) ([]*models.RolePermission, error)`

**RolePermissionGroupRepo**：
- `Assign(ctx, roleID, permissionGroupID) error`
- `Remove(ctx, roleID, permissionGroupID) error`
- `FindByRoleID(ctx, roleID) ([]*models.RolePermissionGroup, error)`

**GroupRoleRepo**：
- `Assign(ctx, groupID, roleID) error`
- `Remove(ctx, groupID, roleID) error`
- `FindByGroupID(ctx, groupID) ([]*models.GroupRole, error)`

**UserRoleRepo**：
- `Assign(ctx, *models.UserRole) error`
- `Remove(ctx, userID, roleID) error`
- `FindByUserID(ctx, userID) ([]*models.UserRole, error)`
- `FindByUserAndRole(ctx, userID, roleID) (*models.UserRole, error)`

TDD: 每个 repo 每个方法一个测试。

- [ ] **Commit**

```bash
git add internal/repository/rbac_join.go internal/repository/rbac_join_test.go
git commit -m "feat: add RBAC join table repositories with tests"
```

---

### Task 8: Run All Repository Tests

- [ ] **Step 1: Run full test suite**

Run: `go test ./internal/repository/... -v -count=1 -race`
Expected: all PASS

- [ ] **Step 2: Run linter**

Run: `golangci-lint run ./internal/repository/...`
Expected: no errors

- [ ] **Step 3: Verify coverage**

Run: `go test ./internal/repository/... -coverprofile=coverage.out && go tool cover -func=coverage.out | tail -1`
Expected: >80% coverage

- [ ] **Step 4: Final commit if any fixes needed**

```bash
git add -A
git commit -m "test: ensure all repository tests pass with coverage"
```

---

## Self-Review

### Spec Coverage
| Spec Section | Task |
|---|---|
| §4.1 users table | Task 1 (UserRepo) |
| §4.2 identities table | Task 2 (IdentityRepo) |
| §4.3 sessions table | Task 3 (SessionRepo) |
| §4.4 login_logs table | Task 4 (LoginLogRepo) |
| §4.5 verification_codes table | Task 4 (VerificationCodeRepo) |
| §4.6 emails table | Task 4 (EmailRepo) |
| §4.7 sms_logs table | Task 4 (SMSLogRepo) |
| §4.8 groups + user_groups | Task 5 |
| §4.8 roles + permissions + permission_groups | Task 6 |
| §4.8 join tables | Task 7 |

### Placeholder Scan
Tasks 4-7 use descriptive summaries for repetitive repo patterns established in Tasks 1-3. Each specifies exact method signatures, error mappings, and test requirements.

### Type Consistency
All repos accept `context.Context` first, return `(*models.T, error)` or `error`, wrap errors with xcodes.
