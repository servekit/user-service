// Package option defines functional options for configuring the user service.
package option

import (
	"github.com/redis/go-redis/v9"
	gidservice "github.com/servekit/gid-service/pkg"
	messageservice "github.com/servekit/message-service/pkg"
	"gorm.io/gorm"

	"github.com/servekit/go-common/captcha"
)

// Option configures a UserService instance.
type Option func(*Options)

// Options holds resolved option values.
type Options struct {
	DB                   *gorm.DB
	RDB                  *redis.Client
	Captcha              *captcha.Captcha
	GIDHandler           *gidservice.Handler
	MessageHandler       *messageservice.Handler
	MiniRefreshErrorHook func(string, error)
}

// WithDB injects an external database connection. UserService will not close it.
func WithDB(db *gorm.DB) Option {
	return func(o *Options) { o.DB = db }
}

// WithRedis injects an external Redis client. UserService will not close it.
func WithRedis(rdb *redis.Client) Option {
	return func(o *Options) { o.RDB = rdb }
}

// WithCaptcha injects an external captcha instance.
func WithCaptcha(c *captcha.Captcha) Option {
	return func(o *Options) { o.Captcha = c }
}

// WithGIDHandler injects a raw gid-service Handler. UserService wraps it
// internally into its GIDService; callers do not need to know that interface.
// If not set, the service builds one from cfg.ThirdParty.GID.
func WithGIDHandler(h *gidservice.Handler) Option {
	return func(o *Options) { o.GIDHandler = h }
}

// WithMessageHandler injects a raw message-service Handler. UserService wraps
// it internally into its MessageService; callers do not need to know that
// interface. If not set, the service builds one from cfg.ThirdParty.Message.
func WithMessageHandler(h *messageservice.Handler) Option {
	return func(o *Options) { o.MessageHandler = h }
}

// WithMiniRefreshErrorHook injects a callback invoked when the WeChat Mini
// Program token-cache background refresh fails. Pass nil to silence these
// errors. The hook is the only way the mini package surfaces async refresh
// failures; it never logs on its own.
func WithMiniRefreshErrorHook(hook func(string, error)) Option {
	return func(o *Options) { o.MiniRefreshErrorHook = hook }
}

// Apply evaluates all options and returns the resolved Options.
func Apply(opts ...Option) Options {
	var o Options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}
