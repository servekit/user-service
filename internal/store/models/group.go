package models

import (
	"time"

	"gorm.io/gorm"
)

// UserGroup represents an organizational group with hierarchical structure and snowflake ID.
type UserGroup struct {
	ID          int64  `gorm:"primaryKey"`
	Name        string `gorm:"size:64;not null;uniqueIndex"`
	Description string `gorm:"size:256"`
	ParentID    *int64 `gorm:"index"`
	Status      string `gorm:"size:16;not null;default:active"` // active / disabled
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}
