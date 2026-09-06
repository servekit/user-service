// Package convert holds helpers shared across service subpackages
// (auth, social, user, etc.) to prevent duplication.
package convert

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/servekit/api/gen/go/user/v1"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/clientinfo"

	"github.com/servekit/go-common/useragent"
)

// DeviceTypeFromUA classifies a raw User-Agent onto the pb.DeviceType value
// for session and login-log rows. os/browser columns are gone — the class
// derives from the UA at read time (iOS → IOS; any other phone OS incl.
// HarmonyOS → ANDROID; HTTP libraries → API; desktop browsers → WEB; no UA
// at all → API for machine callers on direct gRPC, UNSPECIFIED when unknown).
func DeviceTypeFromUA(ua string) int32 {
	switch {
	case ua == "":
		return int32(pb.DeviceType_DEVICE_TYPE_UNSPECIFIED)
	case clientinfo.IsApiClient(ua):
		return int32(pb.DeviceType_DEVICE_TYPE_API)
	}
	r := useragent.Parse(ua)
	switch {
	case r.OS == "iOS":
		return int32(pb.DeviceType_DEVICE_TYPE_IOS)
	case r.DeviceClass == useragent.ClassSmartphone,
		r.DeviceClass == useragent.ClassTablet,
		r.DeviceClass == useragent.ClassMobile,
		r.OS == "Android", r.OS == "HarmonyOS", r.OS == "OpenHarmony":
		return int32(pb.DeviceType_DEVICE_TYPE_ANDROID)
	case r.DeviceClass == useragent.ClassDesktop:
		return int32(pb.DeviceType_DEVICE_TYPE_WEB)
	default:
		return int32(pb.DeviceType_DEVICE_TYPE_WEB)
	}
}

// User maps a stored *models.UserUser to its proto representation.
func User(u *models.UserUser) *pb.User {
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
