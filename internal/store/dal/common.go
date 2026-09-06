// Package dal provides type-safe data access for user-service tables.
//
// Conventions (gorm-cli-development skill §6):
//   - One file per table (user.go, identity.go, ...); cross-table helpers here.
//   - Method names are table-prefixed (CreateUser, GetUserByID).
//   - Functions accept (ctx, tx *gorm.DB) so callers control transactions.
//   - Errors are wrapped via pkg/xcodes.
package dal

import "gorm.io/gorm"

// resolveTableName returns the fully-qualified table name for a GORM model,
// including any NamingStrategy prefix configured on the db instance.
func resolveTableName(db *gorm.DB, model interface{}) string {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		return ""
	}
	return stmt.Table
}
