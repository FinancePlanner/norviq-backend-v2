// Package db embeds the goose migrations so the binary carries its schema.
package db

import "embed"

// Migrations holds migrations/*.sql.
//
//go:embed migrations/*.sql
var Migrations embed.FS
