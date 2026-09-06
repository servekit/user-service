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

// LoginLogFilter narrows a ListLoginLogs query; zero values mean no filter
// on that column (Success nil = both outcomes).
type LoginLogFilter struct {
	UserID   int64
	Provider int32
	Action   int32
	Method   int32
	Success  *bool
}

// ListLoginLogs returns a cursor-paginated list of login logs, newest first.
func ListLoginLogs(ctx context.Context, tx *gorm.DB, f LoginLogFilter, cursor string, pageSize int32) ([]*models.UserLoginLog, string, error) {
	q := gorm.G[models.UserLoginLog](tx).
		Order(generated.UserLoginLog.ID.Desc())

	if f.UserID != 0 {
		q = q.Where(generated.UserLoginLog.UserID.Eq(f.UserID))
	}
	if f.Provider != 0 {
		q = q.Where(generated.UserLoginLog.Provider.Eq(f.Provider))
	}
	if f.Action != 0 {
		q = q.Where(generated.UserLoginLog.Action.Eq(f.Action))
	}
	if f.Method != 0 {
		q = q.Where(generated.UserLoginLog.Method.Eq(f.Method))
	}
	if f.Success != nil {
		q = q.Where(generated.UserLoginLog.Success.Eq(*f.Success))
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
