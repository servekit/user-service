package models

import (
	"time"

	"gorm.io/gorm"
)

// UserUser represents the core user record with snowflake ID.
type UserUser struct {
	ID             int64   `gorm:"primaryKey"`
	Username       *string `gorm:"size:64;uniqueIndex"`
	Nickname       string  `gorm:"size:64;index"`
	RealName       string  `gorm:"size:64"`
	AvatarURL      string  `gorm:"size:512"`
	Email          *string `gorm:"size:256;uniqueIndex"`
	RegionCode     string  `gorm:"size:2;column:region_code;index:idx_user_phone,unique"`
	Phone          *string `gorm:"size:20;index:idx_user_phone,unique"`
	Gender         int32   `gorm:"default:4"` // pb.Gender (1=male, 2=female, 3=other, 4=unknown)
	Birthday       *time.Time
	Timezone       string `gorm:"size:64"`
	Locale         string `gorm:"size:16"`
	Bio            string `gorm:"size:512"`
	Status         int32  `gorm:"not null;default:1"` // pb.UserStatus (1=active, 2=disabled, 3=pending_review)
	RegisterSource int32  // pb.IdentityProvider (1=email, 2=phone, 3=github, 4=google, 5=wechat, 6=apple, 7=wechat_miniprogram)
	UserType       int32  `gorm:"not null;default:1;index"` // pb.UserType (1=normal, 2=internal)
	LastLoginAt    *time.Time
	LastLoginIP    string `gorm:"size:45"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`
}
