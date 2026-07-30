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
	"github.com/servekit/go-common/dbx"
	"github.com/servekit/go-common/lifecycle"
	"github.com/servekit/go-common/ratelimit"
	"github.com/servekit/go-common/redisx"

	gid_service "github.com/servekit/user-service/internal/thirdcall/gid_service"
	message_service "github.com/servekit/user-service/internal/thirdcall/message_service"
	"github.com/servekit/user-service/pkg/config"
	"github.com/servekit/user-service/pkg/option"
)

// This file holds the resource resolve helpers used by service.New. They were
// extracted from service.go to keep that file focused on the Service struct,
// New/Start/Stop/Ping, and the RPC facade delegations.
//
// Each resolve* returns a resource: an injected one (option.With…) is used
// as-is with the caller owning its lifecycle; otherwise it is built from cfg
// and registered with the lifecycle Manager so mgr.Stop shuts it down.

// resolveDB returns the DB to use. If injected via option, ownership stays with
// the caller and nothing is registered with mgr. If created from cfg, a Stopper
// is registered so mgr.Stop closes the connection pool.
func resolveDB(o *option.Options, cfg *config.Config, mgr *lifecycle.Manager) (*gorm.DB, error) {
	if o.DB != nil {
		return o.DB, nil
	}
	db, err := dbx.New(cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	mgr.AddStopper("db", lifecycle.StopFunc(func() {
		if sqlDB, e := db.DB(); e == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	}))
	return db, nil
}

// resolveRedis returns the Redis client to use. If injected via option, ownership
// stays with the caller. If created from cfg, a Stopper is registered so mgr.Stop
// closes the client.
func resolveRedis(o *option.Options, cfg *config.Config, mgr *lifecycle.Manager) (*redis.Client, error) {
	if o.RDB != nil {
		return o.RDB, nil
	}
	rdb, err := redisx.New(cfg.Redis)
	if err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	mgr.AddStopper("redis", lifecycle.StopFunc(func() { _ = rdb.Close() }))
	return rdb, nil
}

// resolveGID returns the GIDService (for this service's domains) and, in module
// mode, the raw *gidservice.Handler (so an embedded message-service can share
// it via messageoption.WithGIDHandler). grpc mode returns a nil raw — there is
// no in-process Handler to share, so message-service dials its own. grpc and
// self-built register a Stopper; an injected Handler is borrowed (parent owns
// lifecycle, no Stopper). The GIDService interface is internal.
func resolveGID(o *option.Options, cfg *config.RemoteServiceConfig[*gidconfig.Config], mgr *lifecycle.Manager) (gid_service.GIDService, *gidservice.Handler, error) {
	// Injected handler takes precedence (a parent shares its gid Handler),
	// even if cfg is nil (no ThirdParty.GID configured).
	if o.GIDHandler != nil {
		return gid_service.NewModule(o.GIDHandler, false), o.GIDHandler, nil
	}
	if cfg == nil {
		return nil, nil, fmt.Errorf("third_party.gid: not configured")
	}
	switch cfg.Mode {
	case "grpc":
		gid, err := gid_service.NewGRPC(cfg.Target)
		if err != nil {
			return nil, nil, fmt.Errorf("init gid-service: %w", err)
		}
		mgr.AddStopper("gid", lifecycle.StopFunc(func() { _ = gid.Close() }))
		return gid, nil, nil
	case "module":
		if cfg.Config == nil {
			return nil, nil, fmt.Errorf("third_party.gid: module config required when no handler injected")
		}
		hdl, err := gidservice.NewModule(cfg.Config)
		if err != nil {
			return nil, nil, fmt.Errorf("init gid-service: %w", err)
		}
		gid := gid_service.NewModule(hdl, true)
		mgr.AddStopper("gid", lifecycle.StopFunc(func() { _ = gid.Close() }))
		return gid, hdl, nil
	default:
		return nil, nil, fmt.Errorf("third_party.gid: unknown mode %q", cfg.Mode)
	}
}

// resolveMessage returns the MessageService. grpc mode dials cfg.Target and
// registers a Stopper; module mode uses an injected raw *messageservice.Handler
// (option.WithMessageHandler) when a parent embeds this service (parent owns
// lifecycle, no Stopper), otherwise builds one from cfg.Config (standalone) and
// registers a Stopper. gidRaw (non-nil in module mode) is shared into
// message-service via WithGIDHandler so it reuses this service's gid Handler;
// when nil (grpc mode) message-service resolves its own gid. The MessageService
// interface is internal.
func resolveMessage(o *option.Options, cfg *config.RemoteServiceConfig[*messageconfig.Config], db *gorm.DB, rdb *redis.Client, gidRaw *gidservice.Handler, mgr *lifecycle.Manager) (message_service.MessageService, error) {
	// Injected handler takes precedence (a parent shares its message Handler),
	// even if cfg is nil (no ThirdParty.Message configured).
	if o.MessageHandler != nil {
		return message_service.NewModule(o.MessageHandler, false), nil
	}
	if cfg == nil {
		return nil, fmt.Errorf("third_party.message: not configured")
	}
	switch cfg.Mode {
	case "grpc":
		if cfg.Target == "" {
			return nil, fmt.Errorf("third_party.message.target is required when mode=grpc")
		}
		msg, err := message_service.NewGRPC(cfg.Target)
		if err != nil {
			return nil, fmt.Errorf("init message-service: %w", err)
		}
		mgr.AddStopper("message-service", lifecycle.StopFunc(func() { _ = msg.Close() }))
		return msg, nil
	case "module":
		if cfg.Config == nil {
			return nil, fmt.Errorf("third_party.message: module config required when no handler injected")
		}
		opts := []messageoption.Option{
			messageoption.WithDB(db),
			messageoption.WithRedis(rdb),
		}
		if gidRaw != nil {
			opts = append(opts, messageoption.WithGIDHandler(gidRaw)) // share this service's gid Handler
		}
		hdl, err := messageservice.NewModule(cfg.Config, opts...)
		if err != nil {
			return nil, fmt.Errorf("init message-service: %w", err)
		}
		msg := message_service.NewModule(hdl, true)
		mgr.AddStopper("message-service", lifecycle.StopFunc(func() { _ = msg.Close() }))
		return msg, nil
	default:
		return nil, fmt.Errorf("third_party.message: unknown mode %q", cfg.Mode)
	}
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
