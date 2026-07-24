package models

import (
	"time"

	"gorm.io/gorm"
)

// RolePermissionGroupMapping is the join table for role -> permission_group.
type RolePermissionGroupMapping struct {
	ID                int64 `gorm:"primaryKey;autoIncrement"`
	RoleID            int64 `gorm:"not null;uniqueIndex:uq_role_perm_groups"`
	PermissionGroupID int64 `gorm:"not null;uniqueIndex:uq_role_perm_groups"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         gorm.DeletedAt `gorm:"index"`
}
