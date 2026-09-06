package models

import (
	"time"
)

// UserPermissionGroupItemMapping is the join table for permission_group -> permission.
type UserPermissionGroupItemMapping struct {
	ID                int64 `gorm:"primaryKey;autoIncrement"`
	PermissionGroupID int64 `gorm:"not null;uniqueIndex:uq_pgi"`
	PermissionID      int64 `gorm:"not null;uniqueIndex:uq_pgi;index:idx_permission_group_item_mappings_permission_id"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
