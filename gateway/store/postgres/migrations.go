package postgres

import "embed"

//go:embed migrations/*.sql
var Migrations embed.FS

func InitialMigrationSQL() string {
	data, err := Migrations.ReadFile("migrations/001_initial.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}
