// Package userservice provides client, server, and in-process module access to user-service.
//
// Client usage:
//
//	c, _ := userservice.NewClient("localhost:9000")
//	defer c.Close()
//	resp, err := c.Register(ctx, &pb.RegisterRequest{...})
package userservice

import (
	"github.com/servekit/user-service/internal/service"
	"github.com/servekit/user-service/pkg/config"
	"github.com/servekit/user-service/pkg/handler"
	"github.com/servekit/user-service/pkg/option"

	"gorm.io/gorm"
)

// Handler is the in-process entry point. Aliased to *handler.Handler so
// external code references it as userservice.Handler, matching the other
// services' pkg shape.
type Handler = handler.Handler

// NewModule creates a Handler from config. Returns *handler.Handler so callers
// interact directly with the gRPC service interface.
// Pass option.WithDB/WithRedis to inject external connections.
func NewModule(cfg *config.Config, opts ...option.Option) (*Handler, error) {
	// Warnings discarded: module mode has no logger; advisory only.
	svc, _, err := service.New(cfg, opts...)
	if err != nil {
		return nil, err
	}
	return handler.New(svc), nil
}

// Migrate applies the current schema (GORM AutoMigrate) to db. It re-exports
// handler.Migrate so embedders and the `migrate` subcommand share one entry
// point:
//
//	userservice.Migrate(parentDB)                                // before NewModule
//	hdl, err := userservice.NewModule(cfg, option.WithDB(parentDB))
func Migrate(db *gorm.DB) error {
	return handler.Migrate(db)
}
