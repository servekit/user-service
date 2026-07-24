package models

// AllModels returns all GORM models for AutoMigrate.
func AllModels() []any {
	return []any{
		&User{},
		&UserIdentity{},
		&UserSession{},
		&UserLoginLog{},
		&UserGroup{},
		&UserGroupMapping{},
		&UserRole{},
		&UserRoleMapping{},
		&UserPermission{},
		&UserPermissionGroup{},
		&PermissionGroupItemMapping{},
		&RolePermissionMapping{},
		&RolePermissionGroupMapping{},
		&GroupRoleMapping{},
	}
}
