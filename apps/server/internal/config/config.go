package config

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"rai/server/internal/syncing"
)

// DatabaseURL accepts a mounted secret without exposing it in command arguments.
func DatabaseURL() (string, error) {
	value, path := os.Getenv("RAI_DATABASE_URL"), os.Getenv("RAI_DATABASE_URL_FILE")
	if value != "" && path != "" {
		return "", errors.New("configure one database URL source")
	}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", errors.New("database secret unavailable")
		}
		value = strings.TrimSpace(string(b))
	}
	if value == "" {
		return "", errors.New("database URL is required")
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.User == nil || u.User.Username() == "" || strings.Trim(u.Path, "/") == "" {
		return "", errors.New("database URL must specify PostgreSQL host, user and database")
	}
	return value, nil
}

// WorkoutUUIDIngestMode selects the single Workout entry for this deployment.
// The default keeps the existing upload path until UUID ingest is explicitly
// enabled.
func WorkoutUUIDIngestMode() (syncing.WorkoutIngestMode, error) {
	value := os.Getenv("RAI_WORKOUT_UUID_INGEST_MODE")
	if value == "" {
		value = string(syncing.WorkoutIngestLegacy)
	}
	return syncing.ParseWorkoutIngestMode(value)
}

// WorkoutPayloadV6 enables protocol-6 negotiation for UUID ingest. It stays
// off unless the deployment explicitly opts in, so a server never advertises a
// payload format its operator has not chosen.
func WorkoutPayloadV6() bool {
	return os.Getenv("RAI_WORKOUT_PAYLOAD_V6") == "1"
}

// ListenAddr validates the address before opening any network listener.
func ListenAddr() (string, error) {
	addr := os.Getenv("RAI_LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", errors.New("invalid HTTP listen address")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", errors.New("invalid HTTP listen port")
	}
	return addr, nil
}
