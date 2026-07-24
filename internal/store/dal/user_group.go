package dal

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/servekit/user-service/internal/store/generated"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"gorm.io/gorm"
)

// CreateUserGroup inserts a new group record.
func CreateUserGroup(ctx context.Context, tx *gorm.DB, group *models.UserGroup) error {
	if err := gorm.G[models.UserGroup](tx).Create(ctx, group); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// GetUserGroupByID returns a group by ID.
func GetUserGroupByID(ctx context.Context, tx *gorm.DB, id int64) (*models.UserGroup, error) {
	group, err := gorm.G[models.UserGroup](tx).
		Where(generated.UserGroup.ID.Eq(id)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrGroupNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &group, nil
}

// GetUserGroupByName returns a group by name.
func GetUserGroupByName(ctx context.Context, tx *gorm.DB, name string) (*models.UserGroup, error) {
	group, err := gorm.G[models.UserGroup](tx).
		Where(generated.UserGroup.Name.Eq(name)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrGroupNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &group, nil
}

// UpdateUserGroup saves changes to a group record (all fields, including zero values).
func UpdateUserGroup(ctx context.Context, tx *gorm.DB, group *models.UserGroup) error {
	result := tx.WithContext(ctx).Save(group)
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	if result.RowsAffected == 0 {
		return xcodes.ErrGroupNotFound.New()
	}
	return nil
}

// DeleteUserGroup removes a group by ID.
func DeleteUserGroup(ctx context.Context, tx *gorm.DB, id int64) error {
	rowsAffected, err := gorm.G[models.UserGroup](tx).
		Where(generated.UserGroup.ID.Eq(id)).
		Delete(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	if rowsAffected == 0 {
		return xcodes.ErrGroupNotFound.New()
	}
	return nil
}

// ListUserGroups returns a paginated list of groups ordered by ID.
func ListUserGroups(ctx context.Context, tx *gorm.DB, status, cursor string, pageSize int32) ([]*models.UserGroup, string, error) {
	q := gorm.G[models.UserGroup](tx).Order(generated.UserGroup.ID)

	if status != "" {
		q = q.Where(generated.UserGroup.Status.Eq(status))
	}
	if cursor != "" {
		cursorID, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, "", xcodes.ErrBadRequest.Wrapf(err, "invalid cursor: %s", cursor)
		}
		q = q.Where(generated.UserGroup.ID.Gt(cursorID))
	}

	results, err := q.Limit(int(pageSize) + 1).Find(ctx)
	if err != nil {
		return nil, "", xcodes.ErrInternal.Wrap(err)
	}

	groups := make([]*models.UserGroup, len(results))
	for i := range results {
		groups[i] = &results[i]
	}

	var nextCursor string
	if len(groups) > int(pageSize) {
		nextCursor = fmt.Sprintf("%d", groups[pageSize].ID)
		groups = groups[:pageSize]
	}
	return groups, nextCursor, nil
}
