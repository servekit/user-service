package models

import (
	"time"

	"gorm.io/gorm"
)

// UserPermission represents a resource:action pair.
type UserPermission struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	Resource    string `gorm:"size:64;not null;uniqueIndex:uq_permissions_resource_action"`
	Action      string `gorm:"size:32;not null;uniqueIndex:uq_permissions_resource_action"`
	Description string `gorm:"size:256"`
	IsBuiltin   bool   `gorm:"not null;default:false"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}
