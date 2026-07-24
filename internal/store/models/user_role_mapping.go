package models

import (
	"time"

	"gorm.io/gorm"
)

// UserRoleMapping is the join table for direct user -> role assignment.
type UserRoleMapping struct {
	ID         int64 `gorm:"primaryKey;autoIncrement"`
	UserID     int64 `gorm:"not null;uniqueIndex:uq_user_roles"`
	RoleID     int64 `gorm:"not null;uniqueIndex:uq_user_roles"`
	AssignedBy *int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  gorm.DeletedAt `gorm:"index"`
}
