// gRPC-side session verification for standalone services that are not behind
// an edge Middleware.
package auth

import (
	"context"

	"github.com/servekit/go-common/grpcx"
	"github.com/servekit/go-common/xerr/xcodes"
	"google.golang.org/grpc"
)

// VerifyInterceptor authenticates each non-public RPC by resolving the bearer
// token (a session id) via GetSession. Construct once with
// NewVerifyInterceptor and install its Unary method in the grpcx chain, with
// grpcx.ErrorInterceptor OUTSIDE it (its rejections are *xerr.Error and need
// the outer mapping to codes.Unauthenticated / HTTP 401).
type VerifyInterceptor struct {
	sessions SessionService
	public   map[string]struct{}
}

// VerifyOption configures a VerifyInterceptor.
type VerifyOption func(*VerifyInterceptor)

// WithPublicMethods marks RPC full-method names (e.g.
// "/user.v1.UserService/Ping") that skip verification: pre-login RPCs and
// health checks.
func WithPublicMethods(methods ...string) VerifyOption {
	return func(v *VerifyInterceptor) {
		for _, m := range methods {
			v.public[m] = struct{}{}
		}
	}
}

// NewVerifyInterceptor constructs a VerifyInterceptor. sessions must be
// non-nil; a nil dependency is a wiring bug in the assembler, not a runtime
// condition to mask.
func NewVerifyInterceptor(sessions SessionService, opts ...VerifyOption) *VerifyInterceptor {
	v := &VerifyInterceptor{
		sessions: sessions,
		public:   make(map[string]struct{}),
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

// Unary returns the gRPC UnaryServerInterceptor. It does NOT implement
// StreamServerInterceptor — the services in this stack expose only unary
// RPCs. Failures (missing/malformed bearer, invalid session, user-service
// unavailable) all fail closed as xcodes.ErrUnauthorized.
func (v *VerifyInterceptor) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if _, ok := v.public[info.FullMethod]; ok {
			return handler(ctx, req)
		}

		token, err := grpcx.BearerTokenFromCtx(ctx)
		if err != nil {
			return nil, xcodes.ErrUnauthorized.New()
		}
		id, err := verifyBearer(ctx, v.sessions, token)
		if err != nil {
			return nil, xcodes.ErrUnauthorized.New()
		}
		return handler(annotateCtx(ctx, id), req)
	}
}
