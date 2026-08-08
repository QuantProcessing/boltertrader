package migrations

import "embed"

// Files contains the ordered Platform schema migrations used at startup.
//
//go:embed 00*.sql
var Files embed.FS
