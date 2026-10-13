package config

import (
	"os"
	"strconv"
	"strings"
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
	OMDBAPIKey  string // Optional: enriches media with IMDb rating/actors; empty skips it
	BasePath    string // Public path prefix (e.g. /hypertube behind the portal); empty = served at /
	// CookieSecure sets the Secure flag on session/CSRF cookies. Default true;
	// set COOKIE_SECURE=false only for local development over plain http.
	CookieSecure bool
}

func Load() Config {
	return Config{
		Port:         envInt("PORT", 8080),
		DatabaseURL:  os.Getenv("DATABASE_URL"),
		SSRUpstream:  envStr("SSR_UPSTREAM", "http://127.0.0.1:4000"),
		SSRCommand:   os.Getenv("SSR_COMMAND"),
		DataDir:      envStr("DATA_DIR", "/data"),
		OMDBAPIKey:   os.Getenv("OMDB_API_KEY"),
		BasePath:     strings.TrimRight(os.Getenv("BASE_PATH"), "/"),
		CookieSecure: envBool("COOKIE_SECURE", true),
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

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

// CookiePath is the Path attribute for cookies: the base path, or "/".
func (c Config) CookiePath() string {
	if c.BasePath == "" {
		return "/"
	}
	return c.BasePath
}
