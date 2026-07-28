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
	// ErrPermissionNotFound indicates the requested permission does not exist.
	ErrPermissionNotFound = xerr.New("PERMISSION_NOT_FOUND", xerr.CategoryNotFound, 404, "permission not found")
	// ErrPermissionIsBuiltin indicates an attempt to modify a built-in permission.
	ErrPermissionIsBuiltin = xerr.New("PERMISSION_IS_BUILTIN", xerr.CategoryBadRequest, 400, "cannot modify built-in permission")
	// ErrPermissionExists indicates the resource:action pair already exists.
	ErrPermissionExists = xerr.New("PERMISSION_EXISTS", xerr.CategoryConflict, 409, "permission already exists")
	// ErrPermissionGroupNotFound indicates the requested permission group does not exist.
	ErrPermissionGroupNotFound = xerr.New("PERMISSION_GROUP_NOT_FOUND", xerr.CategoryNotFound, 404, "permission group not found")
	// ErrPermissionGroupIsBuiltin indicates an attempt to modify a built-in permission group.
	ErrPermissionGroupIsBuiltin = xerr.New("PERMISSION_GROUP_IS_BUILTIN", xerr.CategoryBadRequest, 400, "cannot modify built-in permission group")
	// ErrPermissionGroupExists indicates the group name already exists.
	ErrPermissionGroupExists = xerr.New("PERMISSION_GROUP_EXISTS", xerr.CategoryConflict, 409, "permission group already exists")
)
