package models

import (
	"time"

	"gorm.io/gorm"
)

// UserLoginLog records login attempts (append-only audit table).
type UserLoginLog struct {
	ID         int64  `gorm:"primaryKey;autoIncrement"`
	UserID     *int64 `gorm:"index"`
	IdentityID *int64
	Provider   int32  `gorm:"not null"` // pb.IdentityProvider (1=email, 2=phone, 3=github, ...)
	Action     int32  `gorm:"not null"` // pb.LoginAction (1=login, 2=register, 3=social_login, 4=social_register, 5=bind, 6=unbind)
	Method     int32  // pb.LoginMethod (1=email+password, 2=phone+password, 3=phone code, 4=email code, 5=username+password); 0 = social / legacy rows
	Success    bool   `gorm:"not null"`
	FailReason string `gorm:"size:128"`
	IP         string `gorm:"size:45"`
	UserAgent  string `gorm:"size:512"`
	DeviceType int32  // pb.DeviceType (1=web, 2=ios, 3=android, 4=api)
	OS         string `gorm:"size:32"`
	Browser    string `gorm:"size:32"`
	Country    string `gorm:"size:4"`
	City       string `gorm:"size:64"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  gorm.DeletedAt `gorm:"index"`
}
