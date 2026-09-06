package models

import (
	"time"

	"gorm.io/gorm"
)

// UserAuthLog records login attempts (append-only audit table). os/browser
// and the device class are NOT stored — they derive from the raw UserAgent
// at read time, so parser upgrades re-derive old rows for free.
type UserAuthLog struct {
	ID         int64  `gorm:"primaryKey;autoIncrement"`
	UserID     *int64 `gorm:"index"`
	IdentityID *int64
	Provider   int32 `gorm:"not null"` // pb.IdentityProvider (1=email, 2=phone, 3=github, ...)
	Action     int32 `gorm:"not null"` // pb.LoginAction (1=login, 2=register, 3=social_login, 4=social_register, 5=bind, 6=unbind)
	Method     int32 // pb.LoginMethod (1=email+password, 2=phone+password, 3=phone code, 4=email code, 5=username+password); 0 = social / legacy rows
	Success    bool  `gorm:"not null"`
	// pb.LoginFailReason int — the raw string column is gone.
	FailReason int32
	// The credential subject of the attempt (username/email/phone/oauth
	// uid); its kind derives from Method + Provider. Present even when no
	// user matched.
	Target    string `gorm:"size:256"`
	IP        string `gorm:"size:45"`
	UserAgent string `gorm:"size:512"`
	Country   string `gorm:"size:4"`
	City      string `gorm:"size:64"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}
