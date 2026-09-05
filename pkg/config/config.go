// Package config provides service configuration loading from YAML files.
package config

import (
	"time"

	"github.com/servekit/go-common/captcha"
	"github.com/servekit/go-common/configx"
	"github.com/servekit/go-common/dbx"
	"github.com/servekit/go-common/logging"
	"github.com/servekit/go-common/ratelimit"
	"github.com/servekit/go-common/redisx"

	gidconfig "github.com/servekit/gid-service/pkg/config"
	messageconfig "github.com/servekit/message-service/pkg/config"
)

// serviceName identifies this binary in config file lookup (/etc/<name>) and
// the <NAME>_CONFIG env var. envPrefix scopes all env overrides under
// USER_SERVICE_*.
const (
	serviceName = "user-service"
	envPrefix   = "USER_SERVICE"
)

// Config holds all service configuration.
type Config struct {
	Server     *ServerConfig
	Database   *dbx.Config
	Redis      *redisx.Config
	Captcha    *captcha.Config
	Session    *SessionConfig
	RBAC       *RBACConfig
	OAuth      *OAuthConfig
	ThirdParty *ThirdPartyConfig
	RateLimit  *RateLimitConfig
	Cron       *CronConfig
	Log        *logging.Config
}

// CronConfig holds cron scheduler settings. Per-task specs live in their
// owning domain config (e.g. cfg.Session.ReapCronSpec), not here.
type CronConfig struct {
	Timezone string `default:"Asia/Shanghai"`
}

// ThirdPartyConfig holds third-party service connection settings.
type ThirdPartyConfig struct {
	GID     *RemoteServiceConfig[*gidconfig.Config]
	Message *RemoteServiceConfig[*messageconfig.Config]
}

// RemoteServiceConfig is the shared third_party.<name> section shape,
// aliased from go-common so Mode is the configx.Mode enum.
type RemoteServiceConfig[T any] = configx.RemoteServiceConfig[T]

// RateLimitConfig holds rate limiting settings, organized by purpose.
type RateLimitConfig struct {
	Login *ratelimit.Config // login attempt rate limiting
	Code  *ratelimit.Config // SendVerificationCode volume cap
}

// ServerConfig holds the gRPC listener address.
type ServerConfig struct {
	GRPC string `default:":19094"`
}

// SessionConfig holds session management settings.
// SessionConfig holds session management settings. Prefixes are namespaces
// without a trailing ':' — manager.go adds the ':' separator when composing
// keys.
type SessionConfig struct {
	TTL                time.Duration `default:"168h"`
	MaxSessions        int           `default:"5"` // max concurrent sessions per user, 0 = unlimited
	KeyPrefix          string        `default:"user:session"`
	UserSessionsPrefix string        `default:"user:user_sessions"`
	// SessionCodeTTL is the TTL for one-time short codes minted by
	// IssueSessionCode (OAuth callback handoff). Default 5m.
	SessionCodeTTL time.Duration `default:"5m"`
}

// RBACConfig holds RBAC cache settings and key prefixes.
// Prefixes are namespaces without a trailing ':' — cache code adds the
// ':' separator when composing keys.
type RBACConfig struct {
	Cache                *RBACCacheConfig
	UserPermsPrefix      string `default:"user:rbac:user_perms"`
	UserRolesPrefix      string `default:"user:rbac:user_roles"`
	RolePermsPrefix      string `default:"user:rbac:role_perms"`
	GroupRolesPrefix     string `default:"user:rbac:group_roles"`
	GroupUserPermsPrefix string `default:"user:rbac:group_user_perms"`
}

// RBACCacheConfig holds TTL values for RBAC cache entries.
type RBACCacheConfig struct {
	UserPermsTTL      time.Duration `default:"10m"`
	UserRolesTTL      time.Duration `default:"10m"`
	RolePermsTTL      time.Duration `default:"30m"`
	GroupRolesTTL     time.Duration `default:"30m"`
	GroupUserPermsTTL time.Duration `default:"10m"`
}

// OAuthConfig holds OAuth provider credentials.
type OAuthConfig struct {
	GitHub *OAuthGitHubConfig
	Google *OAuthGoogleConfig
	WeChat *OAuthWeChatConfig
	Apple  *OAuthAppleConfig
}

// OAuthGitHubConfig holds GitHub OAuth credentials.
type OAuthGitHubConfig struct {
	ClientID                   string
	ClientSecret               string
	RedirectURL                string
	AllowedRedirectURLs        []string // exact-match allowlist; empty + non-empty return_to → reject
	AllowArbitraryRedirectURLs bool     // escape hatch for dev/staging; log warning at startup when true
}

// OAuthGoogleConfig holds Google OAuth credentials.
type OAuthGoogleConfig struct {
	ClientID                   string
	ClientSecret               string
	RedirectURL                string
	AllowedRedirectURLs        []string // exact-match allowlist; empty + non-empty return_to → reject
	AllowArbitraryRedirectURLs bool     // escape hatch for dev/staging; log warning at startup when true
}

// OAuthWeChatConfig holds WeChat OAuth credentials.
type OAuthWeChatConfig struct {
	AppID                      string
	AppSecret                  string
	RedirectURL                string
	AllowedRedirectURLs        []string // exact-match allowlist; empty + non-empty return_to → reject
	AllowArbitraryRedirectURLs bool     // escape hatch for dev/staging; log warning at startup when true
}

// OAuthAppleConfig holds Apple Sign-In credentials.
type OAuthAppleConfig struct {
	ClientID                   string
	TeamID                     string
	KeyID                      string
	PrivateKey                 string
	RedirectURL                string
	AllowedRedirectURLs        []string      // exact-match allowlist; empty + non-empty return_to → reject
	AllowArbitraryRedirectURLs bool          // escape hatch for dev/staging; log warning at startup when true
	ClientSecretTTL            time.Duration `default:"30m"` // TTL for the JWT client_secret minted on each token exchange
}

// Load reads configuration from file and environment, expands ${VAR}
// references in the file against the environment, applies defaults, and
// returns a Config. Config file is resolved in order:
//  1. -config flag (e.g. -config /etc/user-service/config.yaml)
//  2. USER_SERVICE_CONFIG environment variable
//  3. Default: config.yaml in working directory and /etc/user-service
//
// ${VAR} expansion lets config.yaml reference secrets by name
// (e.g. database.password: ${DB_PASSWORD}, oauth.github.client_secret:
// ${OAUTH_GITHUB_CLIENT_SECRET}) instead of holding literals, so the template
// (config.example.yaml) can live in git while real values come from .env (see
// .env.example). Unset vars expand to "" (os.ExpandEnv semantics). Flat
// USER_SERVICE_* env vars still override individual fields via viper
// AutomaticEnv.
func Load() (*Config, error) {
	var cfg Config
	if err := configx.Load(&cfg,
		configx.WithServiceName(serviceName),
		configx.WithEnvPrefix(envPrefix),
		configx.WithExpandEnv(),
	); err != nil {
		return nil, err
	}
	return &cfg, nil
}
