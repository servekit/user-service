package dal

import (
	"context"

	"github.com/servekit/user-service/internal/store/generated"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"gorm.io/gorm"
)

// AddPermissionToGroup adds a permission to a permission group.
func AddPermissionToGroup(ctx context.Context, tx *gorm.DB, groupID, permissionID int64) error {
	item := &models.PermissionGroupItemMapping{PermissionGroupID: groupID, PermissionID: permissionID}
	if err := gorm.G[models.PermissionGroupItemMapping](tx).Create(ctx, item); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// RemovePermissionFromGroup removes a permission from a permission group.
//
// The model has no DeletedAt (hard-delete by default): it's a relationship
// join row with no audit value, and a stale row would conflict with the unique
// index uq_pgi (PermissionGroupID, PermissionID) on re-add within the same
// transaction (e.g. UpdatePermissionGroup's full-replace when the new set
// overlaps the old).
func RemovePermissionFromGroup(ctx context.Context, tx *gorm.DB, groupID, permissionID int64) error {
	result := tx.WithContext(ctx).
		Where(generated.PermissionGroupItemMapping.PermissionGroupID.Eq(groupID)).
		Where(generated.PermissionGroupItemMapping.PermissionID.Eq(permissionID)).
		Delete(&models.PermissionGroupItemMapping{})
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	return nil
}

// ListPermissionGroupIDsByItemPermissionID returns the group IDs that contain a permission.
func ListPermissionGroupIDsByItemPermissionID(ctx context.Context, tx *gorm.DB, permissionID int64) ([]int64, error) {
	results, err := gorm.G[models.PermissionGroupItemMapping](tx).
		Where(generated.PermissionGroupItemMapping.PermissionID.Eq(permissionID)).
		Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	ids := make([]int64, len(results))
	for i := range results {
		ids[i] = results[i].PermissionGroupID
	}
	return ids, nil
}

// DeletePermissionGroupItemMappingsByPermissionID removes every group membership
// of a permission (cascade cleanup when the permission is deleted). Hard-deletes.
func DeletePermissionGroupItemMappingsByPermissionID(ctx context.Context, tx *gorm.DB, permissionID int64) error {
	result := tx.WithContext(ctx).
		Where(generated.PermissionGroupItemMapping.PermissionID.Eq(permissionID)).
		Delete(&models.PermissionGroupItemMapping{})
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	return nil
}

// DeletePermissionGroupItemMappingsByGroupID removes every permission in a group
// (cascade cleanup when the group is deleted). Hard-deletes.
func DeletePermissionGroupItemMappingsByGroupID(ctx context.Context, tx *gorm.DB, groupID int64) error {
	result := tx.WithContext(ctx).
		Where(generated.PermissionGroupItemMapping.PermissionGroupID.Eq(groupID)).
		Delete(&models.PermissionGroupItemMapping{})
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	return nil
}
