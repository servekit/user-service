package message_service

import (
	"context"
	"fmt"

	messagepb "github.com/servekit/message-service/gen/message/v1"
	messageservice "github.com/servekit/message-service/pkg"
)

// GRPC is a MessageService backend that delegates to a remote message-service
// over gRPC.
type GRPC struct {
	client *messageservice.Client
}

// NewGRPC creates a MessageService backed by a gRPC connection to message-service.
func NewGRPC(target string) (MessageService, error) {
	client, err := messageservice.NewClient(target)
	if err != nil {
		return nil, fmt.Errorf("dial message-service: %w", err)
	}
	return &GRPC{client: client}, nil
}

// SendEmail delegates to the underlying gRPC client.
func (g *GRPC) SendEmail(ctx context.Context, req *messagepb.SendEmailRequest) (*messagepb.SendResponse, error) {
	return g.client.SendEmail(ctx, req)
}

// SendSMS delegates to the underlying gRPC client.
func (g *GRPC) SendSMS(ctx context.Context, req *messagepb.SendSMSRequest) (*messagepb.SendResponse, error) {
	return g.client.SendSMS(ctx, req)
}

// Close closes the underlying gRPC connection.
func (g *GRPC) Close() error { return g.client.Close() }
