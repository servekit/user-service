package models

import (
	"time"

	"gorm.io/gorm"
)

// UserRegisterProfile captures the registration environment for risk
// control: one row per user, written inside the register transaction,
// never updated afterwards. Only the raw UserAgent is stored — the device
// class derives at read time (same philosophy as UserAuthLog); Device
// stores the hardware model because the Sec-CH-UA-Model hint cannot be
// re-derived from the UA string (same reason as UserSession).
type UserRegisterProfile struct {
	UserID        int64  `gorm:"primaryKey"`
	IP            string `gorm:"size:45;index"` // reverse lookup: IP -> users
	UserAgent     string `gorm:"size:512"`      // full raw UA
	UserAgentHash string `gorm:"size:32;index"` // md5 hex of UserAgent; reverse lookup UA -> users without indexing a 512-byte string
	Device        string `gorm:"size:128"`      // client-hint hardware model, empty when unknown
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     gorm.DeletedAt `gorm:"index"`
}
