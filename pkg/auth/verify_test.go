package auth_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/servekit/go-common/grpcx"

	"github.com/servekit/user-service/pkg/auth"
)

const pingMethod = "/user.v1.UserService/Ping"

func TestVerifyInterceptor_PublicMethod_PassesWithoutToken(t *testing.T) {
	vc := auth.NewVerifyInterceptor(newSessions(), auth.WithPublicMethods(pingMethod))

	resp, err := vc.Unary()(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: pingMethod},
		func(_ context.Context, _ any) (any, error) { return "pong", nil })
	require.NoError(t, err)
	require.Equal(t, "pong", resp)
}

func TestVerifyInterceptor_ValidBearer_InjectsIdentity(t *testing.T) {
	vc := auth.NewVerifyInterceptor(newSessions())

	var (
		gotUserID    int64
		gotSessionID string
	)
	_, err := vc.Unary()(withBearer(context.Background(), "sess-7"), nil,
		&grpc.UnaryServerInfo{FullMethod: protectedMethod},
		func(ctx context.Context, _ any) (any, error) {
			uid, uerr := grpcx.GetUserIDFromCtx(ctx)
			require.NoError(t, uerr)
			gotUserID = uid

			s, serr := auth.SessionIDFromCtx(ctx)
			require.NoError(t, serr)
			gotSessionID = s
			return "ok", nil
		})
	require.NoError(t, err)
	require.Equal(t, int64(7), gotUserID)
	require.Equal(t, "sess-7", gotSessionID)
}

func TestVerifyInterceptor_Rejections(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
	}{
		{name: "no metadata", ctx: context.Background()},
		{
			name: "metadata without authorization",
			ctx:  metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-other", "v")),
		},
		{name: "unknown session id", ctx: withBearer(context.Background(), "no-such-session")},
		{name: "user-service down", ctx: withBearer(context.Background(), "sess-7")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sessions *fakeSessions
			if tt.name == "user-service down" {
				sessions = &fakeSessions{err: errBoom}
			} else {
				sessions = newSessions()
			}

			vc := auth.NewVerifyInterceptor(sessions)
			called := false
			_, err := vc.Unary()(tt.ctx, nil,
				&grpc.UnaryServerInfo{FullMethod: protectedMethod},
				func(context.Context, any) (any, error) {
					called = true
					return nil, nil
				})
			requireUnauthorized(t, err)
			require.False(t, called, "handler must not run on auth failure")
		})
	}
}
