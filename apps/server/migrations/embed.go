package migrations

import "embed"

// Files is imported only by the explicit migrate command.
//
//go:embed *.sql checksums.json
var Files embed.FS
