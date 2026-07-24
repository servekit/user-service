package dal

import (
	"context"
	"fmt"
	"strconv"

	"github.com/servekit/user-service/internal/store/generated"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"gorm.io/gorm"
)

// AddUserGroupMapping adds a user to a group.
func AddUserGroupMapping(ctx context.Context, tx *gorm.DB, ug *models.UserGroupMapping) error {
	if err := gorm.G[models.UserGroupMapping](tx).Create(ctx, ug); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// RemoveUserGroupMapping removes a user from a group.
func RemoveUserGroupMapping(ctx context.Context, tx *gorm.DB, userID, groupID int64) error {
	rowsAffected, err := gorm.G[models.UserGroupMapping](tx).
		Where(generated.UserGroupMapping.UserID.Eq(userID)).
		Where(generated.UserGroupMapping.GroupID.Eq(groupID)).
		Delete(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	if rowsAffected == 0 {
		return xcodes.ErrNotMember.New()
	}
	return nil
}

// ListUserGroupMappingsByGroupID returns members of a group, paginated by cursor.
func ListUserGroupMappingsByGroupID(ctx context.Context, tx *gorm.DB, groupID int64, cursor string, pageSize int32) ([]*models.UserGroupMapping, string, error) {
	q := gorm.G[models.UserGroupMapping](tx).
		Where(generated.UserGroupMapping.GroupID.Eq(groupID)).
		Order(generated.UserGroupMapping.ID)

	if cursor != "" {
		cursorID, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, "", xcodes.ErrBadRequest.Wrapf(err, "invalid cursor: %s", cursor)
		}
		q = q.Where(generated.UserGroupMapping.ID.Gt(cursorID))
	}

	results, err := q.Limit(int(pageSize) + 1).Find(ctx)
	if err != nil {
		return nil, "", xcodes.ErrInternal.Wrap(err)
	}

	ugs := make([]*models.UserGroupMapping, len(results))
	for i := range results {
		ugs[i] = &results[i]
	}

	var nextCursor string
	if len(ugs) > int(pageSize) {
		nextCursor = fmt.Sprintf("%d", ugs[pageSize].ID)
		ugs = ugs[:pageSize]
	}
	return ugs, nextCursor, nil
}

// ListUserGroupMappingsByUserID returns all group memberships for a user.
func ListUserGroupMappingsByUserID(ctx context.Context, tx *gorm.DB, userID int64) ([]*models.UserGroupMapping, error) {
	results, err := gorm.G[models.UserGroupMapping](tx).
		Where(generated.UserGroupMapping.UserID.Eq(userID)).
		Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	ugs := make([]*models.UserGroupMapping, len(results))
	for i := range results {
		ugs[i] = &results[i]
	}
	return ugs, nil
}

// UpdateUserGroupMappingRole changes the role of a user within a group.
func UpdateUserGroupMappingRole(ctx context.Context, tx *gorm.DB, userID, groupID int64, role string) error {
	rowsAffected, err := gorm.G[models.UserGroupMapping](tx).
		Where(generated.UserGroupMapping.UserID.Eq(userID)).
		Where(generated.UserGroupMapping.GroupID.Eq(groupID)).
		Set(generated.UserGroupMapping.Role.Set(role)).
		Update(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	if rowsAffected == 0 {
		return xcodes.ErrNotMember.New()
	}
	return nil
}
