package dal

import (
	"context"

	"github.com/servekit/user-service/internal/store/generated"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"gorm.io/gorm"
)

// AssignRolePermissionGroupMapping adds a permission group to a role.
func AssignRolePermissionGroupMapping(ctx context.Context, tx *gorm.DB, roleID, permissionGroupID int64) error {
	rpg := &models.RolePermissionGroupMapping{RoleID: roleID, PermissionGroupID: permissionGroupID}
	if err := gorm.G[models.RolePermissionGroupMapping](tx).Create(ctx, rpg); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// RemoveRolePermissionGroupMapping removes a permission group from a role. The
// model has no DeletedAt (hard-delete by default); it's a relationship join row
// with no audit value, and a stale row would collide with the
// (role_id, permission_group_id) unique index when UpdateRole re-adds a retained
// permission group.
func RemoveRolePermissionGroupMapping(ctx context.Context, tx *gorm.DB, roleID, permissionGroupID int64) error {
	result := tx.WithContext(ctx).
		Where(generated.RolePermissionGroupMapping.RoleID.Eq(roleID)).
		Where(generated.RolePermissionGroupMapping.PermissionGroupID.Eq(permissionGroupID)).
		Delete(&models.RolePermissionGroupMapping{})
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	return nil
}

// ListRolePermissionGroupMappingsByRoleID returns all permission group assignments for a role.
func ListRolePermissionGroupMappingsByRoleID(ctx context.Context, tx *gorm.DB, roleID int64) ([]*models.RolePermissionGroupMapping, error) {
	results, err := gorm.G[models.RolePermissionGroupMapping](tx).
		Where(generated.RolePermissionGroupMapping.RoleID.Eq(roleID)).
		Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	rpgs := make([]*models.RolePermissionGroupMapping, len(results))
	for i := range results {
		rpgs[i] = &results[i]
	}
	return rpgs, nil
}

// ListRolePermissionGroupMappingsByPermissionGroupID returns roles referencing a permission group.
func ListRolePermissionGroupMappingsByPermissionGroupID(ctx context.Context, tx *gorm.DB, permissionGroupID int64) ([]*models.RolePermissionGroupMapping, error) {
	results, err := gorm.G[models.RolePermissionGroupMapping](tx).
		Where(generated.RolePermissionGroupMapping.PermissionGroupID.Eq(permissionGroupID)).
		Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	rpgs := make([]*models.RolePermissionGroupMapping, len(results))
	for i := range results {
		rpgs[i] = &results[i]
	}
	return rpgs, nil
}

// DeleteRolePermissionGroupMappingsByGroupID removes every role reference to a
// permission group (cascade cleanup when the group is deleted). Hard-deletes.
func DeleteRolePermissionGroupMappingsByGroupID(ctx context.Context, tx *gorm.DB, permissionGroupID int64) error {
	result := tx.WithContext(ctx).
		Where(generated.RolePermissionGroupMapping.PermissionGroupID.Eq(permissionGroupID)).
		Delete(&models.RolePermissionGroupMapping{})
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	return nil
}
