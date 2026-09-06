package dal

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"

	"github.com/servekit/user-service/internal/store/generated"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"gorm.io/gorm"
)

// HashUserAgent returns the md5 hex of a raw User-Agent for exact-match
// reverse lookup, or "" for an empty UA (hashing "" would index noise).
func HashUserAgent(ua string) string {
	if ua == "" {
		return ""
	}
	sum := md5.Sum([]byte(ua))
	return hex.EncodeToString(sum[:])
}

// CreateRegisterProfile inserts a registration-environment row. When the UA
// is non-empty and the hash was not pre-computed, it is filled here so no
// write site can forget it.
func CreateRegisterProfile(ctx context.Context, tx *gorm.DB, p *models.UserRegisterProfile) error {
	if p.UserAgent != "" && p.UserAgentHash == "" {
		p.UserAgentHash = HashUserAgent(p.UserAgent)
	}
	if err := gorm.G[models.UserRegisterProfile](tx).Create(ctx, p); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// GetRegisterProfileByUserID returns the 1:1 registration-environment row,
// or (nil, nil) when the user has none yet (pre-backfill legacy users).
func GetRegisterProfileByUserID(ctx context.Context, tx *gorm.DB, userID int64) (*models.UserRegisterProfile, error) {
	profile, err := gorm.G[models.UserRegisterProfile](tx).
		Where(generated.UserRegisterProfile.UserID.Eq(userID)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &profile, nil
}

// BackfillRegisterProfiles creates the missing 1:1 register-profile rows
// for existing users, sourcing the environment from each user's earliest
// successful register auth-log row (Action 2=register, 4=social_register,
// see models.UserAuthLog). Users with no such log get an empty-env row.
// Idempotent: existing rows are never touched. Returns rows created.
func BackfillRegisterProfiles(ctx context.Context, db *gorm.DB) (int64, error) {
	const batchSize = 500
	var created int64
	var lastID int64
	for {
		var ids []int64
		if err := db.WithContext(ctx).
			Table("user_users uu").
			Select("uu.id").
			Joins("LEFT JOIN user_register_profiles p ON p.user_id = uu.id").
			Where("p.user_id IS NULL AND uu.id > ?", lastID).
			Order("uu.id").
			Limit(batchSize).
			Pluck("uu.id", &ids).Error; err != nil {
			return created, xcodes.ErrInternal.Wrap(err)
		}
		if len(ids) == 0 {
			return created, nil
		}
		for _, uid := range ids {
			lastID = uid
			profile := &models.UserRegisterProfile{UserID: uid}
			earliest, err := gorm.G[models.UserAuthLog](db).
				Where(generated.UserAuthLog.UserID.Eq(uid)).
				Where(generated.UserAuthLog.Action.In(2, 4)).
				Where(generated.UserAuthLog.Success.Eq(true)).
				Order(generated.UserAuthLog.ID.Asc()).
				Take(ctx)
			if err == nil {
				profile.IP = earliest.IP
				profile.UserAgent = earliest.UserAgent
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return created, xcodes.ErrInternal.Wrap(err)
			}
			if err := CreateRegisterProfile(ctx, db, profile); err != nil {
				return created, err
			}
			created++
		}
	}
}
