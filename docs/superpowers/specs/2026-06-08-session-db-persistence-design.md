# Session DB Persistence Design

## Goal

Wire up the existing `SessionRepository` (Postgres) into the service layer so that session state changes are persisted to both Redis and DB atomically.

## Principles

- **DB transaction wraps the entire operation**: DB write → Redis write → commit. If Redis fails, DB rolls back automatically. No compensation logic needed.
- **Get stays Redis-only**: no DB overhead on the hot path.
- **Only state-change operations touch DB**: Create, Revoke, RevokeAll.

## Changes

### 1. `repository.SessionRepository` — expose `DB()`

Add a `DB() *gorm.DB` method so the service layer can control transactions.

### 2. `AuthHandler` — 3 Create paths

Wrap DB INSERT + Redis Create in a DB transaction for:
- `Register` (line ~100)
- `Login` (line ~175)
- `autoRegister` (line ~214)

Flow:
```
tx = sessionRepo.DB().Begin()
defer tx.Rollback()
txRepo = sessionRepo.WithTx(tx)
txRepo.Create(ctx, dbSession)   // DB INSERT
sessionMgr.Create(ctx, id, data) // Redis write
tx.Commit()
```

### 3. `AuthHandler.Logout`

Wrap DB UPDATE + Redis Revoke in a transaction:
```
txRepo.Update RevokedAt → Redis Revoke → commit
```

### 4. `SessionHandler` — add `sessionRepo` dependency

- Add `sessionRepo *repository.SessionRepository` field
- Update `NewSessionHandler` to accept it
- `RevokeSession`: DB UPDATE revoked_at + Redis Revoke in transaction
- `RevokeAllSessions`: DB batch UPDATE revoked_at + Redis RevokeAll in transaction

### 5. `session.Data` → `models.Session` mapping

Map fields when creating DB records:
```
session.Data.UserID      → models.Session.UserID
session.Data.LoginIP     → models.Session.IP
session.Data.LoginMethod → (not in model, skip)
session.Data.UserAgent   → models.Session.UserAgent
session.Data.OS          → models.Session.OS
session.Data.Browser     → models.Session.Browser
session.Data.Device      → models.Session.DeviceType
```

### 6. Update `UserService.newWithDeps`

Pass `sessionRepo` to `NewSessionHandler`.

## What does NOT change

- `session.Manager` — no changes to Redis-only session manager
- `session.Data` struct — stays as-is
- `SessionHandler.ListSessions` — stays Redis-only
- `models.Session` — no schema changes
- Migration — no new columns
