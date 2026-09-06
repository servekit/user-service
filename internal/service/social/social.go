// Package social implements the OAuth redirect login flow: GetOAuthURL
// issues state, SocialLogin consumes it, and the helpers in this package
// enforce the per-provider return_to allowlist and PKCE threading.
package social

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	pb "github.com/servekit/api/gen/go/user/v1"
	gidservice "github.com/servekit/gid-service/pkg"
	"github.com/servekit/user-service/internal/identity"
	"github.com/servekit/user-service/internal/identity/tencent/mini"
	common "github.com/servekit/user-service/internal/service/common"
	userstore "github.com/servekit/user-service/internal/service/session"
	"github.com/servekit/user-service/internal/store/dal"
	"github.com/servekit/user-service/internal/store/models"
	phoneutil "github.com/servekit/user-service/internal/utils/phone"
	"github.com/servekit/user-service/pkg/clientinfo"
	"github.com/servekit/user-service/pkg/config"
	"github.com/servekit/user-service/pkg/xcodes"

	"github.com/servekit/go-common/ptr"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

// oauthStateTTL is how long a GetOAuthURL-issued state stays valid. The OAuth
// redirect flow normally completes in seconds; 10 minutes is generous enough
// for user interaction but short enough to limit replay window.
const oauthStateTTL = 10 * time.Minute

// oauthStateEntry is the Redis value for an outstanding OAuth state. It
// remembers everything the OAuth callback service needs after the provider
// hands control back: which provider the state was issued for, and where
// the browser should be sent after user-service mints a session.
type oauthStateEntry struct {
	Provider     string `json:"provider"`
	ReturnTo     string `json:"return_to"`
	CodeVerifier string `json:"code_verifier,omitempty"` // empty for providers that don't use PKCE (WeChat, MiniProgram)
	CreatedAt    int64  `json:"created_at"`
}

// Service handles social login RPCs (OAuth redirect + code-based login).
type Service struct {
	db              *gorm.DB
	sessionMgr      *userstore.Manager
	socialProviders map[pb.IdentityProvider]identity.SocialProvider
	gid             gidservice.Service
	rdb             *redis.Client
	oauth           *config.OAuthConfig
}

// New creates a new social Service. rdb is used for the OAuth state store
// (CSRF protection on the GetOAuthURL → SocialLogin redirect flow); oauth
// supplies the per-provider redirect URL allowlist.
//
// Returns an error if any configured provider block has a malformed
// RedirectURL — the operator-facing "redirect_uri mismatch" errors from OAuth
// providers are otherwise opaque and surface only at first login.
//
// The returned warnings slice surfaces non-fatal operator concerns (e.g.
// AllowArbitraryRedirectURLs=true on a provider) up to cmd/server, which logs
// them at startup. Library code does not log directly per CLAUDE.md.
func New(
	db *gorm.DB,
	sessionMgr *userstore.Manager,
	socialProviders map[pb.IdentityProvider]identity.SocialProvider,
	gid gidservice.Service,
	rdb *redis.Client,
	oauth *config.OAuthConfig,
) (*Service, []string, error) {
	errs, warnings := validateOAuthConfig(oauth)
	if len(errs) > 0 {
		return nil, nil, xcodes.ErrInternal.New(fmt.Sprintf("oauth config: %s", strings.Join(errs, "; ")))
	}
	return &Service{
		db:              db,
		sessionMgr:      sessionMgr,
		socialProviders: socialProviders,
		gid:             gid,
		rdb:             rdb,
		oauth:           oauth,
	}, warnings, nil
}

// providerRedirectURL returns the FIXED redirect URL registered with the
// OAuth provider (cfg.OAuth.{provider}.RedirectURL). This is the URL the
// provider 302's the browser to with code+state; it belongs to the OAuth
// callback service, NOT to any individual business.
func providerRedirectURL(oauth *config.OAuthConfig, provider pb.IdentityProvider) (string, error) {
	if oauth == nil {
		return "", xcodes.ErrBadRequest.New("oauth config missing")
	}
	switch provider {
	case pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB:
		if oauth.GitHub == nil {
			return "", xcodes.ErrBadRequest.New("github oauth config missing")
		}
		return oauth.GitHub.RedirectURL, nil
	case pb.IdentityProvider_IDENTITY_PROVIDER_GOOGLE:
		if oauth.Google == nil {
			return "", xcodes.ErrBadRequest.New("google oauth config missing")
		}
		return oauth.Google.RedirectURL, nil
	case pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT:
		if oauth.WeChat == nil {
			return "", xcodes.ErrBadRequest.New("wechat oauth config missing")
		}
		return oauth.WeChat.RedirectURL, nil
	case pb.IdentityProvider_IDENTITY_PROVIDER_APPLE:
		if oauth.Apple == nil {
			return "", xcodes.ErrBadRequest.New("apple oauth config missing")
		}
		return oauth.Apple.RedirectURL, nil
	default:
		return "", xcodes.ErrBadRequest.New(fmt.Sprintf("provider %s does not use redirect-based OAuth", provider))
	}
}

// validateReturnTo enforces the per-provider allowlist for business return
// URLs. Empty return_to is always allowed (means "no redirect needed").
// Default-deny: empty allowlist + non-empty return_to is rejected. Operators
// who need the legacy allow-any behavior (dev/staging only) set
// AllowArbitraryRedirectURLs=true, which is surfaced as a startup warning.
func validateReturnTo(oauth *config.OAuthConfig, provider pb.IdentityProvider, returnTo string) error {
	if err := validateReturnToURL(returnTo); err != nil {
		return err
	}
	if returnTo == "" {
		return nil
	}
	if oauth == nil {
		// Defense-in-depth: no OAuth config at all means no allowlist could
		// have been declared. Default-deny rather than silently allow — a
		// future caller that constructs the service without cfg.OAuth (e.g.
		// an in-process module that nonetheless calls GetOAuthURL) would
		// otherwise bypass the entire return_to check.
		return xcodes.ErrBadRequest.New("oauth config is nil; cannot validate return_to")
	}
	var allowed []string
	var allowArbitrary bool
	switch provider {
	case pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB:
		if oauth.GitHub == nil {
			return nil
		}
		allowed = oauth.GitHub.AllowedRedirectURLs
		allowArbitrary = oauth.GitHub.AllowArbitraryRedirectURLs
	case pb.IdentityProvider_IDENTITY_PROVIDER_GOOGLE:
		if oauth.Google == nil {
			return nil
		}
		allowed = oauth.Google.AllowedRedirectURLs
		allowArbitrary = oauth.Google.AllowArbitraryRedirectURLs
	case pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT:
		if oauth.WeChat == nil {
			return nil
		}
		allowed = oauth.WeChat.AllowedRedirectURLs
		allowArbitrary = oauth.WeChat.AllowArbitraryRedirectURLs
	case pb.IdentityProvider_IDENTITY_PROVIDER_APPLE:
		if oauth.Apple == nil {
			return nil
		}
		allowed = oauth.Apple.AllowedRedirectURLs
		allowArbitrary = oauth.Apple.AllowArbitraryRedirectURLs
	default:
		return nil
	}
	if allowArbitrary {
		return nil
	}
	if len(allowed) == 0 {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to %q is not in the allowlist (allowlist empty, AllowArbitraryRedirectURLs=false) for provider %s", returnTo, provider))
	}
	for _, a := range allowed {
		if a == returnTo {
			return nil
		}
	}
	return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to %q is not in the allowlist for provider %s", returnTo, provider))
}

// dangerousReturnToSchemes are URL schemes that can execute code or load
// local resources when used as a 302 target — rejected outright regardless
// of allowlist membership. Comparison is case-insensitive.
//
// This is a DENYLIST, not an allowlist — mobile deep link schemes
// ("myapp://", "com.example.app://") intentionally pass, since they're how
// iOS/Android apps receive OAuth callbacks. Operators must list them
// explicitly in AllowedRedirectURLs.
var dangerousReturnToSchemes = map[string]bool{
	"javascript": true,
	"data":       true,
	"file":       true,
	"vbscript":   true,
	"about":      true,
	"blob":       true,
	"intent":     true, // Android intent:// — can launch arbitrary components
	"content":    true, // Android content:// — exposes content provider data
}

// validateReturnToURL checks the URL itself is safe to be a 302 target,
// regardless of allowlist. Defense-in-depth:
//   - Rejects CR/LF — response-splitting if the value lands in Location header.
//   - Rejects known-dangerous schemes (javascript/data/file/intent/...) — XSS.
//   - Rejects userinfo — phishing pattern (https://victim@evil.com).
//   - Rejects backslash — URL parser confusion (Chrome vs Go net/url).
//   - Rejects relative URLs / fragment-only — BFF must resolve.
//   - Rejects empty scheme — same as relative.
//
// Empty returnTo is allowed (means "no redirect needed" — module mode).
//
// This check runs BEFORE the allowlist check, so it applies even when
// AllowArbitraryRedirectURLs=true. Stops XSS via return_to even when the
// escape hatch is on.
func validateReturnToURL(returnTo string) error {
	if returnTo == "" {
		return nil
	}
	if strings.ContainsAny(returnTo, "\r\n") {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to contains CR/LF (response-splitting risk): %q", returnTo))
	}
	if strings.Contains(returnTo, "\\") {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to contains backslash (URL parser confusion risk): %q", returnTo))
	}
	u, err := url.Parse(returnTo)
	if err != nil {
		return xcodes.ErrBadRequest.Wrap(err)
	}
	if u.Scheme == "" {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to must have a scheme (relative URLs not supported — BFF must resolve): %q", returnTo))
	}
	if dangerousReturnToSchemes[strings.ToLower(u.Scheme)] {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to uses blocked scheme %q: %q", u.Scheme, returnTo))
	}
	if u.User != nil {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to must not contain userinfo: %q", returnTo))
	}
	if u.Host == "" {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("return_to must have a host (absolute URL with authority required): %q", returnTo))
	}
	return nil
}

// issueState stores a server-issued OAuth state in Redis bound to (provider,
// returnTo, codeVerifier). Returns redis.Nil if the state was already taken
// (extremely unlikely with UUID v4, but the NX guard keeps it correct).
//
// codeVerifier is the PKCE code_verifier generated by GetOAuthURL; it is
// threaded back to the provider's ExchangeCode via SocialLogin. Empty for
// flows / providers that don't use PKCE.
func (s *Service) issueState(ctx context.Context, provider pb.IdentityProvider, returnTo, codeVerifier string) (string, error) {
	state := uuid.New().String()
	entry := oauthStateEntry{
		Provider:     provider.String(),
		ReturnTo:     returnTo,
		CodeVerifier: codeVerifier,
		CreatedAt:    time.Now().Unix(),
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return "", xcodes.ErrInternal.Wrap(err)
	}
	key := "oauth:state:" + state
	ok, err := s.rdb.SetNX(ctx, key, payload, oauthStateTTL).Result()
	if err != nil {
		return "", xcodes.ErrInternal.Wrap(err)
	}
	if !ok {
		return "", xcodes.ErrInternal.New("state collision, please retry")
	}
	return state, nil
}

// consumeState atomically reads and deletes the state. Returns the stored
// entry on hit, redis.Nil when the state is missing / expired / already used.
func (s *Service) consumeState(ctx context.Context, state string) (*oauthStateEntry, error) {
	key := "oauth:state:" + state
	// GETDEL atomically reads and removes — one-time use, no replay.
	payload, err := s.rdb.GetDel(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	var entry oauthStateEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &entry, nil
}

// GetOAuthURL returns the OAuth authorization URL (only for redirect-based providers).
//
// The URL embeds the FIXED redirect URL registered at the provider
// (cfg.OAuth.{provider}.RedirectURL — owned by the OAuth callback service).
// The caller only supplies return_to: where the callback service should
// send the browser AFTER user-service has minted a session. return_to is
// recorded against the state so SocialLogin / BindOAuthIdentity can surface
// it back to the callback service in their responses.
func (s *Service) GetOAuthURL(ctx context.Context, req *pb.GetOAuthURLRequest) (*pb.GetOAuthURLResponse, error) {
	prov, ok := s.socialProviders[req.Provider]
	if !ok {
		return nil, xcodes.ErrBadRequest.New("unsupported provider")
	}
	redirectProv, ok := prov.(identity.RedirectProvider)
	if !ok {
		return nil, xcodes.ErrBadRequest.New("provider does not support redirect-based login")
	}
	if err := validateReturnTo(s.oauth, req.Provider, req.ReturnTo); err != nil {
		return nil, xcodes.ErrBadRequest.Wrap(err)
	}

	// Fixed provider-side redirect — comes from config, not the request.
	providerRedirect, err := providerRedirectURL(s.oauth, req.Provider)
	if err != nil {
		return nil, xcodes.ErrBadRequest.Wrap(err)
	}

	// Generate PKCE pair regardless of provider — providers that don't use
	// PKCE simply ignore the challenge. The verifier is stored in the state
	// entry and threaded back through SocialLogin.
	verifier, challenge, err := generatePKCEPair()
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	state := req.State
	if state == "" {
		// Server-issued state (UUID v4) — bound to (provider, return_to,
		// code_verifier) and stored in Redis so SocialLogin can confirm we
		// issued it.
		generated, err := s.issueState(ctx, req.Provider, req.ReturnTo, verifier)
		if err != nil {
			return nil, err
		}
		state = generated
	} else {
		// Caller-supplied state (e.g. PKCE flows). Still record it server-side
		// so SocialLogin can reject state values we did not see at GetOAuthURL.
		if err := s.storeCallerState(ctx, state, req.Provider, req.ReturnTo, verifier); err != nil {
			return nil, err
		}
	}

	authURL, err := redirectProv.GetAuthURL(ctx, providerRedirect, state, challenge)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &pb.GetOAuthURLResponse{Url: authURL, State: state}, nil
}

// storeCallerState records a caller-supplied state in Redis with the same TTL
// and payload shape as issueState. Used when the caller brings their own state
// (e.g. PKCE-style state bound to the BFF's own cookie). The caller's state is
// used as-is for the Redis key. Rejects if the key already exists — prevents
// an attacker who learned the state UUID from overwriting its return_to.
//
// codeVerifier is the PKCE code_verifier (when GetOAuthURL generated one for
// this flow); empty for providers / flows that don't use PKCE.
func (s *Service) storeCallerState(ctx context.Context, state string, provider pb.IdentityProvider, returnTo, codeVerifier string) error {
	entry := oauthStateEntry{
		Provider:     provider.String(),
		ReturnTo:     returnTo,
		CodeVerifier: codeVerifier,
		CreatedAt:    time.Now().Unix(),
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	key := "oauth:state:" + state
	ok, err := s.rdb.SetNX(ctx, key, payload, oauthStateTTL).Result()
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	if !ok {
		return xcodes.ErrBadRequest.New("state already exists; generate a fresh state value")
	}
	return nil
}

// BindOAuthIdentity attaches an OAuth provider identity (GitHub, Google,
// WeChat web, Apple) to an already-authenticated user — the "connect to
// GitHub" / "绑定微信" flow. Unlike SocialLogin, this does NOT create a
// session: the caller is already logged in. The state Redis entry issued by
// GetOAuthURL must be present and match the provider.
//
// Reuse rules:
//   - If the OAuth UID is already linked to req.UserId → no-op, returns the
//     existing Identity (idempotent bind).
//   - If the OAuth UID is already linked to ANOTHER user → ErrIdentityExists.
//   - Otherwise → create a new identity owned by req.UserId.
//
// As a convenience, the user's denormalized Email / AvatarURL are backfilled
// from the OAuth result when they are currently empty.
func (s *Service) BindOAuthIdentity(ctx context.Context, req *pb.BindOAuthIdentityRequest) (*pb.BindOAuthIdentityResponse, error) {
	if req.UserId <= 0 {
		return nil, xcodes.ErrBadRequest.New("user_id is required")
	}
	if req.Provider == pb.IdentityProvider_IDENTITY_PROVIDER_EMAIL ||
		req.Provider == pb.IdentityProvider_IDENTITY_PROVIDER_PHONE ||
		req.Provider == pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT_MINIPROGRAM {
		return nil, xcodes.ErrBadRequest.New("BindOAuthIdentity only supports redirect-based OAuth providers; use BindIdentity for email/phone")
	}

	// Consume state — same CSRF contract as SocialLogin.
	entry, err := s.consumeState(ctx, req.State)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, xcodes.ErrBadRequest.New("missing or expired OAuth state")
	}
	if entry.Provider != req.Provider.String() {
		return nil, xcodes.ErrBadRequest.New("OAuth state provider mismatch")
	}

	// Defense-in-depth: even though validateReturnTo ran at GetOAuthURL time,
	// the entry's return_to could be stale (operator tightened allowlist between
	// GetOAuthURL and BindOAuthIdentity) or corrupt (state-store bug).
	// Re-validate before use. BindOAuthIdentity doesn't surface return_to in
	// its response, but a state whose return_to would currently be rejected
	// must not be trusted as proof of GetOAuthURL origin.
	if err := validateReturnTo(s.oauth, req.Provider, entry.ReturnTo); err != nil {
		return nil, xcodes.ErrBadRequest.Wrap(err)
	}

	prov, ok := s.socialProviders[req.Provider]
	if !ok {
		return nil, xcodes.ErrBadRequest.New("unsupported provider")
	}
	result, err := prov.ExchangeCode(ctx, req.Code, entry.CodeVerifier)
	if err != nil {
		return nil, xcodes.ErrOAuthFailed.Wrap(err)
	}

	// Caller must exist.
	user, err := dal.GetUserByID(ctx, s.db, req.UserId)
	if err != nil {
		return nil, err
	}

	// Duplicate check across the whole user base.
	existing, err := dal.GetIdentityByProviderUID(ctx, s.db, int32(req.Provider), result.ProviderUID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.UserID != req.UserId {
			return nil, xcodes.ErrIdentityExists.New("OAuth account is already linked to another user")
		}
		// Idempotent re-bind: return the existing identity without re-creating.
		return &pb.BindOAuthIdentityResponse{Identity: &pb.Identity{
			Id:          existing.ID,
			Provider:    req.Provider,
			ProviderUid: existing.ProviderUID,
			Verified:    existing.Verified,
			CreatedAt:   timestamppb.New(existing.CreatedAt),
		}}, nil
	}

	ident := &models.UserIdentity{
		UserID:      req.UserId,
		Provider:    int32(req.Provider),
		ProviderUID: result.ProviderUID,
		Verified:    true,
	}
	if err := dal.CreateIdentity(ctx, s.db, ident); err != nil {
		return nil, err
	}

	// Backfill denormalized user fields when the OAuth result has them and
	// the user record doesn't. Helps old accounts that pre-date these fields
	// and avoids forcing a follow-up UpdateProfile call.
	updated := false
	if result.Email != "" && user.Email == nil {
		user.Email = ptr.Ref(result.Email)
		updated = true
	}
	if result.AvatarURL != "" && user.AvatarURL == "" {
		user.AvatarURL = result.AvatarURL
		updated = true
	}
	if result.Nickname != "" && user.Nickname == "" {
		user.Nickname = result.Nickname
		updated = true
	}
	if updated {
		if err := dal.UpdateUser(ctx, s.db, user); err != nil {
			return nil, err
		}
	}

	return &pb.BindOAuthIdentityResponse{Identity: &pb.Identity{
		Id:          ident.ID,
		Provider:    req.Provider,
		ProviderUid: ident.ProviderUID,
		Verified:    ident.Verified,
		CreatedAt:   timestamppb.New(ident.CreatedAt),
	}}, nil
}

// SocialLogin exchanges an OAuth code for user info and creates/finds the user.
// SocialLogin exchanges an OAuth code for user info and creates/finds the user.
// Only for redirect-based OAuth providers (GitHub, Google, WeChat web, Apple).
//
// The response carries return_to from the OAuth state record so the OAuth
// callback service knows where to 302 the browser with the new session.
func (s *Service) SocialLogin(ctx context.Context, req *pb.SocialLoginRequest) (*pb.LoginResponse, error) {
	// Validate state before doing anything else: it must exist in our Redis
	// store and the provider it was issued for must match this request.
	// Skipping this check lets a CSRF attacker inject their own code into a
	// victim's session.
	entry, err := s.consumeState(ctx, req.State)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, xcodes.ErrBadRequest.New("missing or expired OAuth state")
	}
	if entry.Provider != req.Provider.String() {
		return nil, xcodes.ErrBadRequest.New("OAuth state provider mismatch")
	}

	// Defense-in-depth: even though validateReturnTo ran at GetOAuthURL time,
	// the entry's return_to could be stale (operator tightened allowlist between
	// GetOAuthURL and SocialLogin) or corrupt (state-store bug). Re-validate
	// before use.
	if err := validateReturnTo(s.oauth, req.Provider, entry.ReturnTo); err != nil {
		return nil, xcodes.ErrBadRequest.Wrap(err)
	}

	prov, ok := s.socialProviders[req.Provider]
	if !ok {
		return nil, xcodes.ErrBadRequest.New("unsupported provider")
	}
	result, err := prov.ExchangeCode(ctx, req.Code, entry.CodeVerifier)
	if err != nil {
		return nil, xcodes.ErrOAuthFailed.Wrap(err)
	}

	ident, err := dal.GetIdentityByProviderUID(ctx, s.db, int32(req.Provider), result.ProviderUID)
	if err != nil {
		return nil, err
	}

	if ident == nil {
		resp, err := s.registerAndLogin(ctx, req.Provider, result)
		if err != nil {
			return nil, err
		}
		resp.ReturnTo = entry.ReturnTo
		return resp, nil
	}

	user, err := dal.GetUserByID(ctx, s.db, ident.UserID)
	if err != nil {
		return nil, err
	}
	if pb.UserStatus(user.Status) == pb.UserStatus_USER_STATUS_DISABLED {
		return nil, xcodes.ErrUserDisabled.New()
	}

	sessionID, err := s.createSession(ctx, user.ID, req.Provider, pb.LoginAction_LOGIN_ACTION_SOCIAL_LOGIN)
	if err != nil {
		return nil, err
	}

	return &pb.LoginResponse{
		User:      common.ConvertUser(user),
		SessionId: sessionID,
		ReturnTo:  entry.ReturnTo,
	}, nil
}

// MiniProgramLogin handles WeChat Mini Program login via wx.login() code.
func (s *Service) MiniProgramLogin(ctx context.Context, req *pb.MiniProgramLoginRequest) (*pb.LoginResponse, error) {
	prov, ok := s.socialProviders[pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT_MINIPROGRAM]
	if !ok {
		return nil, xcodes.ErrBadRequest.New("wechat miniprogram provider not configured")
	}

	// MiniProgram does not go through GetOAuthURL — no oauth:state entry, no
	// verifier to thread. Empty verifier → provider skips PKCE.
	result, err := prov.ExchangeCode(ctx, req.Code, "")
	if err != nil {
		return nil, xcodes.ErrOAuthFailed.Wrap(err)
	}

	// Merge frontend-collected profile.
	if result.Nickname == "" {
		result.Nickname = req.Nickname
	}
	if result.AvatarURL == "" {
		result.AvatarURL = req.AvatarUrl
	}

	mpProvider := pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT_MINIPROGRAM
	ident, err := dal.GetIdentityByProviderUID(ctx, s.db, int32(mpProvider), result.ProviderUID)
	if err != nil {
		return nil, err
	}

	if ident == nil {
		return s.registerAndLogin(ctx, mpProvider, result)
	}

	user, err := dal.GetUserByID(ctx, s.db, ident.UserID)
	if err != nil {
		return nil, err
	}
	if pb.UserStatus(user.Status) == pb.UserStatus_USER_STATUS_DISABLED {
		return nil, xcodes.ErrUserDisabled.New()
	}

	sessionID, err := s.createSession(ctx, user.ID, mpProvider, pb.LoginAction_LOGIN_ACTION_SOCIAL_LOGIN)
	if err != nil {
		return nil, err
	}

	return &pb.LoginResponse{
		User:      common.ConvertUser(user),
		SessionId: sessionID,
	}, nil
}

// MiniProgramPhoneLogin handles WeChat Mini Program phone number login.
func (s *Service) MiniProgramPhoneLogin(ctx context.Context, req *pb.MiniProgramPhoneLoginRequest) (*pb.LoginResponse, error) {
	prov, ok := s.socialProviders[pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT_MINIPROGRAM]
	if !ok {
		return nil, xcodes.ErrBadRequest.New("wechat miniprogram provider not configured")
	}

	mpProv, ok := prov.(*mini.Provider)
	if !ok {
		return nil, xcodes.ErrInternal.New("invalid miniprogram provider type")
	}

	// Step 1: Exchange login code for openid. MiniProgram skips PKCE —
	// verifier is empty.
	result, err := mpProv.ExchangeCode(ctx, req.LoginCode, "")
	if err != nil {
		return nil, xcodes.ErrOAuthFailed.Wrap(err)
	}

	// Step 2: Exchange phone code for ISO region code + local phone number.
	// WeChat's API returns a dialing code ("86"); the mini provider converts
	// it to ISO alpha-2 ("CN") before returning. Normalize anyway for
	// defense-in-depth against future provider changes.
	rawRC, rawPhone, err := mpProv.GetPhoneNumber(ctx, req.PhoneCode)
	if err != nil {
		return nil, xcodes.ErrOAuthFailed.Wrap(err)
	}
	regionCode := phoneutil.NormalizeRegionCode(rawRC)
	phoneNumber := phoneutil.NormalizePhone(rawPhone)
	result.RegionCode = regionCode
	result.Phone = phoneNumber

	// Merge frontend-collected profile.
	if result.Nickname == "" {
		result.Nickname = req.Nickname
	}
	if result.AvatarURL == "" {
		result.AvatarURL = req.AvatarUrl
	}

	// Step 3: Find user by phone identity. Phone identities are keyed as
	// "<region_code>|<phone>" — the same canonical form used for captcha.
	phoneUID := phoneutil.CaptchaKey(regionCode, phoneNumber)
	phoneIdent, err := dal.GetIdentityByProviderUID(ctx, s.db, int32(pb.IdentityProvider_IDENTITY_PROVIDER_PHONE), phoneUID)
	if err != nil {
		return nil, err
	}

	mpProvider := pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT_MINIPROGRAM

	if phoneIdent != nil {
		// Existing user — ensure miniprogram identity is linked.
		user, err := dal.GetUserByID(ctx, s.db, phoneIdent.UserID)
		if err != nil {
			return nil, err
		}
		if pb.UserStatus(user.Status) == pb.UserStatus_USER_STATUS_DISABLED {
			return nil, xcodes.ErrUserDisabled.New()
		}

		// Link miniprogram identity if not already bound.
		existing, err := dal.GetIdentityByProviderUID(ctx, s.db, int32(mpProvider), result.ProviderUID)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			if err := dal.CreateIdentity(ctx, s.db, &models.UserIdentity{
				UserID:      user.ID,
				Provider:    int32(mpProvider),
				ProviderUID: result.ProviderUID,
				Verified:    true,
			}); err != nil {
				return nil, err
			}
		}

		sessionID, err := s.createSession(ctx, user.ID, mpProvider, pb.LoginAction_LOGIN_ACTION_SOCIAL_LOGIN)
		if err != nil {
			return nil, err
		}

		return &pb.LoginResponse{
			User:      common.ConvertUser(user),
			SessionId: sessionID,
		}, nil
	}

	// New user — register with phone + miniprogram identities.
	sessionID := uuid.New().String()
	var user *models.User

	userID, err := gidservice.NextID(ctx, s.gid)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user = &models.User{
			Nickname:       common.FirstNonEmpty(result.Nickname, "user"),
			AvatarURL:      result.AvatarURL,
			RegionCode:     regionCode,
			Phone:          ptr.Ref(phoneNumber),
			Status:         int32(pb.UserStatus_USER_STATUS_ACTIVE),
			RegisterSource: int32(pb.IdentityProvider_IDENTITY_PROVIDER_PHONE),
		}
		user.ID = userID
		if err := dal.CreateUser(ctx, tx, user); err != nil {
			return err
		}

		// Create phone identity.
		if err := dal.CreateIdentity(ctx, tx, &models.UserIdentity{
			UserID: user.ID, Provider: int32(pb.IdentityProvider_IDENTITY_PROVIDER_PHONE), ProviderUID: phoneUID, Verified: true,
		}); err != nil {
			return err
		}

		// Create miniprogram identity.
		if err := dal.CreateIdentity(ctx, tx, &models.UserIdentity{
			UserID: user.ID, Provider: int32(mpProvider), ProviderUID: result.ProviderUID, Verified: true,
		}); err != nil {
			return err
		}

		now := time.Now()
		ci := clientinfo.FromCtx(ctx)
		if err := dal.CreateSession(ctx, tx, &models.UserSession{
			ID: sessionID, UserID: user.ID,
			IP: ci.IP, UserAgent: ci.UserAgent,
			DeviceType: common.LoginDeviceType(ci),
			OS:         ci.OS, Browser: ci.Browser,
			ExpiresAt: now.Add(s.sessionMgr.TTL()), LastActiveAt: now,
		}); err != nil {
			return err
		}

		uid := user.ID
		if err := dal.CreateLoginLog(ctx, tx, &models.UserLoginLog{
			UserID: &uid, Provider: int32(pb.IdentityProvider_IDENTITY_PROVIDER_PHONE), Action: int32(pb.LoginAction_LOGIN_ACTION_REGISTER), Success: true,
			IP: ci.IP, UserAgent: ci.UserAgent, DeviceType: common.LoginDeviceType(ci),
		}); err != nil {
			return err
		}
		if err := dal.UpdateUserLastLogin(ctx, tx, user.ID, ci.IP); err != nil {
			return err
		}

		return s.sessionMgr.Create(ctx, sessionID, &userstore.Data{
			UserID: user.ID, LoginMethod: mpProvider.String(), LoginAt: now,
			LoginIP: ci.IP, UserAgent: ci.UserAgent, OS: ci.OS, Browser: ci.Browser, Device: ci.Device,
		})
	}); err != nil {
		return nil, err
	}

	return &pb.LoginResponse{
		User:      common.ConvertUser(user),
		SessionId: sessionID,
		IsNew:     true,
	}, nil
}

// --- internal helpers ---

// validateOAuthConfig checks each configured provider's RedirectURL is a valid
// http(s) URL with a host. Catches the most common operator typos at startup
// (trailing slash, http vs https, wrong host) so the error message names the
// field rather than surfacing as an opaque "redirect_uri mismatch" from the
// OAuth provider at first login.
//
// "Configured" means the provider block carries real credentials (see
// (*OAuthXxxConfig).IsConfigured). A nil OR empty-creds provider block is
// treated as not-configured and skipped — so an embedder booting with a
// placeholder &OAuthGitHubConfig{} gets no error. A configured provider with a
// bad/empty RedirectURL still errors (fail-fast preserved). Does NOT verify
// the URL matches what's registered at the provider — user-service has no way
// to know that.
func validateOAuthConfig(cfg *config.OAuthConfig) (errs, warnings []string) {
	if cfg == nil {
		return nil, nil
	}
	type prov struct {
		name                string
		url                 string
		allowArbitrary      bool
		allowedRedirectURLs []string
	}
	configured := make([]prov, 0, 4)
	if cfg.GitHub.IsConfigured() {
		configured = append(configured, prov{"github", cfg.GitHub.RedirectURL, cfg.GitHub.AllowArbitraryRedirectURLs, cfg.GitHub.AllowedRedirectURLs})
	}
	if cfg.Google.IsConfigured() {
		configured = append(configured, prov{"google", cfg.Google.RedirectURL, cfg.Google.AllowArbitraryRedirectURLs, cfg.Google.AllowedRedirectURLs})
	}
	if cfg.WeChat.IsConfigured() {
		configured = append(configured, prov{"wechat", cfg.WeChat.RedirectURL, cfg.WeChat.AllowArbitraryRedirectURLs, cfg.WeChat.AllowedRedirectURLs})
	}
	if cfg.Apple.IsConfigured() {
		configured = append(configured, prov{"apple", cfg.Apple.RedirectURL, cfg.Apple.AllowArbitraryRedirectURLs, cfg.Apple.AllowedRedirectURLs})
	}
	for _, p := range configured {
		if err := validateRedirectURL(p.name, p.url); err != nil {
			errs = append(errs, err.Error())
		}
		for i, entry := range p.allowedRedirectURLs {
			if err := validateReturnToURL(entry); err != nil {
				errs = append(errs, fmt.Sprintf("%s.allowed_redirect_urls[%d]: %s", p.name, i, err))
			}
		}
		if p.allowArbitrary {
			warnings = append(warnings, fmt.Sprintf("oauth.%s.allow_arbitrary_redirect_urls is true — dev/staging only; prod must set AllowedRedirectURLs", p.name))
		}
	}
	return errs, warnings
}

// validateRedirectURL checks a single provider's RedirectURL.
func validateRedirectURL(provider, raw string) error {
	if raw == "" {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("%s.redirect_url is required (must exactly match the URL registered at the OAuth provider, including scheme and trailing slash)", provider))
	}
	u, err := url.Parse(raw)
	if err != nil {
		return xcodes.ErrBadRequest.Wrapf(err, "%s.redirect_url %q is not a valid URL", provider, raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("%s.redirect_url %q must use http or https scheme (got %q)", provider, raw, u.Scheme))
	}
	if u.Host == "" {
		return xcodes.ErrBadRequest.New(fmt.Sprintf("%s.redirect_url %q must have a host", provider, raw))
	}
	return nil
}

// registerAndLogin creates a new user from social login and returns a login response.
func (s *Service) registerAndLogin(ctx context.Context, providerID pb.IdentityProvider, result *identity.SocialResult) (*pb.LoginResponse, error) {
	sessionID := uuid.New().String()
	var user *models.User

	userID, err := gidservice.NextID(ctx, s.gid)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user = &models.User{
			Nickname:       common.FirstNonEmpty(result.Nickname, "user"),
			AvatarURL:      result.AvatarURL,
			Status:         int32(pb.UserStatus_USER_STATUS_ACTIVE),
			RegisterSource: int32(providerID),
		}
		user.ID = userID
		if result.Email != "" {
			user.Email = ptr.Ref(result.Email)
		}
		if err := dal.CreateUser(ctx, tx, user); err != nil {
			return err
		}

		if err := dal.CreateIdentity(ctx, tx, &models.UserIdentity{
			UserID:      user.ID,
			Provider:    int32(providerID),
			ProviderUID: result.ProviderUID,
			Verified:    true,
		}); err != nil {
			return err
		}

		now := time.Now()
		ci := clientinfo.FromCtx(ctx)
		if err := dal.CreateSession(ctx, tx, &models.UserSession{
			ID: sessionID, UserID: user.ID,
			IP: ci.IP, UserAgent: ci.UserAgent,
			DeviceType: common.LoginDeviceType(ci),
			OS:         ci.OS, Browser: ci.Browser,
			ExpiresAt: now.Add(s.sessionMgr.TTL()), LastActiveAt: now,
		}); err != nil {
			return err
		}

		uid := user.ID
		if err := dal.CreateLoginLog(ctx, tx, &models.UserLoginLog{
			UserID: &uid, Provider: int32(providerID), Action: int32(pb.LoginAction_LOGIN_ACTION_SOCIAL_REGISTER), Success: true,
			IP: ci.IP, UserAgent: ci.UserAgent, DeviceType: common.LoginDeviceType(ci),
		}); err != nil {
			return err
		}
		if err := dal.UpdateUserLastLogin(ctx, tx, user.ID, ci.IP); err != nil {
			return err
		}

		return s.sessionMgr.Create(ctx, sessionID, &userstore.Data{
			UserID: user.ID, LoginMethod: providerID.String(), LoginAt: now,
			LoginIP: ci.IP, UserAgent: ci.UserAgent, OS: ci.OS, Browser: ci.Browser, Device: ci.Device,
		})
	}); err != nil {
		return nil, err
	}

	return &pb.LoginResponse{
		User:      common.ConvertUser(user),
		SessionId: sessionID,
		IsNew:     true,
	}, nil
}

// createSession creates a session for an existing user within a transaction.
func (s *Service) createSession(ctx context.Context, userID int64, providerID pb.IdentityProvider, action pb.LoginAction) (string, error) {
	sessionID := uuid.New().String()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		ci := clientinfo.FromCtx(ctx)
		if err := dal.CreateSession(ctx, tx, &models.UserSession{
			ID: sessionID, UserID: userID,
			IP: ci.IP, UserAgent: ci.UserAgent,
			DeviceType: common.LoginDeviceType(ci),
			OS:         ci.OS, Browser: ci.Browser,
			ExpiresAt: now.Add(s.sessionMgr.TTL()), LastActiveAt: now,
		}); err != nil {
			return err
		}

		uid := userID
		if err := dal.CreateLoginLog(ctx, tx, &models.UserLoginLog{
			UserID: &uid, Provider: int32(providerID), Action: int32(action), Success: true,
			IP: ci.IP, UserAgent: ci.UserAgent, DeviceType: common.LoginDeviceType(ci),
		}); err != nil {
			return err
		}
		if err := dal.UpdateUserLastLogin(ctx, tx, userID, ci.IP); err != nil {
			return err
		}

		return s.sessionMgr.Create(ctx, sessionID, &userstore.Data{
			UserID: userID, LoginMethod: providerID.String(), LoginAt: now,
			LoginIP: ci.IP, UserAgent: ci.UserAgent, OS: ci.OS, Browser: ci.Browser, Device: ci.Device,
		})
	})
	if err != nil {
		return "", err
	}
	return sessionID, nil
}
