package dal

import (
	"context"

	"github.com/servekit/user-service/internal/store/generated"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"gorm.io/gorm"
)

// AssignRolePermissionMapping adds a permission to a role.
func AssignRolePermissionMapping(ctx context.Context, tx *gorm.DB, roleID, permissionID int64) error {
	rp := &models.RolePermissionMapping{RoleID: roleID, PermissionID: permissionID}
	if err := gorm.G[models.RolePermissionMapping](tx).Create(ctx, rp); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// RemoveRolePermissionMapping removes a permission from a role. The model has
// no DeletedAt (hard-delete by default); it's a relationship join row with no
// audit value, and a stale row would collide with the (role_id, permission_id)
// unique index when UpdateRole re-adds a retained permission.
func RemoveRolePermissionMapping(ctx context.Context, tx *gorm.DB, roleID, permissionID int64) error {
	result := tx.WithContext(ctx).
		Where(generated.RolePermissionMapping.RoleID.Eq(roleID)).
		Where(generated.RolePermissionMapping.PermissionID.Eq(permissionID)).
		Delete(&models.RolePermissionMapping{})
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	return nil
}

// ListRolePermissionMappingsByRoleID returns all permission assignments for a role.
func ListRolePermissionMappingsByRoleID(ctx context.Context, tx *gorm.DB, roleID int64) ([]*models.RolePermissionMapping, error) {
	results, err := gorm.G[models.RolePermissionMapping](tx).
		Where(generated.RolePermissionMapping.RoleID.Eq(roleID)).
		Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	rps := make([]*models.RolePermissionMapping, len(results))
	for i := range results {
		rps[i] = &results[i]
	}
	return rps, nil
}

// ListRolePermissionMappingsByPermissionID returns role assignments holding a permission.
func ListRolePermissionMappingsByPermissionID(ctx context.Context, tx *gorm.DB, permissionID int64) ([]*models.RolePermissionMapping, error) {
	results, err := gorm.G[models.RolePermissionMapping](tx).
		Where(generated.RolePermissionMapping.PermissionID.Eq(permissionID)).
		Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	rps := make([]*models.RolePermissionMapping, len(results))
	for i := range results {
		rps[i] = &results[i]
	}
	return rps, nil
}

// DeleteRolePermissionMappingsByPermissionID removes every role assignment of a
// permission (cascade cleanup when the permission is deleted). Hard-deletes.
func DeleteRolePermissionMappingsByPermissionID(ctx context.Context, tx *gorm.DB, permissionID int64) error {
	result := tx.WithContext(ctx).
		Where(generated.RolePermissionMapping.PermissionID.Eq(permissionID)).
		Delete(&models.RolePermissionMapping{})
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	return nil
}
