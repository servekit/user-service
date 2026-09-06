package dal

import (
	"context"
	"fmt"
	"strconv"

	"github.com/servekit/user-service/internal/store/generated"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"gorm.io/gorm"
)

// CreateLoginLog inserts a new login log record.
func CreateLoginLog(ctx context.Context, tx *gorm.DB, log *models.UserLoginLog) error {
	if err := gorm.G[models.UserLoginLog](tx).Create(ctx, log); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// ListLoginLogs returns a cursor-paginated list of login logs, newest first.
// userID 0 and provider 0 mean "no filter on that column" (the audit view
// defaults to all users); success is not filterable here because a proto3
// bool cannot distinguish "failed" from "not filtering".
func ListLoginLogs(ctx context.Context, tx *gorm.DB, userID int64, provider int32, cursor string, pageSize int32) ([]*models.UserLoginLog, string, error) {
	q := gorm.G[models.UserLoginLog](tx).
		Order(generated.UserLoginLog.ID.Desc())

	if userID != 0 {
		q = q.Where(generated.UserLoginLog.UserID.Eq(userID))
	}
	if provider != 0 {
		q = q.Where(generated.UserLoginLog.Provider.Eq(provider))
	}

	if cursor != "" {
		cursorID, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, "", xcodes.ErrBadRequest.Wrapf(err, "invalid cursor: %s", cursor)
		}
		q = q.Where(generated.UserLoginLog.ID.Lt(cursorID))
	}

	results, err := q.Limit(int(pageSize) + 1).Find(ctx)
	if err != nil {
		return nil, "", xcodes.ErrInternal.Wrap(err)
	}

	logs := make([]*models.UserLoginLog, len(results))
	for i := range results {
		logs[i] = &results[i]
	}

	var nextCursor string
	if len(logs) > int(pageSize) {
		nextCursor = fmt.Sprintf("%d", logs[pageSize].ID)
		logs = logs[:pageSize]
	}
	return logs, nextCursor, nil
}
