package userservice

import (
	"context"
	"fmt"

	commonv1 "github.com/servekit/api/gen/go/common/v1"
	pb "github.com/servekit/api/gen/go/user/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Client is a gRPC client for user-service shaped like *Handler: it implements
// the generated pb.UserServiceServer interface (unary methods without
// grpc.CallOption), so a consumer can hold either backend behind that one
// generated interface — module mode passes the *Handler, grpc mode passes the
// *Client — with no per-consumer adapter.
//
// The UnimplementedUserServiceServer embed satisfies the interface's
// mustEmbed guard; every RPC below shadows it with a real delegation. When a
// new RPC is added to the proto, add its delegation here — until then grpc
// mode returns codes.Unimplemented for it.
type Client struct {
	pb.UnimplementedUserServiceServer

	conn *grpc.ClientConn
	cli  pb.UserServiceClient
}

// Compile-time assertion: *Client and *Handler expose the same interface.
var _ pb.UserServiceServer = (*Client)(nil)

// NewClient creates a new gRPC client.
func NewClient(target string, opts ...grpc.DialOption) (*Client, error) {
	if len(opts) == 0 {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", target, err)
	}
	return &Client{conn: conn, cli: pb.NewUserServiceClient(conn)}, nil
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// Ping delegates to the remote user-service.
func (c *Client) Ping(ctx context.Context, in *emptypb.Empty) (*commonv1.Pong, error) {
	return c.cli.Ping(ctx, in)
}

// Register delegates to the remote user-service.
func (c *Client) Register(ctx context.Context, in *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	return c.cli.Register(ctx, in)
}

// Login delegates to the remote user-service.
func (c *Client) Login(ctx context.Context, in *pb.LoginRequest) (*pb.LoginResponse, error) {
	return c.cli.Login(ctx, in)
}

// Logout delegates to the remote user-service.
func (c *Client) Logout(ctx context.Context, in *pb.LogoutRequest) (*emptypb.Empty, error) {
	return c.cli.Logout(ctx, in)
}

// GetOAuthURL delegates to the remote user-service.
func (c *Client) GetOAuthURL(ctx context.Context, in *pb.GetOAuthURLRequest) (*pb.GetOAuthURLResponse, error) {
	return c.cli.GetOAuthURL(ctx, in)
}

// SocialLogin delegates to the remote user-service.
func (c *Client) SocialLogin(ctx context.Context, in *pb.SocialLoginRequest) (*pb.LoginResponse, error) {
	return c.cli.SocialLogin(ctx, in)
}

// MiniProgramLogin delegates to the remote user-service.
func (c *Client) MiniProgramLogin(ctx context.Context, in *pb.MiniProgramLoginRequest) (*pb.LoginResponse, error) {
	return c.cli.MiniProgramLogin(ctx, in)
}

// MiniProgramPhoneLogin delegates to the remote user-service.
func (c *Client) MiniProgramPhoneLogin(ctx context.Context, in *pb.MiniProgramPhoneLoginRequest) (*pb.LoginResponse, error) {
	return c.cli.MiniProgramPhoneLogin(ctx, in)
}

// GetProfile delegates to the remote user-service.
func (c *Client) GetProfile(ctx context.Context, in *pb.GetProfileRequest) (*pb.User, error) {
	return c.cli.GetProfile(ctx, in)
}

// UpdateProfile delegates to the remote user-service.
func (c *Client) UpdateProfile(ctx context.Context, in *pb.UpdateProfileRequest) (*pb.User, error) {
	return c.cli.UpdateProfile(ctx, in)
}

// ChangePassword delegates to the remote user-service.
func (c *Client) ChangePassword(ctx context.Context, in *pb.ChangePasswordRequest) (*emptypb.Empty, error) {
	return c.cli.ChangePassword(ctx, in)
}

// ResetPassword delegates to the remote user-service.
func (c *Client) ResetPassword(ctx context.Context, in *pb.ResetPasswordRequest) (*emptypb.Empty, error) {
	return c.cli.ResetPassword(ctx, in)
}

// ListIdentities delegates to the remote user-service.
func (c *Client) ListIdentities(ctx context.Context, in *pb.ListIdentitiesRequest) (*pb.ListIdentitiesResponse, error) {
	return c.cli.ListIdentities(ctx, in)
}

// BindIdentity delegates to the remote user-service.
func (c *Client) BindIdentity(ctx context.Context, in *pb.BindIdentityRequest) (*pb.Identity, error) {
	return c.cli.BindIdentity(ctx, in)
}

// BindOAuthIdentity delegates to the remote user-service.
func (c *Client) BindOAuthIdentity(ctx context.Context, in *pb.BindOAuthIdentityRequest) (*pb.BindOAuthIdentityResponse, error) {
	return c.cli.BindOAuthIdentity(ctx, in)
}

// UnbindIdentity delegates to the remote user-service.
func (c *Client) UnbindIdentity(ctx context.Context, in *pb.UnbindIdentityRequest) (*emptypb.Empty, error) {
	return c.cli.UnbindIdentity(ctx, in)
}

// SendVerificationCode delegates to the remote user-service.
func (c *Client) SendVerificationCode(ctx context.Context, in *pb.SendVerificationCodeRequest) (*pb.SendVerificationCodeResponse, error) {
	return c.cli.SendVerificationCode(ctx, in)
}

// ListSessions delegates to the remote user-service.
func (c *Client) ListSessions(ctx context.Context, in *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	return c.cli.ListSessions(ctx, in)
}

// RevokeSession delegates to the remote user-service.
func (c *Client) RevokeSession(ctx context.Context, in *pb.RevokeSessionRequest) (*emptypb.Empty, error) {
	return c.cli.RevokeSession(ctx, in)
}

// RevokeAllSessions delegates to the remote user-service.
func (c *Client) RevokeAllSessions(ctx context.Context, in *pb.RevokeAllSessionsRequest) (*emptypb.Empty, error) {
	return c.cli.RevokeAllSessions(ctx, in)
}

// GetSession delegates to the remote user-service.
func (c *Client) GetSession(ctx context.Context, in *pb.GetSessionRequest) (*pb.GetSessionResponse, error) {
	return c.cli.GetSession(ctx, in)
}

// IssueSessionCode delegates to the remote user-service.
func (c *Client) IssueSessionCode(ctx context.Context, in *pb.IssueSessionCodeRequest) (*pb.IssueSessionCodeResponse, error) {
	return c.cli.IssueSessionCode(ctx, in)
}

// ExchangeSessionCode delegates to the remote user-service.
func (c *Client) ExchangeSessionCode(ctx context.Context, in *pb.ExchangeSessionCodeRequest) (*pb.ExchangeSessionCodeResponse, error) {
	return c.cli.ExchangeSessionCode(ctx, in)
}

// CreateUser delegates to the remote user-service.
func (c *Client) CreateUser(ctx context.Context, in *pb.CreateUserRequest) (*pb.CreateUserResponse, error) {
	return c.cli.CreateUser(ctx, in)
}

// GetUser delegates to the remote user-service.
func (c *Client) GetUser(ctx context.Context, in *pb.GetUserRequest) (*pb.User, error) {
	return c.cli.GetUser(ctx, in)
}

// ListUsers delegates to the remote user-service.
func (c *Client) ListUsers(ctx context.Context, in *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
	return c.cli.ListUsers(ctx, in)
}

// ListUsersPaged delegates to the remote user-service.
func (c *Client) ListUsersPaged(ctx context.Context, in *pb.ListUsersPagedRequest) (*pb.ListUsersPagedResponse, error) {
	return c.cli.ListUsersPaged(ctx, in)
}

// DisableUser delegates to the remote user-service.
func (c *Client) DisableUser(ctx context.Context, in *pb.DisableUserRequest) (*pb.User, error) {
	return c.cli.DisableUser(ctx, in)
}

// GetLoginLogs delegates to the remote user-service.
func (c *Client) GetLoginLogs(ctx context.Context, in *pb.GetLoginLogsRequest) (*pb.GetLoginLogsResponse, error) {
	return c.cli.GetLoginLogs(ctx, in)
}

// CreateGroup delegates to the remote user-service.
func (c *Client) CreateGroup(ctx context.Context, in *pb.CreateGroupRequest) (*pb.Group, error) {
	return c.cli.CreateGroup(ctx, in)
}

// GetGroup delegates to the remote user-service.
func (c *Client) GetGroup(ctx context.Context, in *pb.GetGroupRequest) (*pb.Group, error) {
	return c.cli.GetGroup(ctx, in)
}

// UpdateGroup delegates to the remote user-service.
func (c *Client) UpdateGroup(ctx context.Context, in *pb.UpdateGroupRequest) (*pb.Group, error) {
	return c.cli.UpdateGroup(ctx, in)
}

// ListGroups delegates to the remote user-service.
func (c *Client) ListGroups(ctx context.Context, in *pb.ListGroupsRequest) (*pb.ListGroupsResponse, error) {
	return c.cli.ListGroups(ctx, in)
}

// DeleteGroup delegates to the remote user-service.
func (c *Client) DeleteGroup(ctx context.Context, in *pb.DeleteGroupRequest) (*emptypb.Empty, error) {
	return c.cli.DeleteGroup(ctx, in)
}

// AddGroupMember delegates to the remote user-service.
func (c *Client) AddGroupMember(ctx context.Context, in *pb.AddGroupMemberRequest) (*emptypb.Empty, error) {
	return c.cli.AddGroupMember(ctx, in)
}

// RemoveGroupMember delegates to the remote user-service.
func (c *Client) RemoveGroupMember(ctx context.Context, in *pb.RemoveGroupMemberRequest) (*emptypb.Empty, error) {
	return c.cli.RemoveGroupMember(ctx, in)
}

// ListGroupMembers delegates to the remote user-service.
func (c *Client) ListGroupMembers(ctx context.Context, in *pb.ListGroupMembersRequest) (*pb.ListGroupMembersResponse, error) {
	return c.cli.ListGroupMembers(ctx, in)
}

// CreateRole delegates to the remote user-service.
func (c *Client) CreateRole(ctx context.Context, in *pb.CreateRoleRequest) (*pb.Role, error) {
	return c.cli.CreateRole(ctx, in)
}

// UpdateRole delegates to the remote user-service.
func (c *Client) UpdateRole(ctx context.Context, in *pb.UpdateRoleRequest) (*pb.Role, error) {
	return c.cli.UpdateRole(ctx, in)
}

// DeleteRole delegates to the remote user-service.
func (c *Client) DeleteRole(ctx context.Context, in *pb.DeleteRoleRequest) (*emptypb.Empty, error) {
	return c.cli.DeleteRole(ctx, in)
}

// ListRoles delegates to the remote user-service.
func (c *Client) ListRoles(ctx context.Context, in *pb.ListRolesRequest) (*pb.ListRolesResponse, error) {
	return c.cli.ListRoles(ctx, in)
}

// GetRole delegates to the remote user-service.
func (c *Client) GetRole(ctx context.Context, in *pb.GetRoleRequest) (*pb.Role, error) {
	return c.cli.GetRole(ctx, in)
}

// ListPermissions delegates to the remote user-service.
func (c *Client) ListPermissions(ctx context.Context, in *pb.ListPermissionsRequest) (*pb.ListPermissionsResponse, error) {
	return c.cli.ListPermissions(ctx, in)
}

// CreatePermission delegates to the remote user-service.
func (c *Client) CreatePermission(ctx context.Context, in *pb.CreatePermissionRequest) (*pb.Permission, error) {
	return c.cli.CreatePermission(ctx, in)
}

// GetPermission delegates to the remote user-service.
func (c *Client) GetPermission(ctx context.Context, in *pb.GetPermissionRequest) (*pb.Permission, error) {
	return c.cli.GetPermission(ctx, in)
}

// UpdatePermission delegates to the remote user-service.
func (c *Client) UpdatePermission(ctx context.Context, in *pb.UpdatePermissionRequest) (*pb.Permission, error) {
	return c.cli.UpdatePermission(ctx, in)
}

// DeletePermission delegates to the remote user-service.
func (c *Client) DeletePermission(ctx context.Context, in *pb.DeletePermissionRequest) (*emptypb.Empty, error) {
	return c.cli.DeletePermission(ctx, in)
}

// CreatePermissionGroup delegates to the remote user-service.
func (c *Client) CreatePermissionGroup(ctx context.Context, in *pb.CreatePermissionGroupRequest) (*pb.PermissionGroup, error) {
	return c.cli.CreatePermissionGroup(ctx, in)
}

// GetPermissionGroup delegates to the remote user-service.
func (c *Client) GetPermissionGroup(ctx context.Context, in *pb.GetPermissionGroupRequest) (*pb.PermissionGroup, error) {
	return c.cli.GetPermissionGroup(ctx, in)
}

// UpdatePermissionGroup delegates to the remote user-service.
func (c *Client) UpdatePermissionGroup(ctx context.Context, in *pb.UpdatePermissionGroupRequest) (*pb.PermissionGroup, error) {
	return c.cli.UpdatePermissionGroup(ctx, in)
}

// DeletePermissionGroup delegates to the remote user-service.
func (c *Client) DeletePermissionGroup(ctx context.Context, in *pb.DeletePermissionGroupRequest) (*emptypb.Empty, error) {
	return c.cli.DeletePermissionGroup(ctx, in)
}

// ListPermissionGroups delegates to the remote user-service.
func (c *Client) ListPermissionGroups(ctx context.Context, in *pb.ListPermissionGroupsRequest) (*pb.ListPermissionGroupsResponse, error) {
	return c.cli.ListPermissionGroups(ctx, in)
}

// AddGroupRole delegates to the remote user-service.
func (c *Client) AddGroupRole(ctx context.Context, in *pb.AddGroupRoleRequest) (*emptypb.Empty, error) {
	return c.cli.AddGroupRole(ctx, in)
}

// RemoveGroupRole delegates to the remote user-service.
func (c *Client) RemoveGroupRole(ctx context.Context, in *pb.RemoveGroupRoleRequest) (*emptypb.Empty, error) {
	return c.cli.RemoveGroupRole(ctx, in)
}

// ListGroupRoles delegates to the remote user-service.
func (c *Client) ListGroupRoles(ctx context.Context, in *pb.ListGroupRolesRequest) (*pb.ListGroupRolesResponse, error) {
	return c.cli.ListGroupRoles(ctx, in)
}

// AssignRole delegates to the remote user-service.
func (c *Client) AssignRole(ctx context.Context, in *pb.AssignRoleRequest) (*emptypb.Empty, error) {
	return c.cli.AssignRole(ctx, in)
}

// RevokeRole delegates to the remote user-service.
func (c *Client) RevokeRole(ctx context.Context, in *pb.RevokeRoleRequest) (*emptypb.Empty, error) {
	return c.cli.RevokeRole(ctx, in)
}

// ListUserRoles delegates to the remote user-service.
func (c *Client) ListUserRoles(ctx context.Context, in *pb.ListUserRolesRequest) (*pb.ListUserRolesResponse, error) {
	return c.cli.ListUserRoles(ctx, in)
}
