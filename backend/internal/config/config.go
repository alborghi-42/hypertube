package config

import (
	"os"
	"strconv"
	"strings"
	"time"
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
	// OAuth login (42 intra, GitHub); see oauth.go.
	OAuth OAuthConfig
	// SMTP for password-reset email; empty SMTPHost logs the email instead.
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string
	// BitTorrent client (media downloads, DATA_DIR/downloads).
	TorrentListenPort   int           // peer port, TCP+UDP; 0 = random
	TorrentNoUpload     bool          // never upload to peers
	TorrentSeed         bool          // keep seeding completed files
	TorrentStallTimeout time.Duration // fail a download with no progress for this long; 0 = never
	// OpenSubtitles REST API (subtitles); empty key disables fetching.
	OpenSubtitlesAPIKey    string
	OpenSubtitlesUserAgent string
	OpenSubtitlesUsername  string // login for the download endpoint
	OpenSubtitlesPassword  string
	// Downloads not watched for this long are deleted from disk (42-18); 0 = never.
	MediaRetention time.Duration
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
		OAuth:        loadOAuth(),
		SMTPHost:     os.Getenv("SMTP_HOST"),
		SMTPPort:     envInt("SMTP_PORT", 587),
		SMTPUser:     os.Getenv("SMTP_USER"),
		SMTPPassword: os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:     envStr("SMTP_FROM", "hypertube@localhost"),

		TorrentListenPort:   envInt("TORRENT_LISTEN_PORT", 6881),
		TorrentNoUpload:     envBool("TORRENT_NO_UPLOAD", false),
		TorrentSeed:         envBool("TORRENT_SEED", false),
		TorrentStallTimeout: envDuration("TORRENT_STALL_TIMEOUT", 10*time.Minute),
		MediaRetention:      envDuration("MEDIA_RETENTION", 720*time.Hour),

		OpenSubtitlesAPIKey:    os.Getenv("OPENSUBTITLES_API_KEY"),
		OpenSubtitlesUserAgent: envStr("OPENSUBTITLES_USER_AGENT", "Hypertube v1.0"),
		OpenSubtitlesUsername:  os.Getenv("OPENSUBTITLES_USERNAME"),
		OpenSubtitlesPassword:  os.Getenv("OPENSUBTITLES_PASSWORD"),
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

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
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
