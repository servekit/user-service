// Package message_service adapts message-service to user-service's internal
// needs. The MessageService interface is internal: external callers inject the
// raw *messageservice.Handler via option.WithMessageHandler; this package wraps
// it for user-service's own business code.
package message_service

import (
	"context"

	messagepb "github.com/servekit/message-service/gen/message/v1"
)

// MessageService sends emails and SMS via message-service. Both methods take
// message-service's native request types verbatim — the caller decides every
// field. This package is a stateless transport adapter: it imposes no policy.
type MessageService interface {
	SendEmail(ctx context.Context, req *messagepb.SendEmailRequest) (*messagepb.SendResponse, error)
	SendSMS(ctx context.Context, req *messagepb.SendSMSRequest) (*messagepb.SendResponse, error)
	Close() error
}

// Compile-time assertions: both backends satisfy MessageService.
var (
	_ MessageService = (*Module)(nil)
	_ MessageService = (*GRPC)(nil)
)
