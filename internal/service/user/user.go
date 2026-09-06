package user

import (
	"context"
	"errors"
	"strconv"
	"time"

	pb "github.com/servekit/api/gen/go/user/v1"
	gidservice "github.com/servekit/gid-service/pkg"
	common "github.com/servekit/user-service/internal/service/common"
	"github.com/servekit/user-service/internal/store/dal"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/internal/utils/credentials"
	"github.com/servekit/user-service/internal/utils/pagination"
	phoneutil "github.com/servekit/user-service/internal/utils/phone"
	"github.com/servekit/user-service/pkg/clientinfo"
	"github.com/servekit/user-service/pkg/xcodes"

	"github.com/servekit/go-common/captcha"
	"github.com/servekit/go-common/ptr"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

// SessionRevoker revokes all sessions for a user. Satisfied by
// *session.Service; declared here so this package does not need to import
// the session subpackage and risk future import cycles.
type SessionRevoker interface {
	RevokeAllByUserID(ctx context.Context, userID int64) error
}

// Service handles user management RPCs.
type Service struct {
	db      *gorm.DB
	gid     gidservice.Service
	revoker SessionRevoker
	captcha *captcha.Captcha
}

// New creates a new user Service. revoker may be nil when session revocation
// is not required (e.g. admin tools that never disable users); DisableUser
// falls back to a status-only update in that case. captcha is required for
// ResetPassword; pass nil only in tests that don't exercise that RPC.
func New(db *gorm.DB, gid gidservice.Service, revoker SessionRevoker, captchaSvc *captcha.Captcha) *Service {
	return &Service{db: db, gid: gid, revoker: revoker, captcha: captchaSvc}
}

// GetProfile returns the current user's profile.
func (s *Service) GetProfile(ctx context.Context, req *pb.GetProfileRequest) (*pb.User, error) {
	user, err := dal.GetUserByID(ctx, s.db, req.GetUserId())
	if err != nil {
		return nil, err
	}
	return common.ConvertUser(user), nil
}

// UpdateProfile updates the current user's profile.
func (s *Service) UpdateProfile(ctx context.Context, req *pb.UpdateProfileRequest) (*pb.User, error) {
	user, err := dal.GetUserByID(ctx, s.db, req.GetUserId())
	if err != nil {
		return nil, err
	}
	if req.Username != "" {
		user.Username = common.PtrIfNonEmpty(req.Username)
	}
	if req.Nickname != "" {
		user.Nickname = req.Nickname
	}
	if req.RealName != "" {
		user.RealName = req.RealName
	}
	if req.AvatarUrl != "" {
		user.AvatarURL = req.AvatarUrl
	}
	if req.Birthday != "" {
		// TODO: parse birthday and update user.Birthday
		_ = req.Birthday
	}
	if req.Timezone != "" {
		user.Timezone = req.Timezone
	}
	if req.Locale != "" {
		user.Locale = req.Locale
	}
	if req.Bio != "" {
		user.Bio = req.Bio
	}
	if req.Gender != pb.Gender_GENDER_UNSPECIFIED {
		user.Gender = int32(req.Gender)
	}
	if err := dal.UpdateUser(ctx, s.db, user); err != nil {
		return nil, err
	}
	return common.ConvertUser(user), nil
}

// ChangePassword changes the user's password. When the account is in
// PENDING_REVIEW status (e.g. created by an admin via CreateUser), a successful
// change flips the status to ACTIVE — this is the activation path.
//
// Side effect: revokes every active session for the user (including the one
// used to make this call) after the password update commits. Matches
// ResetPassword's behavior and closes the window where an attacker who
// hijacked an account could keep using the leaked session after the victim
// changed their password. BFFs must follow up with a Login to mint a new
// session for the caller.
func (s *Service) ChangePassword(ctx context.Context, req *pb.ChangePasswordRequest) (*emptypb.Empty, error) {
	userID := req.GetUserId()

	var ident *models.UserIdentity
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user, err := dal.GetUserByID(ctx, tx, userID)
		if err != nil {
			return err
		}

		idents, err := dal.ListIdentitiesByUserID(ctx, tx, userID)
		if err != nil {
			return err
		}
		for _, id := range idents {
			if id.Credentials != "" {
				ident = id
				break
			}
		}
		if ident == nil {
			return xcodes.ErrIdentityNotFound.New()
		}
		if err := credentials.VerifyPassword(ident.Credentials, req.OldPassword); err != nil {
			return xcodes.ErrPasswordWrong.New()
		}

		hash, err := credentials.HashPassword(req.NewPassword)
		if err != nil {
			return xcodes.ErrInternal.Wrap(err)
		}
		if err := dal.UpdateIdentityCredentials(ctx, tx, ident.ID, hash); err != nil {
			return err
		}

		if pb.UserStatus(user.Status) == pb.UserStatus_USER_STATUS_PENDING_REVIEW {
			user.Status = int32(pb.UserStatus_USER_STATUS_ACTIVE)
			if err := dal.UpdateUser(ctx, tx, user); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Revoke all sessions AFTER the tx commits — see ResetPassword for the
	// atomicity contract (Redis write is not rolled back if we fail later,
	// but that over-revokes safely). nil revoker = test/feature flag that
	// skips session invalidation (e.g. admin tools that don't have a session
	// store wired up).
	if s.revoker != nil {
		if err := s.revoker.RevokeAllByUserID(ctx, userID); err != nil {
			return nil, err
		}
	}
	return &emptypb.Empty{}, nil
}

// ResetPassword resets a forgotten password via a verification code.
// Prerequisite: SendVerificationCode with purpose=PASSWORD_RESET against the
// same email or region_code+phone. After the password is updated, all active
// sessions for the user are revoked — they must sign in again with the new
// password.
func (s *Service) ResetPassword(ctx context.Context, req *pb.ResetPasswordRequest) (*emptypb.Empty, error) {
	if s.captcha == nil {
		return nil, xcodes.ErrInternal.New("captcha not configured")
	}
	if req.NewPassword == "" {
		return nil, xcodes.ErrBadRequest.New("new_password is required")
	}

	// Resolve target key + channel from request.
	var (
		targetKey string
		channel   pb.VerificationChannel
		provider  pb.IdentityProvider
	)
	if req.Email != "" {
		targetKey = req.Email
		channel = pb.VerificationChannel_VERIFICATION_CHANNEL_EMAIL
		provider = pb.IdentityProvider_IDENTITY_PROVIDER_EMAIL
	} else if req.RegionCode != "" && req.Phone != "" {
		rc := phoneutil.NormalizeRegionCode(req.RegionCode)
		p := phoneutil.NormalizePhone(req.Phone)
		if rc == "" || p == "" {
			return nil, xcodes.ErrBadRequest.New("region_code and phone are required for phone reset")
		}
		targetKey = phoneutil.CaptchaKey(rc, p)
		channel = pb.VerificationChannel_VERIFICATION_CHANNEL_SMS
		provider = pb.IdentityProvider_IDENTITY_PROVIDER_PHONE
	} else {
		return nil, xcodes.ErrBadRequest.New("email or region_code+phone is required")
	}

	// Verify the code (purpose=PASSWORD_RESET, channel-derived from target).
	codeResult, err := s.captcha.Verify(ctx, targetKey, req.Code,
		strconv.Itoa(int(pb.VerificationPurpose_VERIFICATION_PURPOSE_PASSWORD_RESET)),
		strconv.Itoa(int(channel)),
	)
	if err != nil {
		return nil, xcodes.ErrBadRequest.Wrap(err)
	}
	if !codeResult.Matched {
		return nil, xcodes.ErrBadRequest.New("invalid verification code")
	}

	// Find the user via the matching identity (provider + targetKey).
	ident, err := dal.GetIdentityByProviderUID(ctx, s.db, int32(provider), targetKey)
	if err != nil {
		return nil, err
	}
	if ident == nil {
		return nil, xcodes.ErrIdentityNotFound.New()
	}

	// Hash + update credentials on the identity.
	hash, hashErr := credentials.HashPassword(req.NewPassword)
	if hashErr != nil {
		return nil, xcodes.ErrInternal.Wrap(hashErr)
	}
	if err := dal.UpdateIdentityCredentials(ctx, s.db, ident.ID, hash); err != nil {
		return nil, err
	}

	// Invalidate all sessions — the user must sign in again with the new
	// password. Failure to revoke surfaces as an error so we don't silently
	// leave stale sessions alive after a security event.
	if s.revoker != nil {
		if err := s.revoker.RevokeAllByUserID(ctx, ident.UserID); err != nil {
			return nil, err
		}
	}
	return &emptypb.Empty{}, nil
}

// CreateUser creates a new user as an administrator. The new account starts in
// PENDING_REVIEW status; the user activates it by changing the initial password
// via ChangePassword, which flips the status to ACTIVE.
func (s *Service) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.CreateUserResponse, error) {
	hasEmail := req.Email != ""
	hasPhone := req.RegionCode != "" && req.Phone != ""
	if !hasEmail && !hasPhone {
		return nil, xcodes.ErrBadRequest.New("email or region_code+phone is required")
	}
	if hasEmail && hasPhone {
		return nil, xcodes.ErrBadRequest.New("only one of email or phone may be set")
	}

	hash, err := credentials.HashPassword(req.Password)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	userID, err := gidservice.NextID(ctx, s.gid)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	provider := pb.IdentityProvider_IDENTITY_PROVIDER_EMAIL
	var targetKey string
	if hasEmail {
		targetKey = req.Email
	} else {
		provider = pb.IdentityProvider_IDENTITY_PROVIDER_PHONE
		targetKey = phoneutil.CaptchaKey(phoneutil.NormalizeRegionCode(req.RegionCode), phoneutil.NormalizePhone(req.Phone))
	}

	var user *models.User
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if hasEmail {
			if existing, err := dal.GetUserByEmail(ctx, tx, req.Email); err == nil && existing != nil {
				return xcodes.ErrEmailExists.New()
			} else if err != nil && !errors.Is(err, xcodes.ErrUserNotFound.New()) {
				return err
			}
		}
		if hasPhone {
			cc := phoneutil.NormalizeRegionCode(req.RegionCode)
			p := phoneutil.NormalizePhone(req.Phone)
			if existing, err := dal.GetUserByPhone(ctx, tx, cc, p); err == nil && existing != nil {
				return xcodes.ErrPhoneExists.New()
			} else if err != nil && !errors.Is(err, xcodes.ErrUserNotFound.New()) {
				return err
			}
		}
		if req.Username != "" {
			if existing, err := dal.GetUserByUsername(ctx, tx, req.Username); err == nil && existing != nil {
				return xcodes.ErrUsernameExists.New()
			} else if err != nil && !errors.Is(err, xcodes.ErrUserNotFound.New()) {
				return err
			}
		}

		user = &models.User{
			Username:       common.PtrIfNonEmpty(req.Username),
			Nickname:       common.FirstNonEmpty(req.Nickname, req.Username, "user"),
			RealName:       req.RealName,
			RegionCode:     phoneutil.NormalizeRegionCode(req.RegionCode),
			Phone:          ptr.Ref(phoneutil.NormalizePhone(req.Phone)),
			Gender:         int32(req.Gender),
			Timezone:       common.FirstNonEmpty(req.Timezone, "Asia/Shanghai"),
			Locale:         common.FirstNonEmpty(req.Locale, "zh-CN"),
			Status:         int32(pb.UserStatus_USER_STATUS_PENDING_REVIEW),
			RegisterSource: int32(pb.IdentityProvider_IDENTITY_PROVIDER_ADMIN),
			UserType:       int32(req.UserType),
		}
		user.ID = userID
		if hasEmail {
			user.Email = ptr.Ref(req.Email)
		}
		if err := dal.CreateUser(ctx, tx, user); err != nil {
			return err
		}

		return dal.CreateIdentity(ctx, tx, &models.UserIdentity{
			UserID:      user.ID,
			Provider:    int32(provider),
			ProviderUID: targetKey,
			Credentials: hash,
			Verified:    true,
		})
	}); err != nil {
		return nil, err
	}

	return &pb.CreateUserResponse{User: common.ConvertUser(user)}, nil
}

// GetUser returns a user by ID (admin).
func (s *Service) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.User, error) {
	user, err := dal.GetUserByID(ctx, s.db, req.UserId)
	if err != nil {
		return nil, err
	}
	return common.ConvertUser(user), nil
}

// ListUsers returns a paginated list of users (admin).
func (s *Service) ListUsers(ctx context.Context, req *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
	f := dal.UserFilter{
		UserFilterCore: dal.UserFilterCore{
			Status:         int32(req.Status),
			Gender:         int32(req.Gender),
			RegisterSource: int32(req.RegisterSource),
			RegisterDevice: int32(req.RegisterDevice),
			UserType:       int32(req.UserType),
			Locale:         req.Locale,
			Timezone:       req.Timezone,
			RegisterIP:     req.RegisterIp,
			LastLoginIP:    req.LastLoginIp,
			NicknamePrefix: req.Nickname,
			UserIDs:        req.UserIds,
			Email:          req.Email,
			RegionCode:     req.RegionCode,
			Phone:          req.Phone,
			Username:       req.Username,
			OrderBy:        int32(req.OrderBy),
			Descending:     req.Descending,
		},
	}
	f.PageSize = int(req.PageSize)

	if req.CreatedAtStart != nil {
		t := req.CreatedAtStart.AsTime()
		f.CreatedAtStart = &t
	}
	if req.CreatedAtEnd != nil {
		t := req.CreatedAtEnd.AsTime()
		f.CreatedAtEnd = &t
	}
	if req.LastLoginAtStart != nil {
		t := req.LastLoginAtStart.AsTime()
		f.LastLoginAtStart = &t
	}
	if req.LastLoginAtEnd != nil {
		t := req.LastLoginAtEnd.AsTime()
		f.LastLoginAtEnd = &t
	}

	if cursor := req.GetCursor(); cursor != "" {
		c, err := pagination.DecodePageCursor(cursor)
		if err != nil {
			return nil, xcodes.ErrBadRequest.Wrap(err)
		}
		f.AfterID = c.ID
		if t := pagination.CursorToTime(c.CreatedAt); !t.IsZero() {
			f.AfterCreatedAt = &t
		}
		if t := pagination.CursorToTime(c.UpdatedAt); !t.IsZero() {
			f.AfterUpdatedAt = &t
		}
		if t := pagination.CursorToTime(c.LastLoginAt); !t.IsZero() {
			f.AfterLastLoginAt = &t
		}
	}

	users, err := dal.ListUsers(ctx, s.db, f)
	if err != nil {
		return nil, err
	}

	pg := f.Normalize()
	hasNext := len(users) > pg.PageSize
	if hasNext {
		users = users[:pg.PageSize]
	}

	pbUsers := make([]*pb.User, len(users))
	for i, u := range users {
		pbUsers[i] = common.ConvertUser(u)
	}

	var nextCursor string
	if hasNext && len(users) > 0 {
		last := users[len(users)-1]
		lastLogin := time.Time{}
		if last.LastLoginAt != nil {
			lastLogin = *last.LastLoginAt
		}
		nextCursor = pagination.EncodePageCursor(pagination.PageCursor{
			ID:          last.ID,
			CreatedAt:   pagination.CursorFromTime(last.CreatedAt),
			UpdatedAt:   pagination.CursorFromTime(last.UpdatedAt),
			LastLoginAt: pagination.CursorFromTime(lastLogin),
		})
	}

	return &pb.ListUsersResponse{Users: pbUsers, NextCursor: nextCursor}, nil
}

// ListUsersPaged returns a page of users with optional total count, using
// offset pagination. Use this for admin UIs that need page numbers and
// totals; for stable iteration under concurrent writes, use ListUsers.
func (s *Service) ListUsersPaged(ctx context.Context, req *pb.ListUsersPagedRequest) (*pb.ListUsersPagedResponse, error) {
	f := dal.UserPagedFilter{
		UserFilterCore: dal.UserFilterCore{
			Status:         int32(req.Status),
			Gender:         int32(req.Gender),
			RegisterSource: int32(req.RegisterSource),
			RegisterDevice: int32(req.RegisterDevice),
			UserType:       int32(req.UserType),
			Locale:         req.Locale,
			Timezone:       req.Timezone,
			RegisterIP:     req.RegisterIp,
			LastLoginIP:    req.LastLoginIp,
			NicknamePrefix: req.Nickname,
			UserIDs:        req.UserIds,
			Email:          req.Email,
			RegionCode:     req.RegionCode,
			Phone:          req.Phone,
			Username:       req.Username,
			OrderBy:        int32(req.OrderBy),
			Descending:     req.Descending,
		},
	}
	f.Page = int(req.Page)
	f.PageSize = int(req.PageSize)
	f.Count = req.Count

	if req.CreatedAtStart != nil {
		t := req.CreatedAtStart.AsTime()
		f.CreatedAtStart = &t
	}
	if req.CreatedAtEnd != nil {
		t := req.CreatedAtEnd.AsTime()
		f.CreatedAtEnd = &t
	}
	if req.LastLoginAtStart != nil {
		t := req.LastLoginAtStart.AsTime()
		f.LastLoginAtStart = &t
	}
	if req.LastLoginAtEnd != nil {
		t := req.LastLoginAtEnd.AsTime()
		f.LastLoginAtEnd = &t
	}

	users, total, err := dal.ListUsersPaged(ctx, s.db, f)
	if err != nil {
		return nil, err
	}

	pbUsers := make([]*pb.User, len(users))
	for i, u := range users {
		pbUsers[i] = common.ConvertUser(u)
	}

	var totalPages int32
	if req.Count {
		pp := f.Normalize()
		totalPages = int32((total + int64(pp.PageSize) - 1) / int64(pp.PageSize))
	}

	return &pb.ListUsersPagedResponse{
		Users:      pbUsers,
		Total:      total,
		TotalPages: totalPages,
	}, nil
}

// DisableUser disables or enables a user (admin). Disabling also revokes all
// active sessions for the user so they cannot keep using the system on a
// previously issued token. Re-enabling does not auto-restore sessions — the
// user must sign in again.
func (s *Service) DisableUser(ctx context.Context, req *pb.DisableUserRequest) (*pb.User, error) {
	user, err := dal.GetUserByID(ctx, s.db, req.UserId)
	if err != nil {
		return nil, err
	}
	if req.Disable {
		user.Status = int32(pb.UserStatus_USER_STATUS_DISABLED)
	} else {
		user.Status = int32(pb.UserStatus_USER_STATUS_ACTIVE)
	}
	if err := dal.UpdateUser(ctx, s.db, user); err != nil {
		return nil, err
	}
	if req.Disable && s.revoker != nil {
		if err := s.revoker.RevokeAllByUserID(ctx, user.ID); err != nil {
			return nil, err
		}
	}
	return common.ConvertUser(user), nil
}

// GetLoginLogs returns login logs (admin).
func (s *Service) GetLoginLogs(ctx context.Context, req *pb.GetLoginLogsRequest) (*pb.GetLoginLogsResponse, error) {
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	logs, nextCursor, err := dal.ListLoginLogs(ctx, s.db, req.UserId, int32(req.Provider), req.Cursor, pageSize)
	if err != nil {
		return nil, err
	}
	pbLogs := make([]*pb.LoginLog, len(logs))
	for i, l := range logs {
		// The log row stores the raw UA but no os/browser columns — derive
		// them on read so old rows parse under future rules too.
		osName, browser, _ := clientinfo.ParseUA(l.UserAgent)
		pbLogs[i] = &pb.LoginLog{
			Id:         l.ID,
			Provider:   pb.IdentityProvider(l.Provider),
			Action:     pb.LoginAction(l.Action),
			Success:    l.Success,
			FailReason: l.FailReason,
			Ip:         l.IP,
			Method:     pb.LoginMethod(l.Method),
			DeviceType: pb.DeviceType(l.DeviceType),
			Os:         osName,
			Browser:    browser,
			CreatedAt:  timestamppb.New(l.CreatedAt),
		}
		if l.UserID != nil {
			pbLogs[i].UserId = *l.UserID
		}
	}
	return &pb.GetLoginLogsResponse{Logs: pbLogs, NextCursor: nextCursor}, nil
}

// ListIdentities returns all identities for the current user.
func (s *Service) ListIdentities(ctx context.Context, req *pb.ListIdentitiesRequest) (*pb.ListIdentitiesResponse, error) {
	idents, err := dal.ListIdentitiesByUserID(ctx, s.db, req.GetUserId())
	if err != nil {
		return nil, err
	}
	pbIdents := make([]*pb.Identity, len(idents))
	for i, id := range idents {
		pbIdents[i] = &pb.Identity{
			Id:          id.ID,
			Provider:    pb.IdentityProvider(id.Provider),
			ProviderUid: id.ProviderUID,
			Verified:    id.Verified,
			CreatedAt:   timestamppb.New(id.CreatedAt),
		}
	}
	return &pb.ListIdentitiesResponse{Identities: pbIdents}, nil
}

// BindIdentity binds a new EMAIL or PHONE identity to the current user after
// verifying a BIND-purpose code. Provider UID is derived from the request:
// EMAIL → req.Email; PHONE → "<region_code>|<phone>". OAuth providers cannot
// be bound through this RPC — they only arrive via the OAuth callback flow.
func (s *Service) BindIdentity(ctx context.Context, req *pb.BindIdentityRequest) (*pb.Identity, error) {
	if s.captcha == nil {
		return nil, xcodes.ErrInternal.New("captcha not configured")
	}
	if req.UserId <= 0 {
		return nil, xcodes.ErrBadRequest.New("user_id is required")
	}
	if req.Provider != pb.IdentityProvider_IDENTITY_PROVIDER_EMAIL &&
		req.Provider != pb.IdentityProvider_IDENTITY_PROVIDER_PHONE {
		return nil, xcodes.ErrBadRequest.New("only EMAIL or PHONE providers can be bound")
	}

	var (
		targetKey string
		channel   pb.VerificationChannel
	)
	if req.Provider == pb.IdentityProvider_IDENTITY_PROVIDER_EMAIL {
		if req.Email == "" {
			return nil, xcodes.ErrBadRequest.New("email is required for EMAIL provider")
		}
		targetKey = req.Email
		channel = pb.VerificationChannel_VERIFICATION_CHANNEL_EMAIL
	} else {
		rc := phoneutil.NormalizeRegionCode(req.RegionCode)
		p := phoneutil.NormalizePhone(req.Phone)
		if rc == "" || p == "" {
			return nil, xcodes.ErrBadRequest.New("region_code and phone are required for PHONE provider")
		}
		targetKey = phoneutil.CaptchaKey(rc, p)
		channel = pb.VerificationChannel_VERIFICATION_CHANNEL_SMS
	}

	codeResult, err := s.captcha.Verify(ctx, targetKey, req.Code,
		strconv.Itoa(int(pb.VerificationPurpose_VERIFICATION_PURPOSE_BIND)),
		strconv.Itoa(int(channel)),
	)
	if err != nil {
		return nil, xcodes.ErrBadRequest.Wrap(err)
	}
	if !codeResult.Matched {
		return nil, xcodes.ErrBadRequest.New("invalid verification code")
	}

	// Reject duplicates: another user may already own this identity.
	existing, err := dal.GetIdentityByProviderUID(ctx, s.db, int32(req.Provider), targetKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, xcodes.ErrIdentityExists.New()
	}

	// Confirm the caller exists before creating a dangling identity row.
	// There is no FK constraint (per project convention), so this pre-check is
	// the only thing preventing a request with a forged user_id from leaving
	// orphan rows in user_identities.
	if _, err := dal.GetUserByID(ctx, s.db, req.UserId); err != nil {
		return nil, err
	}

	ident := &models.UserIdentity{
		UserID:      req.UserId,
		Provider:    int32(req.Provider),
		ProviderUID: targetKey,
		Verified:    true,
	}
	if req.Password != "" {
		hash, hashErr := credentials.HashPassword(req.Password)
		if hashErr != nil {
			return nil, xcodes.ErrInternal.Wrap(hashErr)
		}
		ident.Credentials = hash
	}
	if err := dal.CreateIdentity(ctx, s.db, ident); err != nil {
		return nil, err
	}

	// Backfill user.Email / user.Phone / RegionCode when binding the first
	// identity of that kind — convenient for legacy accounts that were
	// created without contact info.
	user, err := dal.GetUserByID(ctx, s.db, req.UserId)
	if err != nil {
		return nil, err
	}
	updated := false
	if req.Provider == pb.IdentityProvider_IDENTITY_PROVIDER_EMAIL && user.Email == nil {
		user.Email = ptr.Ref(req.Email)
		updated = true
	} else if req.Provider == pb.IdentityProvider_IDENTITY_PROVIDER_PHONE && user.Phone == nil {
		user.RegionCode = phoneutil.NormalizeRegionCode(req.RegionCode)
		user.Phone = ptr.Ref(phoneutil.NormalizePhone(req.Phone))
		updated = true
	}
	if updated {
		if err := dal.UpdateUser(ctx, s.db, user); err != nil {
			return nil, err
		}
	}

	return &pb.Identity{
		Id:          ident.ID,
		Provider:    req.Provider,
		ProviderUid: ident.ProviderUID,
		Verified:    ident.Verified,
		CreatedAt:   timestamppb.New(ident.CreatedAt),
	}, nil
}

// UnbindIdentity removes one of the caller's own identities. The caller must
// first run SendVerificationCode against the identity's target with the
// matching verify purpose (VERIFY_EMAIL for EMAIL, VERIFY_PHONE for PHONE)
// and pass the resulting code here. The service refuses when identity.user_id
// != req.user_id (defense against cross-user tampering now that the request
// carries the caller's user_id). Deleting the last credential-bearing
// identity is refused — otherwise the user could lock themselves out.
func (s *Service) UnbindIdentity(ctx context.Context, req *pb.UnbindIdentityRequest) (*emptypb.Empty, error) {
	if s.captcha == nil {
		return nil, xcodes.ErrInternal.New("captcha not configured")
	}
	if req.UserId <= 0 {
		return nil, xcodes.ErrBadRequest.New("user_id is required")
	}
	ident, err := dal.GetIdentityByID(ctx, s.db, req.IdentityId)
	if err != nil {
		return nil, err
	}
	if ident == nil {
		return nil, xcodes.ErrIdentityNotFound.New()
	}
	// Ownership check — the proto now requires user_id precisely so this
	// guard exists. Without it, any logged-in user who learned an identity_id
	// could attempt to delete another user's identity.
	if ident.UserID != req.UserId {
		return nil, xcodes.ErrForbidden.New("identity does not belong to the caller")
	}

	// Determine channel + purpose from the identity provider.
	var (
		channel pb.VerificationChannel
		purpose pb.VerificationPurpose
	)
	switch pb.IdentityProvider(ident.Provider) {
	case pb.IdentityProvider_IDENTITY_PROVIDER_EMAIL:
		channel = pb.VerificationChannel_VERIFICATION_CHANNEL_EMAIL
		purpose = pb.VerificationPurpose_VERIFICATION_PURPOSE_VERIFY_EMAIL
	case pb.IdentityProvider_IDENTITY_PROVIDER_PHONE:
		channel = pb.VerificationChannel_VERIFICATION_CHANNEL_SMS
		purpose = pb.VerificationPurpose_VERIFICATION_PURPOSE_VERIFY_PHONE
	default:
		return nil, xcodes.ErrBadRequest.New("only EMAIL or PHONE identities can be unbound")
	}

	codeResult, err := s.captcha.Verify(ctx, ident.ProviderUID, req.Code,
		strconv.Itoa(int(purpose)),
		strconv.Itoa(int(channel)),
	)
	if err != nil {
		return nil, xcodes.ErrBadRequest.Wrap(err)
	}
	if !codeResult.Matched {
		return nil, xcodes.ErrBadRequest.New("invalid verification code")
	}

	// Refuse to remove the last credential-bearing identity — that would
	// lock the user out of password login.
	idents, err := dal.ListIdentitiesByUserID(ctx, s.db, ident.UserID)
	if err != nil {
		return nil, err
	}
	credentialCount := 0
	for _, id := range idents {
		if id.Credentials != "" {
			credentialCount++
		}
	}
	if ident.Credentials != "" && credentialCount <= 1 {
		return nil, xcodes.ErrBadRequest.New("cannot remove the last credential identity")
	}

	if err := dal.DeleteIdentity(ctx, s.db, ident.ID); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}
