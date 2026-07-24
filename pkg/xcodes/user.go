package xcodes

import "github.com/servekit/go-common/xerr"

var (
	// ErrUserNotFound indicates the requested user does not exist.
	ErrUserNotFound = xerr.New("USER_NOT_FOUND", xerr.CategoryNotFound, 404, "user not found")
	// ErrUserExists indicates a user with the same identifier already exists.
	ErrUserExists = xerr.New("USER_EXISTS", xerr.CategoryConflict, 409, "user already exists")
	// ErrUserDisabled indicates the user account has been disabled.
	ErrUserDisabled = xerr.New("USER_DISABLED", xerr.CategoryForbidden, 403, "user is disabled")
	// ErrNicknameTaken indicates the nickname is already in use.
	ErrNicknameTaken = xerr.New("NICKNAME_TAKEN", xerr.CategoryConflict, 409, "nickname already taken")
	// ErrEmailExists indicates the email is already registered.
	ErrEmailExists = xerr.New("EMAIL_EXISTS", xerr.CategoryConflict, 409, "email already registered")
	// ErrPhoneExists indicates the phone is already registered.
	ErrPhoneExists = xerr.New("PHONE_EXISTS", xerr.CategoryConflict, 409, "phone already registered")
	// ErrUsernameExists indicates the username is already taken.
	ErrUsernameExists = xerr.New("USERNAME_EXISTS", xerr.CategoryConflict, 409, "username already taken")
)
