package models

import (
	"time"

	"gorm.io/gorm"
)

// GroupRoleMapping is the join table for group -> role.
type GroupRoleMapping struct {
	ID        int64 `gorm:"primaryKey;autoIncrement"`
	GroupID   int64 `gorm:"not null;uniqueIndex:uq_group_roles"`
	RoleID    int64 `gorm:"not null;uniqueIndex:uq_group_roles"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}
