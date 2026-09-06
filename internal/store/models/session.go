package models

import (
	"time"

	"gorm.io/gorm"
)

// UserSession represents a user login session. Redis is primary storage; PG is for persistence.
type UserSession struct {
	ID          string    `gorm:"size:64;primaryKey"`
	UserID      int64     `gorm:"not null;index"`
	LoginMethod string    `gorm:"size:64"`  // LOGIN_METHOD_* or IDENTITY_PROVIDER_*
	LoginTarget string    `gorm:"size:256"` // credential subject (username/email/phone/oauth uid)
	Device      string    `gorm:"size:128"` // hardware name when known (client hint > UA)
	IP          string    `gorm:"size:45"`
	UserAgent   string    `gorm:"size:512"`
	DeviceType  int32     // pb.DeviceType (1=web, 2=ios, 3=android, 4=api)
	OS          string    `gorm:"size:32"`
	Browser     string    `gorm:"size:32"`
	Country     string    `gorm:"size:4"`
	City        string    `gorm:"size:64"`
	ExpiresAt   time.Time `gorm:"not null;index:idx_user_sessions_expires"`
	RevokedAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}
