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

// CreateAuthLog inserts a new login log record.
func CreateAuthLog(ctx context.Context, tx *gorm.DB, log *models.UserAuthLog) error {
	if err := gorm.G[models.UserAuthLog](tx).Create(ctx, log); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// AuthLogFilter narrows a ListAuthLogs query; zero values mean no filter
// on that column (Success nil = both outcomes).
type AuthLogFilter struct {
	UserID   int64
	Provider int32
	Action   int32
	Method   int32
	Success  *bool
}

// ListAuthLogs returns a cursor-paginated list of login logs, newest first.
func ListAuthLogs(ctx context.Context, tx *gorm.DB, f AuthLogFilter, cursor string, pageSize int32) ([]*models.UserAuthLog, string, error) {
	q := gorm.G[models.UserAuthLog](tx).
		Order(generated.UserAuthLog.ID.Desc())

	if f.UserID != 0 {
		q = q.Where(generated.UserAuthLog.UserID.Eq(f.UserID))
	}
	if f.Provider != 0 {
		q = q.Where(generated.UserAuthLog.Provider.Eq(f.Provider))
	}
	if f.Action != 0 {
		q = q.Where(generated.UserAuthLog.Action.Eq(f.Action))
	}
	if f.Method != 0 {
		q = q.Where(generated.UserAuthLog.Method.Eq(f.Method))
	}
	if f.Success != nil {
		q = q.Where(generated.UserAuthLog.Success.Eq(*f.Success))
	}

	if cursor != "" {
		cursorID, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, "", xcodes.ErrBadRequest.Wrapf(err, "invalid cursor: %s", cursor)
		}
		q = q.Where(generated.UserAuthLog.ID.Lt(cursorID))
	}

	results, err := q.Limit(int(pageSize) + 1).Find(ctx)
	if err != nil {
		return nil, "", xcodes.ErrInternal.Wrap(err)
	}

	logs := make([]*models.UserAuthLog, len(results))
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
