package models

import (
	"time"

	"gorm.io/gorm"
)

// UserGroupMapping is the join table for user-group membership.
type UserGroupMapping struct {
	ID        int64  `gorm:"primaryKey;autoIncrement"`
	UserID    int64  `gorm:"not null;uniqueIndex:uq_user_groups"`
	GroupID   int64  `gorm:"not null;uniqueIndex:uq_user_groups"`
	Role      string `gorm:"size:32;default:member"` // owner / admin / member
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}
