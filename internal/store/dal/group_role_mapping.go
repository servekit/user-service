package dal

import (
	"context"

	"github.com/servekit/user-service/internal/store/generated"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"gorm.io/gorm"
)

// AssignGroupRoleMapping adds a role to a group.
func AssignGroupRoleMapping(ctx context.Context, tx *gorm.DB, groupID, roleID int64) error {
	gr := &models.UserGroupRoleMapping{GroupID: groupID, RoleID: roleID}
	if err := gorm.G[models.UserGroupRoleMapping](tx).Create(ctx, gr); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// RemoveGroupRoleMapping removes a role from a group.
func RemoveGroupRoleMapping(ctx context.Context, tx *gorm.DB, groupID, roleID int64) error {
	_, err := gorm.G[models.UserGroupRoleMapping](tx).
		Where(generated.UserGroupRoleMapping.GroupID.Eq(groupID)).
		Where(generated.UserGroupRoleMapping.RoleID.Eq(roleID)).
		Delete(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// ListGroupRoleMappingsByGroupID returns all role assignments for a group.
func ListGroupRoleMappingsByGroupID(ctx context.Context, tx *gorm.DB, groupID int64) ([]*models.UserGroupRoleMapping, error) {
	results, err := gorm.G[models.UserGroupRoleMapping](tx).
		Where(generated.UserGroupRoleMapping.GroupID.Eq(groupID)).
		Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	grs := make([]*models.UserGroupRoleMapping, len(results))
	for i := range results {
		grs[i] = &results[i]
	}
	return grs, nil
}

// ListGroupRoleMappingsByRoleID returns all group assignments for a role.
func ListGroupRoleMappingsByRoleID(ctx context.Context, tx *gorm.DB, roleID int64) ([]*models.UserGroupRoleMapping, error) {
	results, err := gorm.G[models.UserGroupRoleMapping](tx).
		Where(generated.UserGroupRoleMapping.RoleID.Eq(roleID)).
		Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	grs := make([]*models.UserGroupRoleMapping, len(results))
	for i := range results {
		grs[i] = &results[i]
	}
	return grs, nil
}
