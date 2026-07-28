package models

import (
	"time"

	"gorm.io/gorm"
)

// UserRole represents a named set of permissions.
type UserRole struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	Name        string `gorm:"size:64;not null;uniqueIndex"`
	Description string `gorm:"size:256"`
	IsBuiltin   bool   `gorm:"not null;default:false"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}
