// Package message_service provides two MessageService backends: in-process module and gRPC.
package message_service

import (
	"context"
	"fmt"

	messagepb "github.com/servekit/message-service/gen/message/v1"
	messageservice "github.com/servekit/message-service/pkg"
	messageconfig "github.com/servekit/message-service/pkg/config"
	messageoption "github.com/servekit/message-service/pkg/option"
	messagethirdcall "github.com/servekit/message-service/pkg/thirdcall"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Module is a MessageService backend backed by an in-process message-service.
type Module struct {
	hdl *messageservice.Handler
}

// NewModule creates a MessageService backed by an in-process message-service
// instance. Parent DB/Redis are injected (not owned); message-service uses
// them instead of creating its own connections from cfg. gid must already
// satisfy message-service's GIDService interface (caller adapts if needed).
func NewModule(cfg *messageconfig.Config, parentDB *gorm.DB, parentRDB *redis.Client, gid messagethirdcall.GIDService) (*Module, error) {
	if cfg == nil {
		return nil, fmt.Errorf("message-service module: config is nil")
	}
	hdl, err := messageservice.NewModule(
		cfg,
		messageoption.WithDB(parentDB),
		messageoption.WithRedis(parentRDB),
		messageoption.WithGIDService(gid),
	)
	if err != nil {
		return nil, fmt.Errorf("init message-service module: %w", err)
	}
	return &Module{hdl: hdl}, nil
}

// SendEmail delegates to the embedded message-service handler.
func (m *Module) SendEmail(ctx context.Context, req *messagepb.SendEmailRequest) (*messagepb.SendResponse, error) {
	return m.hdl.SendEmail(ctx, req)
}

// SendSMS delegates to the embedded message-service handler.
func (m *Module) SendSMS(ctx context.Context, req *messagepb.SendSMSRequest) (*messagepb.SendResponse, error) {
	return m.hdl.SendSMS(ctx, req)
}

// Close stops the embedded message-service (cron jobs, persistence writers, etc.).
func (m *Module) Close() error { return m.hdl.Stop() }
