package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/servekit/go-common/dbx"
	"github.com/servekit/go-common/logging"

	"github.com/servekit/user-service/internal/store/dal"
	pkg "github.com/servekit/user-service/pkg"
	"github.com/servekit/user-service/pkg/config"
)

// runBackfillRegisterEnv creates the missing user_register_profiles rows
// from historical auth logs (spec 2026-09-06 §6). Idempotent; run once
// after the register-path deploy, alongside `migrate`.
func runBackfillRegisterEnv() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logging.Setup(cfg.Log)

	db, err := dbx.New(cfg.Database)
	if err != nil {
		return fmt.Errorf("init database: %w", err)
	}
	if err := pkg.Migrate(db); err != nil {
		return err
	}

	created, err := dal.BackfillRegisterProfiles(context.Background(), db)
	if err != nil {
		return err
	}
	slog.Info("backfill-register-env done", "created", created)
	return nil
}
