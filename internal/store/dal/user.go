package dal

import (
	"context"
	"errors"
	"time"

	"github.com/servekit/go-common/dbx"

	"github.com/servekit/user-service/internal/store/generated"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"gorm.io/gorm"
)

// UserFilterCore holds the filter + sort fields shared by the cursor-based
// ListUsers and the offset-based ListUsersPaged. Embedding this in both
// filter types keeps the WHERE-clause logic in a single place.
type UserFilterCore struct {
	Status           int32   // pb.UserStatus; 0 = no filter
	Gender           int32   // pb.Gender; 0 = no filter
	RegisterSource   int32   // pb.IdentityProvider; 0 = no filter
	RegisterDevice   int32   // pb.DeviceType; 0 = no filter
	UserType         int32   // pb.UserType; 0 = no filter
	UserIDs          []int64 // exact batch lookup; empty = no filter
	Locale           string
	Timezone         string
	RegisterIP       string
	LastLoginIP      string
	NicknamePrefix   string     // LIKE 'prefix%'; empty = no filter; uses nickname index
	CreatedAtStart   *time.Time // nil = no lower bound
	CreatedAtEnd     *time.Time // nil = no upper bound
	LastLoginAtStart *time.Time
	LastLoginAtEnd   *time.Time
	UserIDs          []int64 // empty = no filter; otherwise IN match
	Email            string  // exact match; empty = no filter
	RegionCode       string  // exact ISO alpha-2 match; empty = no filter
	Phone            string  // exact match; empty = no filter
	Username         string  // exact match; empty = no filter

	// Sort spec shared by both list paths.
	OrderBy    int32 // pb.UserSortField; UNSPECIFIED falls back to ID
	Descending bool
}

// UserFilter holds optional filter conditions for the cursor-based ListUsers.
// nil/zero/empty values mean "no filter on this field". dal must NOT depend
// on proto, so enums are stored as raw int32 (callers translate from proto
// enum values).
//
// Sort + cursor semantics: ListUsers orders by (sort_col, id) so pagination
// is stable. To advance past a page, callers must supply AfterID (always)
// AND the matching After* sort-column value from the previous page's last
// row. Cursor encode/decode lives in internal/utils/pagination.
type UserFilter struct {
	UserFilterCore

	// Cursor anchors populated by service layer from a decoded page token.
	AfterCreatedAt   *time.Time // populated when OrderBy == CREATED_AT
	AfterUpdatedAt   *time.Time // populated when OrderBy == UPDATED_AT
	AfterLastLoginAt *time.Time // populated when OrderBy == LAST_LOGIN_AT

	dbx.Pagination
}

// UserPagedFilter holds filter + offset-pagination options for ListUsersPaged.
// Use this for admin UIs that need page numbers and total counts; for stable
// iteration under concurrent writes, use UserFilter (cursor) instead.
type UserPagedFilter struct {
	UserFilterCore
	dbx.PageParams
}

// CreateUser inserts a new user record.
func CreateUser(ctx context.Context, tx *gorm.DB, user *models.User) error {
	if err := gorm.G[models.User](tx).Create(ctx, user); err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	return nil
}

// GetUserByID returns a user by ID.
func GetUserByID(ctx context.Context, tx *gorm.DB, id int64) (*models.User, error) {
	user, err := gorm.G[models.User](tx).
		Where(generated.User.ID.Eq(id)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrUserNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &user, nil
}

// GetUserByEmail returns a user by email address.
func GetUserByEmail(ctx context.Context, tx *gorm.DB, email string) (*models.User, error) {
	user, err := gorm.G[models.User](tx).
		Where(generated.User.Email.Eq(email)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrUserNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &user, nil
}

// GetUserByPhone returns a user by region code + phone number.
// Both arguments are required; the lookup uses the composite unique index.
func GetUserByPhone(ctx context.Context, tx *gorm.DB, regionCode, phone string) (*models.User, error) {
	user, err := gorm.G[models.User](tx).
		Where(generated.User.RegionCode.Eq(regionCode)).
		Where(generated.User.Phone.Eq(phone)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrUserNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &user, nil
}

// GetUserByUsername returns a user by username.
func GetUserByUsername(ctx context.Context, tx *gorm.DB, username string) (*models.User, error) {
	user, err := gorm.G[models.User](tx).
		Where(generated.User.Username.Eq(username)).
		Take(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, xcodes.ErrUserNotFound.New()
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &user, nil
}

// UpdateUser saves changes to a user record (all fields, including zero values).
func UpdateUser(ctx context.Context, tx *gorm.DB, user *models.User) error {
	result := tx.WithContext(ctx).Save(user)
	if result.Error != nil {
		return xcodes.ErrInternal.Wrap(result.Error)
	}
	if result.RowsAffected == 0 {
		return xcodes.ErrUserNotFound.New()
	}
	return nil
}

// UpdateUserLastLogin updates the last login timestamp and IP.
func UpdateUserLastLogin(ctx context.Context, tx *gorm.DB, id int64, ip string) error {
	rowsAffected, err := gorm.G[models.User](tx).
		Where(generated.User.ID.Eq(id)).
		Set(
			generated.User.LastLoginAt.Now(),
			generated.User.LastLoginIP.Set(ip),
		).
		Update(ctx)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	if rowsAffected == 0 {
		return xcodes.ErrUserNotFound.New()
	}
	return nil
}

// ListUsers returns a page of users. Sort and cursor semantics live on the
// filter: callers set OrderBy/Descending plus the After* anchors decoded
// from a page token (internal/utils/pagination), then this function emits
// the (sort_col, id) ORDER BY and the matching row-value WHERE.
//
// The returned slice may be pageSize+1 long; service layer trims via
// dbx.TrimPage and re-encodes the last row as the next page token.
// All zero/empty/nil fields in f are treated as "no filter on this field".
func ListUsers(ctx context.Context, tx *gorm.DB, f UserFilter) ([]*models.User, error) {
	pg := f.Normalize()

	q := gorm.G[models.User](tx).Scopes(func(*gorm.Statement) {})
	q = applyUserOrder(q, f.OrderBy, f.Descending)
	q = applyUserCursor(q, f.OrderBy, f.Descending, pg.AfterID, f.AfterCreatedAt, f.AfterUpdatedAt, f.AfterLastLoginAt)
	q = applyUserFilters(q, f.UserFilterCore)

	results, err := q.Limit(pg.FetchLimit()).Find(ctx)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	users := make([]*models.User, len(results))
	for i := range results {
		users[i] = &results[i]
	}
	return users, nil
}

// ListUsersPaged returns a page of users with optional total count, using
// offset pagination. Use this for admin UIs that need page numbers and
// total counts. For stable iteration under concurrent writes, use ListUsers
// (cursor) instead.
//
// When f.Count is false, COUNT(*) is skipped and the returned total is 0.
func ListUsersPaged(ctx context.Context, tx *gorm.DB, f UserPagedFilter) ([]*models.User, int64, error) {
	pp := f.Normalize()

	q := gorm.G[models.User](tx).Scopes(func(*gorm.Statement) {})
	q = applyUserFilters(q, f.UserFilterCore)

	var total int64
	if f.Count {
		count, err := q.Count(ctx, "*")
		if err != nil {
			return nil, 0, xcodes.ErrInternal.Wrap(err)
		}
		total = count
	}

	q = applyUserOrder(q, f.OrderBy, f.Descending)

	offset := (pp.Page - 1) * pp.PageSize
	if offset > 0 {
		q = q.Offset(offset)
	}

	results, err := q.Limit(pp.PageSize).Find(ctx)
	if err != nil {
		return nil, 0, xcodes.ErrInternal.Wrap(err)
	}

	users := make([]*models.User, len(results))
	for i := range results {
		users[i] = &results[i]
	}
	return users, total, nil
}

// applyUserFilters applies the shared WHERE clauses from UserFilterCore.
// Order/cursor are applied separately because they interact with pagination
// (cursor emits a WHERE on the sort column; order emits the ORDER BY).
func applyUserFilters(q gorm.ChainInterface[models.User], f UserFilterCore) gorm.ChainInterface[models.User] {
	if f.Status != 0 {
		q = q.Where(generated.User.Status.Eq(f.Status))
	}
	if f.Gender != 0 {
		q = q.Where(generated.User.Gender.Eq(f.Gender))
	}
	if f.RegisterSource != 0 {
		q = q.Where(generated.User.RegisterSource.Eq(f.RegisterSource))
	}
	if f.RegisterDevice != 0 {
		q = q.Where(generated.User.RegisterDevice.Eq(f.RegisterDevice))
	}
	if f.UserType != 0 {
		q = q.Where(generated.User.UserType.Eq(f.UserType))
	}
	if f.Locale != "" {
		q = q.Where(generated.User.Locale.Eq(f.Locale))
	}
	if f.Timezone != "" {
		q = q.Where(generated.User.Timezone.Eq(f.Timezone))
	}
	if f.RegisterIP != "" {
		q = q.Where(generated.User.RegisterIP.Eq(f.RegisterIP))
	}
	if f.LastLoginIP != "" {
		q = q.Where(generated.User.LastLoginIP.Eq(f.LastLoginIP))
	}
	if f.CreatedAtStart != nil {
		q = q.Where(generated.User.CreatedAt.Gte(*f.CreatedAtStart))
	}
	if f.CreatedAtEnd != nil {
		q = q.Where(generated.User.CreatedAt.Lt(*f.CreatedAtEnd))
	}
	if f.LastLoginAtStart != nil {
		q = q.Where(generated.User.LastLoginAt.Gte(*f.LastLoginAtStart))
	}
	if f.LastLoginAtEnd != nil {
		q = q.Where(generated.User.LastLoginAt.Lt(*f.LastLoginAtEnd))
	}
	if f.NicknamePrefix != "" {
		// Prefix match: LIKE 'prefix%' can use the nickname B-tree index.
		q = q.Where(generated.User.Nickname.Like(f.NicknamePrefix + "%"))
	}
	if len(f.UserIDs) > 0 {
		q = q.Where(generated.User.ID.In(f.UserIDs...))
	}
	if f.Email != "" {
		q = q.Where(generated.User.Email.Eq(f.Email))
	}
	if f.RegionCode != "" && f.Phone != "" {
		q = q.
			Where(generated.User.RegionCode.Eq(f.RegionCode)).
			Where(generated.User.Phone.Eq(f.Phone))
	}
	if f.Username != "" {
		q = q.Where(generated.User.Username.Eq(f.Username))
	}
	return q
}

// Sort field constants matching pb.UserSortField. Kept as raw int32 so the
// dal layer does not depend on the generated proto package.
const (
	sortFieldID          int32 = 1
	sortFieldCreatedAt   int32 = 2
	sortFieldUpdatedAt   int32 = 3
	sortFieldLastLoginAt int32 = 4
)

// applyUserOrder emits the (sort_col, id) ORDER BY. The id tiebreaker keeps
// pagination stable when sort_col has duplicate values. UNSPECIFIED falls
// back to id so legacy callers (and bare-numeric tokens) keep working.
func applyUserOrder(q gorm.ChainInterface[models.User], orderBy int32, descending bool) gorm.ChainInterface[models.User] {
	switch orderBy {
	case sortFieldCreatedAt:
		if descending {
			return q.Order(generated.User.CreatedAt.Desc()).Order(generated.User.ID.Desc())
		}
		return q.Order(generated.User.CreatedAt).Order(generated.User.ID)
	case sortFieldUpdatedAt:
		if descending {
			return q.Order(generated.User.UpdatedAt.Desc()).Order(generated.User.ID.Desc())
		}
		return q.Order(generated.User.UpdatedAt).Order(generated.User.ID)
	case sortFieldLastLoginAt:
		if descending {
			return q.Order(generated.User.LastLoginAt.Desc()).Order(generated.User.ID.Desc())
		}
		return q.Order(generated.User.LastLoginAt).Order(generated.User.ID)
	default:
		if descending {
			return q.Order(generated.User.ID.Desc())
		}
		return q.Order(generated.User.ID)
	}
}

// applyUserCursor advances the query past the (sort_col, id) tuple of the
// previous page's last row. This is the only way to page safely when ORDER
// BY uses a non-id column: a bare `id < afterID` cursor drops rows whose
// id ordering disagrees with the sort column (the common case).
//
// Callers must pass the matching After* sort value alongside afterID. When
// only afterID is set the cursor degrades to id-only (correct only for
// the default ID-ascending sort).
func applyUserCursor(
	q gorm.ChainInterface[models.User],
	orderBy int32,
	descending bool,
	afterID int64,
	afterCreatedAt, afterUpdatedAt, afterLastLoginAt *time.Time,
) gorm.ChainInterface[models.User] {
	if afterID == 0 {
		return q
	}
	switch orderBy {
	case sortFieldCreatedAt:
		if afterCreatedAt == nil {
			return q
		}
		if descending {
			return q.Where("created_at < ? OR (created_at = ? AND id < ?)", *afterCreatedAt, *afterCreatedAt, afterID)
		}
		return q.Where("created_at > ? OR (created_at = ? AND id > ?)", *afterCreatedAt, *afterCreatedAt, afterID)
	case sortFieldUpdatedAt:
		if afterUpdatedAt == nil {
			return q
		}
		if descending {
			return q.Where("updated_at < ? OR (updated_at = ? AND id < ?)", *afterUpdatedAt, *afterUpdatedAt, afterID)
		}
		return q.Where("updated_at > ? OR (updated_at = ? AND id > ?)", *afterUpdatedAt, *afterUpdatedAt, afterID)
	case sortFieldLastLoginAt:
		if afterLastLoginAt == nil {
			return q
		}
		if descending {
			return q.Where("last_login_at < ? OR (last_login_at = ? AND id < ?)", *afterLastLoginAt, *afterLastLoginAt, afterID)
		}
		return q.Where("last_login_at > ? OR (last_login_at = ? AND id > ?)", *afterLastLoginAt, *afterLastLoginAt, afterID)
	default:
		if descending {
			return q.Where("id < ?", afterID)
		}
		return q.Where("id > ?", afterID)
	}
}
