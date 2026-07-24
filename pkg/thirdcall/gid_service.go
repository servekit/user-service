// Package thirdcall provides adapters for third-party service calls.
package thirdcall

import (
	"context"

	gidconfig "github.com/servekit/gid-service/pkg/config"

	gid_service "github.com/servekit/user-service/internal/thirdcall/gid_service"
	"github.com/servekit/user-service/pkg/config"
)

// GIDService generates globally unique IDs via gid-service.
type GIDService interface {
	NextID(ctx context.Context) (int64, error)
}

// NewGIDService creates a GIDService based on config mode.
func NewGIDService(cfg *config.RemoteServiceConfig[*gidconfig.Config]) (GIDService, error) {
	switch cfg.Mode {
	case "grpc":
		return gid_service.NewGRPC(cfg.Target)
	default:
		return gid_service.NewModule(cfg.Config)
	}
}
