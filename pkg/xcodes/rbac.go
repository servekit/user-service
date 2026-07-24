package xcodes

import "github.com/servekit/go-common/xerr"

var (
	// ErrPermissionDenied indicates the user lacks the required permission.
	ErrPermissionDenied = xerr.New("PERMISSION_DENIED", xerr.CategoryForbidden, 403, "permission denied")
	// ErrRoleNotFound indicates the requested role does not exist.
	ErrRoleNotFound = xerr.New("ROLE_NOT_FOUND", xerr.CategoryNotFound, 404, "role not found")
	// ErrRoleIsBuiltin indicates an attempt to modify a built-in role.
	ErrRoleIsBuiltin = xerr.New("ROLE_IS_BUILTIN", xerr.CategoryBadRequest, 400, "cannot modify built-in role")
	// ErrGroupNotFound indicates the requested group does not exist.
	ErrGroupNotFound = xerr.New("GROUP_NOT_FOUND", xerr.CategoryNotFound, 404, "group not found")
	// ErrAlreadyMember indicates the user is already a group member.
	ErrAlreadyMember = xerr.New("ALREADY_MEMBER", xerr.CategoryConflict, 409, "user is already a group member")
	// ErrNotMember indicates the user is not a group member.
	ErrNotMember = xerr.New("NOT_MEMBER", xerr.CategoryNotFound, 404, "user is not a group member")
)
