package rbac

import (
	"context"

	pb "github.com/servekit/api/gen/go/user/v1"
	gidservice "github.com/servekit/gid-service/pkg"
	"github.com/servekit/user-service/internal/store/dal"
	"github.com/servekit/user-service/internal/store/models"
	"github.com/servekit/user-service/pkg/xcodes"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- Groups ---

// CreateGroup creates a new group.
func (s *Service) CreateGroup(ctx context.Context, req *pb.CreateGroupRequest) (*pb.Group, error) {
	groupID, err := gidservice.NextID(ctx, s.gid)
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}

	group := &models.UserGroup{Name: req.Name, Description: req.Description}
	group.ID = groupID
	if req.ParentId != 0 {
		group.ParentID = &req.ParentId
	}
	if err := dal.CreateUserGroup(ctx, s.db, group); err != nil {
		return nil, err
	}
	return groupModelToProto(group), nil
}

// GetGroup returns a group by ID.
func (s *Service) GetGroup(ctx context.Context, req *pb.GetGroupRequest) (*pb.Group, error) {
	group, err := dal.GetUserGroupByID(ctx, s.db, req.GroupId)
	if err != nil {
		return nil, err
	}
	return groupModelToProto(group), nil
}

// UpdateGroup updates a group.
func (s *Service) UpdateGroup(ctx context.Context, req *pb.UpdateGroupRequest) (*pb.Group, error) {
	group, err := dal.GetUserGroupByID(ctx, s.db, req.GroupId)
	if err != nil {
		return nil, err
	}
	if req.Name != "" {
		group.Name = req.Name
	}
	if req.Description != "" {
		group.Description = req.Description
	}
	if err := dal.UpdateUserGroup(ctx, s.db, group); err != nil {
		return nil, err
	}
	return groupModelToProto(group), nil
}

// DeleteGroup deletes a group.
func (s *Service) DeleteGroup(ctx context.Context, req *pb.DeleteGroupRequest) (*emptypb.Empty, error) {
	if err := dal.DeleteUserGroup(ctx, s.db, req.GroupId); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ListGroups returns a paginated list of groups.
func (s *Service) ListGroups(ctx context.Context, req *pb.ListGroupsRequest) (*pb.ListGroupsResponse, error) {
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	groups, nextCursor, err := dal.ListUserGroups(ctx, s.db, req.Status, req.Cursor, pageSize)
	if err != nil {
		return nil, err
	}
	pbGroups := make([]*pb.Group, len(groups))
	for i, g := range groups {
		pbGroups[i] = groupModelToProto(g)
	}
	return &pb.ListGroupsResponse{Groups: pbGroups, NextCursor: nextCursor}, nil
}

// --- Group Members ---

// AddGroupMember adds a member to a group.
func (s *Service) AddGroupMember(ctx context.Context, req *pb.AddGroupMemberRequest) (*emptypb.Empty, error) {
	ug := &models.UserGroupMapping{UserID: req.UserId, GroupID: req.GroupId, Role: req.Role}
	if err := dal.AddUserGroupMapping(ctx, s.db, ug); err != nil {
		return nil, err
	}
	if err := s.permCache.InvalidateUser(ctx, req.UserId); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// RemoveGroupMember removes a member from a group.
func (s *Service) RemoveGroupMember(ctx context.Context, req *pb.RemoveGroupMemberRequest) (*emptypb.Empty, error) {
	if err := dal.RemoveUserGroupMapping(ctx, s.db, req.UserId, req.GroupId); err != nil {
		return nil, err
	}
	if err := s.permCache.InvalidateUser(ctx, req.UserId); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ListGroupMembers returns members of a group.
func (s *Service) ListGroupMembers(ctx context.Context, req *pb.ListGroupMembersRequest) (*pb.ListGroupMembersResponse, error) {
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	ugs, nextCursor, err := dal.ListUserGroupMappingsByGroupID(ctx, s.db, req.GroupId, req.Cursor, pageSize)
	if err != nil {
		return nil, err
	}
	members := make([]*pb.GroupMember, len(ugs))
	for i, ug := range ugs {
		members[i] = &pb.GroupMember{
			UserId:    ug.UserID,
			Role:      ug.Role,
			CreatedAt: timestamppb.New(ug.CreatedAt),
		}
	}
	return &pb.ListGroupMembersResponse{Members: members, NextCursor: nextCursor}, nil
}

// --- internal helpers ---

// getGroupMemberIDs returns all user IDs that are members of a group.
func (s *Service) getGroupMemberIDs(ctx context.Context, groupID int64) ([]int64, error) {
	userIDSet := make(map[int64]struct{})
	var cursor string
	for {
		members, nextCursor, err := dal.ListUserGroupMappingsByGroupID(ctx, s.db, groupID, cursor, 1000)
		if err != nil {
			return nil, xcodes.ErrInternal.Wrap(err)
		}
		for _, m := range members {
			userIDSet[m.UserID] = struct{}{}
		}
		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}
	userIDs := make([]int64, 0, len(userIDSet))
	for id := range userIDSet {
		userIDs = append(userIDs, id)
	}
	return userIDs, nil
}
