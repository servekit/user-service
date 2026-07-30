package message_service

import (
	"context"

	messagepb "github.com/servekit/message-service/gen/message/v1"
	messageservice "github.com/servekit/message-service/pkg"
)

// Module is a MessageService backend backed by an in-process message-service.
type Module struct {
	hdl  *messageservice.Handler
	owns bool
}

// NewModule wraps a pre-built message-service Handler as a MessageService.
// owns=true when the caller built the Handler (Close Stops it); false when it
// was injected by a parent that owns its lifecycle (Close is a no-op).
func NewModule(h *messageservice.Handler, owns bool) MessageService {
	return &Module{hdl: h, owns: owns}
}

// SendEmail delegates to the embedded message-service handler.
func (m *Module) SendEmail(ctx context.Context, req *messagepb.SendEmailRequest) (*messagepb.SendResponse, error) {
	return m.hdl.SendEmail(ctx, req)
}

// SendSMS delegates to the embedded message-service handler.
func (m *Module) SendSMS(ctx context.Context, req *messagepb.SendSMSRequest) (*messagepb.SendResponse, error) {
	return m.hdl.SendSMS(ctx, req)
}

// Close stops the Handler only if this wrapper owns it (self-built). A borrowed
// (injected) Handler is left to its owner, so Close is a no-op — the parent's
// lifecycle Stops it.
func (m *Module) Close() error {
	if !m.owns {
		return nil
	}
	return m.hdl.Stop()
}
