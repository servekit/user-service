package xcodes

import "github.com/servekit/go-common/xerr"

var (
	// ErrIdentityNotFound indicates the requested identity does not exist.
	ErrIdentityNotFound = xerr.New("IDENTITY_NOT_FOUND", xerr.CategoryNotFound, 404, "identity not found")
	// ErrIdentityExists indicates the identity is already bound.
	ErrIdentityExists = xerr.New("IDENTITY_EXISTS", xerr.CategoryConflict, 409, "identity already bound")
	// ErrIdentityBound indicates the identity is bound to another user.
	ErrIdentityBound = xerr.New("IDENTITY_BOUND_OTHER", xerr.CategoryConflict, 409, "identity bound to another user")
	// ErrPasswordWrong indicates an incorrect password was provided.
	ErrPasswordWrong = xerr.New("PASSWORD_WRONG", xerr.CategoryUnauthorized, 401, "invalid password")
	// ErrOAuthFailed indicates an OAuth authentication failure.
	ErrOAuthFailed = xerr.New("OAUTH_FAILED", xerr.CategoryUnauthorized, 401, "OAuth authentication failed")
	// ErrOAuthExchangeFailed indicates the OAuth code-exchange step (token endpoint) failed.
	ErrOAuthExchangeFailed = xerr.New("OAUTH_EXCHANGE_FAILED", xerr.CategoryUnauthorized, 401, "OAuth code exchange failed")
	// ErrUserInfoFetchFailed indicates the provider rejected the userinfo request
	// or returned a non-2xx status after a successful token exchange.
	ErrUserInfoFetchFailed = xerr.New("USER_INFO_FETCH_FAILED", xerr.CategoryInternal, 502, "failed to fetch user info from provider")
	// ErrAppleTokenInvalid indicates the Apple ID token failed validation or decoding.
	ErrAppleTokenInvalid = xerr.New("APPLE_TOKEN_INVALID", xerr.CategoryUnauthorized, 401, "invalid Apple ID token")
	// ErrVerificationCode indicates an invalid or expired verification code.
	ErrVerificationCode = xerr.New("VERIFICATION_CODE_INVALID", xerr.CategoryBadRequest, 400, "invalid or expired verification code")
)
