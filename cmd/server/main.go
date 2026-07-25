// Package main is the entry point for user-service gRPC server.
//
// Build:
//
//	go build -o user-service ./cmd/server
//
// Run scenarios:
//
//	# 1. Default: looks for ./config.yaml in working directory
//	./user-service
//
//	# 2. Custom config via -config flag
//	./user-service -config /etc/user-service/production.yaml
//
//	# 3. Custom config via env var
//	USER_SERVICE_CONFIG=/etc/user-service/production.yaml ./user-service
//
//	# 4. Override individual fields with env vars (USER_SERVICE_ prefix, nested keys joined with _)
//	USER_SERVICE_DATABASE_HOST=db.prod USER_SERVICE_REDIS_ADDR=redis:6379 ./user-service
//
// Config file resolution order:
//   - -config flag → highest priority
//   - USER_SERVICE_CONFIG env var
//   - ./config.yaml in working directory
//   - /etc/user-service/config.yaml
//
// Listeners (configurable in config.yaml):
//
//	server:
//	  grpc:
//	    addr: ":19094"      # gRPC endpoint
//	  gateway:
//	    addr: ":18084"      # HTTP gateway (grpc-gateway)
package main

import (
	"log/slog"
	"os"

	"github.com/servekit/go-common/logging"

	userservice "github.com/servekit/user-service/pkg"
	"github.com/servekit/user-service/pkg/config"
	"github.com/servekit/user-service/pkg/option"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}
	logging.Setup(cfg.Log)

	srv, warnings, err := userservice.NewServer(
		cfg,
		userservice.WithServiceOptions(
			option.WithMiniRefreshErrorHook(func(appID string, err error) {
				slog.Error("mini: background token refresh failed", "appid", appID, "error", err)
			}),
		),
	)
	if err != nil {
		slog.Error("init server", "error", err)
		os.Exit(1)
	}
	for _, w := range warnings {
		slog.Warn(w)
	}
	srv.Run()
}
