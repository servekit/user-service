package xcodes

import "github.com/servekit/go-common/xerr"

var (
	// ErrSessionExpired indicates the session has expired.
	ErrSessionExpired = xerr.New("SESSION_EXPIRED", xerr.CategoryUnauthorized, 401, "session expired")
	// ErrSessionInvalid indicates the session is not valid.
	ErrSessionInvalid = xerr.New("SESSION_INVALID", xerr.CategoryUnauthorized, 401, "invalid session")
)
