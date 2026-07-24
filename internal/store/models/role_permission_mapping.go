package models

import (
	"time"

	"gorm.io/gorm"
)

// RolePermissionMapping is the join table for role -> permission (direct binding).
type RolePermissionMapping struct {
	ID           int64 `gorm:"primaryKey;autoIncrement"`
	RoleID       int64 `gorm:"not null;uniqueIndex:uq_role_permissions"`
	PermissionID int64 `gorm:"not null;uniqueIndex:uq_role_permissions"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    gorm.DeletedAt `gorm:"index"`
}
