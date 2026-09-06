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

// CreateUserPermission inserts a new permission record.
func CreateUserPermission(ctx context.Context, tx *gorm.DB, perm *models.UserPermission) error {
	if err := gorm.G[models.UserPermission](tx).Create(ctx, perm); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// GetUserPermissionByID returns a permission by ID.
func GetUserPermissionByID(ctx context.Context, tx *gorm.DB, id int64) (*models.UserPermission, error) {
	perm, err := gorm.G[models.UserPermission](tx).
		Where(generated.UserPermission.ID.Eq(id)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrPermissionNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &perm, nil
}

// GetUserPermissionByResourceAction returns a permission by resource and action.
func GetUserPermissionByResourceAction(ctx context.Context, tx *gorm.DB, resource, action string) (*models.UserPermission, error) {
	perm, err := gorm.G[models.UserPermission](tx).
		Where(generated.UserPermission.Resource.Eq(resource)).
		Where(generated.UserPermission.Action.Eq(action)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrPermissionNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &perm, nil
}

// ListUserPermissionsByPermissionGroupID returns all permissions in a permission group.
// UserPermissionGroupItemMapping is hard-deleted (no deleted_at column), so the JOIN
// needs no soft-delete filter; the FROM table (UserPermission) is still auto-filtered
// by GORM for its own soft-delete.
func ListUserPermissionsByPermissionGroupID(ctx context.Context, tx *gorm.DB, groupID int64) ([]*models.UserPermission, error) {
	permTable := resolveTableName(tx, &models.UserPermission{})
	pgiTable := resolveTableName(tx, &models.UserPermissionGroupItemMapping{})
	results, err := gorm.G[models.UserPermission](tx).
		Raw(fmt.Sprintf(`SELECT %s.* FROM %s JOIN %s ON %s.permission_id = %s.id WHERE %s.permission_group_id = ?`,
			permTable, permTable,
			pgiTable, pgiTable, permTable,
			pgiTable),
			groupID).
		Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	perms := make([]*models.UserPermission, len(results))
	for i := range results {
		perms[i] = &results[i]
	}
	return perms, nil
}

// UpdateUserPermission saves all fields of a permission (including zero values).
func UpdateUserPermission(ctx context.Context, tx *gorm.DB, perm *models.UserPermission) error {
	result := tx.WithContext(ctx).Save(perm)
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	if result.RowsAffected == 0 {
		return xcodes.ErrPermissionNotFound.New()
	}
	return nil
}

// DeleteUserPermission removes a permission by ID if it is not built-in.
func DeleteUserPermission(ctx context.Context, tx *gorm.DB, id int64) error {
	perm, err := gorm.G[models.UserPermission](tx).
		Where(generated.UserPermission.ID.Eq(id)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return xcodes.ErrPermissionNotFound.New()
		}
		return xcodes.ErrInternal.Wrap(err)
	}
	if perm.IsBuiltin {
		return xcodes.ErrPermissionIsBuiltin.New()
	}
	_, err = gorm.G[models.UserPermission](tx).
		Where(generated.UserPermission.ID.Eq(id)).
		Delete(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// ListUserPermissions returns a cursor-paginated list of permissions ordered by ID.
func ListUserPermissions(ctx context.Context, tx *gorm.DB, cursor string, pageSize int32) ([]*models.UserPermission, string, error) {
	q := gorm.G[models.UserPermission](tx).Order(generated.UserPermission.ID)
	if cursor != "" {
		cursorID, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, "", xcodes.ErrBadRequest.Wrapf(err, "invalid cursor: %s", cursor)
		}
		q = q.Where(generated.UserPermission.ID.Gt(cursorID))
	}
	results, err := q.Limit(int(pageSize) + 1).Find(ctx)
	if err != nil {
		return nil, "", xcodes.ErrInternal.Wrap(err)
	}
	perms := make([]*models.UserPermission, len(results))
	for i := range results {
		perms[i] = &results[i]
	}
	var nextCursor string
	if len(perms) > int(pageSize) {
		nextCursor = fmt.Sprintf("%d", perms[pageSize].ID)
		perms = perms[:pageSize]
	}
	return perms, nextCursor, nil
}
