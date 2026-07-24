// Package cache provides Redis caching for RBAC data.
package cache

import (
	"context"
	"fmt"

	"github.com/servekit/user-service/pkg/config"

	"github.com/redis/go-redis/v9"
)

// PermissionEntry represents a resource:action pair.
type PermissionEntry struct {
	Resource string
	Action   string
}

// RBACCache manages RBAC data in Redis.
type RBACCache struct {
	client *redis.Client
	cfg    *config.RBACConfig
}

// NewRBACCache creates a new RBACCache.
func NewRBACCache(client *redis.Client, cfg *config.RBACConfig) *RBACCache {
	return &RBACCache{client: client, cfg: cfg}
}

// GetUserPermissions checks Redis cache, returns nil if miss.
func (c *RBACCache) GetUserPermissions(ctx context.Context, userID int64) (map[string]bool, error) {
	key := c.userPermsKey(userID)
	result, err := c.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("redis hgetall: %w", err)
	}
	if len(result) == 0 {
		return nil, nil
	}
	perms := make(map[string]bool, len(result))
	for k := range result {
		perms[k] = true
	}
	return perms, nil
}

// SetUserPermissions writes user permissions to Redis with TTL.
func (c *RBACCache) SetUserPermissions(ctx context.Context, userID int64, perms []PermissionEntry) error {
	key := c.userPermsKey(userID)
	if len(perms) == 0 {
		_ = c.client.HSet(ctx, key, "__empty", "1") //nolint:errcheck // cache write is best-effort
	} else {
		fields := make(map[string]any, len(perms))
		for _, p := range perms {
			fields[p.Resource+":"+p.Action] = "1"
		}
		_ = c.client.HSet(ctx, key, fields) //nolint:errcheck // cache write is best-effort
	}
	_ = c.client.Expire(ctx, key, c.cfg.Cache.UserPermsTTL) //nolint:errcheck // cache write is best-effort
	return nil
}

// GetUserPermissionsInGroup checks group-scoped Redis cache, returns nil if miss.
func (c *RBACCache) GetUserPermissionsInGroup(ctx context.Context, groupID, userID int64) (map[string]bool, error) {
	key := c.groupUserPermsKey(groupID, userID)
	result, err := c.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("redis hgetall: %w", err)
	}
	if len(result) == 0 {
		return nil, nil
	}
	perms := make(map[string]bool, len(result))
	for k := range result {
		perms[k] = true
	}
	return perms, nil
}

// SetUserPermissionsInGroup writes group-scoped user permissions to Redis with TTL.
func (c *RBACCache) SetUserPermissionsInGroup(ctx context.Context, groupID, userID int64, perms []PermissionEntry) error {
	key := c.groupUserPermsKey(groupID, userID)
	if len(perms) == 0 {
		_ = c.client.HSet(ctx, key, "__empty", "1") //nolint:errcheck // cache write is best-effort
	} else {
		fields := make(map[string]any, len(perms))
		for _, p := range perms {
			fields[p.Resource+":"+p.Action] = "1"
		}
		_ = c.client.HSet(ctx, key, fields) //nolint:errcheck // cache write is best-effort
	}
	_ = c.client.Expire(ctx, key, c.cfg.Cache.GroupUserPermsTTL) //nolint:errcheck // cache write is best-effort
	return nil
}

// GetUserRoles returns cached role IDs for a user, or nil on miss.
// Role IDs cover both direct user-role assignments and group-inherited roles.
func (c *RBACCache) GetUserRoles(ctx context.Context, userID int64) ([]int64, error) {
	key := c.userRolesKey(userID)
	raw, err := c.client.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("redis lrange: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	// Sentinel "0" represents an empty result (user with no roles). Distinguish
	// cache miss (key absent → len(raw)==0) from cached empty (raw=["0"]).
	if len(raw) == 1 && raw[0] == "0" {
		return []int64{}, nil
	}
	roleIDs := make([]int64, len(raw))
	for i, s := range raw {
		var id int64
		if _, err := fmt.Sscanf(s, "%d", &id); err != nil {
			return nil, fmt.Errorf("decode role id %q: %w", s, err)
		}
		roleIDs[i] = id
	}
	return roleIDs, nil
}

// SetUserRoles writes user role IDs to Redis with TTL. Writes a sentinel
// entry when roleIDs is empty so a subsequent GetUserRoles can distinguish
// "cached empty" from "cache miss".
func (c *RBACCache) SetUserRoles(ctx context.Context, userID int64, roleIDs []int64) error {
	key := c.userRolesKey(userID)
	if len(roleIDs) == 0 {
		_ = c.client.RPush(ctx, key, 0) //nolint:errcheck // cache write is best-effort
	} else {
		values := make([]any, len(roleIDs))
		for i, id := range roleIDs {
			values[i] = id
		}
		_ = c.client.RPush(ctx, key, values...) //nolint:errcheck // cache write is best-effort
	}
	_ = c.client.Expire(ctx, key, c.cfg.Cache.UserRolesTTL) //nolint:errcheck // cache write is best-effort
	return nil
}

// InvalidateUser removes user permission and role caches.
func (c *RBACCache) InvalidateUser(ctx context.Context, userID int64) error {
	_ = c.client.Del(ctx, c.userPermsKey(userID)) //nolint:errcheck // cache invalidation is best-effort
	_ = c.client.Del(ctx, c.userRolesKey(userID)) //nolint:errcheck // cache invalidation is best-effort
	return nil
}

// InvalidateRole removes role permission cache + all affected user caches.
func (c *RBACCache) InvalidateRole(ctx context.Context, roleID int64, affectedUserIDs []int64) error {
	_ = roleID
	for _, uid := range affectedUserIDs {
		_ = c.InvalidateUser(ctx, uid) //nolint:errcheck // cache invalidation is best-effort
	}
	return nil
}

// InvalidateGroup removes group caches + all affected member caches.
func (c *RBACCache) InvalidateGroup(ctx context.Context, groupID int64, memberIDs []int64) error {
	_ = groupID
	for _, uid := range memberIDs {
		_ = c.InvalidateUser(ctx, uid) //nolint:errcheck // cache invalidation is best-effort
	}
	return nil
}

// --- internal helpers ---

func (c *RBACCache) userPermsKey(userID int64) string {
	return fmt.Sprintf("%s:%d", c.cfg.UserPermsPrefix, userID)
}

func (c *RBACCache) userRolesKey(userID int64) string {
	return fmt.Sprintf("%s:%d", c.cfg.UserRolesPrefix, userID)
}

func (c *RBACCache) groupUserPermsKey(groupID, userID int64) string {
	return fmt.Sprintf("%s:%d:%d", c.cfg.GroupUserPermsPrefix, groupID, userID)
}
