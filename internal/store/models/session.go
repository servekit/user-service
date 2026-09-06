package models

import (
	"time"

	"gorm.io/gorm"
)

// UserSession represents a user login session. Redis is primary storage; PG
// is for persistence. os/browser and the device class are NOT stored — they
// derive from the raw UserAgent at read time; device IS stored because the
// Sec-CH-UA-Model hint (UA-reduction-era Android models) cannot be
// re-derived from the UA string.
type UserSession struct {
	ID     string `gorm:"size:64;primaryKey"`
	UserID int64  `gorm:"not null;index"`
	// pb.LoginMethod — the auth strategy; social logins stay UNSPECIFIED
	// with Provider carrying the IdP.
	Method int32 `gorm:"not null;default:0"`
	// pb.IdentityProvider — the IdP for social/mini-program logins and
	// direct registrations; UNSPECIFIED for plain credential logins.
	Provider  int32  `gorm:"not null;default:0"`
	Target    string `gorm:"size:256"` // credential subject (username/email/phone/oauth uid)
	Device    string `gorm:"size:128"` // hardware name when known (client hint > UA)
	IP        string `gorm:"size:45"`
	UserAgent string `gorm:"size:512"`
	Country   string `gorm:"size:4"`
	City      string `gorm:"size:64"`
	RevokedAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}
