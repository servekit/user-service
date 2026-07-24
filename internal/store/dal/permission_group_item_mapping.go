package dal

import (
	"context"
	"fmt"

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
func RemovePermissionFromGroup(ctx context.Context, tx *gorm.DB, groupID, permissionID int64) error {
	_, err := gorm.G[models.PermissionGroupItemMapping](tx).
		Where(generated.PermissionGroupItemMapping.PermissionGroupID.Eq(groupID)).
		Where(generated.PermissionGroupItemMapping.PermissionID.Eq(permissionID)).
		Delete(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// ListPermissionsByGroupID returns all permissions in a permission group.
func ListPermissionsByGroupID(ctx context.Context, tx *gorm.DB, groupID int64) ([]*models.UserPermission, error) {
	permTable := resolveTableName(tx, &models.UserPermission{})
	pgiTable := resolveTableName(tx, &models.PermissionGroupItemMapping{})
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
