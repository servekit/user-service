package rbac

import (
	"context"
	"errors"
	"fmt"
	"strings"

	pb "github.com/servekit/user-service/gen/user/v1"
	"github.com/servekit/user-service/internal/cache"
	"github.com/servekit/user-service/internal/store/dal"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
)

// --- Permissions ---

// CreatePermission adds a custom (non-builtin) permission.
func (s *Service) CreatePermission(ctx context.Context, req *pb.CreatePermissionRequest) (*pb.Permission, error) {
	if _, err := dal.GetUserPermissionByResourceAction(ctx, s.db, req.Resource, req.Action); err == nil {
		return nil, xcodes.ErrPermissionExists.New()
	} else if !errors.Is(err, xcodes.ErrPermissionNotFound.New()) {
		return nil, err
	}
	perm := &models.UserPermission{Resource: req.Resource, Action: req.Action, Description: req.Description}
	if err := dal.CreateUserPermission(ctx, s.db, perm); err != nil {
		return nil, err
	}
	return permissionModelToProto(perm), nil
}

// GetPermission returns a permission by ID.
func (s *Service) GetPermission(ctx context.Context, req *pb.GetPermissionRequest) (*pb.Permission, error) {
	perm, err := dal.GetUserPermissionByID(ctx, s.db, req.PermissionId)
	if err != nil {
		return nil, err
	}
	return permissionModelToProto(perm), nil
}

// UpdatePermission updates a permission; builtin permissions are rejected.
func (s *Service) UpdatePermission(ctx context.Context, req *pb.UpdatePermissionRequest) (*pb.Permission, error) {
	perm, err := dal.GetUserPermissionByID(ctx, s.db, req.PermissionId)
	if err != nil {
		return nil, err
	}
	if perm.IsBuiltin {
		return nil, xcodes.ErrPermissionIsBuiltin.New()
	}
	newResource := perm.Resource
	if req.Resource != "" {
		newResource = req.Resource
	}
	newAction := perm.Action
	if req.Action != "" {
		newAction = req.Action
	}
	if newResource != perm.Resource || newAction != perm.Action {
		if existing, err := dal.GetUserPermissionByResourceAction(ctx, s.db, newResource, newAction); err == nil {
			if existing.ID != perm.ID {
				return nil, xcodes.ErrPermissionExists.New()
			}
		} else if !errors.Is(err, xcodes.ErrPermissionNotFound.New()) {
			return nil, err
		}
	}
	if req.Resource != "" {
		perm.Resource = req.Resource
	}
	if req.Action != "" {
		perm.Action = req.Action
	}
	if req.Description != "" {
		perm.Description = req.Description
	}
	if err := dal.UpdateUserPermission(ctx, s.db, perm); err != nil {
		return nil, err
	}
	if err := s.invalidatePermissionCache(ctx, req.PermissionId); err != nil {
		return nil, err
	}
	return permissionModelToProto(perm), nil
}

// DeletePermission removes a permission; builtin permissions are rejected.
func (s *Service) DeletePermission(ctx context.Context, req *pb.DeletePermissionRequest) (*emptypb.Empty, error) {
	perm, err := dal.GetUserPermissionByID(ctx, s.db, req.PermissionId)
	if err != nil {
		return nil, err
	}
	if perm.IsBuiltin {
		return nil, xcodes.ErrPermissionIsBuiltin.New()
	}
	if err := s.invalidatePermissionCache(ctx, req.PermissionId); err != nil {
		return nil, err
	}
	if err := dal.DeleteUserPermission(ctx, s.db, req.PermissionId); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ListPermissions returns cursor-paginated permissions.
func (s *Service) ListPermissions(ctx context.Context, req *pb.ListPermissionsRequest) (*pb.ListPermissionsResponse, error) {
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	perms, nextCursor, err := dal.ListUserPermissions(ctx, s.db, req.Cursor, pageSize)
	if err != nil {
		return nil, err
	}
	pbPerms := make([]*pb.Permission, len(perms))
	for i, p := range perms {
		pbPerms[i] = permissionModelToProto(p)
	}
	return &pb.ListPermissionsResponse{Permissions: pbPerms, NextCursor: nextCursor}, nil
}

// --- cache invalidation helpers ---

// invalidatePermissionCache drops resolved-permission caches for every user who
// holds the permission via any role (direct or via a permission group).
func (s *Service) invalidatePermissionCache(ctx context.Context, permID int64) error {
	userIDs, err := s.getUserIDsByPermission(ctx, permID)
	if err != nil {
		return err
	}
	for _, uid := range userIDs {
		if err := s.cache.InvalidateUser(ctx, uid); err != nil {
			return err
		}
	}
	return nil
}

// getUserIDsByPermission resolves permission → roles (direct + via groups) → users.
func (s *Service) getUserIDsByPermission(ctx context.Context, permID int64) ([]int64, error) {
	roleSet := make(map[int64]struct{})

	direct, err := dal.ListRolePermissionMappingsByPermissionID(ctx, s.db, permID)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	for _, rp := range direct {
		roleSet[rp.RoleID] = struct{}{}
	}

	groupIDs, err := dal.ListPermissionGroupIDsByItemPermissionID(ctx, s.db, permID)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	for _, pgid := range groupIDs {
		rpgs, err := dal.ListRolePermissionGroupMappingsByPermissionGroupID(ctx, s.db, pgid)
		if err != nil {
			return nil, xcodes.ErrInternal.Wrap(err)
		}
		for _, rpg := range rpgs {
			roleSet[rpg.RoleID] = struct{}{}
		}
	}

	userSet := make(map[int64]struct{})
	for roleID := range roleSet {
		users, err := s.getUserIDsByRole(ctx, roleID)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			userSet[u] = struct{}{}
		}
	}
	out := make([]int64, 0, len(userSet))
	for u := range userSet {
		out = append(out, u)
	}
	return out, nil
}

// --- PermissionGroups ---

// CreatePermissionGroup creates a custom permission group and attaches the given permissions.
func (s *Service) CreatePermissionGroup(ctx context.Context, req *pb.CreatePermissionGroupRequest) (*pb.PermissionGroup, error) {
	if _, err := dal.GetUserPermissionGroupByName(ctx, s.db, req.Name); err == nil {
		return nil, xcodes.ErrPermissionGroupExists.New()
	} else if !errors.Is(err, xcodes.ErrPermissionGroupNotFound.New()) {
		return nil, err
	}
	pg := &models.UserPermissionGroup{Name: req.Name, Description: req.Description}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := dal.CreateUserPermissionGroup(ctx, tx, pg); err != nil {
			return err
		}
		for _, pid := range req.PermissionIds {
			if err := dal.AddPermissionToGroup(ctx, tx, pg.ID, pid); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return s.permissionGroupWithItems(ctx, pg)
}

// GetPermissionGroup returns a permission group with its permissions populated.
func (s *Service) GetPermissionGroup(ctx context.Context, req *pb.GetPermissionGroupRequest) (*pb.PermissionGroup, error) {
	pg, err := dal.GetUserPermissionGroupByID(ctx, s.db, req.PermissionGroupId)
	if err != nil {
		return nil, err
	}
	return s.permissionGroupWithItems(ctx, pg)
}

// UpdatePermissionGroup updates name/description and FULLY REPLACES the group's permission set.
func (s *Service) UpdatePermissionGroup(ctx context.Context, req *pb.UpdatePermissionGroupRequest) (*pb.PermissionGroup, error) {
	pg, err := dal.GetUserPermissionGroupByID(ctx, s.db, req.PermissionGroupId)
	if err != nil {
		return nil, err
	}
	if pg.IsBuiltin {
		return nil, xcodes.ErrPermissionGroupIsBuiltin.New()
	}
	newName := pg.Name
	if req.Name != "" {
		newName = req.Name
	}
	if newName != pg.Name {
		if existing, err := dal.GetUserPermissionGroupByName(ctx, s.db, newName); err == nil {
			if existing.ID != pg.ID {
				return nil, xcodes.ErrPermissionGroupExists.New()
			}
		} else if !errors.Is(err, xcodes.ErrPermissionGroupNotFound.New()) {
			return nil, err
		}
	}
	if req.Name != "" {
		pg.Name = req.Name
	}
	if req.Description != "" {
		pg.Description = req.Description
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := dal.UpdateUserPermissionGroup(ctx, tx, pg); err != nil {
			return err
		}
		existing, err := dal.ListUserPermissionsByPermissionGroupID(ctx, tx, pg.ID)
		if err != nil {
			return err
		}
		for _, p := range existing {
			if err := dal.RemovePermissionFromGroup(ctx, tx, pg.ID, p.ID); err != nil {
				return err
			}
		}
		for _, pid := range req.PermissionIds {
			if err := dal.AddPermissionToGroup(ctx, tx, pg.ID, pid); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.invalidatePermissionGroupCache(ctx, pg.ID); err != nil {
		return nil, err
	}
	return s.permissionGroupWithItems(ctx, pg)
}

// DeletePermissionGroup removes a permission group; builtin groups are rejected.
func (s *Service) DeletePermissionGroup(ctx context.Context, req *pb.DeletePermissionGroupRequest) (*emptypb.Empty, error) {
	pg, err := dal.GetUserPermissionGroupByID(ctx, s.db, req.PermissionGroupId)
	if err != nil {
		return nil, err
	}
	if pg.IsBuiltin {
		return nil, xcodes.ErrPermissionGroupIsBuiltin.New()
	}
	if err := s.invalidatePermissionGroupCache(ctx, req.PermissionGroupId); err != nil {
		return nil, err
	}
	if err := dal.DeleteUserPermissionGroup(ctx, s.db, req.PermissionGroupId); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ListPermissionGroups returns cursor-paginated permission groups (without items).
func (s *Service) ListPermissionGroups(ctx context.Context, req *pb.ListPermissionGroupsRequest) (*pb.ListPermissionGroupsResponse, error) {
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	pgs, nextCursor, err := dal.ListUserPermissionGroups(ctx, s.db, req.Cursor, pageSize)
	if err != nil {
		return nil, err
	}
	pbGroups := make([]*pb.PermissionGroup, len(pgs))
	for i, pg := range pgs {
		pbGroups[i] = permissionGroupModelToProto(pg)
	}
	return &pb.ListPermissionGroupsResponse{Groups: pbGroups, NextCursor: nextCursor}, nil
}

// permissionGroupWithItems builds a PermissionGroup proto with its permissions populated.
func (s *Service) permissionGroupWithItems(ctx context.Context, pg *models.UserPermissionGroup) (*pb.PermissionGroup, error) {
	out := permissionGroupModelToProto(pg)
	perms, err := dal.ListUserPermissionsByPermissionGroupID(ctx, s.db, pg.ID)
	if err != nil {
		return nil, err
	}
	pbPerms := make([]*pb.Permission, len(perms))
	for i, p := range perms {
		pbPerms[i] = permissionModelToProto(p)
	}
	out.Permissions = pbPerms
	return out, nil
}

// invalidatePermissionGroupCache drops caches for users holding any role that references this group.
func (s *Service) invalidatePermissionGroupCache(ctx context.Context, pgID int64) error {
	rpgs, err := dal.ListRolePermissionGroupMappingsByPermissionGroupID(ctx, s.db, pgID)
	if err != nil {
		return xcodes.ErrInternal.Wrap(err)
	}
	roleSet := make(map[int64]struct{})
	for _, rpg := range rpgs {
		roleSet[rpg.RoleID] = struct{}{}
	}
	userSet := make(map[int64]struct{})
	for roleID := range roleSet {
		users, err := s.getUserIDsByRole(ctx, roleID)
		if err != nil {
			return err
		}
		for _, u := range users {
			userSet[u] = struct{}{}
		}
	}
	for uid := range userSet {
		if err := s.cache.InvalidateUser(ctx, uid); err != nil {
			return err
		}
	}
	return nil
}

// --- internal API used by middleware / cache ---

// GetUserPermissions returns all permissions for a user acquired through roles (direct + group-inherited).
// Checks the resolved-permissions cache first; on miss, resolves via GetUserRoles
// (which itself is cached) and populates the perms cache.
func (s *Service) GetUserPermissions(ctx context.Context, userID int64) ([]cache.PermissionEntry, error) {
	if cached, err := s.cache.GetUserPermissions(ctx, userID); err == nil && cached != nil {
		result := make([]cache.PermissionEntry, 0, len(cached))
		for k := range cached {
			resource, action, _ := strings.Cut(k, ":")
			result = append(result, cache.PermissionEntry{Resource: resource, Action: action})
		}
		return result, nil
	}

	roleIDs, err := s.GetUserRoles(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(roleIDs) == 0 {
		return nil, nil
	}

	permIDs, err := s.collectPermissionIDs(ctx, roleIDs)
	if err != nil {
		return nil, err
	}
	if len(permIDs) == 0 {
		return nil, nil
	}

	perms, err := s.resolvePermissions(ctx, permIDs)
	if err != nil {
		return nil, err
	}
	_ = s.cache.SetUserPermissions(ctx, userID, perms) //nolint:errcheck // cache write is best-effort
	return perms, nil
}

// GetUserRoles returns all role IDs for a user (direct + group-inherited).
// Read-through cache: cache first, DB fallback via collectRoleIDs, populate cache on miss.
func (s *Service) GetUserRoles(ctx context.Context, userID int64) ([]int64, error) {
	if cached, err := s.cache.GetUserRoles(ctx, userID); err == nil && cached != nil {
		return cached, nil
	}
	roleIDs, err := s.collectRoleIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	_ = s.cache.SetUserRoles(ctx, userID, roleIDs) //nolint:errcheck // cache write is best-effort
	return roleIDs, nil
}

// GetUserGroups returns all group memberships for a user with their in-group role.
func (s *Service) GetUserGroups(ctx context.Context, userID int64) ([]UserGroupEntry, error) {
	ugs, err := dal.ListUserGroupMappingsByUserID(ctx, s.db, userID)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	result := make([]UserGroupEntry, len(ugs))
	for i, ug := range ugs {
		result[i] = UserGroupEntry{GroupID: ug.GroupID, Role: ug.Role}
	}
	return result, nil
}

// --- internal helpers ---

func (s *Service) collectRoleIDs(ctx context.Context, userID int64) ([]int64, error) {
	roleSet := make(map[int64]struct{})

	directURs, err := dal.ListUserRoleMappingsByUserID(ctx, s.db, userID)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	for _, ur := range directURs {
		roleSet[ur.RoleID] = struct{}{}
	}

	groups, err := dal.ListUserGroupMappingsByUserID(ctx, s.db, userID)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	for _, ug := range groups {
		groupRoles, err := dal.ListGroupRoleMappingsByGroupID(ctx, s.db, ug.GroupID)
		if err != nil {
			return nil, fmt.Errorf("group roles for group %d: %w", ug.GroupID, err)
		}
		for _, gr := range groupRoles {
			roleSet[gr.RoleID] = struct{}{}
		}
	}

	roleIDs := make([]int64, 0, len(roleSet))
	for id := range roleSet {
		roleIDs = append(roleIDs, id)
	}
	return roleIDs, nil
}

func (s *Service) collectPermissionIDs(ctx context.Context, roleIDs []int64) (map[int64]struct{}, error) {
	permSet := make(map[int64]struct{})

	for _, roleID := range roleIDs {
		rps, err := dal.ListRolePermissionMappingsByRoleID(ctx, s.db, roleID)
		if err != nil {
			return nil, fmt.Errorf("role permissions for role %d: %w", roleID, err)
		}
		for _, rp := range rps {
			permSet[rp.PermissionID] = struct{}{}
		}

		rpgs, err := dal.ListRolePermissionGroupMappingsByRoleID(ctx, s.db, roleID)
		if err != nil {
			return nil, fmt.Errorf("role perm groups for role %d: %w", roleID, err)
		}
		for _, rpg := range rpgs {
			pgPerms, err := dal.ListUserPermissionsByPermissionGroupID(ctx, s.db, rpg.PermissionGroupID)
			if err != nil {
				return nil, fmt.Errorf("permissions in group %d: %w", rpg.PermissionGroupID, err)
			}
			for _, p := range pgPerms {
				permSet[p.ID] = struct{}{}
			}
		}
	}
	return permSet, nil
}

func (s *Service) resolvePermissions(ctx context.Context, permIDs map[int64]struct{}) ([]cache.PermissionEntry, error) {
	seen := make(map[string]struct{})
	result := make([]cache.PermissionEntry, 0, len(permIDs))

	for permID := range permIDs {
		perm, err := dal.GetUserPermissionByID(ctx, s.db, permID)
		if err != nil {
			continue
		}
		key := perm.Resource + ":" + perm.Action
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, cache.PermissionEntry{Resource: perm.Resource, Action: perm.Action})
	}
	return result, nil
}
