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
	}
}
