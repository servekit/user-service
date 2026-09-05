// Package service contains user-service business logic.
//
// Layering contract (see golang-service-development skill §2):
//   - This is the SERVICE ROOT. It holds the Service struct + New + Start/Stop +
//     resource resolve helpers + one-line facade methods (one per RPC).
//   - Business logic lives in SUBPACKAGES (internal/service/<domain>/). This
//     file does NOT contain RPC implementations — only delegations.
//   - Handler calls service.X; service.X is a one-line facade that calls
//     s.<domain>.X in the subpackage.
//   - Service methods take proto types DIRECTLY and return proto types.
//
// Lifecycle:
//   - Service holds a *lifecycle.Manager. Owned resources (DB, Redis, jobs
//     scheduler) register Stoppers with mgr; injected resources do not.
//   - Start triggers mgr.Start (concurrent startup of registered Starters).
//   - Stop triggers mgr.Stop (LIFO-style concurrent shutdown).
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"google.golang.org/protobuf/types/known/emptypb"

	commonv1 "github.com/servekit/api/gen/go/common/v1"
	pb "github.com/servekit/api/gen/go/user/v1"
	"github.com/servekit/user-service/internal/jobs"
	authsvc "github.com/servekit/user-service/internal/service/auth"
	rbacsvc "github.com/servekit/user-service/internal/service/rbac"
	"github.com/servekit/user-service/internal/service/session"
	socialsvc "github.com/servekit/user-service/internal/service/social"
	usersvc "github.com/servekit/user-service/internal/service/user"
	"github.com/servekit/user-service/internal/version"
	"github.com/servekit/user-service/pkg/config"
	"github.com/servekit/user-service/pkg/option"

	gidservice "github.com/servekit/gid-service/pkg"
	"github.com/servekit/go-common/cronx"
	"github.com/servekit/go-common/dbx"
	"github.com/servekit/go-common/lifecycle"
	"github.com/servekit/go-common/redisx"
	messageservice "github.com/servekit/message-service/pkg"
)

// Service holds user-service business state. Each domain field is a subpackage
// *Service instance constructed in New() from resolved resources.
type Service struct {
	cfg *config.Config
	mgr *lifecycle.Manager

	db         *gorm.DB
	rdb        *redis.Client
	gid        gidservice.Service
	message    messageservice.Service
	sessionMgr *session.Manager // technical component, separate from sessionsvc subpackage

	// Domain subpackages
	auth    *authsvc.Service
	user    *usersvc.Service
	social  *socialsvc.Service
	sess    *session.Service
	rbacSvc *rbacsvc.Service

	// startedAt is set once in New; Ping returns it for uptime.
	startedAt int64
}

// New constructs a Service from config and functional options.
// By default DB and Redis are created from config and closed on Stop().
// Pass option.WithDB/WithRedis to inject external connections (not closed on Stop).
//
// The returned warnings slice surfaces non-fatal operator concerns (currently
// from social.New: AllowArbitraryRedirectURLs=true) up to the caller. Library
// code does not log per CLAUDE.md; only cmd/server logs.
func New(cfg *config.Config, opts ...option.Option) (*Service, []string, error) {
	o := option.Apply(opts...)

	mgr := lifecycle.NewManager()

	db, err := dbx.Connect(cfg.Database, o.DB, mgr)
	if err != nil {
		return nil, nil, err
	}

	rdb, err := redisx.Connect(cfg.Redis, o.RDB, mgr)
	if err != nil {
		if cerr := mgr.Stop(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("rollback: %w", cerr))
		}
		return nil, nil, err
	}

	gid, gidRaw, err := resolveGID(&o, thirdPartyGID(cfg), mgr)
	if err != nil {
		if cerr := mgr.Stop(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("rollback: %w", cerr))
		}
		return nil, nil, err
	}

	message, err := resolveMessage(&o, thirdPartyMessage(cfg), db, rdb, gidRaw, mgr)
	if err != nil {
		if cerr := mgr.Stop(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("rollback: %w", cerr))
		}
		return nil, nil, err
	}

	captchaSvc, err := resolveCaptcha(&o, cfg, rdb)
	if err != nil {
		if cerr := mgr.Stop(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("rollback: %w", cerr))
		}
		return nil, nil, err
	}

	svc, socialWarnings, err := newWithDeps(cfg, db, rdb, gid, message, captchaSvc, o.MiniRefreshErrorHook)
	if err != nil {
		if cerr := mgr.Stop(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("rollback: %w", cerr))
		}
		return nil, nil, err
	}
	svc.cfg = cfg
	svc.mgr = mgr

	if err := svc.setupJobs(); err != nil {
		if cerr := mgr.Stop(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("rollback: %w", cerr))
		}
		return nil, nil, err
	}
	return svc, socialWarnings, nil
}

// Start starts all owned components concurrently via the lifecycle manager.
func (s *Service) Start() error { return s.mgr.Start() }

// Stop stops all owned components (LIFO concurrent shutdown via the lifecycle manager).
func (s *Service) Stop() error { return s.mgr.Stop() }

// Ping is a health-check RPC. Returns only public, non-sensitive info.
func (s *Service) Ping(ctx context.Context) (*commonv1.Pong, error) {
	v := version.Get()
	return &commonv1.Pong{
		Service:   "user-service",
		Version:   v.Version,
		GitCommit: v.GitCommit,
		GitBranch: v.GitBranch,
		BuildTime: v.BuildTime,
		GoVersion: v.GoVersion,
		Status:    "SERVING",
		Now:       time.Now().UnixMilli(),
		StartedAt: s.startedAt,
	}, nil
}

// Resource resolve helpers (resolveDB/resolveRedis/resolveGID/resolveMessage/
// resolveCaptcha + rate-limit defaults) and the subpackage wiring constructor
// newWithDeps live in helper.go.

// setupJobs builds the jobs.Scheduler from cfg.Cron, registers it with mgr so
// its lifecycle is managed alongside other owned resources, and wires periodic
// jobs. Current state: no periodic jobs registered.
func (s *Service) setupJobs() error {
	tz := "Asia/Shanghai"
	if s.cfg.Cron != nil && s.cfg.Cron.Timezone != "" {
		tz = s.cfg.Cron.Timezone
	}
	scheduler, err := jobs.New(&jobs.Deps{
		Config: &cronx.Config{Timezone: tz, OverlapPolicy: "skip"},
	})
	if err != nil {
		return fmt.Errorf("init jobs: %w", err)
	}
	// Register periodic tasks here. Current state: no tasks.
	// Example (commented out):
	//   if err := scheduler.AddFunc(s.cfg.Session.ReapCronSpec, func() { ... }); err != nil {
	//       return fmt.Errorf("register reap: %w", err)
	//   }
	s.mgr.Add("jobs", scheduler)
	return nil
}

// --- gRPC facade delegations (43 RPCs) ---
// Usage-facing documentation lives on pkg/handler — these are thin internal
// delegations kept bare on purpose. For the full contract see user.proto.

// Register delegates to auth subpackage.
func (s *Service) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	return s.auth.Register(ctx, req)
}

// Login delegates to auth subpackage.
func (s *Service) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	return s.auth.Login(ctx, req)
}

// Logout delegates to auth subpackage.
func (s *Service) Logout(ctx context.Context, req *pb.LogoutRequest) (*emptypb.Empty, error) {
	return s.auth.Logout(ctx, req)
}

// RefreshSession delegates to session subpackage.
func (s *Service) RefreshSession(ctx context.Context, req *pb.RefreshSessionRequest) (*emptypb.Empty, error) {
	return s.sess.RefreshSession(ctx, req)
}

// GetOAuthURL delegates to social subpackage.
func (s *Service) GetOAuthURL(ctx context.Context, req *pb.GetOAuthURLRequest) (*pb.GetOAuthURLResponse, error) {
	return s.social.GetOAuthURL(ctx, req)
}

// SocialLogin delegates to social subpackage.
func (s *Service) SocialLogin(ctx context.Context, req *pb.SocialLoginRequest) (*pb.LoginResponse, error) {
	return s.social.SocialLogin(ctx, req)
}

// MiniProgramLogin delegates to social subpackage.
func (s *Service) MiniProgramLogin(ctx context.Context, req *pb.MiniProgramLoginRequest) (*pb.LoginResponse, error) {
	return s.social.MiniProgramLogin(ctx, req)
}

// MiniProgramPhoneLogin delegates to social subpackage.
func (s *Service) MiniProgramPhoneLogin(ctx context.Context, req *pb.MiniProgramPhoneLoginRequest) (*pb.LoginResponse, error) {
	return s.social.MiniProgramPhoneLogin(ctx, req)
}

// GetProfile delegates to user subpackage.
func (s *Service) GetProfile(ctx context.Context, req *pb.GetProfileRequest) (*pb.User, error) {
	return s.user.GetProfile(ctx, req)
}

// UpdateProfile delegates to user subpackage.
func (s *Service) UpdateProfile(ctx context.Context, req *pb.UpdateProfileRequest) (*pb.User, error) {
	return s.user.UpdateProfile(ctx, req)
}

// ChangePassword delegates to user subpackage.
func (s *Service) ChangePassword(ctx context.Context, req *pb.ChangePasswordRequest) (*emptypb.Empty, error) {
	return s.user.ChangePassword(ctx, req)
}

// ResetPassword delegates to user subpackage.
func (s *Service) ResetPassword(ctx context.Context, req *pb.ResetPasswordRequest) (*emptypb.Empty, error) {
	return s.user.ResetPassword(ctx, req)
}

// ListIdentities delegates to user subpackage.
func (s *Service) ListIdentities(ctx context.Context, req *pb.ListIdentitiesRequest) (*pb.ListIdentitiesResponse, error) {
	return s.user.ListIdentities(ctx, req)
}

// BindIdentity delegates to user subpackage.
func (s *Service) BindIdentity(ctx context.Context, req *pb.BindIdentityRequest) (*pb.Identity, error) {
	return s.user.BindIdentity(ctx, req)
}

// BindOAuthIdentity delegates to social subpackage.
func (s *Service) BindOAuthIdentity(ctx context.Context, req *pb.BindOAuthIdentityRequest) (*pb.BindOAuthIdentityResponse, error) {
	return s.social.BindOAuthIdentity(ctx, req)
}

// UnbindIdentity delegates to user subpackage.
func (s *Service) UnbindIdentity(ctx context.Context, req *pb.UnbindIdentityRequest) (*emptypb.Empty, error) {
	return s.user.UnbindIdentity(ctx, req)
}

// SendVerificationCode delegates to auth subpackage.
func (s *Service) SendVerificationCode(ctx context.Context, req *pb.SendVerificationCodeRequest) (*pb.SendVerificationCodeResponse, error) {
	return s.auth.SendVerificationCode(ctx, req)
}

// ListSessions delegates to session subpackage.
func (s *Service) ListSessions(ctx context.Context, req *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	return s.sess.ListSessions(ctx, req)
}

// RevokeSession delegates to session subpackage.
func (s *Service) RevokeSession(ctx context.Context, req *pb.RevokeSessionRequest) (*emptypb.Empty, error) {
	return s.sess.RevokeSession(ctx, req)
}

// RevokeAllSessions delegates to session subpackage.
func (s *Service) RevokeAllSessions(ctx context.Context, req *pb.RevokeAllSessionsRequest) (*emptypb.Empty, error) {
	return s.sess.RevokeAllSessions(ctx, req)
}

// GetSession delegates to session subpackage.
func (s *Service) GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.GetSessionResponse, error) {
	return s.sess.GetSession(ctx, req)
}

// IssueSessionCode delegates to session subpackage. Used by the OAuth
// callback service to mint a one-time short code in place of leaking
// session_id via URL query (Referer / browser history / CDN logs).
func (s *Service) IssueSessionCode(ctx context.Context, req *pb.IssueSessionCodeRequest) (*pb.IssueSessionCodeResponse, error) {
	return s.sess.IssueSessionCode(ctx, req)
}

// ExchangeSessionCode delegates to session subpackage. Business side
// trades the one-time short code (issued by IssueSessionCode) for the
// underlying session_id + user_id, then sets its own domain cookie.
func (s *Service) ExchangeSessionCode(ctx context.Context, req *pb.ExchangeSessionCodeRequest) (*pb.ExchangeSessionCodeResponse, error) {
	return s.sess.ExchangeSessionCode(ctx, req)
}

// GetUser delegates to user subpackage.
func (s *Service) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.User, error) {
	return s.user.GetUser(ctx, req)
}

// CreateUser delegates to user subpackage.
func (s *Service) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.CreateUserResponse, error) {
	return s.user.CreateUser(ctx, req)
}

// ListUsers delegates to user subpackage.
func (s *Service) ListUsers(ctx context.Context, req *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
	return s.user.ListUsers(ctx, req)
}

// ListUsersPaged delegates to user subpackage.
func (s *Service) ListUsersPaged(ctx context.Context, req *pb.ListUsersPagedRequest) (*pb.ListUsersPagedResponse, error) {
	return s.user.ListUsersPaged(ctx, req)
}

// DisableUser delegates to user subpackage.
func (s *Service) DisableUser(ctx context.Context, req *pb.DisableUserRequest) (*pb.User, error) {
	return s.user.DisableUser(ctx, req)
}

// GetLoginLogs delegates to user subpackage.
func (s *Service) GetLoginLogs(ctx context.Context, req *pb.GetLoginLogsRequest) (*pb.GetLoginLogsResponse, error) {
	return s.user.GetLoginLogs(ctx, req)
}

// CreateGroup delegates to rbac subpackage.
func (s *Service) CreateGroup(ctx context.Context, req *pb.CreateGroupRequest) (*pb.Group, error) {
	return s.rbacSvc.CreateGroup(ctx, req)
}

// GetGroup delegates to rbac subpackage.
func (s *Service) GetGroup(ctx context.Context, req *pb.GetGroupRequest) (*pb.Group, error) {
	return s.rbacSvc.GetGroup(ctx, req)
}

// UpdateGroup delegates to rbac subpackage.
func (s *Service) UpdateGroup(ctx context.Context, req *pb.UpdateGroupRequest) (*pb.Group, error) {
	return s.rbacSvc.UpdateGroup(ctx, req)
}

// ListGroups delegates to rbac subpackage.
func (s *Service) ListGroups(ctx context.Context, req *pb.ListGroupsRequest) (*pb.ListGroupsResponse, error) {
	return s.rbacSvc.ListGroups(ctx, req)
}

// DeleteGroup delegates to rbac subpackage.
func (s *Service) DeleteGroup(ctx context.Context, req *pb.DeleteGroupRequest) (*emptypb.Empty, error) {
	return s.rbacSvc.DeleteGroup(ctx, req)
}

// AddGroupMember delegates to rbac subpackage.
func (s *Service) AddGroupMember(ctx context.Context, req *pb.AddGroupMemberRequest) (*emptypb.Empty, error) {
	return s.rbacSvc.AddGroupMember(ctx, req)
}

// RemoveGroupMember delegates to rbac subpackage.
func (s *Service) RemoveGroupMember(ctx context.Context, req *pb.RemoveGroupMemberRequest) (*emptypb.Empty, error) {
	return s.rbacSvc.RemoveGroupMember(ctx, req)
}

// ListGroupMembers delegates to rbac subpackage.
func (s *Service) ListGroupMembers(ctx context.Context, req *pb.ListGroupMembersRequest) (*pb.ListGroupMembersResponse, error) {
	return s.rbacSvc.ListGroupMembers(ctx, req)
}

// CreateRole delegates to rbac subpackage.
func (s *Service) CreateRole(ctx context.Context, req *pb.CreateRoleRequest) (*pb.Role, error) {
	return s.rbacSvc.CreateRole(ctx, req)
}

// UpdateRole delegates to rbac subpackage.
func (s *Service) UpdateRole(ctx context.Context, req *pb.UpdateRoleRequest) (*pb.Role, error) {
	return s.rbacSvc.UpdateRole(ctx, req)
}

// DeleteRole delegates to rbac subpackage.
func (s *Service) DeleteRole(ctx context.Context, req *pb.DeleteRoleRequest) (*emptypb.Empty, error) {
	return s.rbacSvc.DeleteRole(ctx, req)
}

// ListRoles delegates to rbac subpackage.
func (s *Service) ListRoles(ctx context.Context, req *pb.ListRolesRequest) (*pb.ListRolesResponse, error) {
	return s.rbacSvc.ListRoles(ctx, req)
}

// GetRole delegates to rbac subpackage.
func (s *Service) GetRole(ctx context.Context, req *pb.GetRoleRequest) (*pb.Role, error) {
	return s.rbacSvc.GetRole(ctx, req)
}

// ListPermissions delegates to rbac subpackage.
func (s *Service) ListPermissions(ctx context.Context, req *pb.ListPermissionsRequest) (*pb.ListPermissionsResponse, error) {
	return s.rbacSvc.ListPermissions(ctx, req)
}

// CreatePermission delegates to rbac subpackage.
func (s *Service) CreatePermission(ctx context.Context, req *pb.CreatePermissionRequest) (*pb.Permission, error) {
	return s.rbacSvc.CreatePermission(ctx, req)
}

// GetPermission delegates to rbac subpackage.
func (s *Service) GetPermission(ctx context.Context, req *pb.GetPermissionRequest) (*pb.Permission, error) {
	return s.rbacSvc.GetPermission(ctx, req)
}

// UpdatePermission delegates to rbac subpackage.
func (s *Service) UpdatePermission(ctx context.Context, req *pb.UpdatePermissionRequest) (*pb.Permission, error) {
	return s.rbacSvc.UpdatePermission(ctx, req)
}

// DeletePermission delegates to rbac subpackage.
func (s *Service) DeletePermission(ctx context.Context, req *pb.DeletePermissionRequest) (*emptypb.Empty, error) {
	return s.rbacSvc.DeletePermission(ctx, req)
}

// ListPermissionGroups delegates to rbac subpackage.
func (s *Service) ListPermissionGroups(ctx context.Context, req *pb.ListPermissionGroupsRequest) (*pb.ListPermissionGroupsResponse, error) {
	return s.rbacSvc.ListPermissionGroups(ctx, req)
}

// CreatePermissionGroup delegates to rbac subpackage.
func (s *Service) CreatePermissionGroup(ctx context.Context, req *pb.CreatePermissionGroupRequest) (*pb.PermissionGroup, error) {
	return s.rbacSvc.CreatePermissionGroup(ctx, req)
}

// GetPermissionGroup delegates to rbac subpackage.
func (s *Service) GetPermissionGroup(ctx context.Context, req *pb.GetPermissionGroupRequest) (*pb.PermissionGroup, error) {
	return s.rbacSvc.GetPermissionGroup(ctx, req)
}

// UpdatePermissionGroup delegates to rbac subpackage.
func (s *Service) UpdatePermissionGroup(ctx context.Context, req *pb.UpdatePermissionGroupRequest) (*pb.PermissionGroup, error) {
	return s.rbacSvc.UpdatePermissionGroup(ctx, req)
}

// DeletePermissionGroup delegates to rbac subpackage.
func (s *Service) DeletePermissionGroup(ctx context.Context, req *pb.DeletePermissionGroupRequest) (*emptypb.Empty, error) {
	return s.rbacSvc.DeletePermissionGroup(ctx, req)
}

// AddGroupRole delegates to rbac subpackage.
func (s *Service) AddGroupRole(ctx context.Context, req *pb.AddGroupRoleRequest) (*emptypb.Empty, error) {
	return s.rbacSvc.AddGroupRole(ctx, req)
}

// RemoveGroupRole delegates to rbac subpackage.
func (s *Service) RemoveGroupRole(ctx context.Context, req *pb.RemoveGroupRoleRequest) (*emptypb.Empty, error) {
	return s.rbacSvc.RemoveGroupRole(ctx, req)
}

// ListGroupRoles delegates to rbac subpackage.
func (s *Service) ListGroupRoles(ctx context.Context, req *pb.ListGroupRolesRequest) (*pb.ListGroupRolesResponse, error) {
	return s.rbacSvc.ListGroupRoles(ctx, req)
}

// AssignRole delegates to rbac subpackage.
func (s *Service) AssignRole(ctx context.Context, req *pb.AssignRoleRequest) (*emptypb.Empty, error) {
	return s.rbacSvc.AssignRole(ctx, req)
}

// RevokeRole delegates to rbac subpackage.
func (s *Service) RevokeRole(ctx context.Context, req *pb.RevokeRoleRequest) (*emptypb.Empty, error) {
	return s.rbacSvc.RevokeRole(ctx, req)
}

// ListUserRoles delegates to rbac subpackage.
func (s *Service) ListUserRoles(ctx context.Context, req *pb.ListUserRolesRequest) (*pb.ListUserRolesResponse, error) {
	return s.rbacSvc.ListUserRoles(ctx, req)
}
