package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/servekit/go-common/grpcx"
	"github.com/stretchr/testify/require"

	"github.com/servekit/user-service/pkg/auth"
)

// errBoom simulates a user-service outage for fail-closed tests.
var errBoom = errors.New("user-service down")

// recordingHandler records what the middleware let through: whether it ran at
// all, the headers it saw, and the request context it can observe.
type recordingHandler struct {
	called  bool
	headers http.Header
	ctx     context.Context
}

func (h *recordingHandler) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	h.called = true
	h.headers = r.Header
	h.ctx = r.Context()
}

func newGet(path string) *http.Request {
	return httptest.NewRequest(http.MethodGet, path, nil)
}

func TestMiddleware_Rejections(t *testing.T) {
	tests := []struct {
		name      string
		req       *http.Request
		wantCalls int // expected GetSession invocations
	}{
		{
			name:      "missing authorization header",
			req:       newGet("/api/v1/things"),
			wantCalls: 0,
		},
		{
			name: "wrong scheme",
			req: func() *http.Request {
				r := newGet("/api/v1/things")
				r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
				return r
			}(),
			wantCalls: 0,
		},
		{
			name: "bearer with empty token",
			req: func() *http.Request {
				r := newGet("/api/v1/things")
				r.Header.Set("Authorization", "Bearer ")
				return r
			}(),
			wantCalls: 0,
		},
		{
			name: "oversized token rejected before session lookup",
			req: func() *http.Request {
				r := newGet("/api/v1/things")
				r.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 129))
				return r
			}(),
			wantCalls: 0,
		},
		{
			name: "unknown session id",
			req: func() *http.Request {
				r := newGet("/api/v1/things")
				r.Header.Set("Authorization", "Bearer no-such-session")
				return r
			}(),
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := newSessions()
			next := &recordingHandler{}
			m := auth.NewMiddleware(sessions).Wrap(next)

			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, tt.req)

			require.Equal(t, http.StatusUnauthorized, rec.Code)
			require.False(t, next.called, "handler must not run on auth failure")
			require.Equal(t, tt.wantCalls, sessions.calls)
		})
	}
}

func TestMiddleware_UserServiceDown_FailsClosed(t *testing.T) {
	sessions := &fakeSessions{err: errBoom}
	next := &recordingHandler{}
	m := auth.NewMiddleware(sessions).Wrap(next)

	r := newGet("/api/v1/things")
	r.Header.Set("Authorization", "Bearer sess-7")
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, r)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.False(t, next.called)
}

func TestMiddleware_ValidSession_AnnotatesAndForwards(t *testing.T) {
	next := &recordingHandler{}
	m := auth.NewMiddleware(newSessions()).Wrap(next)

	r := newGet("/api/v1/things")
	r.Header.Set("Authorization", "Bearer sess-7")
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, r)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, next.called)
	// Wire form carries the Grpc-Metadata- prefix (what grpc-gateway
	// forwards); identity is also attached to the request context.
	require.Equal(t, "7", next.headers.Get("Grpc-Metadata-X-User-Id"))
	require.Equal(t, "sess-7", next.headers.Get("Grpc-Metadata-X-Session-Id"))

	uid, err := grpcx.GetUserIDFromCtx(next.ctx)
	require.NoError(t, err)
	require.Equal(t, int64(7), uid)
	sid, err := auth.SessionIDFromCtx(next.ctx)
	require.NoError(t, err)
	require.Equal(t, "sess-7", sid)
}

func TestMiddleware_StripsInboundIdentityHeaders(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
	}{
		{
			name: "public path",
			req:  newGet("/api/v1/auth/login"),
		},
		{
			name: "OPTIONS preflight",
			req:  httptest.NewRequest(http.MethodOptions, "/api/v1/things", nil),
		},
		{
			name: "authenticated request",
			req: func() *http.Request {
				r := newGet("/api/v1/things")
				r.Header.Set("Authorization", "Bearer sess-7")
				return r
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := &recordingHandler{}
			m := auth.NewMiddleware(newSessions(),
				auth.WithPublicPaths("/api/v1/auth/login")).Wrap(next)

			// Spoof in both the plain and the Grpc-Metadata- wire form.
			tt.req.Header.Set(auth.XUserID, "666")
			tt.req.Header.Set(auth.XSessionID, "spoofed")
			tt.req.Header.Set("Grpc-Metadata-X-User-Id", "666")
			tt.req.Header.Set("Grpc-Metadata-X-Session-Id", "spoofed")
			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, tt.req)

			require.Equal(t, http.StatusOK, rec.Code)
			require.True(t, next.called)

			// On the authenticated path the middleware rewrites the headers
			// with the verified identity; on public/OPTIONS paths it drops
			// them entirely. Either way the spoofed values must not survive.
			for _, h := range []string{auth.XUserID, "Grpc-Metadata-X-User-Id"} {
				if got := next.headers.Get(h); got != "" {
					require.Equal(t, "7", got)
				}
			}
			for _, h := range []string{auth.XSessionID, "Grpc-Metadata-X-Session-Id"} {
				require.NotEqual(t, "spoofed", next.headers.Get(h))
			}
		})
	}
}

func TestMiddleware_PublicRoutes_SkipVerification(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "exact public path", path: "/api/v1/auth/login"},
		{name: "public prefix with path parameter", path: "/v1/file-links/abc123/download"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := newSessions()
			next := &recordingHandler{}
			m := auth.NewMiddleware(sessions,
				auth.WithPublicPaths("/api/v1/auth/login"),
				auth.WithPublicPrefixes("/v1/file-links/")).Wrap(next)

			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, newGet(tt.path))

			require.Equal(t, http.StatusOK, rec.Code)
			require.True(t, next.called)
			require.Equal(t, 0, sessions.calls, "public routes must not hit user-service")
		})
	}
}

func TestMiddleware_SlidingRenewal_EveryRequestVerifies(t *testing.T) {
	// Each protected request must call GetSession (which slides the session
	// TTL at user-service) — that is the validate-on-use contract.
	sessions := newSessions()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	m := auth.NewMiddleware(sessions).Wrap(next)

	for i := 0; i < 3; i++ {
		r := newGet("/api/v1/things")
		r.Header.Set("Authorization", "Bearer sess-9")
		m.ServeHTTP(httptest.NewRecorder(), r)
	}
	require.Equal(t, 3, sessions.calls)
}
