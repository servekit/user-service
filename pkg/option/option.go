// Package option defines functional options for configuring the user service.
package option

import (
	"github.com/servekit/go-common/captcha"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/servekit/user-service/pkg/thirdcall"
)

// Option configures a UserService instance.
type Option func(*Options)

// Options holds resolved option values.
type Options struct {
	DB                   *gorm.DB
	RDB                  *redis.Client
	Captcha              *captcha.Captcha
	GIDService           thirdcall.GIDService
	MessageService       thirdcall.MessageService
	MiniRefreshErrorHook func(appID string, err error)
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

// WithGIDService injects an external gid-service instance.
// If not set, the service creates one from cfg.ThirdParty.GID.
func WithGIDService(svc thirdcall.GIDService) Option {
	return func(o *Options) { o.GIDService = svc }
}

// WithMessageService injects an external message-service instance.
// If not set, the service creates one from cfg.ThirdParty.Message.
func WithMessageService(svc thirdcall.MessageService) Option {
	return func(o *Options) { o.MessageService = svc }
}

// WithMiniRefreshErrorHook injects a callback invoked when the WeChat Mini
// Program token-cache background refresh fails. Pass nil to silence these
// errors. The hook is the only way the mini package surfaces async refresh
// failures; it never logs on its own.
func WithMiniRefreshErrorHook(hook func(appID string, err error)) Option {
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
