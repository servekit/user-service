package dal

import (
	"context"
	"errors"
	"time"

	"github.com/servekit/user-service/internal/store/generated"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"gorm.io/gorm"
)

// CreateSession inserts a new session record.
func CreateSession(ctx context.Context, tx *gorm.DB, session *models.UserSession) error {
	if err := gorm.G[models.UserSession](tx).Create(ctx, session); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// GetSessionByID returns a session by ID.
func GetSessionByID(ctx context.Context, tx *gorm.DB, id string) (*models.UserSession, error) {
	session, err := gorm.G[models.UserSession](tx).
		Where(generated.UserSession.ID.Eq(id)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrSessionInvalid.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &session, nil
}

// ListSessionsByUserID returns all sessions for a user.
// ListSessionsByUserID returns the user's PG session rows newest first,
// bounded by limit (0 = uncapped). beforeCreated (when non-zero) pages the
// history strictly below that timestamp (cursor semantics). Includes live,
// revoked, and lapsed rows — callers classify; limit should cover the live
// set plus the history window.
func ListSessionsByUserID(ctx context.Context, tx *gorm.DB, userID int64, limit int, beforeCreated time.Time, revokedOnly *bool) ([]*models.UserSession, error) {
	q := gorm.G[models.UserSession](tx).
		Where(generated.UserSession.UserID.Eq(userID)).
		Order(generated.UserSession.CreatedAt.Desc())
	if revokedOnly != nil {
		if *revokedOnly {
			q = q.Where(generated.UserSession.RevokedAt.IsNotNull())
		} else {
			q = q.Where(generated.UserSession.RevokedAt.IsNull())
		}
	}
	if !beforeCreated.IsZero() {
		q = q.Where(generated.UserSession.CreatedAt.Lt(beforeCreated))
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	results, err := q.Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	sessions := make([]*models.UserSession, len(results))
	for i := range results {
		sessions[i] = &results[i]
	}
	return sessions, nil
}

// RevokeSession marks a session as revoked.
func RevokeSession(ctx context.Context, tx *gorm.DB, id string) error {
	rowsAffected, err := gorm.G[models.UserSession](tx).
		Where(generated.UserSession.ID.Eq(id)).
		Where(generated.UserSession.RevokedAt.IsNull()).
		Set(generated.UserSession.RevokedAt.Now()).
		Update(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	if rowsAffected == 0 {
		return xcodes.ErrSessionInvalid.New()
	}
	return nil
}

// RevokeSessionsByIDs stamps revoked_at on exactly the named sessions. The
// caller passes the session IDs Redis still reports as live, so tombstones of
// sessions that already lapsed (RevokedAt NULL, Redis evicted) keep reading
// EXPIRED in the history view — a user-scoped UPDATE over RevokedAt IS NULL
// used to rewrite them as explicit logouts. Idempotent: rows already revoked
// or absent are untouched. The timestamp is bound app-side (one value for the
// whole statement) rather than dialect NOW() so the query stays portable to
// the sqlite test harness.
func RevokeSessionsByIDs(ctx context.Context, tx *gorm.DB, sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	_, err := gorm.G[models.UserSession](tx).
		Where(generated.UserSession.ID.In(sessionIDs...)).
		Where(generated.UserSession.RevokedAt.IsNull()).
		Set(generated.UserSession.RevokedAt.Set(time.Now())).
		Update(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}
