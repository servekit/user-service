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

// CreateUserPermissionGroup inserts a new permission group record.
func CreateUserPermissionGroup(ctx context.Context, tx *gorm.DB, pg *models.UserPermissionGroup) error {
	if err := gorm.G[models.UserPermissionGroup](tx).Create(ctx, pg); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// GetUserPermissionGroupByID returns a permission group by ID.
func GetUserPermissionGroupByID(ctx context.Context, tx *gorm.DB, id int64) (*models.UserPermissionGroup, error) {
	pg, err := gorm.G[models.UserPermissionGroup](tx).
		Where(generated.UserPermissionGroup.ID.Eq(id)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrPermissionGroupNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &pg, nil
}

// GetUserPermissionGroupByName returns a permission group by name.
func GetUserPermissionGroupByName(ctx context.Context, tx *gorm.DB, name string) (*models.UserPermissionGroup, error) {
	pg, err := gorm.G[models.UserPermissionGroup](tx).
		Where(generated.UserPermissionGroup.Name.Eq(name)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrPermissionGroupNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &pg, nil
}

// UpdateUserPermissionGroup saves all fields of a permission group.
func UpdateUserPermissionGroup(ctx context.Context, tx *gorm.DB, pg *models.UserPermissionGroup) error {
	result := tx.WithContext(ctx).Save(pg)
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	if result.RowsAffected == 0 {
		return xcodes.ErrPermissionGroupNotFound.New()
	}
	return nil
}

// DeleteUserPermissionGroup removes a permission group by ID if it is not built-in.
func DeleteUserPermissionGroup(ctx context.Context, tx *gorm.DB, id int64) error {
	pg, err := gorm.G[models.UserPermissionGroup](tx).
		Where(generated.UserPermissionGroup.ID.Eq(id)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return xcodes.ErrPermissionGroupNotFound.New()
		}
		return xcodes.ErrInternal.Wrap(err)
	}
	if pg.IsBuiltin {
		return xcodes.ErrPermissionGroupIsBuiltin.New()
	}
	_, err = gorm.G[models.UserPermissionGroup](tx).
		Where(generated.UserPermissionGroup.ID.Eq(id)).
		Delete(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// ListUserPermissionGroups returns a cursor-paginated list of permission groups ordered by ID.
func ListUserPermissionGroups(ctx context.Context, tx *gorm.DB, cursor string, pageSize int32) ([]*models.UserPermissionGroup, string, error) {
	q := gorm.G[models.UserPermissionGroup](tx).Order(generated.UserPermissionGroup.ID)
	if cursor != "" {
		cursorID, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, "", xcodes.ErrBadRequest.Wrapf(err, "invalid cursor: %s", cursor)
		}
		q = q.Where(generated.UserPermissionGroup.ID.Gt(cursorID))
	}
	results, err := q.Limit(int(pageSize) + 1).Find(ctx)
	if err != nil {
		return nil, "", xcodes.ErrInternal.Wrap(err)
	}
	pgs := make([]*models.UserPermissionGroup, len(results))
	for i := range results {
		pgs[i] = &results[i]
	}
	var nextCursor string
	if len(pgs) > int(pageSize) {
		nextCursor = fmt.Sprintf("%d", pgs[pageSize].ID)
		pgs = pgs[:pageSize]
	}
	return pgs, nextCursor, nil
}
