package auth_test

import (
	"context"
	"testing"

	"github.com/servekit/go-common/grpcx"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/servekit/user-service/pkg/auth"
)

const protectedMethod = "/user.v1.UserService/GetProfile"

func TestTrustedIdentityUnary_PropagatesMetadata(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		auth.XUserID, "42",
		auth.XSessionID, "sess-42",
	))

	var (
		gotUserID    int64
		gotSessionID string
	)
	_, err := auth.TrustedIdentityUnary()(ctx, nil,
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
	require.Equal(t, int64(42), gotUserID)
	require.Equal(t, "sess-42", gotSessionID)
}

func TestTrustedIdentityUnary_NoMetadata_PassesThroughUnannotated(t *testing.T) {
	called := false
	_, err := auth.TrustedIdentityUnary()(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: protectedMethod},
		func(ctx context.Context, _ any) (any, error) {
			called = true
			// Handlers requiring identity enforce its presence themselves.
			_, uerr := grpcx.GetUserIDFromCtx(ctx)
			require.Error(t, uerr)
			return "ok", nil
		})
	require.NoError(t, err)
	require.True(t, called)
}

func TestTrustedIdentityUnary_MalformedUserID_Ignored(t *testing.T) {
	// A malformed value must not panic or half-annotate: the user id is left
	// out while the well-formed session id still propagates.
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		auth.XUserID, "not-a-number",
		auth.XSessionID, "sess-42",
	))

	_, err := auth.TrustedIdentityUnary()(ctx, nil,
		&grpc.UnaryServerInfo{FullMethod: protectedMethod},
		func(ctx context.Context, _ any) (any, error) {
			_, uerr := grpcx.GetUserIDFromCtx(ctx)
			require.Error(t, uerr)
			s, serr := auth.SessionIDFromCtx(ctx)
			require.NoError(t, serr)
			require.Equal(t, "sess-42", s)
			return "ok", nil
		})
	require.NoError(t, err)
}

func TestWithSessionID_RoundTrip(t *testing.T) {
	ctx := auth.WithSessionID(context.Background(), "sess-42")
	sid, err := auth.SessionIDFromCtx(ctx)
	require.NoError(t, err)
	require.Equal(t, "sess-42", sid)
}
