package models

import (
	"time"
)

// UserRolePermissionMapping is the join table for role -> permission (direct binding).
type UserRolePermissionMapping struct {
	ID           int64 `gorm:"primaryKey;autoIncrement"`
	RoleID       int64 `gorm:"not null;uniqueIndex:uq_role_permissions"`
	PermissionID int64 `gorm:"not null;uniqueIndex:uq_role_permissions;index:idx_role_permission_mappings_permission_id"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
