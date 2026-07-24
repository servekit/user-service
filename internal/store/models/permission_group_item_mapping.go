package models

import (
	"time"

	"gorm.io/gorm"
)

// PermissionGroupItemMapping is the join table for permission_group -> permission.
type PermissionGroupItemMapping struct {
	ID                int64 `gorm:"primaryKey;autoIncrement"`
	PermissionGroupID int64 `gorm:"not null;uniqueIndex:uq_pgi"`
	PermissionID      int64 `gorm:"not null;uniqueIndex:uq_pgi"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         gorm.DeletedAt `gorm:"index"`
}
