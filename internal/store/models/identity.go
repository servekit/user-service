package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// UserIdentity represents a login method bound to a user.
//
// Verified semantics: a UserIdentity is created ONLY after the owning flow has
// cryptographically confirmed control of the target:
//
//   - EMAIL / PHONE identities: the calling RPC (Register / BindIdentity /
//     ChangePassword-via-code / ResetPassword / code-based Login) has already
//     verified the captcha code via go-common/captcha before writing the row.
//     Reaching the row insert == the code matched.
//   - OAuth identities (GitHub / Google / WeChat / Apple / MiniProgram): the
//     calling RPC has completed ExchangeCode with the provider, which returns
//     the provider UID only for users who control the OAuth account.
//   - ADMIN identities (created via CreateUser): the admin attests control at
//     creation time; no code is sent.
//
// There is currently no separate "click a verification link in your email"
// flow — Verified is set true at every write site for the reasons above. If
// such a flow is added later (e.g. SendVerificationCode(purpose=VERIFY_EMAIL)
// → click → ConfirmEmail RPC), that single path will flip Verified true on a
// row that was previously false; until then, false is unreachable from any
// production write path and the field exists only to support future flows.
type UserIdentity struct {
	ID          int64     `gorm:"primaryKey;autoIncrement"`
	UserID      int64     `gorm:"not null;index"`
	Provider    int32     `gorm:"not null;uniqueIndex:uq_user_identity_provider"`          // pb.IdentityProvider (1=email, 2=phone, 3=github, 4=google, 5=wechat, 6=apple, 7=wechat_miniprogram)
	ProviderUID string    `gorm:"size:256;not null;uniqueIndex:uq_user_identity_provider"` // email address / phone / OAuth UID
	// Credentials holds a raw bcrypt password hash (or an OAuth token). It is
	// plain text, NOT type:json — a json column rejects the raw `$2a$...` hash
	// on PostgreSQL ("invalid input syntax for type json"), which broke every
	// password-identity write. JSON values (OAuthData) live in their own field.
	Credentials string `gorm:"size:512"` // bcrypt hash / OAuth token
	Verified    bool      `gorm:"not null;default:false"`
	OAuthData   OAuthData `gorm:"type:json"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}

// OAuthData stores provider-specific token data for social login identities.
type OAuthData struct {
	AccessToken string `json:"access_token,omitempty"`
	SessionKey  string `json:"session_key,omitempty"`
	UnionID     string `json:"unionid,omitempty"`
}

// Scan implements sql.Scanner. Handles []byte (postgres/mysql) and string
// (sqlite/modernc) so OAuthData is portable across all supported dialects.
func (o *OAuthData) Scan(value any) error {
	if value == nil {
		*o = OAuthData{}
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("models: cannot scan %T into OAuthData", value)
	}
	if len(bytes) == 0 {
		*o = OAuthData{}
		return nil
	}
	if err := json.Unmarshal(bytes, o); err != nil {
		return fmt.Errorf("models: unmarshal OAuthData: %w", err)
	}
	return nil
}

// Value implements driver.Valuer. Non-OAuth identities (zero OAuthData) store
// NULL; OAuth identities marshal their token data as JSON.
func (o OAuthData) Value() (driver.Value, error) {
	if o == (OAuthData{}) {
		return nil, nil
	}
	b, err := json.Marshal(o)
	if err != nil {
		return nil, fmt.Errorf("models: marshal OAuthData: %w", err)
	}
	return b, nil
}
