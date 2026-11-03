package migrations

import (
	"io/fs"
	"strconv"
	"strings"
)

// LatestVersion reports the highest migration version embedded in this binary.
// Release metadata must not carry a hand-maintained schema number: `rai
// version` reported 19 while migration 20 was already the current schema.
func LatestVersion() int64 {
	entries, err := fs.ReadDir(Files, ".")
	if err != nil {
		return 0
	}
	var latest int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, found := strings.Cut(entry.Name(), "_")
		if !found {
			continue
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			continue
		}
		if version > latest {
			latest = version
		}
	}
	return latest
}
