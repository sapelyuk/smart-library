// Package config offers small helpers to read service configuration
// from environment variables with defaults.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// String returns the value of the environment variable or fallback when unset.
func String(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

// Int returns an integer value of the environment variable or fallback.
func Int(key string, fallback int) int {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

// Duration returns a duration value of the environment variable or fallback.
func Duration(key string, fallback time.Duration) time.Duration {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

// Bool returns a boolean value of the environment variable or fallback.
// Recognized truthy values: "1", "true", "yes", "on".
func Bool(key string, fallback bool) bool {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

// RequireString fails fast when a mandatory variable is missing.
func RequireString(key string) (string, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return "", fmt.Errorf("config: required environment variable %s is not set", key)
	}
	return value, nil
}
