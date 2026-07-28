package dal

import (
	"context"
	"errors"
	"fmt"

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
			return nil, xcodes.ErrNotFound.New()
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
			return nil, xcodes.ErrNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &perm, nil
}

// ListAllUserPermissions returns all permissions.
func ListAllUserPermissions(ctx context.Context, tx *gorm.DB) ([]*models.UserPermission, error) {
	results, err := gorm.G[models.UserPermission](tx).Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	perms := make([]*models.UserPermission, len(results))
	for i := range results {
		perms[i] = &results[i]
	}
	return perms, nil
}

// ListUserPermissionsByPermissionGroupID returns all permissions in a permission group.
func ListUserPermissionsByPermissionGroupID(ctx context.Context, tx *gorm.DB, groupID int64) ([]*models.UserPermission, error) {
	permTable := resolveTableName(tx, &models.UserPermission{})
	pgiTable := resolveTableName(tx, &models.PermissionGroupItemMapping{})
	results, err := gorm.G[models.UserPermission](tx).
		Raw(fmt.Sprintf(`SELECT %s.* FROM %s
			JOIN %s ON %s.permission_id = %s.id
			WHERE %s.permission_group_id = ?`,
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
