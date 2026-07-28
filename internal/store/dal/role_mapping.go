package dal

import (
	"context"
	"errors"

	"github.com/servekit/user-service/internal/store/generated"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"gorm.io/gorm"
)

// AssignUserRoleMapping adds a role to a user.
func AssignUserRoleMapping(ctx context.Context, tx *gorm.DB, ur *models.UserRoleMapping) error {
	if err := gorm.G[models.UserRoleMapping](tx).Create(ctx, ur); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// RemoveUserRoleMapping removes a role from a user.
func RemoveUserRoleMapping(ctx context.Context, tx *gorm.DB, userID, roleID int64) error {
	_, err := gorm.G[models.UserRoleMapping](tx).
		Where(generated.UserRoleMapping.UserID.Eq(userID)).
		Where(generated.UserRoleMapping.RoleID.Eq(roleID)).
		Delete(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// ListUserRoleMappingsByUserID returns all role assignments for a user.
func ListUserRoleMappingsByUserID(ctx context.Context, tx *gorm.DB, userID int64) ([]*models.UserRoleMapping, error) {
	results, err := gorm.G[models.UserRoleMapping](tx).
		Where(generated.UserRoleMapping.UserID.Eq(userID)).
		Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	urs := make([]*models.UserRoleMapping, len(results))
	for i := range results {
		urs[i] = &results[i]
	}
	return urs, nil
}

// ListUserRoleMappingsByRoleID returns all user assignments for a role.
func ListUserRoleMappingsByRoleID(ctx context.Context, tx *gorm.DB, roleID int64) ([]*models.UserRoleMapping, error) {
	results, err := gorm.G[models.UserRoleMapping](tx).
		Where(generated.UserRoleMapping.RoleID.Eq(roleID)).
		Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	urs := make([]*models.UserRoleMapping, len(results))
	for i := range results {
		urs[i] = &results[i]
	}
	return urs, nil
}

// GetUserRoleMapping returns a specific user-role assignment.
func GetUserRoleMapping(ctx context.Context, tx *gorm.DB, userID, roleID int64) (*models.UserRoleMapping, error) {
	ur, err := gorm.G[models.UserRoleMapping](tx).
		Where(generated.UserRoleMapping.UserID.Eq(userID)).
		Where(generated.UserRoleMapping.RoleID.Eq(roleID)).
		First(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &ur, nil
}
