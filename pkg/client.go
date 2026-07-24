package userservice

import (
	"fmt"

	pb "github.com/servekit/user-service/gen/user/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client wraps the generated gRPC client for user-service.
// Embeds pb.UserServiceClient so all RPC methods are directly available.
type Client struct {
	conn *grpc.ClientConn
	pb.UserServiceClient
}

// NewClient creates a new gRPC client.
func NewClient(target string, opts ...grpc.DialOption) (*Client, error) {
	if len(opts) == 0 {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", target, err)
	}
	return &Client{conn: conn, UserServiceClient: pb.NewUserServiceClient(conn)}, nil
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
