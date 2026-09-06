package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/servekit/go-common/grpcx"

	"github.com/servekit/user-service/pkg/auth"
)

// The edge-middleware design rests on one grpc-gateway behavior: a request
// header named Grpc-Metadata-X-User-Id crosses the transcode boundary and
// arrives server-side as x-user-id metadata (the default mux forwards only
// Authorization and Grpc-Metadata-* headers). These tests pin that contract.

// mdValue returns the first value for key, case-insensitively: the gateway's
// annotation leaves canonical casing on map keys (X-User-Id), while the
// gRPC wire form is lowercase.
func mdValue(md metadata.MD, key string) string {
	for k, vals := range md {
		if len(vals) > 0 && strings.EqualFold(k, key) {
			return vals[0]
		}
	}
	return ""
}

// TestGatewayForwardsIdentityHeaders drives the gateway's own header
// conversion (the exact function the mux runs before dialing the backend).
func TestGatewayForwardsIdentityHeaders(t *testing.T) {
	mux := runtime.NewServeMux()

	req := httptest.NewRequest("POST", "/api/v1/things", nil)
	req.Header.Set("Grpc-Metadata-X-User-Id", "42")
	req.Header.Set("Grpc-Metadata-X-Session-Id", "sess-42")

	ctx, err := runtime.AnnotateIncomingContext(req.Context(), mux, req,
		"/user.v1.UserService/GetProfile")
	require.NoError(t, err)

	md, ok := metadata.FromIncomingContext(ctx)
	require.True(t, ok)
	require.Equal(t, "42", mdValue(md, auth.XUserID))
	require.Equal(t, "sess-42", mdValue(md, auth.XSessionID))
}

// TestMiddlewareToInterceptorRoundTrip composes the real artifacts without a
// live gRPC server: Middleware verifies and writes the identity headers, the
// gateway's annotation converts them to metadata, TrustedIdentityUnary lifts
// them into the handler context.
func TestMiddlewareToInterceptorRoundTrip(t *testing.T) {
	mux := runtime.NewServeMux()

	// next stands in for the gateway mux: convert what Middleware wrote into
	// incoming metadata, then run the propagate interceptor over a handler.
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ctx, err := runtime.AnnotateIncomingContext(r.Context(), mux, r,
			"/user.v1.UserService/GetProfile")
		require.NoError(t, err)

		_, err = auth.TrustedIdentityUnary()(ctx, nil,
			&grpc.UnaryServerInfo{FullMethod: "/user.v1.UserService/GetProfile"},
			func(ctx context.Context, _ any) (any, error) {
				uid, uerr := grpcx.GetUserIDFromCtx(ctx)
				require.NoError(t, uerr)
				require.Equal(t, int64(7), uid)

				sid, serr := auth.SessionIDFromCtx(ctx)
				require.NoError(t, serr)
				require.Equal(t, "sess-7", sid)
				return "ok", nil
			})
		require.NoError(t, err)
	})

	m := auth.NewMiddleware(newSessions()).Wrap(next)
	r := httptest.NewRequest("POST", "/api/v1/things", nil)
	r.Header.Set("Authorization", "Bearer sess-7")
	// Spoofed identity must not survive to the annotation step.
	r.Header.Set("Grpc-Metadata-X-User-Id", "666")

	m.ServeHTTP(httptest.NewRecorder(), r)
}
