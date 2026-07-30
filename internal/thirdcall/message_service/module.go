package message_service

import (
	"context"

	messagepb "github.com/servekit/message-service/gen/message/v1"
	messageservice "github.com/servekit/message-service/pkg"
)

// Module is a MessageService backend backed by an in-process message-service.
type Module struct {
	hdl *messageservice.Handler
}

// NewModule wraps a pre-built message-service Handler as a MessageService. The
// module owns none of the Handler's lifecycle: resolveMessage registers the raw
// Handler with the lifecycle Manager (mgr.Add drives its Start/Stop), whether
// it was built here or injected by a parent. See Close for why it is a no-op.
func NewModule(h *messageservice.Handler) MessageService {
	return &Module{hdl: h}
}

// SendEmail delegates to the embedded message-service handler.
func (m *Module) SendEmail(ctx context.Context, req *messagepb.SendEmailRequest) (*messagepb.SendResponse, error) {
	return m.hdl.SendEmail(ctx, req)
}

// SendSMS delegates to the embedded message-service handler.
func (m *Module) SendSMS(ctx context.Context, req *messagepb.SendSMSRequest) (*messagepb.SendResponse, error) {
	return m.hdl.SendSMS(ctx, req)
}

// Close is a no-op. The Handler's lifecycle is owned by the lifecycle Manager
// (resolveMessage registers it via mgr.Add), not by this module, so the module
// has nothing to release. The method exists only to satisfy the MessageService
// interface, whose grpc backend needs a real Close to drop its connection.
func (m *Module) Close() error {
	return nil
}
