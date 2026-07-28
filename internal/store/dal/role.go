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

// CreateUserRole inserts a new role record.
func CreateUserRole(ctx context.Context, tx *gorm.DB, role *models.UserRole) error {
	if err := gorm.G[models.UserRole](tx).Create(ctx, role); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// GetUserRoleByID returns a role by ID.
func GetUserRoleByID(ctx context.Context, tx *gorm.DB, id int64) (*models.UserRole, error) {
	role, err := gorm.G[models.UserRole](tx).
		Where(generated.UserRole.ID.Eq(id)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrRoleNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &role, nil
}

// GetUserRoleByName returns a role by name.
func GetUserRoleByName(ctx context.Context, tx *gorm.DB, name string) (*models.UserRole, error) {
	role, err := gorm.G[models.UserRole](tx).
		Where(generated.UserRole.Name.Eq(name)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrRoleNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &role, nil
}

// UpdateUserRole saves changes to a role record (all fields, including zero values).
func UpdateUserRole(ctx context.Context, tx *gorm.DB, role *models.UserRole) error {
	result := tx.WithContext(ctx).Save(role)
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	if result.RowsAffected == 0 {
		return xcodes.ErrRoleNotFound.New()
	}
	return nil
}

// DeleteUserRole removes a role by ID if it is not built-in.
func DeleteUserRole(ctx context.Context, tx *gorm.DB, id int64) error {
	role, err := gorm.G[models.UserRole](tx).
		Where(generated.UserRole.ID.Eq(id)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return xcodes.ErrRoleNotFound.New()
		}
		return xcodes.ErrInternal.Wrap(err)
	}
	if role.IsBuiltin {
		return xcodes.ErrRoleIsBuiltin.New()
	}
	_, err = gorm.G[models.UserRole](tx).
		Where(generated.UserRole.ID.Eq(id)).
		Delete(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// ListUserRoles returns a paginated list of roles ordered by ID.
func ListUserRoles(ctx context.Context, tx *gorm.DB, cursor string, pageSize int32) ([]*models.UserRole, string, error) {
	q := gorm.G[models.UserRole](tx).Order(generated.UserRole.ID)

	if cursor != "" {
		cursorID, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, "", xcodes.ErrBadRequest.Wrapf(err, "invalid cursor: %s", cursor)
		}
		q = q.Where(generated.UserRole.ID.Gt(cursorID))
	}

	results, err := q.Limit(int(pageSize) + 1).Find(ctx)
	if err != nil {
		return nil, "", xcodes.ErrInternal.Wrap(err)
	}

	roles := make([]*models.UserRole, len(results))
	for i := range results {
		roles[i] = &results[i]
	}

	var nextCursor string
	if len(roles) > int(pageSize) {
		nextCursor = fmt.Sprintf("%d", roles[pageSize].ID)
		roles = roles[:pageSize]
	}
	return roles, nextCursor, nil
}
