// Package auth provides reusable authentication middleware for services and
// gateways that authenticate bearers against user-service sessions.
//
// The bearer credential IS a user-service session id — an opaque token, no
// JWT layer. Verification calls GetSession, which slides the session TTL
// forward on every authenticated request: an active user stays logged in, an
// idle one expires after the session TTL (validate-on-use).
//
// Three artifacts, usable independently:
//
//   - Middleware: HTTP middleware that verifies at the edge (a gateway's
//     public surface) and short-circuits 401 before the request reaches any
//     backend. On success it rewrites the trusted identity headers
//     (X-UserID / X-SessionID), which grpc-gateway forwards to gRPC as
//     metadata.
//   - TrustedIdentityUnary: a gRPC interceptor that lifts those trusted
//     metadata values into the handler context (grpcx.UserIDKey plus this
//     package's session-id key), so handlers read identity uniformly via
//     grpcx.GetUserIDFromCtx / SessionIDFromCtx.
//   - VerifyInterceptor: a gRPC interceptor that verifies the session itself,
//     for standalone gRPC services that are not behind an edge Middleware.
//
// Trust model: the identity headers are written ONLY by Middleware, which
// strips any inbound copies first — a client cannot spoof them over HTTP.
// The gRPC port behind the gateway must not be exposed publicly; direct gRPC
// callers sit inside the trusted network (the same posture as the other
// internal services). Unary RPCs only.
package auth

import (
	"context"
	"errors"

	pb "github.com/servekit/api/gen/go/user/v1"
	"github.com/servekit/go-common/grpcx"
)

// Trusted identity metadata, set by Middleware after a successful verify and
// read by TrustedIdentityUnary. Lowercase because gRPC metadata keys must be.
//
// grpc-gateway's default mux forwards only Authorization and Grpc-Metadata-*
// request headers as gRPC metadata, so the HTTP wire form carries the
// Grpc-Metadata- prefix; the gateway strips it, leaving the plain metadata
// key for the backend.
const (
	XUserID    = "x-user-id"
	XSessionID = "x-session-id"

	xUserHeader    = "Grpc-Metadata-X-User-Id"
	xSessionHeader = "Grpc-Metadata-X-Session-Id"

	maxTokenLen = 128 // mirrors protovalidate max_len on GetSessionRequest.session_id
)

// SessionService is the subset of user-service needed to verify sessions.
// Satisfied by the module-mode *Handler and the gRPC-mode *Client alike, and
// trivial to stub in tests.
type SessionService interface {
	GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.GetSessionResponse, error)
}

// sessionIDKeyType is an unexported context-key type, preventing collisions
// with keys defined elsewhere.
type sessionIDKeyType struct{}

// sessionIDKey holds the verified session id for the current request.
var sessionIDKey = sessionIDKeyType{}

// errUnauthorized collapses every verification failure (missing/malformed
// token, unknown/expired session, downstream error) into one shape: fail
// closed at the trust boundary without distinguishing causes for the client.
var errUnauthorized = errors.New("auth: unauthorized")

// SessionIDFromCtx returns the verified session id attached to the request
// context by Middleware or TrustedIdentityUnary. It errors when called
// outside an authenticated request.
func SessionIDFromCtx(ctx context.Context) (string, error) {
	sid, ok := ctx.Value(sessionIDKey).(string)
	if !ok {
		return "", errors.New("auth: session id not found in context")
	}
	return sid, nil
}

// identity is the verified caller identity produced by verifyBearer.
type identity struct {
	UserID    int64
	SessionID string
}

// verifyBearer validates a bearer token (a session id) against user-service
// and returns the identity. The GetSession call slides the session TTL —
// mounting any of this package's artifacts on a request path makes that path
// validate-on-use.
func verifyBearer(ctx context.Context, sessions SessionService, token string) (identity, error) {
	if token == "" || len(token) > maxTokenLen {
		return identity{}, errUnauthorized
	}
	resp, err := sessions.GetSession(ctx, &pb.GetSessionRequest{SessionId: token})
	if err != nil || resp.GetUserId() == 0 {
		return identity{}, errUnauthorized
	}
	return identity{UserID: resp.GetUserId(), SessionID: token}, nil
}

// annotateCtx attaches a verified identity to ctx so handlers (and raw HTTP
// handlers behind Middleware) read it uniformly: user id via
// grpcx.GetUserIDFromCtx, session id via SessionIDFromCtx.
func annotateCtx(ctx context.Context, id identity) context.Context {
	ctx = context.WithValue(ctx, grpcx.UserIDKey, id.UserID)
	return context.WithValue(ctx, sessionIDKey, id.SessionID)
}
