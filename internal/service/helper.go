package service

import (
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	gidservice "github.com/servekit/gid-service/pkg"
	gidconfig "github.com/servekit/gid-service/pkg/config"
	messageservice "github.com/servekit/message-service/pkg"
	messageconfig "github.com/servekit/message-service/pkg/config"
	messageoption "github.com/servekit/message-service/pkg/option"

	"github.com/servekit/go-common/captcha"
	"github.com/servekit/go-common/lifecycle"
	"github.com/servekit/go-common/ratelimit"

	pb "github.com/servekit/api/gen/go/user/v1"
	"github.com/servekit/user-service/internal/cache"
	"github.com/servekit/user-service/internal/identity"
	"github.com/servekit/user-service/internal/identity/apple"
	"github.com/servekit/user-service/internal/identity/github"
	"github.com/servekit/user-service/internal/identity/google"
	"github.com/servekit/user-service/internal/identity/tencent/mini"
	"github.com/servekit/user-service/internal/identity/tencent/wechat"
	authsvc "github.com/servekit/user-service/internal/service/auth"
	rbacsvc "github.com/servekit/user-service/internal/service/rbac"
	"github.com/servekit/user-service/internal/service/session"
	socialsvc "github.com/servekit/user-service/internal/service/social"
	usersvc "github.com/servekit/user-service/internal/service/user"
	"github.com/servekit/user-service/pkg/config"
	"github.com/servekit/user-service/pkg/option"
)

// This file holds the resource resolve helpers used by service.New. They were
// extracted from service.go to keep that file focused on the Service struct,
// New/Start/Stop/Ping, and the RPC facade delegations.
//
// Each resolve* returns a resource: an injected one (option.With…) is used
// as-is with the caller owning its lifecycle; otherwise it is built from cfg
// and registered with the lifecycle Manager, which starts and stops it.

// resolveGID returns the gid dependency (for this service's domains) and, in
// module mode, the raw *gidservice.Handler (so an embedding downstream can
// share it via its WithGIDHandler option). Construction delegates to
// gidservice.Connect, which owns the mode switch and lifecycle registration;
// only the adoption of a parent-injected Handler stays here — it reads this
// service's own options and the parent owns that lifecycle.
func resolveGID(o *option.Options, cfg *config.RemoteServiceConfig[*gidconfig.Config], mgr *lifecycle.Manager) (gidservice.Service, *gidservice.Handler, error) {
	// Injected handler takes precedence (a parent shares its gid Handler),
	// even if cfg is nil (no ThirdParty.GID configured).
	if o.GIDHandler != nil {
		return o.GIDHandler, o.GIDHandler, nil
	}
	if cfg == nil {
		return nil, nil, fmt.Errorf("third_party.gid: not configured")
	}
	return gidservice.Connect(gidservice.ConnectConfig{
		Mode:   cfg.Mode,
		Target: cfg.Target,
		Config: cfg.Config,
	}, mgr)
}

// resolveMessage returns the message dependency. Construction delegates to
// messageservice.Connect; only the adoption of a parent-injected Handler
// stays here (this service's own option, parent-owned lifecycle). gidRaw is
// passed unconditionally: WithGIDHandler(nil) is equivalent to not injecting
// (nil field ≡ not injected), so when this service's gid runs in grpc mode
// message-service simply resolves its own gid from its own config.
func resolveMessage(o *option.Options, cfg *config.RemoteServiceConfig[*messageconfig.Config], db *gorm.DB, rdb *redis.Client, gidRaw *gidservice.Handler, mgr *lifecycle.Manager) (messageservice.Service, error) {
	// Injected handler takes precedence (a parent shares its message Handler),
	// even if cfg is nil (no ThirdParty.Message configured).
	if o.MessageHandler != nil {
		return o.MessageHandler, nil
	}
	if cfg == nil {
		return nil, fmt.Errorf("third_party.message: not configured")
	}
	msg, _, err := messageservice.Connect(messageservice.ConnectConfig{
		Mode:   cfg.Mode,
		Target: cfg.Target,
		Config: cfg.Config,
		Opts: []messageoption.Option{
			messageoption.WithDB(db),
			messageoption.WithRedis(rdb),
			messageoption.WithGIDHandler(gidRaw), // nil = not injected; message resolves its own
		},
	}, mgr)
	return msg, err
}

// resolveCaptcha returns the captcha service to use. If injected via option,
// ownership stays with the caller. Otherwise it is created from cfg.Captcha
// (falling back to a built-in default) using the provided Redis client.
func resolveCaptcha(o *option.Options, cfg *config.Config, rdb *redis.Client) (*captcha.Captcha, error) {
	if o.Captcha != nil {
		return o.Captcha, nil
	}
	captchaCfg := cfg.Captcha
	if captchaCfg == nil {
		captchaCfg = defaultCaptchaConfig()
	}
	normalizeCaptchaPurposes(captchaCfg)
	// A purpose entry without an explicit code format defaults to 6-digit
	// codes. go-common's generator dereferences the format pointer
	// unconditionally (formats[purpose] = pc.CodeFormat → charset(nil)), so a
	// nil format would panic on the first Generate call.
	for _, pc := range captchaCfg.Purposes {
		if pc != nil && pc.CodeFormat == nil {
			pc.CodeFormat = captcha.FormatDigit6
		}
	}
	return captcha.New(captchaCfg, captcha.WithRedisClient(rdb))
}

// defaultCaptchaConfig returns a sane default used when the caller does not
// declare a captcha block in config. It covers the purposes user-service
// supports (register / login / password_reset / bind / verify_email /
// verify_phone). The numeric purpose keys mirror purposeKey() in auth.go,
// which stringifies the VerificationPurpose enum value.
//
// Each purpose allows up to 5 codes per 5 minutes per target — the shortest
// window also becomes the code TTL (5 min), matching what most users expect
// for an email/SMS verification code. Stricter or per-purpose tuning belongs
// in the operator's config.yaml.
func defaultCaptchaConfig() *captcha.Config {
	rule := &ratelimit.Rule{Window: 5 * time.Minute, Max: 5}
	purposes := make(map[string]*captcha.PurposeConfig, 6)
	for _, p := range []string{
		"1", // REGISTER
		"2", // LOGIN
		"3", // VERIFY_EMAIL
		"4", // VERIFY_PHONE
		"5", // PASSWORD_RESET
		"6", // BIND
	} {
		purposes[p] = &captcha.PurposeConfig{RateRules: []*ratelimit.Rule{rule}}
	}
	return &captcha.Config{
		Prefix:      "captcha",
		MaxAttempts: 3,
		Purposes:    purposes,
	}
}

// captchaPurposeAliases maps operator-friendly purpose names (as written in
// config.yaml) to the numeric keys purposeKey() emits at runtime. The captcha
// library treats the key as opaque — it is only a Redis key component
// (captcha:<purpose>:<channel>:<target>) — so purposeKey keeps emitting the
// numeric form and the Redis key layout stays stable; this rewrite exists
// solely to make the config readable. Mirrors the VerificationPurpose proto
// enum (api/proto/user/v1/user.proto) and the numeric literals in
// defaultCaptchaConfig above.
var captchaPurposeAliases = map[string]string{
	"register":       "1", // VERIFICATION_PURPOSE_REGISTER
	"login":          "2", // VERIFICATION_PURPOSE_LOGIN
	"verify_email":   "3", // VERIFICATION_PURPOSE_VERIFY_EMAIL
	"verify_phone":   "4", // VERIFICATION_PURPOSE_VERIFY_PHONE
	"password_reset": "5", // VERIFICATION_PURPOSE_PASSWORD_RESET
	"bind":           "6", // VERIFICATION_PURPOSE_BIND
}

// normalizeCaptchaPurposes rewrites named purpose keys in cfg.Purposes to the
// numeric form purposeKey() produces, in place. Already-numeric or unknown
// keys pass through unchanged, so legacy numeric configs keep working and
// custom keys are not silently dropped.
func normalizeCaptchaPurposes(cfg *captcha.Config) {
	if cfg == nil || len(cfg.Purposes) == 0 {
		return
	}
	renamed := make(map[string]*captcha.PurposeConfig, len(cfg.Purposes))
	for k, v := range cfg.Purposes {
		if num, ok := captchaPurposeAliases[k]; ok {
			k = num
		}
		renamed[k] = v
	}
	cfg.Purposes = renamed
}

// resolveLoginRateLimit returns the login attempt limiter config, falling
// back to a built-in default when the operator has not configured one.
func resolveLoginRateLimit(cfg *config.Config) *ratelimit.Config {
	if cfg.RateLimit != nil && cfg.RateLimit.Login != nil {
		return cfg.RateLimit.Login
	}
	return &ratelimit.Config{
		Prefix: "login:rate",
		Global: []*ratelimit.Rule{
			{Window: 5 * time.Minute, Max: 20},
		},
		Rules: map[string][]*ratelimit.Rule{
			"fail": {
				{Window: 5 * time.Minute, Max: 5},
				{Window: time.Hour, Max: 15},
			},
		},
	}
}

// resolveCodeRateLimit returns the SendVerificationCode limiter config. This
// is a service-wide cap on outbound verification-code volume — it catches
// attackers rotating targets (different emails/phones) when no per-IP limit
// is available. The captcha library's per-target RateRules still applies
// independently.
func resolveCodeRateLimit(cfg *config.Config) *ratelimit.Config {
	if cfg.RateLimit != nil && cfg.RateLimit.Code != nil {
		return cfg.RateLimit.Code
	}
	return &ratelimit.Config{
		Prefix: "code:rate",
		Rules: map[string][]*ratelimit.Rule{
			"send": {
				{Window: time.Minute, Max: 100},
				{Window: time.Hour, Max: 1000},
			},
		},
	}
}

// thirdPartyGID returns cfg.ThirdParty.GID, or nil if ThirdParty is unset — so
// resolveGID's injected-handler check fires without a nil-deref at the call site.
func thirdPartyGID(cfg *config.Config) *config.RemoteServiceConfig[*gidconfig.Config] {
	if cfg.ThirdParty == nil {
		return nil
	}
	return cfg.ThirdParty.GID
}

// thirdPartyMessage returns cfg.ThirdParty.Message, or nil if ThirdParty is unset.
func thirdPartyMessage(cfg *config.Config) *config.RemoteServiceConfig[*messageconfig.Config] {
	if cfg.ThirdParty == nil {
		return nil
	}
	return cfg.ThirdParty.Message
}

// defaultSessionConfig returns the documented SessionConfig defaults (the
// values SessionConfig declares via `default:` tags). Used when cfg.Session is
// nil so session.NewManager doesn't nil-deref. Mirroring the struct tags here
// keeps the fallback consistent with what configx.Load would have produced.
func defaultSessionConfig() *config.SessionConfig {
	return &config.SessionConfig{
		TTL:                168 * time.Hour,
		MaxSessions:        5,
		KeyPrefix:          "user:session",
		UserSessionsPrefix: "user:user_sessions",
		SessionCodeTTL:     5 * time.Minute,
	}
}

// resolveRBACConfig returns an RBACConfig safe to pass to cache.NewRBACCache:
// never nil, and with a non-nil Cache sub-config so cache writes (which deref
// cfg.Cache) don't panic. Builds a local value — does not mutate the caller's
// cfg. The string prefixes are preserved as-is from cfg.
func resolveRBACConfig(cfg *config.RBACConfig) *config.RBACConfig {
	if cfg == nil {
		return &config.RBACConfig{Cache: &config.RBACCacheConfig{}}
	}
	if cfg.Cache != nil {
		return cfg
	}
	// Cache is nil: shallow-copy to avoid mutating the caller's config, then
	// fill Cache on the copy.
	resolved := *cfg
	resolved.Cache = &config.RBACCacheConfig{}
	return &resolved
}

// --- service construction ---

// newWithDeps constructs the Service with all subpackages wired up.
//
// Nil-safety: sub-configs that may be nil in a minimal/empty config (e.g. an
// embedder booting with no OAuth providers, no Session/RBAC block) are
// resolved to safe defaults before use. A provider is "configured" only when
// it carries real credentials (nil OR empty-creds => skipped entirely — not
// built, not validated). Configured providers still get full validation via
// socialsvc.New, so a configured-but-misconfigured provider (bad redirect_url)
// still fails at startup.
func newWithDeps(cfg *config.Config, db *gorm.DB, rdb *redis.Client, gid gidservice.Service, message messageservice.Service, captchaSvc *captcha.Captcha, miniRefreshErrorHook func(string, error)) (*Service, []string, error) {
	// A fully-nil cfg (e.g. an embedder that left third_party.user.config empty)
	// boots as an empty config — every sub-config then resolves to its defaults.
	if cfg == nil {
		cfg = &config.Config{}
	}
	// Session manager — fall back to documented defaults when cfg.Session is nil.
	sessionCfg := cfg.Session
	if sessionCfg == nil {
		sessionCfg = defaultSessionConfig()
	}
	sessionMgr := session.NewManager(rdb, sessionCfg)

	// OAuth — only construct providers that carry real credentials. A nil
	// cfg.OAuth or nil/empty-creds provider blocks are "not configured" and
	// skipped entirely. oauthCfg stays as cfg.OAuth (possibly nil) so the
	// social service preserves its nil-oauth defense-in-depth in
	// validateReturnTo, and validateOAuthConfig short-circuits on nil.
	oauthCfg := cfg.OAuth

	var wechatMgr *mini.Manager
	socialProviders := make(map[pb.IdentityProvider]identity.SocialProvider)
	if oauthCfg != nil {
		if oauthCfg.WeChat.IsConfigured() {
			wechatMgr = mini.NewManager(&mini.Config{
				Credentials: map[string]string{
					oauthCfg.WeChat.AppID: oauthCfg.WeChat.AppSecret,
				},
				OnRefreshError: miniRefreshErrorHook,
			})
		}
		if oauthCfg.GitHub.IsConfigured() {
			socialProviders[pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB] = github.New(
				oauthCfg.GitHub.ClientID, oauthCfg.GitHub.ClientSecret, oauthCfg.GitHub.RedirectURL,
			)
		}
		if oauthCfg.Google.IsConfigured() {
			socialProviders[pb.IdentityProvider_IDENTITY_PROVIDER_GOOGLE] = google.New(
				oauthCfg.Google.ClientID, oauthCfg.Google.ClientSecret, oauthCfg.Google.RedirectURL,
			)
		}
		if oauthCfg.WeChat.IsConfigured() {
			socialProviders[pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT] = wechat.New(
				oauthCfg.WeChat.AppID, oauthCfg.WeChat.AppSecret, oauthCfg.WeChat.RedirectURL,
			)
			socialProviders[pb.IdentityProvider_IDENTITY_PROVIDER_WECHAT_MINIPROGRAM] = mini.NewProvider(
				oauthCfg.WeChat.AppID, wechatMgr,
			)
		}
		if oauthCfg.Apple.IsConfigured() {
			// apple.New parses the private key strictly; only call it when Apple
			// is fully configured so an empty key never reaches it.
			appleProvider, err := apple.New(apple.Config{
				ClientID:        oauthCfg.Apple.ClientID,
				TeamID:          oauthCfg.Apple.TeamID,
				KeyID:           oauthCfg.Apple.KeyID,
				RedirectURL:     oauthCfg.Apple.RedirectURL,
				PrivateKeyPEM:   oauthCfg.Apple.PrivateKey,
				ClientSecretTTL: oauthCfg.Apple.ClientSecretTTL,
			})
			if err != nil {
				return nil, nil, fmt.Errorf("init apple provider: %w", err)
			}
			socialProviders[pb.IdentityProvider_IDENTITY_PROVIDER_APPLE] = appleProvider
		}
	}

	// RBAC — fall back to an empty config when nil; ensure Cache sub-config is
	// non-nil so later cache writes (which deref cfg.RBAC.Cache) don't panic.
	// Build a local resolved value to avoid mutating the caller's cfg.
	rbacCfg := resolveRBACConfig(cfg.RBAC)
	rbacCache := cache.NewRBACCache(rdb, rbacCfg)

	// Subpackages
	loginRateLimit := resolveLoginRateLimit(cfg)
	loginLimiter := ratelimit.NewRedisLimiter(rdb, loginRateLimit)
	codeLimiter := ratelimit.NewRedisLimiter(rdb, resolveCodeRateLimit(cfg))
	sessionHandler := session.New(db, sessionMgr)
	authHandler := authsvc.New(db, sessionMgr, captchaSvc, loginLimiter, codeLimiter, gid, message)
	userHandler := usersvc.New(db, gid, sessionHandler, captchaSvc)
	socialHandler, socialWarnings, err := socialsvc.New(db, sessionMgr, socialProviders, gid, rdb, oauthCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("init social service: %w", err)
	}
	rbacHandler := rbacsvc.New(db, rbacCache, gid)

	return &Service{
		db:         db,
		rdb:        rdb,
		gid:        gid,
		message:    message,
		sessionMgr: sessionMgr,
		auth:       authHandler,
		user:       userHandler,
		social:     socialHandler,
		sess:       sessionHandler,
		rbacSvc:    rbacHandler,
		startedAt:  time.Now().UnixMilli(),
	}, socialWarnings, nil
}
