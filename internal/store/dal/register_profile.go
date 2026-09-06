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
