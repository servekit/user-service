// Package models holds the GORM persistence models for user-service;
// AllModels is the AutoMigrate registry every deployment path shares.
package models

// AllModels returns all GORM models for AutoMigrate.
func AllModels() []any {
	return []any{
		&UserUser{},
		&UserIdentity{},
		&UserSession{},
		&UserAuthLog{},
		&UserGroup{},
		&UserGroupMapping{},
		&UserRole{},
		&UserRoleMapping{},
		&UserPermission{},
		&UserPermissionGroup{},
		&UserPermissionGroupItemMapping{},
		&UserRolePermissionMapping{},
		&UserRolePermissionGroupMapping{},
		&UserGroupRoleMapping{},
		&UserRegisterProfile{},
	}
}
