package config

import (
	"os"
	"strconv"
)

// Config holds all runtime configuration, sourced from environment variables
// (locally via .env / docker-compose, in production via the hypertube-secrets
// ExternalSecret and chart env).
type Config struct {
	Port        int    // HTTP port the Go server listens on (public entrypoint)
	DatabaseURL string // Postgres DSN; empty disables DB access (M0 shell)
	SSRUpstream string // Base URL of the Angular SSR server we reverse-proxy to
	SSRCommand  string // If set, command to spawn the SSR server (production image)
	DataDir     string // Root for torrent/hls/subtitle storage (PVC in production)
}

func Load() Config {
	return Config{
		Port:        envInt("PORT", 8080),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		SSRUpstream: envStr("SSR_UPSTREAM", "http://127.0.0.1:4000"),
		SSRCommand:  os.Getenv("SSR_COMMAND"),
		DataDir:     envStr("DATA_DIR", "/data"),
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
