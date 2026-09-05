package rbac

import (
	"context"
	"errors"
	"fmt"
	"time"

	pb "github.com/servekit/api/gen/go/user/v1"
	"github.com/servekit/user-service/internal/store/dal"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

// --- Roles ---

// CreateRole creates a new role.
func (s *Service) CreateRole(ctx context.Context, req *pb.CreateRoleRequest) (*pb.Role, error) {
	role := &models.UserRole{Name: req.Name, Description: req.Description}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := dal.CreateUserRole(ctx, tx, role); err != nil {
			return err
		}
		for _, pid := range req.PermissionIds {
			if err := dal.AssignRolePermissionMapping(ctx, tx, role.ID, pid); err != nil {
				return err
			}
		}
		for _, pgid := range req.PermissionGroupIds {
			if err := dal.AssignRolePermissionGroupMapping(ctx, tx, role.ID, pgid); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return roleModelToProto(role), nil
}

// UpdateRole updates a role.
func (s *Service) UpdateRole(ctx context.Context, req *pb.UpdateRoleRequest) (*pb.Role, error) {
	role, err := dal.GetUserRoleByID(ctx, s.db, req.RoleId)
	if err != nil {
		return nil, err
	}
	if req.Name != "" {
		role.Name = req.Name
	}
	if req.Description != "" {
		role.Description = req.Description
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := dal.UpdateUserRole(ctx, tx, role); err != nil {
			return err
		}

		// Replace permissions (full replacement)
		existingRPs, err := dal.ListRolePermissionMappingsByRoleID(ctx, tx, role.ID)
		if err != nil {
			return err
		}
		for _, rp := range existingRPs {
			if err := dal.RemoveRolePermissionMapping(ctx, tx, role.ID, rp.PermissionID); err != nil {
				return err
			}
		}
		for _, pid := range req.PermissionIds {
			if err := dal.AssignRolePermissionMapping(ctx, tx, role.ID, pid); err != nil {
				return err
			}
		}

		// Replace permission groups
		existingRPGs, err := dal.ListRolePermissionGroupMappingsByRoleID(ctx, tx, role.ID)
		if err != nil {
			return err
		}
		for _, rpg := range existingRPGs {
			if err := dal.RemoveRolePermissionGroupMapping(ctx, tx, role.ID, rpg.PermissionGroupID); err != nil {
				return err
			}
		}
		for _, pgid := range req.PermissionGroupIds {
			if err := dal.AssignRolePermissionGroupMapping(ctx, tx, role.ID, pgid); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Invalidate cache for affected users
	userIDs, err := s.getUserIDsByRole(ctx, role.ID)
	if err != nil {
		return nil, err
	}
	if err := s.cache.InvalidateRole(ctx, role.ID, userIDs); err != nil {
		return nil, err
	}

	return roleModelToProto(role), nil
}

// DeleteRole deletes a role.
func (s *Service) DeleteRole(ctx context.Context, req *pb.DeleteRoleRequest) (*emptypb.Empty, error) {
	// Get affected users BEFORE delete (after delete, the lookup would return nothing)
	userIDs, err := s.getUserIDsByRole(ctx, req.RoleId)
	if err != nil {
		return nil, err
	}
	if err := dal.DeleteUserRole(ctx, s.db, req.RoleId); err != nil {
		return nil, err
	}
	if err := s.cache.InvalidateRole(ctx, req.RoleId, userIDs); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ListRoles returns a paginated list of roles.
func (s *Service) ListRoles(ctx context.Context, req *pb.ListRolesRequest) (*pb.ListRolesResponse, error) {
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	roles, nextCursor, err := dal.ListUserRoles(ctx, s.db, req.Cursor, pageSize)
	if err != nil {
		return nil, err
	}
	pbRoles := make([]*pb.Role, len(roles))
	for i, r := range roles {
		pbRoles[i] = roleModelToProto(r)
	}
	return &pb.ListRolesResponse{Roles: pbRoles, NextCursor: nextCursor}, nil
}

// GetRole returns a role by ID with its permissions and permission groups populated.
func (s *Service) GetRole(ctx context.Context, req *pb.GetRoleRequest) (*pb.Role, error) {
	role, err := dal.GetUserRoleByID(ctx, s.db, req.RoleId)
	if err != nil {
		return nil, err
	}
	return s.roleModelToProtoFull(ctx, role)
}

// --- User Roles ---

// AssignRole assigns a role to a user.
func (s *Service) AssignRole(ctx context.Context, req *pb.AssignRoleRequest) (*emptypb.Empty, error) {
	ur := &models.UserRoleMapping{UserID: req.UserId, RoleID: req.RoleId}
	if err := dal.AssignUserRoleMapping(ctx, s.db, ur); err != nil {
		return nil, err
	}
	if err := s.cache.InvalidateUser(ctx, req.UserId); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// RevokeRole revokes a role from a user.
func (s *Service) RevokeRole(ctx context.Context, req *pb.RevokeRoleRequest) (*emptypb.Empty, error) {
	if err := dal.RemoveUserRoleMapping(ctx, s.db, req.UserId, req.RoleId); err != nil {
		return nil, err
	}
	if err := s.cache.InvalidateUser(ctx, req.UserId); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ListUserRoles returns roles assigned to a user, including roles inherited
// via group membership. The source field on each UserRole distinguishes
// direct assignments ("direct") from group-derived roles
// ("group:<group_name>").
func (s *Service) ListUserRoles(ctx context.Context, req *pb.ListUserRolesRequest) (*pb.ListUserRolesResponse, error) {
	// Direct assignments.
	directMappings, err := dal.ListUserRoleMappingsByUserID(ctx, s.db, req.UserId)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	// Group memberships → for each group, expand its roles.
	groupMappings, err := dal.ListUserGroupMappingsByUserID(ctx, s.db, req.UserId)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	type groupSource struct {
		groupID   int64
		groupName string
	}
	groupByID := make(map[int64]groupSource, len(groupMappings))
	for _, ug := range groupMappings {
		group, err := dal.GetUserGroupByID(ctx, s.db, ug.GroupID)
		if err != nil {
			return nil, err
		}
		groupByID[ug.GroupID] = groupSource{groupID: group.ID, groupName: group.Name}
	}

	// Build (roleID → {mappingID, source}) preserving discovery order. When a
	// role arrives both directly and via a group, the direct assignment wins so
	// admins see the canonical mapping id and source.
	type entry struct {
		mappingID int64 // 0 when group-derived
		source    string
		createdAt time.Time
	}
	seen := make(map[int64]*entry)
	order := make([]int64, 0, len(directMappings)+len(groupMappings))
	for _, ur := range directMappings {
		if _, ok := seen[ur.RoleID]; !ok {
			seen[ur.RoleID] = &entry{mappingID: ur.ID, source: "direct", createdAt: ur.CreatedAt}
			order = append(order, ur.RoleID)
		}
	}
	for _, ug := range groupMappings {
		groupRoles, err := dal.ListGroupRoleMappingsByGroupID(ctx, s.db, ug.GroupID)
		if err != nil {
			return nil, xcodes.ErrInternal.Wrap(err)
		}
		src := fmt.Sprintf("group:%s", groupByID[ug.GroupID].groupName)
		for _, gr := range groupRoles {
			if _, ok := seen[gr.RoleID]; !ok {
				seen[gr.RoleID] = &entry{source: src, createdAt: gr.CreatedAt}
				order = append(order, gr.RoleID)
			}
		}
	}

	// Materialize role rows once for names.
	roleCache := make(map[int64]*models.UserRole, len(order))
	for _, roleID := range order {
		role, err := dal.GetUserRoleByID(ctx, s.db, roleID)
		if err != nil {
			return nil, err
		}
		roleCache[roleID] = role
	}

	out := make([]*pb.UserRole, 0, len(order))
	for _, roleID := range order {
		e := seen[roleID]
		ur := &pb.UserRole{
			Id:        e.mappingID,
			RoleId:    roleID,
			RoleName:  roleCache[roleID].Name,
			Source:    e.source,
			CreatedAt: timestamppb.New(e.createdAt),
		}
		out = append(out, ur)
	}
	return &pb.ListUserRolesResponse{Roles: out}, nil
}

// --- Group Roles ---

// AddGroupRole assigns a role to a group.
func (s *Service) AddGroupRole(ctx context.Context, req *pb.AddGroupRoleRequest) (*emptypb.Empty, error) {
	if err := dal.AssignGroupRoleMapping(ctx, s.db, req.GroupId, req.RoleId); err != nil {
		return nil, err
	}
	memberIDs, err := s.getGroupMemberIDs(ctx, req.GroupId)
	if err != nil {
		return nil, err
	}
	if err := s.cache.InvalidateGroup(ctx, req.GroupId, memberIDs); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// RemoveGroupRole removes a role from a group.
func (s *Service) RemoveGroupRole(ctx context.Context, req *pb.RemoveGroupRoleRequest) (*emptypb.Empty, error) {
	if err := dal.RemoveGroupRoleMapping(ctx, s.db, req.GroupId, req.RoleId); err != nil {
		return nil, err
	}
	memberIDs, err := s.getGroupMemberIDs(ctx, req.GroupId)
	if err != nil {
		return nil, err
	}
	if err := s.cache.InvalidateGroup(ctx, req.GroupId, memberIDs); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ListGroupRoles returns roles attached to a group.
func (s *Service) ListGroupRoles(ctx context.Context, req *pb.ListGroupRolesRequest) (*pb.ListGroupRolesResponse, error) {
	mappings, err := dal.ListGroupRoleMappingsByGroupID(ctx, s.db, req.GroupId)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	out := make([]*pb.Role, 0, len(mappings))
	for _, gr := range mappings {
		role, err := dal.GetUserRoleByID(ctx, s.db, gr.RoleID)
		if err != nil {
			return nil, err
		}
		out = append(out, roleModelToProto(role))
	}
	return &pb.ListGroupRolesResponse{Roles: out}, nil
}

// --- internal helpers ---

// roleModelToProtoFull builds a Role proto with permissions + permission groups
// populated. Dangling mapping references (pointing at deleted permissions or
// groups) are skipped rather than failing the read.
func (s *Service) roleModelToProtoFull(ctx context.Context, r *models.UserRole) (*pb.Role, error) {
	out := roleModelToProto(r)

	rps, err := dal.ListRolePermissionMappingsByRoleID(ctx, s.db, r.ID)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	perms := make([]*pb.Permission, 0, len(rps))
	for _, rp := range rps {
		p, err := dal.GetUserPermissionByID(ctx, s.db, rp.PermissionID)
		if err != nil {
			if errors.Is(err, xcodes.ErrPermissionNotFound.New()) {
				continue
			}
			return nil, err
		}
		perms = append(perms, permissionModelToProto(p))
	}
	out.Permissions = perms

	rpgs, err := dal.ListRolePermissionGroupMappingsByRoleID(ctx, s.db, r.ID)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	groups := make([]*pb.PermissionGroup, 0, len(rpgs))
	for _, rpg := range rpgs {
		pg, err := dal.GetUserPermissionGroupByID(ctx, s.db, rpg.PermissionGroupID)
		if err != nil {
			if errors.Is(err, xcodes.ErrPermissionGroupNotFound.New()) {
				continue
			}
			return nil, err
		}
		groups = append(groups, permissionGroupModelToProto(pg))
	}
	out.PermGroups = groups

	return out, nil
}

// getUserIDsByRole returns all user IDs that have a specific role (direct + via group).
func (s *Service) getUserIDsByRole(ctx context.Context, roleID int64) ([]int64, error) {
	directURs, err := dal.ListUserRoleMappingsByRoleID(ctx, s.db, roleID)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	userIDSet := make(map[int64]struct{})
	for _, ur := range directURs {
		userIDSet[ur.UserID] = struct{}{}
	}

	groupRoles, err := dal.ListGroupRoleMappingsByRoleID(ctx, s.db, roleID)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	for _, gr := range groupRoles {
		members, _, err := dal.ListUserGroupMappingsByGroupID(ctx, s.db, gr.GroupID, "", 1000)
		if err != nil {
			return nil, xcodes.ErrInternal.Wrap(err)
		}
		for _, m := range members {
			userIDSet[m.UserID] = struct{}{}
		}
	}

	userIDs := make([]int64, 0, len(userIDSet))
	for id := range userIDSet {
		userIDs = append(userIDs, id)
	}
	return userIDs, nil
}
