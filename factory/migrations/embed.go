package migrations

import "embed"

// Files contains the control-plane schema migrations.
//
//go:embed *.sql
var Files embed.FS
