// Package common holds helpers shared across service subpackages
// (auth, social, user, etc.) to prevent duplication.
package common

import (
	"context"

	gidv1 "github.com/servekit/gid-service/gen/gid/v1"
	gidservice "github.com/servekit/gid-service/pkg"
	pb "github.com/servekit/user-service/gen/user/v1"
	"github.com/servekit/user-service/internal/store/models"

	"google.golang.org/protobuf/types/known/timestamppb"
)

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

// NextID fetches one int64 ID from the gid dependency over the generated
// (proto-shaped) interface, unwrapping the request/response for callers that
// just need the number.
func NextID(ctx context.Context, gid gidservice.Service) (int64, error) {
	resp, err := gid.NextID(ctx, &gidv1.NextIDRequest{})
	if err != nil {
		return 0, err
	}
	return resp.GetId(), nil
}
