package models

import (
	"time"
)

// UserGroupRoleMapping is the join table for group -> role.
type UserGroupRoleMapping struct {
	ID        int64 `gorm:"primaryKey;autoIncrement"`
	GroupID   int64 `gorm:"not null;uniqueIndex:uq_group_roles"`
	RoleID    int64 `gorm:"not null;uniqueIndex:uq_group_roles;index:idx_group_role_mappings_role_id"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
