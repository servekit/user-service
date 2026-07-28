package dal

import (
	"context"
	"errors"

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
			return nil, xcodes.ErrNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &pg, nil
}

// ListAllUserPermissionGroups returns all permission groups.
func ListAllUserPermissionGroups(ctx context.Context, tx *gorm.DB) ([]*models.UserPermissionGroup, error) {
	results, err := gorm.G[models.UserPermissionGroup](tx).Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	pgs := make([]*models.UserPermissionGroup, len(results))
	for i := range results {
		pgs[i] = &results[i]
	}
	return pgs, nil
}
