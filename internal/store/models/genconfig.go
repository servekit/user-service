package models

import (
	"database/sql"

	"gorm.io/cli/gorm/field"
	"gorm.io/cli/gorm/genconfig"
)

var _ = genconfig.Config{
	OutPath: "internal/store/generated",

	FieldTypeMap: map[any]any{
		sql.NullTime{}: field.Time{},
	},
}
