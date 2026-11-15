package postgres

// RequiredSchemaVersion is a runtime compatibility gate, not a migrator.
// An owner-side test pins it to the embedded migration inventory so API and
// Worker never import admin, goose or migrations to discover this number.
const RequiredSchemaVersion int64 = 62
