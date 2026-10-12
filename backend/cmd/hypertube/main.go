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
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/X2Borgo/hypertube/backend/internal/config"
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
	router.Use(gin.Recovery())

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := router.Group("/api")
	{
		api.GET("/ping", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"message": "pong"})
		})
	}

	// Everything that is not /api, /stream or /healthz is the Angular app.
	router.NoRoute(ssrProxy(cfg, log))

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           router,
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
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}
