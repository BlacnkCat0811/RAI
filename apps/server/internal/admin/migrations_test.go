package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"rai/server/internal/storage/postgres"
	"rai/server/migrations"
)

func TestFrozenMigrations(t *testing.T) {
	b, err := migrations.Files.ReadFile("checksums.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]string
	if err = json.Unmarshal(b, &expected); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		count++
		b, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(b)
		if expected[entry.Name()] != hex.EncodeToString(h[:]) {
			t.Fatalf("migration %s missing or changed; add a new migration", entry.Name())
		}
	}
	if count == 0 || count != len(expected) {
		t.Fatal("checksum manifest and migrations disagree")
	}
}

func TestRuntimeSchemaGateMatchesMigrations(t *testing.T) {
	if postgres.RequiredSchemaVersion != migrations.LatestVersion() {
		t.Fatal("runtime schema gate is stale")
	}
}
