// Package userservice provides in-process and gRPC client access to user-service.
package userservice

import (
	"errors"

	"github.com/servekit/go-common/grpcx"
	"github.com/servekit/go-common/signalx"

	pb "github.com/servekit/user-service/gen/user/v1"
	"github.com/servekit/user-service/internal/service"
	"github.com/servekit/user-service/pkg/config"
	"github.com/servekit/user-service/pkg/handler"
	"github.com/servekit/user-service/pkg/option"

	"buf.build/go/protovalidate"
	protovalidate_middleware "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/protovalidate"
	"google.golang.org/grpc"
)

// ServerOption configures a Server.
type ServerOption func(*serverOptions)

type serverOptions struct {
	serviceOpts []option.Option
}

// WithServiceOptions passes through options to the underlying service.New call.
func WithServiceOptions(opts ...option.Option) ServerOption {
	return func(o *serverOptions) { o.serviceOpts = append(o.serviceOpts, opts...) }
}

// Server wraps a gRPC server for user-service. Implements signalx.Service so it
// can be passed to signalx.RunWithForceQuit: Start brings up the service then
// the gRPC/gateway listeners; Stop tears them down in reverse.
type Server struct {
	grpcSrv *grpcx.Server
	svc     *service.Service
	hdl     *handler.Handler
}

// Compile-time assertion that *Server satisfies signalx.Service.
var _ signalx.Service = (*Server)(nil)

// NewServer creates a Server with all dependencies.
//
// The returned warnings slice surfaces non-fatal operator concerns (e.g.
// AllowArbitraryRedirectURLs=true on an OAuth provider) up to the caller.
// Library code does not log per CLAUDE.md; the caller (cmd/server) is
// responsible for logging them at startup.
func NewServer(cfg *config.Config, opts ...ServerOption) (*Server, []string, error) {
	var o serverOptions
	for _, opt := range opts {
		opt(&o)
	}

	svc, warnings, err := service.New(cfg, o.serviceOpts...)
	if err != nil {
		return nil, nil, err
	}
	hdl := handler.New(svc)

	validator, err := protovalidate.New()
	if err != nil {
		return nil, nil, err
	}

	grpcSrv := grpcx.New(
		&grpcx.ServerConfig{
			GRPCAddr:    cfg.Server.GRPC,
			GatewayAddr: cfg.Server.HTTP,
		},
		func(s *grpc.Server) { pb.RegisterUserServiceServer(s, hdl) },
		pb.RegisterUserServiceHandlerFromEndpoint,
		grpcx.ErrorInterceptor,
		protovalidate_middleware.UnaryServerInterceptor(validator),
	)

	return &Server{grpcSrv: grpcSrv, svc: svc, hdl: hdl}, warnings, nil
}

// Start starts the service and the gRPC server. Rolls back svc.Start on
// grpcSrv.Start failure.
func (s *Server) Start() error {
	if err := s.svc.Start(); err != nil {
		return err
	}
	if err := s.grpcSrv.Start(); err != nil {
		return errors.Join(err, s.svc.Stop())
	}
	return nil
}

// Stop stops the gRPC server and the service. Errors from both are joined.
func (s *Server) Stop() error {
	return errors.Join(s.grpcSrv.Stop(), s.svc.Stop())
}
