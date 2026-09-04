package models

import (
	"time"

	"gorm.io/gorm"
)

// UserSession represents a user login session. Redis is primary storage; PG is for persistence.
type UserSession struct {
	ID         string    `gorm:"size:64;primaryKey"`
	UserID     int64     `gorm:"not null;index"`
	IP         string    `gorm:"size:45"`
	UserAgent  string    `gorm:"size:512"`
	DeviceType int32     // pb.DeviceType (1=web, 2=ios, 3=android, 4=api)
	OS         string    `gorm:"size:32"`
	Browser    string    `gorm:"size:32"`
	Country    string    `gorm:"size:4"`
	City       string    `gorm:"size:64"`
	ExpiresAt  time.Time `gorm:"not null;index:idx_user_sessions_expires"`
	// LastActiveAt is initialized to the session creation time, then updated on
	// each activity. autoCreateTime sets it on insert (portable — no DB default
	// expression); explicit Updates change it thereafter.
	LastActiveAt time.Time `gorm:"not null;autoCreateTime"`
	RevokedAt    *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    gorm.DeletedAt `gorm:"index"`
}
