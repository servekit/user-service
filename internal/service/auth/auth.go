package auth

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	gidservice "github.com/servekit/gid-service/pkg"
	messagepb "github.com/servekit/message-service/gen/message/v1"
	messageservice "github.com/servekit/message-service/pkg"
	common "github.com/servekit/user-service/internal/service/common"

	pb "github.com/servekit/user-service/gen/user/v1"
	userstore "github.com/servekit/user-service/internal/service/session"
	"github.com/servekit/user-service/internal/store/dal"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/internal/utils/credentials"
	phoneutil "github.com/servekit/user-service/internal/utils/phone"
	"github.com/servekit/user-service/pkg/xcodes"

	"github.com/servekit/go-common/captcha"
	"github.com/servekit/go-common/ptr"
	"github.com/servekit/go-common/ratelimit"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
)

// Service handles authentication RPCs.
type Service struct {
	db           *gorm.DB
	sessionMgr   *userstore.Manager
	captcha      *captcha.Captcha
	loginLimiter ratelimit.Limiter
	codeLimiter  ratelimit.Limiter
	gid          gidservice.Service
	message      messageservice.Service
}

// New creates a new auth Service. loginLimiter caps login attempts per
// target; codeLimiter is a coarser cap on SendVerificationCode volume to
// catch attackers rotating targets when no per-IP limit is available.
// Both must be non-nil for production use.
func New(
	db *gorm.DB,
	sessionMgr *userstore.Manager,
	captchaSvc *captcha.Captcha,
	loginLimiter ratelimit.Limiter,
	codeLimiter ratelimit.Limiter,
	gid gidservice.Service,
	message messageservice.Service,
) *Service {
	return &Service{
		db:           db,
		sessionMgr:   sessionMgr,
		captcha:      captchaSvc,
		loginLimiter: loginLimiter,
		codeLimiter:  codeLimiter,
		gid:          gid,
		message:      message,
	}
}

// Register handles user registration.
func (s *Service) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	if req.Provider != pb.IdentityProvider_IDENTITY_PROVIDER_EMAIL && req.Provider != pb.IdentityProvider_IDENTITY_PROVIDER_PHONE {
		return nil, xcodes.ErrBadRequest.New("unsupported provider")
	}

	// Verify the code sent to target before proceeding
	channel := providerToChannel(req.Provider)
	var targetKey string
	if req.Provider == pb.IdentityProvider_IDENTITY_PROVIDER_EMAIL {
		if req.Email == "" {
			return nil, xcodes.ErrBadRequest.New("email is required for email registration")
		}
		targetKey = req.Email
	} else {
		rc := phoneutil.NormalizeRegionCode(req.RegionCode)
		p := phoneutil.NormalizePhone(req.Phone)
		if rc == "" || p == "" {
			return nil, xcodes.ErrBadRequest.New("region_code and phone are required for phone registration")
		}
		targetKey = phoneutil.CaptchaKey(rc, p)
	}
	codeResult, err := s.captcha.Verify(ctx, targetKey, req.Code,
		purposeKey(pb.VerificationPurpose_VERIFICATION_PURPOSE_REGISTER),
		channelKey(channel),
		captcha.WithCaptchaID(req.CaptchaId),
	)
	if err != nil {
		return nil, xcodes.ErrBadRequest.Wrap(err)
	}
	if !codeResult.Matched {
		return nil, xcodes.ErrBadRequest.New("invalid verification code")
	}

	if req.Password == "" {
		return nil, xcodes.ErrBadRequest.New("password is required")
	}
	hash, hashErr := credentials.HashPassword(req.Password)
	if hashErr != nil {
		return nil, xcodes.ErrInternal.Wrap(hashErr)
	}
	credentials := hash

	sessionID := uuid.New().String()
	var user *models.User

	userID, err := gidservice.NextID(ctx, s.gid)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Duplicate check inside transaction to prevent race condition
		existing, findErr := dal.GetIdentityByProviderUID(ctx, tx, int32(req.Provider), targetKey)
		if findErr != nil {
			return findErr
		}
		if existing != nil {
			return xcodes.ErrIdentityExists.New()
		}

		user = &models.User{
			Username:       common.PtrIfNonEmpty(req.Username),
			Nickname:       common.FirstNonEmpty(req.Nickname, "user"),
			Gender:         int32(req.Gender),
			Timezone:       common.FirstNonEmpty(req.Timezone, "Asia/Shanghai"),
			Locale:         common.FirstNonEmpty(req.Locale, "zh-CN"),
			Status:         int32(pb.UserStatus_USER_STATUS_ACTIVE),
			RegisterSource: int32(req.Provider),
		}
		user.ID = userID
		if req.Provider == pb.IdentityProvider_IDENTITY_PROVIDER_EMAIL {
			user.Email = ptr.Ref(req.Email)
		} else {
			user.RegionCode = phoneutil.NormalizeRegionCode(req.RegionCode)
			user.Phone = ptr.Ref(phoneutil.NormalizePhone(req.Phone))
		}
		if err := dal.CreateUser(ctx, tx, user); err != nil {
			return err
		}

		ident := &models.UserIdentity{
			UserID:      user.ID,
			Provider:    int32(req.Provider),
			ProviderUID: targetKey,
			Credentials: credentials,
			Verified:    true,
		}
		if err := dal.CreateIdentity(ctx, tx, ident); err != nil {
			return err
		}

		now := time.Now()
		sessData := &userstore.Data{UserID: user.ID, LoginMethod: req.Provider.String(), LoginAt: now}
		dbSession := &models.UserSession{
			ID: sessionID, UserID: user.ID,
			IP: sessData.LoginIP, UserAgent: sessData.UserAgent,
			OS: sessData.OS, Browser: sessData.Browser,
			ExpiresAt: now.Add(s.sessionMgr.TTL()), LastActiveAt: now,
		}
		if err := dal.CreateSession(ctx, tx, dbSession); err != nil {
			return err
		}

		uid := user.ID
		if err := dal.CreateLoginLog(ctx, tx, &models.UserLoginLog{
			UserID: &uid, Provider: int32(req.Provider), Action: int32(pb.LoginAction_LOGIN_ACTION_REGISTER), Success: true,
		}); err != nil {
			return err
		}
		if err := dal.UpdateUserLastLogin(ctx, tx, user.ID, ""); err != nil {
			return err
		}

		return s.sessionMgr.Create(ctx, sessionID, sessData)
	}); err != nil {
		return nil, err
	}

	return &pb.RegisterResponse{
		User:      common.ConvertUser(user),
		SessionId: sessionID,
	}, nil
}

// Login handles user login with method-based routing.
func (s *Service) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	lookupTarget, _ := resolveLoginTarget(req)

	// Rate limit by target. Fail CLOSED on limiter errors: a Redis outage
	// should block logins rather than silently lifting brute-force protection.
	if ok, err := s.loginLimiter.Allow(ctx, "global", lookupTarget); err != nil {
		return nil, xcodes.ErrServiceUnavailable.Wrap(err)
	} else if !ok {
		return nil, xcodes.ErrTooManyRequests.New("too many login attempts, please try again later")
	}

	provider := methodToProvider(req.Method)
	if provider == pb.IdentityProvider_IDENTITY_PROVIDER_UNSPECIFIED && req.Method != pb.LoginMethod_LOGIN_METHOD_USERNAME_PASSWORD {
		return nil, xcodes.ErrBadRequest.New("unsupported login method")
	}

	// Resolve identity and user based on login method
	var user *models.User
	var ident *models.UserIdentity

	switch req.Method {
	case pb.LoginMethod_LOGIN_METHOD_USERNAME_PASSWORD:
		// Username login: find user first, then find their credential identity
		var err error
		user, err = dal.GetUserByUsername(ctx, s.db, lookupTarget)
		if err != nil {
			return nil, err
		}
		idents, err := dal.ListIdentitiesByUserID(ctx, s.db, user.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range idents {
			if id.Credentials != "" {
				ident = id
				break
			}
		}
		if ident == nil {
			return nil, xcodes.ErrIdentityNotFound.New()
		}

	default:
		// Identity-based login: find by provider + target
		var err error
		ident, err = dal.GetIdentityByProviderUID(ctx, s.db, int32(provider), lookupTarget)
		if err != nil {
			return nil, err
		}
		if ident == nil {
			if req.Method == pb.LoginMethod_LOGIN_METHOD_PHONE_CODE || req.Method == pb.LoginMethod_LOGIN_METHOD_EMAIL_CODE {
				return s.autoRegister(ctx, req.Method, lookupTarget)
			}
			return nil, xcodes.ErrIdentityNotFound.New()
		}
	}

	// Verify credentials
	if err := s.verifyCredentials(ctx, req, ident); err != nil {
		// Increment failure counter for rate limiting. Fail CLOSED: when the
		// limiter is unavailable, refuse the login rather than risk bypassing
		// the per-target failure cap.
		if _, limiterErr := s.loginLimiter.Allow(ctx, "fail", lookupTarget); limiterErr != nil {
			return nil, xcodes.ErrServiceUnavailable.Wrap(limiterErr)
		}
		// Record failed login attempt
		userID := ident.UserID
		if logErr := dal.CreateLoginLog(ctx, s.db, &models.UserLoginLog{
			UserID: &userID, Provider: int32(provider), Action: int32(pb.LoginAction_LOGIN_ACTION_LOGIN),
			Success: false, FailReason: "wrong_password",
		}); logErr != nil {
			// Audit log failure should not mask auth error
		}
		return nil, err
	}

	// Load user if not already loaded (username login pre-loads user)
	if user == nil {
		var err error
		user, err = dal.GetUserByID(ctx, s.db, ident.UserID)
		if err != nil {
			return nil, err
		}
	}
	if pb.UserStatus(user.Status) == pb.UserStatus_USER_STATUS_DISABLED {
		return nil, xcodes.ErrUserDisabled.New()
	}

	// Create session + audit log + update last login
	sessionID := uuid.New().String()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		sessData := &userstore.Data{UserID: user.ID, LoginMethod: req.Method.String(), LoginAt: now}
		dbSession := &models.UserSession{
			ID: sessionID, UserID: user.ID,
			IP: sessData.LoginIP, UserAgent: sessData.UserAgent,
			OS: sessData.OS, Browser: sessData.Browser,
			ExpiresAt: now.Add(s.sessionMgr.TTL()), LastActiveAt: now,
		}
		if err := dal.CreateSession(ctx, tx, dbSession); err != nil {
			return err
		}

		uid := user.ID
		if err := dal.CreateLoginLog(ctx, tx, &models.UserLoginLog{
			UserID: &uid, Provider: int32(provider), Action: int32(pb.LoginAction_LOGIN_ACTION_LOGIN), Success: true,
		}); err != nil {
			return err
		}
		if err := dal.UpdateUserLastLogin(ctx, tx, user.ID, ""); err != nil {
			return err
		}

		return s.sessionMgr.Create(ctx, sessionID, sessData)
	}); err != nil {
		return nil, err
	}

	return &pb.LoginResponse{
		User:      common.ConvertUser(user),
		SessionId: sessionID,
	}, nil
}

// verifyCredentials verifies password or code based on login method.
func (s *Service) verifyCredentials(ctx context.Context, req *pb.LoginRequest, ident *models.UserIdentity) error {
	switch req.Method {
	case pb.LoginMethod_LOGIN_METHOD_EMAIL_PASSWORD,
		pb.LoginMethod_LOGIN_METHOD_PHONE_PASSWORD,
		pb.LoginMethod_LOGIN_METHOD_USERNAME_PASSWORD:
		if err := credentials.VerifyPassword(ident.Credentials, req.Password); err != nil {
			return xcodes.ErrPasswordWrong.New()
		}
		return nil

	case pb.LoginMethod_LOGIN_METHOD_EMAIL_CODE,
		pb.LoginMethod_LOGIN_METHOD_PHONE_CODE:
		_, captchaKey := resolveLoginTarget(req)
		if captchaKey == "" {
			return xcodes.ErrBadRequest.New("email or region_code+phone is required")
		}
		channel := methodToChannel(req.Method)
		codeResult, err := s.captcha.Verify(ctx, captchaKey, req.Code,
			purposeKey(pb.VerificationPurpose_VERIFICATION_PURPOSE_LOGIN),
			channelKey(channel),
			captcha.WithCaptchaID(req.CaptchaId),
		)
		if err != nil {
			return xcodes.ErrBadRequest.Wrap(err)
		}
		if !codeResult.Matched {
			return xcodes.ErrBadRequest.New("invalid verification code")
		}
		return nil

	default:
		return xcodes.ErrBadRequest.New("unsupported login method")
	}
}

// autoRegister creates a new user and identity for code-based login (phone/email code).
func (s *Service) autoRegister(ctx context.Context, method pb.LoginMethod, targetKey string) (*pb.LoginResponse, error) {
	provider := methodToProvider(method)
	sessionID := uuid.New().String()
	var user *models.User

	userID, err := gidservice.NextID(ctx, s.gid)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user = &models.User{
			Nickname:       "user",
			Status:         int32(pb.UserStatus_USER_STATUS_ACTIVE),
			RegisterSource: int32(provider),
		}
		user.ID = userID
		if provider == pb.IdentityProvider_IDENTITY_PROVIDER_PHONE {
			rc, p := splitPhoneKey(targetKey)
			user.RegionCode = rc
			user.Phone = ptr.Ref(p)
		} else {
			user.Email = ptr.Ref(targetKey)
		}
		if err := dal.CreateUser(ctx, tx, user); err != nil {
			return err
		}

		ident := &models.UserIdentity{
			UserID: user.ID, Provider: int32(provider), ProviderUID: targetKey, Verified: true,
		}
		if err := dal.CreateIdentity(ctx, tx, ident); err != nil {
			return err
		}

		now := time.Now()
		sessData := &userstore.Data{UserID: user.ID, LoginMethod: method.String(), LoginAt: now}
		dbSession := &models.UserSession{
			ID: sessionID, UserID: user.ID,
			ExpiresAt: now.Add(s.sessionMgr.TTL()), LastActiveAt: now,
		}
		if err := dal.CreateSession(ctx, tx, dbSession); err != nil {
			return err
		}

		uid := user.ID
		if err := dal.CreateLoginLog(ctx, tx, &models.UserLoginLog{
			UserID: &uid, Provider: int32(provider), Action: int32(pb.LoginAction_LOGIN_ACTION_REGISTER), Success: true,
		}); err != nil {
			return err
		}
		if err := dal.UpdateUserLastLogin(ctx, tx, user.ID, ""); err != nil {
			return err
		}

		return s.sessionMgr.Create(ctx, sessionID, sessData)
	}); err != nil {
		return nil, err
	}

	return &pb.LoginResponse{
		User:      common.ConvertUser(user),
		SessionId: sessionID,
		IsNew:     true,
	}, nil
}

// Logout handles user logout.
func (s *Service) Logout(ctx context.Context, req *pb.LogoutRequest) (*emptypb.Empty, error) {
	if req.SessionId != "" {
		// Pure read — we're about to revoke, so sliding the TTL window would be wasted work.
		data, err := s.sessionMgr.Get(ctx, req.SessionId)
		if err != nil {
			return nil, err
		}
		if err := s.revokeSession(ctx, req.SessionId, data.UserID); err != nil {
			return nil, err
		}
	}
	return &emptypb.Empty{}, nil
}

// SendVerificationCode sends a verification code.
func (s *Service) SendVerificationCode(ctx context.Context, req *pb.SendVerificationCodeRequest) (*pb.SendVerificationCodeResponse, error) {
	if err := validateDeliverySpec(req); err != nil {
		return nil, xcodes.ErrBadRequest.Wrap(err)
	}
	var key string
	if req.Email != "" {
		key = req.Email
	} else if req.RegionCode != "" && req.Phone != "" {
		key = phoneutil.CaptchaKey(phoneutil.NormalizeRegionCode(req.RegionCode), phoneutil.NormalizePhone(req.Phone))
	} else {
		return nil, xcodes.ErrBadRequest.New("email or region_code+phone is required")
	}
	// Global cap on code-sending volume — catches attackers rotating targets
	// when no per-IP limit is available. The captcha library separately
	// enforces a per-target cap via its RateRules; this is the floor that
	// limits total outbound SMS/email regardless of how the caller rotates.
	if s.codeLimiter != nil {
		if ok, err := s.codeLimiter.Allow(ctx, "send", "global"); err != nil {
			return nil, xcodes.ErrServiceUnavailable.Wrap(err)
		} else if !ok {
			return nil, xcodes.ErrTooManyRequests.New("too many verification code requests, please try again later")
		}
	}
	purpose := purposeKey(req.Purpose)
	channel := channelKey(req.Channel)
	captchaID, _, err := s.captcha.Generate(ctx, key, purpose, channel, captcha.WithSend(func(ctx context.Context, target, code, purpose, channel string) error {
		return s.deliverCode(ctx, req, target, code)
	}))
	if err != nil {
		return nil, xcodes.ErrBadRequest.Wrap(err)
	}
	return &pb.SendVerificationCodeResponse{CaptchaId: captchaID}, nil
}

// --- helpers ---

// resolveLoginTarget extracts the canonical lookup key + captcha key from a
// LoginRequest based on its method.
//
// - USERNAME_PASSWORD: lookupTarget = req.Username; captchaKey unused ("")
// - EMAIL_*:           lookupTarget = req.Email;    captchaKey = req.Email
// - PHONE_*:           lookupTarget = CaptchaKey(cc, phone); captchaKey same
//
// Returns ("", "") if the relevant fields are empty.
func resolveLoginTarget(req *pb.LoginRequest) (lookupTarget, captchaKey string) {
	switch req.Method {
	case pb.LoginMethod_LOGIN_METHOD_USERNAME_PASSWORD:
		return req.Username, ""
	case pb.LoginMethod_LOGIN_METHOD_EMAIL_PASSWORD, pb.LoginMethod_LOGIN_METHOD_EMAIL_CODE:
		return req.Email, req.Email
	case pb.LoginMethod_LOGIN_METHOD_PHONE_PASSWORD, pb.LoginMethod_LOGIN_METHOD_PHONE_CODE:
		rc := phoneutil.NormalizeRegionCode(req.RegionCode)
		p := phoneutil.NormalizePhone(req.Phone)
		key := phoneutil.CaptchaKey(rc, p)
		return key, key
	default:
		return "", ""
	}
}

// splitPhoneKey splits a "<cc>|<phone>" captcha key back into country code and phone.
// Returns ("", key) if the key doesn't contain "|".
func splitPhoneKey(key string) (cc, phone string) {
	idx := strings.Index(key, "|")
	if idx < 0 {
		return "", key
	}
	return key[:idx], key[idx+1:]
}

// methodToProvider maps a LoginMethod to its IdentityProvider enum.
func methodToProvider(m pb.LoginMethod) pb.IdentityProvider {
	switch m {
	case pb.LoginMethod_LOGIN_METHOD_EMAIL_PASSWORD, pb.LoginMethod_LOGIN_METHOD_EMAIL_CODE:
		return pb.IdentityProvider_IDENTITY_PROVIDER_EMAIL
	case pb.LoginMethod_LOGIN_METHOD_PHONE_PASSWORD, pb.LoginMethod_LOGIN_METHOD_PHONE_CODE:
		return pb.IdentityProvider_IDENTITY_PROVIDER_PHONE
	default:
		return pb.IdentityProvider_IDENTITY_PROVIDER_UNSPECIFIED
	}
}

// providerToChannel maps an IdentityProvider to a VerificationChannel.
func providerToChannel(provider pb.IdentityProvider) pb.VerificationChannel {
	if provider == pb.IdentityProvider_IDENTITY_PROVIDER_PHONE {
		return pb.VerificationChannel_VERIFICATION_CHANNEL_SMS
	}
	return pb.VerificationChannel_VERIFICATION_CHANNEL_EMAIL
}

// methodToChannel maps a LoginMethod to a VerificationChannel.
func methodToChannel(m pb.LoginMethod) pb.VerificationChannel {
	if m == pb.LoginMethod_LOGIN_METHOD_PHONE_CODE {
		return pb.VerificationChannel_VERIFICATION_CHANNEL_SMS
	}
	return pb.VerificationChannel_VERIFICATION_CHANNEL_EMAIL
}

// deliverCode routes a verification code to message-service based on req.Channel.
// For email: target is the email address. For SMS: target is a
// "<region_code>|<phone>" captcha key — we split it back to ISO region code
// and local phone, which is exactly the format message-service expects.
//
// All delivery content (template_id, code_param_key, content, subject, body)
// is provided by the caller in req — user-service has no built-in templates.
// The only substitution user-service performs is "{code}" → actual code.
//
// Returns a bad-request error if (purpose, channel) has no valid scene
// (e.g. VERIFY_PHONE on the EMAIL channel).
func (s *Service) deliverCode(ctx context.Context, req *pb.SendVerificationCodeRequest, target, code string) error {
	switch req.Channel {
	case pb.VerificationChannel_VERIFICATION_CHANNEL_EMAIL:
		scene, err := mapEmailScene(req.Purpose)
		if err != nil {
			return err
		}
		// {code} substitution applies to subject, body, and html_body. Missing
		// placeholder = silent no-op (caller's responsibility).
		emailReq := &messagepb.SendEmailRequest{
			To:       []*messagepb.EmailAddress{{Email: target}},
			Subject:  strings.ReplaceAll(req.EmailSubject, "{code}", code),
			Body:     strings.ReplaceAll(req.EmailBody, "{code}", code),
			Scene:    scene,
			SenderId: req.SenderId,
		}
		if req.EmailHtmlBody != "" {
			emailReq.HtmlBody = strings.ReplaceAll(req.EmailHtmlBody, "{code}", code)
		}
		_, err = s.message.SendEmail(ctx, emailReq)
		return err
	case pb.VerificationChannel_VERIFICATION_CHANNEL_SMS:
		scene, err := mapSmsScene(req.Purpose)
		if err != nil {
			return err
		}
		rc, phone := splitPhoneKey(target)
		regionCode := phoneutil.NormalizeRegionCode(rc)
		if regionCode == "" || phone == "" {
			return xcodes.ErrBadRequest.New("invalid phone captcha key")
		}
		smsReq := &messagepb.SendSMSRequest{
			RegionCode: regionCode,
			Phone:      phone,
			Scene:      scene,
			SenderId:   req.SenderId,
			SignName:   req.SignName,
		}
		// Path selection mirrors message-service: region decides. CN vendors
		// reject raw content (regulatory), so they need template+params;
		// international vendors split into raw-content and template-based —
		// pass through whichever the caller set, vendor picks.
		if regionCode == "CN" {
			paramKey := req.SmsCodeParamKey
			if paramKey == "" {
				paramKey = "code"
			}
			smsReq.TemplateId = req.SmsTemplateId
			smsReq.TemplateParams = map[string]string{paramKey: code}
		} else if req.SmsTemplateId != "" {
			// International with template (Byteplus / Tencent intl).
			paramKey := req.SmsCodeParamKey
			if paramKey == "" {
				paramKey = "code"
			}
			smsReq.TemplateId = req.SmsTemplateId
			smsReq.TemplateParams = map[string]string{paramKey: code}
		} else {
			// International with raw content (Aliyun intl / Twilio).
			// {code} substitution is the caller's convention; missing
			// placeholder means the code is silently dropped.
			smsReq.Content = strings.ReplaceAll(req.SmsContent, "{code}", code)
		}
		_, err = s.message.SendSMS(ctx, smsReq)
		return err
	default:
		return xcodes.ErrBadRequest.New("unsupported verification channel")
	}
}

// validateDeliverySpec enforces the channel- and region-specific delivery
// field requirements that protovalidate cannot express. Path selection
// mirrors message-service: destination region decides the SMS path.
//
//   - EMAIL:                  email_subject and email_body are both required.
//   - SMS + region_code=="CN": sms_template_id and sign_name are required
//     (domestic vendors reject raw content; signature is regulatory-mandated).
//   - SMS + other region_code: exactly one of sms_template_id or sms_content
//     is required. International vendors split into raw-content (Aliyun intl,
//     Twilio) and template-based (Byteplus, Tencent intl); caller picks based
//     on the vendor their route uses.
//
// {code} placeholder: sms_content / email_body / email_subject may use the
// literal "{code}" substring; user-service substitutes it with the actual
// code. Missing "{code}" is a silent no-op (the code is dropped). Caller's
// responsibility to include it.
func validateDeliverySpec(req *pb.SendVerificationCodeRequest) error {
	switch req.Channel {
	case pb.VerificationChannel_VERIFICATION_CHANNEL_EMAIL:
		if req.EmailSubject == "" || req.EmailBody == "" {
			return fmt.Errorf("email_subject and email_body are required for email channel")
		}
	case pb.VerificationChannel_VERIFICATION_CHANNEL_SMS:
		// SMS target is region_code+phone (proto CEL enforces target_exclusive).
		// When the caller has not set region_code yet the channel is SMS, the
		// target validation downstream will surface a clearer error — no-op here.
		if req.RegionCode == "" {
			return nil
		}
		if req.RegionCode == "CN" {
			if req.SmsTemplateId == "" {
				return fmt.Errorf("sms_template_id is required for CN (domestic) SMS")
			}
			if req.SignName == "" {
				return fmt.Errorf("sign_name is required for CN (domestic) SMS")
			}
		} else {
			hasContent := req.SmsContent != ""
			hasTemplate := req.SmsTemplateId != ""
			if hasContent == hasTemplate {
				return fmt.Errorf("international (non-CN) SMS requires exactly one of sms_content or sms_template_id")
			}
		}
	}
	return nil
}

// mapEmailScene maps a VerificationPurpose to the EmailScene used on the email
// channel. Returns an error for purposes that have no email equivalent
// (VERIFY_PHONE) or are unrecognized — no silent fallback.
func mapEmailScene(p pb.VerificationPurpose) (messagepb.EmailScene, error) {
	switch p {
	case pb.VerificationPurpose_VERIFICATION_PURPOSE_REGISTER:
		return messagepb.EmailScene_EMAIL_SCENE_REGISTER, nil
	case pb.VerificationPurpose_VERIFICATION_PURPOSE_LOGIN:
		return messagepb.EmailScene_EMAIL_SCENE_LOGIN_CODE, nil
	case pb.VerificationPurpose_VERIFICATION_PURPOSE_VERIFY_EMAIL:
		return messagepb.EmailScene_EMAIL_SCENE_VERIFY_EMAIL, nil
	case pb.VerificationPurpose_VERIFICATION_PURPOSE_PASSWORD_RESET:
		return messagepb.EmailScene_EMAIL_SCENE_FORGOT_PASSWORD, nil
	case pb.VerificationPurpose_VERIFICATION_PURPOSE_BIND:
		return messagepb.EmailScene_EMAIL_SCENE_BIND_ACCOUNT, nil
	case pb.VerificationPurpose_VERIFICATION_PURPOSE_VERIFY_PHONE:
		return 0, fmt.Errorf("verify_phone has no email scene")
	default:
		return 0, fmt.Errorf("unknown verification purpose: %v", p)
	}
}

// mapSmsScene maps a VerificationPurpose to the SmsScene used on the SMS channel.
// Returns an error for purposes that have no SMS equivalent (VERIFY_EMAIL) or
// are unrecognized — no silent fallback.
func mapSmsScene(p pb.VerificationPurpose) (messagepb.SmsScene, error) {
	switch p {
	case pb.VerificationPurpose_VERIFICATION_PURPOSE_REGISTER:
		return messagepb.SmsScene_SMS_SCENE_REGISTER, nil
	case pb.VerificationPurpose_VERIFICATION_PURPOSE_LOGIN:
		return messagepb.SmsScene_SMS_SCENE_LOGIN_CODE, nil
	case pb.VerificationPurpose_VERIFICATION_PURPOSE_VERIFY_PHONE:
		return messagepb.SmsScene_SMS_SCENE_VERIFY_PHONE, nil
	case pb.VerificationPurpose_VERIFICATION_PURPOSE_PASSWORD_RESET:
		return messagepb.SmsScene_SMS_SCENE_FORGOT_PASSWORD, nil
	case pb.VerificationPurpose_VERIFICATION_PURPOSE_BIND:
		return messagepb.SmsScene_SMS_SCENE_BIND_ACCOUNT, nil
	case pb.VerificationPurpose_VERIFICATION_PURPOSE_VERIFY_EMAIL:
		return 0, fmt.Errorf("verify_email has no sms scene")
	default:
		return 0, fmt.Errorf("unknown verification purpose: %v", p)
	}
}

// purposeKey returns the captcha purpose key for a VerificationPurpose.
// The numeric value is used directly — it is opaque to the captcha library
// (which only treats it as a Redis key component) and shorter than the
// textual name. The mapping is stable because proto enum values are stable.
func purposeKey(p pb.VerificationPurpose) string {
	return strconv.Itoa(int(p))
}

// channelKey returns the captcha channel key for a VerificationChannel.
// See purposeKey for why the numeric value is used directly.
func channelKey(c pb.VerificationChannel) string {
	return strconv.Itoa(int(c))
}

// revokeSession revokes a session in both DB and Redis within a DB transaction.
func (s *Service) revokeSession(ctx context.Context, sessionID string, userID int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := dal.RevokeSession(ctx, tx, sessionID); err != nil {
			return err
		}
		return s.sessionMgr.Revoke(ctx, sessionID, userID)
	})
}
