// Package handler implements pb.UserServiceServer as a thin shim over
// internal/service. Each RPC method is a one-line delegation — service takes
// the proto request directly. Handler holds NO business logic.
//
// Handler also implements signalx.Service (Start/Stop). Start/Stop delegate to
// the underlying *service.Service, whose lifecycle.Manager owns the DB, Redis,
// and jobs scheduler.
package handler

import (
	"context"

	"github.com/servekit/go-common/signalx"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/servekit/user-service/gen/user/v1"
	"github.com/servekit/user-service/internal/service"
)

// Handler implements pb.UserServiceServer.
type Handler struct {
	pb.UnimplementedUserServiceServer

	svc *service.Service
}

// New constructs a Handler wrapping svc.
func New(svc *service.Service) *Handler { return &Handler{svc: svc} }

// Compile-time assertions.
var (
	_ pb.UserServiceServer = (*Handler)(nil)
	_ signalx.Service      = (*Handler)(nil)
)

// Start delegates to the underlying service, starting all owned components.
func (h *Handler) Start() error { return h.svc.Start() }

// Stop delegates to the underlying service, stopping all owned components.
func (h *Handler) Stop() error { return h.svc.Stop() }

// Ping is a health-check RPC.
func (h *Handler) Ping(ctx context.Context, _ *emptypb.Empty) (*pb.Pong, error) {
	return h.svc.Ping(ctx)
}

// --- gRPC method delegations ---
// Each method delegates to internal/service. Comments below describe "how to
// use" (prerequisites, side effects, follow-up RPCs) for in-process module
// callers; for the full contract see user.proto.

// Register creates an email/phone user and opens a session.
// Prerequisite: SendVerificationCode(purpose=REGISTER); pass its captcha_id back here.
func (h *Handler) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	return h.svc.Register(ctx, req)
}

// Login authenticates by method (see LoginMethod enum). Code methods
// auto-register on missing identity (response.is_new=true); password
// methods never do.
func (h *Handler) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	return h.svc.Login(ctx, req)
}

// Logout revokes the current session. No-op if session_id is empty.
func (h *Handler) Logout(ctx context.Context, req *pb.LogoutRequest) (*emptypb.Empty, error) {
	return h.svc.Logout(ctx, req)
}

// RefreshSession extends the session TTL (sliding window). Call from the API
// gateway on each authenticated request, or periodically from the client.
func (h *Handler) RefreshSession(ctx context.Context, req *pb.RefreshSessionRequest) (*emptypb.Empty, error) {
	return h.svc.RefreshSession(ctx, req)
}

// GetOAuthURL returns the OAuth authorization URL — step 1 of redirect-based
// OAuth (GitHub/Google/WeChat web/Apple). Step 2 is SocialLogin with the
// code+state that comes back via redirect_url.
func (h *Handler) GetOAuthURL(ctx context.Context, req *pb.GetOAuthURLRequest) (*pb.GetOAuthURLResponse, error) {
	return h.svc.GetOAuthURL(ctx, req)
}

// SocialLogin exchanges an OAuth code for user info, find-or-creates the
// user, opens a session. Step 2 of the OAuth flow started by GetOAuthURL.
func (h *Handler) SocialLogin(ctx context.Context, req *pb.SocialLoginRequest) (*pb.LoginResponse, error) {
	return h.svc.SocialLogin(ctx, req)
}

// MiniProgramLogin: silent WeChat Mini Program login via wx.login() code.
// Use MiniProgramPhoneLogin afterward if phone number is needed.
func (h *Handler) MiniProgramLogin(ctx context.Context, req *pb.MiniProgramLoginRequest) (*pb.LoginResponse, error) {
	return h.svc.MiniProgramLogin(ctx, req)
}

// MiniProgramPhoneLogin: exchanges wx.login + getPhoneNumber codes for
// openid+phone. Links miniprogram identity to existing phone user, or
// registers a new user with both identities.
func (h *Handler) MiniProgramPhoneLogin(ctx context.Context, req *pb.MiniProgramPhoneLoginRequest) (*pb.LoginResponse, error) {
	return h.svc.MiniProgramPhoneLogin(ctx, req)
}

// GetProfile returns a user by ID. Authorization (requester == user_id) is
// the caller's responsibility.
func (h *Handler) GetProfile(ctx context.Context, req *pb.GetProfileRequest) (*pb.User, error) {
	return h.svc.GetProfile(ctx, req)
}

// UpdateProfile: partial update of mutable profile fields (empty fields ignored).
func (h *Handler) UpdateProfile(ctx context.Context, req *pb.UpdateProfileRequest) (*pb.User, error) {
	return h.svc.UpdateProfile(ctx, req)
}

// ChangePassword: verifies old → sets new. Side effect: a successful call on
// a PENDING_REVIEW user (created via CreateUser) activates the account.
func (h *Handler) ChangePassword(ctx context.Context, req *pb.ChangePasswordRequest) (*emptypb.Empty, error) {
	return h.svc.ChangePassword(ctx, req)
}

// ResetPassword via verification code (purpose=PASSWORD_RESET).
func (h *Handler) ResetPassword(ctx context.Context, req *pb.ResetPasswordRequest) (*emptypb.Empty, error) {
	return h.svc.ResetPassword(ctx, req)
}

// ListIdentities returns every identity linked to the user (email/phone/OAuth
// providers/miniprogram openid).
func (h *Handler) ListIdentities(ctx context.Context, req *pb.ListIdentitiesRequest) (*pb.ListIdentitiesResponse, error) {
	return h.svc.ListIdentities(ctx, req)
}

// BindIdentity: link a new email/phone identity. Requires SendVerificationCode
// (purpose=BIND).
func (h *Handler) BindIdentity(ctx context.Context, req *pb.BindIdentityRequest) (*pb.Identity, error) {
	return h.svc.BindIdentity(ctx, req)
}

// BindOAuthIdentity: link an OAuth provider identity (GitHub/Google/WeChat
// web/Apple) to an already-authenticated user. Caller is expected to have
// run GetOAuthURL first and now possess code+state from the OAuth callback.
func (h *Handler) BindOAuthIdentity(ctx context.Context, req *pb.BindOAuthIdentityRequest) (*pb.BindOAuthIdentityResponse, error) {
	return h.svc.BindOAuthIdentity(ctx, req)
}

// UnbindIdentity: remove one of the caller's own identities (requires
// confirmation code and user_id ownership check).
func (h *Handler) UnbindIdentity(ctx context.Context, req *pb.UnbindIdentityRequest) (*emptypb.Empty, error) {
	return h.svc.UnbindIdentity(ctx, req)
}

// SendVerificationCode generates + dispatches a code; returns captcha_id to
// pass back to the consuming RPC (Register/Login/BindIdentity/ResetPassword).
// Caller provides ALL delivery content (templates, sign_name, body, etc.).
func (h *Handler) SendVerificationCode(ctx context.Context, req *pb.SendVerificationCodeRequest) (*pb.SendVerificationCodeResponse, error) {
	return h.svc.SendVerificationCode(ctx, req)
}

// ListSessions returns active sessions for a user (device-management UIs).
func (h *Handler) ListSessions(ctx context.Context, req *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	return h.svc.ListSessions(ctx, req)
}

// RevokeSession: logout a specific session by ID. Authorization is caller's
// responsibility.
func (h *Handler) RevokeSession(ctx context.Context, req *pb.RevokeSessionRequest) (*emptypb.Empty, error) {
	return h.svc.RevokeSession(ctx, req)
}

// RevokeAllSessions: logout everywhere. Recommended after ChangePassword,
// ResetPassword, or DisableUser.
func (h *Handler) RevokeAllSessions(ctx context.Context, req *pb.RevokeAllSessionsRequest) (*emptypb.Empty, error) {
	return h.svc.RevokeAllSessions(ctx, req)
}

// GetSession: resolves session_id → user_id + metadata. Primary BFF entry
// point for cookie-based auth: read session_id from cookie, call GetSession,
// inject the returned user_id into downstream RPCs. Read-only against Redis.
func (h *Handler) GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.GetSessionResponse, error) {
	return h.svc.GetSession(ctx, req)
}

// IssueSessionCode: mints a one-time short code referencing session_id.
// Called by the OAuth callback service after SocialLogin; the code is
// passed via URL query to the business side instead of leaking session_id.
func (h *Handler) IssueSessionCode(ctx context.Context, req *pb.IssueSessionCodeRequest) (*pb.IssueSessionCodeResponse, error) {
	return h.svc.IssueSessionCode(ctx, req)
}

// ExchangeSessionCode: trades a one-time short code for session_id +
// user_id. Called once by the business side's return_to handler, which
// then sets its own domain cookie. Replay returns ErrSessionInvalid.
func (h *Handler) ExchangeSessionCode(ctx context.Context, req *pb.ExchangeSessionCodeRequest) (*pb.ExchangeSessionCodeResponse, error) {
	return h.svc.ExchangeSessionCode(ctx, req)
}

// GetUser (admin): returns a user by ID.
func (h *Handler) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.User, error) {
	return h.svc.GetUser(ctx, req)
}

// CreateUser (admin): creates a user in PENDING_REVIEW status; the user
// activates it via ChangePassword with the initial password.
func (h *Handler) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.CreateUserResponse, error) {
	return h.svc.CreateUser(ctx, req)
}

// ListUsers: cursor-paginated user list with rich filters. Prefer over
// ListUsersPaged for stable iteration under concurrent writes.
func (h *Handler) ListUsers(ctx context.Context, req *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
	return h.svc.ListUsers(ctx, req)
}

// ListUsersPaged: offset-paginated list with optional total count, for admin
// UIs that need page numbers. Set count=false on follow-up loads.
func (h *Handler) ListUsersPaged(ctx context.Context, req *pb.ListUsersPagedRequest) (*pb.ListUsersPagedResponse, error) {
	return h.svc.ListUsersPaged(ctx, req)
}

// DisableUser: toggles DISABLED/ACTIVE. Does NOT revoke sessions — call
// RevokeAllSessions separately for immediate logout.
func (h *Handler) DisableUser(ctx context.Context, req *pb.DisableUserRequest) (*pb.User, error) {
	return h.svc.DisableUser(ctx, req)
}

// GetLoginLogs: cursor-paginated login audit logs.
func (h *Handler) GetLoginLogs(ctx context.Context, req *pb.GetLoginLogsRequest) (*pb.GetLoginLogsResponse, error) {
	return h.svc.GetLoginLogs(ctx, req)
}

// CreateGroup: creates an organizational unit. Follow up with AddGroupMember
// and AddGroupRole so members inherit the group's permissions.
func (h *Handler) CreateGroup(ctx context.Context, req *pb.CreateGroupRequest) (*pb.Group, error) {
	return h.svc.CreateGroup(ctx, req)
}

// GetGroup returns a group by ID.
func (h *Handler) GetGroup(ctx context.Context, req *pb.GetGroupRequest) (*pb.Group, error) {
	return h.svc.GetGroup(ctx, req)
}

// UpdateGroup: updates name/description (parent_id is immutable).
func (h *Handler) UpdateGroup(ctx context.Context, req *pb.UpdateGroupRequest) (*pb.Group, error) {
	return h.svc.UpdateGroup(ctx, req)
}

// ListGroups: cursor-paginated groups filtered by status.
func (h *Handler) ListGroups(ctx context.Context, req *pb.ListGroupsRequest) (*pb.ListGroupsResponse, error) {
	return h.svc.ListGroups(ctx, req)
}

// DeleteGroup: removes a group; does NOT cascade to direct user→role
// assignments. Clean up AddGroupRole mappings first.
func (h *Handler) DeleteGroup(ctx context.Context, req *pb.DeleteGroupRequest) (*emptypb.Empty, error) {
	return h.svc.DeleteGroup(ctx, req)
}

// AddGroupMember: adds a user with in-group role (owner/admin/member).
// Invalidates the user's RBAC cache.
func (h *Handler) AddGroupMember(ctx context.Context, req *pb.AddGroupMemberRequest) (*emptypb.Empty, error) {
	return h.svc.AddGroupMember(ctx, req)
}

// RemoveGroupMember: invalidates the user's RBAC cache. Permissions arriving
// via this group's roles are dropped.
func (h *Handler) RemoveGroupMember(ctx context.Context, req *pb.RemoveGroupMemberRequest) (*emptypb.Empty, error) {
	return h.svc.RemoveGroupMember(ctx, req)
}

// ListGroupMembers: cursor-paginated, optionally filtered by in-group role.
func (h *Handler) ListGroupMembers(ctx context.Context, req *pb.ListGroupMembersRequest) (*pb.ListGroupMembersResponse, error) {
	return h.svc.ListGroupMembers(ctx, req)
}

// CreateRole: bundles permission_ids + permission_group_ids into a named role.
// Get IDs from ListPermissions / ListPermissionGroups.
func (h *Handler) CreateRole(ctx context.Context, req *pb.CreateRoleRequest) (*pb.Role, error) {
	return h.svc.CreateRole(ctx, req)
}

// UpdateRole: FULLY REPLACES the permission set — pass the complete list,
// not deltas. Invalidates cache for all affected users.
func (h *Handler) UpdateRole(ctx context.Context, req *pb.UpdateRoleRequest) (*pb.Role, error) {
	return h.svc.UpdateRole(ctx, req)
}

// DeleteRole: cascades to user/group assignments; invalidates cache for
// affected users. Builtin roles should not be deleted.
func (h *Handler) DeleteRole(ctx context.Context, req *pb.DeleteRoleRequest) (*emptypb.Empty, error) {
	return h.svc.DeleteRole(ctx, req)
}

// ListRoles: cursor-paginated roles.
func (h *Handler) ListRoles(ctx context.Context, req *pb.ListRolesRequest) (*pb.ListRolesResponse, error) {
	return h.svc.ListRoles(ctx, req)
}

// ListPermissions: cursor-paginated permission catalog. Until the service layer
// is migrated to the new paginated signature, pagination params are ignored and
// the full list is returned in a single page (next_cursor="", total=0).
func (h *Handler) ListPermissions(ctx context.Context, _ *pb.ListPermissionsRequest) (*pb.ListPermissionsResponse, error) {
	return h.svc.ListPermissions(ctx, &emptypb.Empty{})
}

// ListPermissionGroups: cursor-paginated permission-group catalog. Until the
// service layer is migrated to the new paginated signature, pagination params
// are ignored and the full list is returned in a single page.
func (h *Handler) ListPermissionGroups(ctx context.Context, _ *pb.ListPermissionGroupsRequest) (*pb.ListPermissionGroupsResponse, error) {
	return h.svc.ListPermissionGroups(ctx, &emptypb.Empty{})
}

// AddGroupRole: grants a role to a group; members inherit. Invalidates cache
// for all current group members.
func (h *Handler) AddGroupRole(ctx context.Context, req *pb.AddGroupRoleRequest) (*emptypb.Empty, error) {
	return h.svc.AddGroupRole(ctx, req)
}

// RemoveGroupRole: invalidates cache for all current group members. Direct
// user assignments (AssignRole) are unaffected.
func (h *Handler) RemoveGroupRole(ctx context.Context, req *pb.RemoveGroupRoleRequest) (*emptypb.Empty, error) {
	return h.svc.RemoveGroupRole(ctx, req)
}

// ListGroupRoles: lists roles attached to a group.
func (h *Handler) ListGroupRoles(ctx context.Context, req *pb.ListGroupRolesRequest) (*pb.ListGroupRolesResponse, error) {
	return h.svc.ListGroupRoles(ctx, req)
}

// AssignRole: grants a role directly to a user. Use AddGroupRole for
// group-wide permissions. Invalidates the user's RBAC cache.
func (h *Handler) AssignRole(ctx context.Context, req *pb.AssignRoleRequest) (*emptypb.Empty, error) {
	return h.svc.AssignRole(ctx, req)
}

// RevokeRole: revokes a direct assignment; does NOT touch group-inherited
// permissions (call RemoveGroupRole for that).
func (h *Handler) RevokeRole(ctx context.Context, req *pb.RevokeRoleRequest) (*emptypb.Empty, error) {
	return h.svc.RevokeRole(ctx, req)
}

// ListUserRoles: lists roles assigned to a user.
func (h *Handler) ListUserRoles(ctx context.Context, req *pb.ListUserRolesRequest) (*pb.ListUserRolesResponse, error) {
	return h.svc.ListUserRoles(ctx, req)
}
