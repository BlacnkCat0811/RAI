package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseURL(t *testing.T) {
	t.Setenv("RAI_DATABASE_URL_FILE", "")
	t.Setenv("RAI_DATABASE_URL", "")
	if _, err := DatabaseURL(); err == nil {
		t.Fatal("missing URL accepted")
	}
	for _, value := range []string{"invalid", "postgres://localhost/db", "postgres://user@localhost/"} {
		t.Setenv("RAI_DATABASE_URL", value)
		if _, err := DatabaseURL(); err == nil {
			t.Fatalf("invalid URL accepted: %q", value)
		}
	}
	value := "postgres://user:example@localhost:5432/rai_dev?sslmode=disable"
	t.Setenv("RAI_DATABASE_URL", value)
	if got, err := DatabaseURL(); err != nil || got != value {
		t.Fatalf("valid URL rejected: %v", err)
	}
	path := filepath.Join(t.TempDir(), "url")
	if err := os.WriteFile(path, []byte(value+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RAI_DATABASE_URL_FILE", path)
	if _, err := DatabaseURL(); err == nil {
		t.Fatal("two URL sources accepted")
	}
	t.Setenv("RAI_DATABASE_URL", "")
	if got, err := DatabaseURL(); err != nil || got != value {
		t.Fatalf("secret file rejected: %v", err)
	}
}

func TestListenAddr(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{{"", true}, {"0.0.0.0:8080", true}, {"[::1]:8080", true}, {"bad", false}, {"localhost:0", false}, {"localhost:65536", false}} {
		t.Setenv("RAI_LISTEN_ADDR", tc.value)
		_, err := ListenAddr()
		if (err == nil) != tc.valid {
			t.Errorf("ListenAddr(%q): %v", tc.value, err)
		}
	}
}

func TestWorkoutUUIDIngestMode(t *testing.T) {
	t.Setenv("RAI_WORKOUT_UUID_INGEST_MODE", "")
	if got, err := WorkoutUUIDIngestMode(); err != nil || got != "legacy" {
		t.Fatalf("default: %q %v", got, err)
	}
	for _, value := range []string{"legacy", "uuid", "uuid_paused"} {
		t.Setenv("RAI_WORKOUT_UUID_INGEST_MODE", value)
		if got, err := WorkoutUUIDIngestMode(); err != nil || string(got) != value {
			t.Fatalf("%q rejected: %v", value, err)
		}
	}
	t.Setenv("RAI_WORKOUT_UUID_INGEST_MODE", "UUID")
	if _, err := WorkoutUUIDIngestMode(); err == nil {
		t.Fatal("invalid mode accepted")
	}
}
