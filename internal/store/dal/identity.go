package dal

import (
	"context"
	"errors"

	"github.com/servekit/user-service/internal/store/generated"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"gorm.io/gorm"
)

// CreateIdentity inserts a new identity record.
func CreateIdentity(ctx context.Context, tx *gorm.DB, identity *models.UserIdentity) error {
	if err := gorm.G[models.UserIdentity](tx).Create(ctx, identity); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// GetIdentityByID returns an identity by ID.
func GetIdentityByID(ctx context.Context, tx *gorm.DB, id int64) (*models.UserIdentity, error) {
	identity, err := gorm.G[models.UserIdentity](tx).
		Where(generated.UserIdentity.ID.Eq(id)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrIdentityNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &identity, nil
}

// GetIdentityByProviderUID returns an identity by provider and provider UID,
// or (nil, nil) when no row matches. Absent is a normal outcome here: every
// caller branches on nil (register/bind duplicate checks proceed, login
// resolves not-found to its own ErrIdentityNotFound / auto-register path).
// Returning ErrIdentityNotFound from the dal instead short-circuits those
// nil branches and breaks registration and code-login outright.
func GetIdentityByProviderUID(ctx context.Context, tx *gorm.DB, provider int32, providerUID string) (*models.UserIdentity, error) {
	identity, err := gorm.G[models.UserIdentity](tx).
		Where(generated.UserIdentity.Provider.Eq(provider)).
		Where(generated.UserIdentity.ProviderUID.Eq(providerUID)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &identity, nil
}

// ListIdentitiesByUserID returns all identities for a user.
func ListIdentitiesByUserID(ctx context.Context, tx *gorm.DB, userID int64) ([]*models.UserIdentity, error) {
	results, err := gorm.G[models.UserIdentity](tx).
		Where(generated.UserIdentity.UserID.Eq(userID)).
		Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	identities := make([]*models.UserIdentity, len(results))
	for i := range results {
		identities[i] = &results[i]
	}
	return identities, nil
}

// UpdateIdentityCredentials updates the stored credentials for an identity.
func UpdateIdentityCredentials(ctx context.Context, tx *gorm.DB, id int64, credentials string) error {
	rowsAffected, err := gorm.G[models.UserIdentity](tx).
		Where(generated.UserIdentity.ID.Eq(id)).
		Set(generated.UserIdentity.Credentials.Set(credentials)).
		Update(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	if rowsAffected == 0 {
		return xcodes.ErrIdentityNotFound.New()
	}
	return nil
}

// DeleteIdentity removes an identity by ID.
func DeleteIdentity(ctx context.Context, tx *gorm.DB, id int64) error {
	rowsAffected, err := gorm.G[models.UserIdentity](tx).
		Where(generated.UserIdentity.ID.Eq(id)).
		Delete(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	if rowsAffected == 0 {
		return xcodes.ErrIdentityNotFound.New()
	}
	return nil
}
