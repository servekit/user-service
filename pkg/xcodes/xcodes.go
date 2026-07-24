// Package xcodes defines all error codes for user-service.
//
// Common error categories (bad request, unauthorized, etc.) are defined here
// with xerr.New using the same reason strings and HTTP codes as go-common's
// xerr/xcodes package, so behavior is identical and grpcx.ErrorInterceptor
// maps them to gRPC codes the same way. Business-specific error codes live
// in domain files: identity.go, rbac.go, session.go, user.go.
package xcodes

import "github.com/servekit/go-common/xerr"

// Common error codes — values mirror github.com/servekit/go-common/xerr/xcodes.
var (
	// ErrBadRequest indicates an invalid or malformed request.
	ErrBadRequest = xerr.New("BAD_REQUEST", xerr.CategoryBadRequest, 400, "bad request")
	// ErrUnauthorized indicates missing or invalid authentication.
	ErrUnauthorized = xerr.New("UNAUTHORIZED", xerr.CategoryUnauthorized, 401, "unauthorized")
	// ErrForbidden indicates the user lacks permission.
	ErrForbidden = xerr.New("FORBIDDEN", xerr.CategoryForbidden, 403, "forbidden")
	// ErrNotFound indicates the requested resource does not exist.
	ErrNotFound = xerr.New("NOT_FOUND", xerr.CategoryNotFound, 404, "not found")
	// ErrConflict indicates a duplicate or conflicting operation.
	ErrConflict = xerr.New("CONFLICT", xerr.CategoryConflict, 409, "conflict")
	// ErrTooManyRequests indicates rate limiting.
	ErrTooManyRequests = xerr.New("TOO_MANY_REQUESTS", xerr.CategoryTooManyRequests, 429, "too many requests")
	// ErrInternal indicates an unexpected server error.
	ErrInternal = xerr.New("INTERNAL_ERROR", xerr.CategoryInternal, 500, "internal server error")
	// ErrServiceUnavailable indicates the service is temporarily unavailable.
	ErrServiceUnavailable = xerr.New("SERVICE_UNAVAILABLE", xerr.CategoryServiceUnavailable, 503, "service unavailable")
)
