package auth_test

import (
	"context"
	"errors"
	"testing"

	pb "github.com/servekit/api/gen/go/user/v1"
	"github.com/servekit/go-common/xerr"
	"github.com/servekit/go-common/xerr/xcodes"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/servekit/user-service/pkg"
	"github.com/servekit/user-service/pkg/auth"
)

// Compile-time: the module-mode *Handler and the gRPC-mode *Client both
// satisfy auth.SessionService through the userservice.Service interface —
// anything that can serve GetSession can back the middleware.
var _ auth.SessionService = userservice.Service(nil)

// fakeSessions is a stand-in for user-service: it maps session ids to user
// ids, counts GetSession calls, or returns an error when told to.
type fakeSessions struct {
	users map[string]int64
	err   error
	calls int
}

func (f *fakeSessions) GetSession(_ context.Context, req *pb.GetSessionRequest) (*pb.GetSessionResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	uid, ok := f.users[req.GetSessionId()]
	if !ok {
		return nil, errors.New("session invalid")
	}
	return &pb.GetSessionResponse{UserId: uid}, nil
}

func newSessions() *fakeSessions {
	return &fakeSessions{users: map[string]int64{"sess-7": 7, "sess-9": 9}}
}

func withBearer(ctx context.Context, token string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
}

// requireUnauthorized asserts the error maps to the predefined ErrUnauthorized
// code (category Unauthorized, 401) — the single shape clients see for any
// auth-path failure.
func requireUnauthorized(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var xe *xerr.Error
	require.ErrorAs(t, err, &xe)
	require.Equal(t, xerr.CategoryUnauthorized, xe.Category())
	require.ErrorIs(t, err, xcodes.ErrUnauthorized.New())
}

func TestSessionIDFromCtx_Missing(t *testing.T) {
	_, err := auth.SessionIDFromCtx(context.Background())
	require.Error(t, err)
}
