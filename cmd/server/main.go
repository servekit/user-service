// Command server is the user-service gRPC + HTTP entry point.
package main

import (
	"log/slog"
	"os"

	"github.com/servekit/go-common/logging"
	"github.com/servekit/go-common/signalx"

	pkg "github.com/servekit/user-service/pkg"
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

	srv, warnings, err := pkg.NewServer(
		cfg,
		pkg.WithServiceOptions(
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

	if err := signalx.RunWithForceQuit(srv); err != nil {
		slog.Error("run server", "error", err)
		os.Exit(1)
	}
}
