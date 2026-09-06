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
func ListSessionsByUserID(ctx context.Context, tx *gorm.DB, userID int64, limit int, beforeCreated time.Time) ([]*models.UserSession, error) {
	q := gorm.G[models.UserSession](tx).
		Where(generated.UserSession.UserID.Eq(userID)).
		Order(generated.UserSession.CreatedAt.Desc())
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

// RevokeAllUserSessions revokes all active sessions for a user.
func RevokeAllUserSessions(ctx context.Context, tx *gorm.DB, userID int64) error {
	_, err := gorm.G[models.UserSession](tx).
		Where(generated.UserSession.UserID.Eq(userID)).
		Where(generated.UserSession.RevokedAt.IsNull()).
		Set(generated.UserSession.RevokedAt.Now()).
		Update(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}
