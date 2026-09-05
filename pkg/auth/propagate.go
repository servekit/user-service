// gRPC-side identity propagation: lift the trusted identity metadata (set by
// Middleware at the HTTP edge) into the handler context.
package auth

import (
	"context"
	"strconv"
	"strings"

	"github.com/servekit/go-common/grpcx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TrustedIdentityUnary returns a gRPC unary server interceptor that copies
// the X-UserID / X-SessionID metadata — written by this package's Middleware
// at the HTTP edge — into the handler context as grpcx.UserIDKey and this
// package's session-id key.
//
// It performs NO verification: mounting it asserts the gRPC listener sits
// inside the trusted network behind the gateway (the same posture as the
// other internal services). Requests without identity metadata pass through
// unchanged; handlers that require identity enforce its presence themselves
// (grpcx.GetUserIDFromCtx / SessionIDFromCtx return errors).
func TrustedIdentityUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if raw := firstValue(md, XUserID); raw != "" {
				// A malformed value means the trusted caller violated the
				// contract; leave the context unannotated rather than trust
				// a partial parse — handlers requiring identity fail closed.
				if uid, err := strconv.ParseInt(raw, 10, 64); err == nil {
					ctx = context.WithValue(ctx, grpcx.UserIDKey, uid)
				}
			}
			if sid := firstValue(md, XSessionID); sid != "" {
				ctx = context.WithValue(ctx, sessionIDKey, sid)
			}
		}
		return handler(ctx, req)
	}
}

// firstValue returns the first metadata value for key, or "" when absent.
// MD.Get only lowercases the lookup key, while grpc-gateway's annotation can
// leave canonical casing on map keys (Grpc-Metadata-X-User-Id → X-User-Id),
// so fall back to a case-insensitive scan.
func firstValue(md metadata.MD, key string) string {
	if values := md.Get(key); len(values) > 0 {
		return values[0]
	}
	for k, values := range md {
		if len(values) > 0 && strings.EqualFold(k, key) {
			return values[0]
		}
	}
	return ""
}
