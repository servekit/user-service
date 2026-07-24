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
)

// NewModule creates a Handler from config. Returns *handler.Handler so callers
// interact directly with the gRPC service interface.
// Pass option.WithDB/WithRedis to inject external connections.
func NewModule(cfg *config.Config, opts ...option.Option) (*handler.Handler, error) {
	// Warnings discarded: module mode has no logger; advisory only.
	svc, _, err := service.New(cfg, opts...)
	if err != nil {
		return nil, err
	}
	return handler.New(svc), nil
}
