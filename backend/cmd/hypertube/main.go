package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alborghi-42/hypertube/backend/internal/auth"
	"github.com/alborghi-42/hypertube/backend/internal/config"
	"github.com/alborghi-42/hypertube/backend/internal/mail"
	"github.com/alborghi-42/hypertube/backend/internal/media"
)

// Hypertube entrypoint. The Go server is the single public process (PID 1 in
// the container): it serves /api/* and /stream/* itself and reverse-proxies
// everything else to the Angular SSR server, which it spawns when SSR_COMMAND
// is set (production image). In local dev the frontend runs `ng serve` with a
// proxy config instead, so the reverse proxy is unused.
func main() {
	cfg := config.Load()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var pool *pgxpool.Pool
	if cfg.DatabaseURL != "" {
		var err error
		pool, err = pgxpool.New(ctx, cfg.DatabaseURL)
		if err != nil {
			log.Error("invalid DATABASE_URL", "error", err)
			os.Exit(1)
		}
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			// Non-fatal at startup: the db-init job may still be running.
			log.Warn("database not reachable yet", "error", err)
		}
	} else {
		log.Warn("DATABASE_URL not set, running without database")
	}

	ssr := startSSR(ctx, cfg, log)

	router := gin.New()
	router.HandleMethodNotAllowed = true
	router.Use(gin.Recovery())

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Every /api route goes through the CSRF check (double-submit cookie).
	cookies := auth.CookieOptions{Path: cfg.CookiePath(), Secure: cfg.CookieSecure}
	api := router.Group("/api", auth.CSRF(cookies))
	{
		api.GET("/ping", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"message": "pong"})
		})
	}

	if pool != nil {
		// Public: auth entry points (register/login/logout, later OAuth and
		// password reset) and the public movie GETs. Everything else on /api
		// and all of /stream requires a session.
		sessions := auth.NewSessions(pool, cookies)
		authed := api.Group("", sessions.Require())
		stream := router.Group("/stream", sessions.Require())

		auth.NewHandler(pool, sessions, log).RegisterRoutes(api, authed)
		auth.NewProfileHandler(pool, cfg.DataDir, log).RegisterRoutes(api, authed)
		auth.NewOAuth(pool, sessions, cookies, cfg.BasePath, cfg.OAuth, log).RegisterRoutes(api)
		mailer := mail.New(mail.Config{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Password: cfg.SMTPPassword, From: cfg.SMTPFrom}, log)
		auth.NewResetHandler(pool, sessions, mailer, cfg.OAuth.PublicURL, cfg.BasePath, log).RegisterRoutes(api)

		// Single shared BitTorrent client; closed after the HTTP server stops.
		tc, err := media.NewTorrentClient(media.TorrentConfig{
			DataDir:      filepath.Join(cfg.DataDir, "downloads"),
			ListenPort:   cfg.TorrentListenPort,
			NoUpload:     cfg.TorrentNoUpload,
			Seed:         cfg.TorrentSeed,
			StallTimeout: cfg.TorrentStallTimeout,
		}, log)
		if err != nil {
			log.Error("torrent client failed to start", "error", err)
			os.Exit(1)
		}
		store := media.NewStore(pool)
		engine := media.NewEngine(tc, store, log)
		// engine.PostProcess: mkv/avi conversion hook (ticket 42-16).
		defer tc.Close()
		defer engine.Close()
		providers := media.NewRegistry(
			media.NewArchiveOrg(cfg.OMDBAPIKey, log),
			media.NewPublicDomainTorrents(cfg.OMDBAPIKey, log),
		)
		media.NewHandler(store, providers, engine, log).RegisterRoutes(api, authed, stream)
		go engine.RunRetention(ctx, cfg.MediaRetention) // 42-18: delete unwatched downloads
	} else {
		log.Warn("auth and media routes disabled: they require DATABASE_URL")
	}

	// Everything that is not /api, /stream or /healthz is the Angular app;
	// unknown paths or methods on those prefixes get a JSON error instead.
	ssrFallback := ssrProxy(cfg, log)
	router.NoRoute(backendError(http.StatusNotFound, "Not found", ssrFallback))
	router.NoMethod(backendError(http.StatusMethodNotAllowed, "Method not allowed", ssrFallback))

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           withBasePath(cfg.BasePath, router),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("listening", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	if ssr != nil && ssr.Process != nil {
		_ = ssr.Process.Signal(syscall.SIGTERM)
		_ = ssr.Wait()
	}
}

// startSSR spawns the Angular SSR node process when SSR_COMMAND is set and
// restarts is left to the container runtime: if the SSR process dies we exit
// so Kubernetes restarts the pod as a whole.
func startSSR(ctx context.Context, cfg config.Config, log *slog.Logger) *exec.Cmd {
	if cfg.SSRCommand == "" {
		return nil
	}
	parts := strings.Fields(cfg.SSRCommand)
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		log.Error("failed to start SSR server", "command", cfg.SSRCommand, "error", err)
		os.Exit(1)
	}
	log.Info("started SSR server", "pid", cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		if ctx.Err() == nil {
			log.Error("SSR server exited unexpectedly", "error", err)
			os.Exit(1)
		}
	}()
	return cmd
}

func ssrProxy(cfg config.Config, log *slog.Logger) gin.HandlerFunc {
	upstream, err := url.Parse(cfg.SSRUpstream)
	if err != nil {
		log.Error("invalid SSR_UPSTREAM", "value", cfg.SSRUpstream, "error", err)
		os.Exit(1)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Warn("SSR proxy error", "path", r.URL.Path, "error", err)
		w.WriteHeader(http.StatusBadGateway)
	}
	return func(c *gin.Context) {
		// The SSR app is built with the base path as its base href and expects it back.
		c.Request.URL.Path = cfg.BasePath + c.Request.URL.Path
		c.Request.URL.RawPath = ""
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

// isBackendPath reports whether path belongs to the Go server (/api, /stream,
// /healthz and anything below them) rather than the Angular app. Paths are
// already stripped of the base path by withBasePath.
func isBackendPath(path string) bool {
	for _, prefix := range []string{"/api", "/stream", "/healthz"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// backendError answers backend paths with a JSON error and hands every other
// path to fallback (the SSR proxy), so SPA routes keep working for any method.
func backendError(status int, message string, fallback gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isBackendPath(c.Request.URL.Path) {
			c.AbortWithStatusJSON(status, gin.H{"error": message})
			return
		}
		// gin sets Allow before NoMethod handlers; it does not apply to the SSR response.
		c.Writer.Header().Del("Allow")
		fallback(c)
	}
}

// withBasePath serves the app under basePath (the portal forwards /hypertube/... unchanged):
// the prefix is stripped before routing, /healthz stays reachable at the root for probes.
func withBasePath(basePath string, next http.Handler) http.Handler {
	if basePath == "" {
		return next
	}
	stripped := http.StripPrefix(basePath, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == basePath:
			http.Redirect(w, r, basePath+"/", http.StatusMovedPermanently)
		case strings.HasPrefix(r.URL.Path, basePath+"/"):
			stripped.ServeHTTP(w, r)
		case r.URL.Path == "/healthz":
			next.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}
