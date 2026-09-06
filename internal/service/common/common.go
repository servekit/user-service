// Package common holds helpers shared across service subpackages
// (auth, social, user, etc.) to prevent duplication.
package common

import (
	"strings"

	pb "github.com/servekit/api/gen/go/user/v1"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/clientinfo"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// LoginDeviceType maps the captured client environment onto the pb.DeviceType
// value stored in session and login-log rows. A caller with no User-Agent at
// all (direct gRPC / machine client, no edge middleware in front) is
// classified as API rather than Web.
func LoginDeviceType(ci clientinfo.ClientInfo) int32 {
	switch {
	case strings.Contains(ci.OS, "iOS"):
		return int32(pb.DeviceType_DEVICE_TYPE_IOS)
	case strings.Contains(ci.OS, "Android"):
		return int32(pb.DeviceType_DEVICE_TYPE_ANDROID)
	case ci.UserAgent == "":
		return int32(pb.DeviceType_DEVICE_TYPE_API)
	default:
		return int32(pb.DeviceType_DEVICE_TYPE_WEB)
	}
}

// ConvertUser maps a stored *models.User to its proto representation.
func ConvertUser(u *models.User) *pb.User {
	p := &pb.User{
		Id:             u.ID,
		Nickname:       u.Nickname,
		RealName:       u.RealName,
		AvatarUrl:      u.AvatarURL,
		Bio:            u.Bio,
		RegisterSource: pb.IdentityProvider(u.RegisterSource),
		Gender:         pb.Gender(u.Gender),
		Status:         pb.UserStatus(u.Status),
		UserType:       pb.UserType(u.UserType),
	}
	if u.Username != nil {
		p.Username = *u.Username
	}
	if u.Email != nil {
		p.Email = *u.Email
	}
	p.RegionCode = u.RegionCode
	if u.Phone != nil {
		p.Phone = *u.Phone
	}
	if u.Birthday != nil {
		p.Birthday = u.Birthday.Format("2006-01-02")
	}
	if u.Timezone != "" {
		p.Timezone = u.Timezone
	}
	if u.Locale != "" {
		p.Locale = u.Locale
	}
	if u.LastLoginAt != nil {
		p.LastLoginAt = timestamppb.New(*u.LastLoginAt)
	}
	if !u.CreatedAt.IsZero() {
		p.CreatedAt = timestamppb.New(u.CreatedAt)
	}
	if !u.UpdatedAt.IsZero() {
		p.UpdatedAt = timestamppb.New(u.UpdatedAt)
	}
	return p
}

// FirstNonEmpty returns the first non-empty argument, or "" if all are empty.
func FirstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// PtrIfNonEmpty returns a pointer to s when s is non-empty, nil otherwise.
// Useful for optional unique fields where "" must be stored as NULL so the
// DB unique constraint does not collapse multiple empty values onto one row.
func PtrIfNonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
