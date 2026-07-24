package rbac

import (
	"context"
	"fmt"
	"strings"

	pb "github.com/servekit/user-service/gen/user/v1"
	"github.com/servekit/user-service/internal/cache"
	"github.com/servekit/user-service/internal/store/dal"
	"github.com/servekit/user-service/pkg/xcodes"

	"google.golang.org/protobuf/types/known/emptypb"
)

// --- Permissions ---

// ListPermissions returns all permissions.
func (s *Service) ListPermissions(ctx context.Context, _ *emptypb.Empty) (*pb.ListPermissionsResponse, error) {
	perms, err := dal.ListAllUserPermissions(ctx, s.db)
	if err != nil {
		return nil, err
	}
	pbPerms := make([]*pb.Permission, len(perms))
	for i, p := range perms {
		pbPerms[i] = &pb.Permission{Id: p.ID, Resource: p.Resource, Action: p.Action, Description: p.Description}
	}
	return &pb.ListPermissionsResponse{Permissions: pbPerms}, nil
}

// ListPermissionGroups returns all permission groups.
func (s *Service) ListPermissionGroups(ctx context.Context, _ *emptypb.Empty) (*pb.ListPermissionGroupsResponse, error) {
	pgs, err := dal.ListAllUserPermissionGroups(ctx, s.db)
	if err != nil {
		return nil, err
	}
	pbGroups := make([]*pb.PermissionGroup, len(pgs))
	for i, pg := range pgs {
		pbGroups[i] = &pb.PermissionGroup{Id: pg.ID, Name: pg.Name, Description: pg.Description}
	}
	return &pb.ListPermissionGroupsResponse{Groups: pbGroups}, nil
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
