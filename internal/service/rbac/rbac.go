package rbac

import (
	gidservice "github.com/servekit/gid-service/pkg"
	pb "github.com/servekit/user-service/gen/user/v1"
	"github.com/servekit/user-service/internal/cache"
	"github.com/servekit/user-service/internal/store/models"

	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

// Service handles RBAC management RPCs.
type Service struct {
	db    *gorm.DB
	cache *cache.RBACCache
	gid   gidservice.Service
}

// New creates a new rbac Service.
func New(
	db *gorm.DB,
	cache *cache.RBACCache,
	gid gidservice.Service,
) *Service {
	return &Service{
		db:    db,
		cache: cache,
		gid:   gid,
	}
}

// UserGroupEntry represents a user's membership in a group with their in-group role.
type UserGroupEntry struct {
	GroupID int64
	Role    string // owner / admin / member
}

// --- model to proto helpers ---

func groupModelToProto(g *models.UserGroup) *pb.Group {
	p := &pb.Group{
		Id: g.ID, Name: g.Name, Description: g.Description, Status: g.Status,
	}
	if g.ParentID != nil {
		p.ParentId = *g.ParentID
	}
	if !g.CreatedAt.IsZero() {
		p.CreatedAt = timestamppb.New(g.CreatedAt)
	}
	if !g.UpdatedAt.IsZero() {
		p.UpdatedAt = timestamppb.New(g.UpdatedAt)
	}
	return p
}

func roleModelToProto(r *models.UserRole) *pb.Role {
	p := &pb.Role{
		Id: r.ID, Name: r.Name, Description: r.Description, IsBuiltin: r.IsBuiltin,
	}
	if !r.CreatedAt.IsZero() {
		p.CreatedAt = timestamppb.New(r.CreatedAt)
	}
	if !r.UpdatedAt.IsZero() {
		p.UpdatedAt = timestamppb.New(r.UpdatedAt)
	}
	return p
}

func permissionModelToProto(p *models.UserPermission) *pb.Permission {
	return &pb.Permission{
		Id:          p.ID,
		Resource:    p.Resource,
		Action:      p.Action,
		Description: p.Description,
		IsBuiltin:   p.IsBuiltin,
	}
}

// permissionGroupModelToProto returns a PermissionGroup proto WITHOUT its
// permissions populated. Use permissionGroupWithItems for the full list.
func permissionGroupModelToProto(pg *models.UserPermissionGroup) *pb.PermissionGroup {
	return &pb.PermissionGroup{
		Id:          pg.ID,
		Name:        pg.Name,
		Description: pg.Description,
		IsBuiltin:   pg.IsBuiltin,
	}
}
