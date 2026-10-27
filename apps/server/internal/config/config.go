package config

import (
	"errors"
	"os"
	"strings"
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
	return value, nil
}
