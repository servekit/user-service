package models

import (
	"time"
)

// UserRolePermissionGroupMapping is the join table for role -> permission_group.
type UserRolePermissionGroupMapping struct {
	ID                int64 `gorm:"primaryKey;autoIncrement"`
	RoleID            int64 `gorm:"not null;uniqueIndex:uq_role_perm_groups"`
	PermissionGroupID int64 `gorm:"not null;uniqueIndex:uq_role_perm_groups;index:idx_role_permission_group_mappings_permission_group_id"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
