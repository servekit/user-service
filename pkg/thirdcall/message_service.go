package thirdcall

import (
	"context"
	"fmt"

	messagepb "github.com/servekit/message-service/gen/message/v1"
	messageconfig "github.com/servekit/message-service/pkg/config"
	messagethirdcall "github.com/servekit/message-service/pkg/thirdcall"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	message_service "github.com/servekit/user-service/internal/thirdcall/message_service"
	"github.com/servekit/user-service/pkg/config"
)

// MessageService sends emails and SMS via message-service (module or gRPC).
// Both methods take message-service's native request types verbatim — the
// caller decides every field (content vs. template, vendor, sender_id, ...).
// This package is a stateless transport adapter: it imposes no policy.
type MessageService interface {
	SendEmail(ctx context.Context, req *messagepb.SendEmailRequest) (*messagepb.SendResponse, error)
	SendSMS(ctx context.Context, req *messagepb.SendSMSRequest) (*messagepb.SendResponse, error)
	Close() error
}

// Compile-time assertions: both backends satisfy MessageService.
var (
	_ MessageService = (*message_service.GRPC)(nil)
	_ MessageService = (*message_service.Module)(nil)
)

// gidAdapter bridges user-service's GIDService to message-service's GIDService.
// Same shape, different Go types — an adapter is required.
type gidAdapter struct {
	upstream GIDService
}

// Compile-time assertion: *gidAdapter satisfies message-service's GIDService.
var _ messagethirdcall.GIDService = (*gidAdapter)(nil)

func (a *gidAdapter) NextID(ctx context.Context) (int64, error) {
	return a.upstream.NextID(ctx)
}

// NewMessageService creates a MessageService based on config mode.
// parentDB/parentRDB/parentGID are used only in module mode; gRPC mode dials
// cfg.Target and ignores them.
func NewMessageService(cfg *config.RemoteServiceConfig[*messageconfig.Config], parentDB *gorm.DB, parentRDB *redis.Client, parentGID GIDService) (MessageService, error) {
	switch cfg.Mode {
	case "module":
		if cfg.Config == nil {
			return nil, fmt.Errorf("third_party.message.config is required when mode=module")
		}
		return message_service.NewModule(cfg.Config, parentDB, parentRDB, &gidAdapter{upstream: parentGID})
	case "grpc", "":
		if cfg.Target == "" {
			return nil, fmt.Errorf("third_party.message.target is required when mode=grpc")
		}
		return message_service.NewGRPC(cfg.Target)
	default:
		return nil, fmt.Errorf("third_party.message: unknown mode %q", cfg.Mode)
	}
}
