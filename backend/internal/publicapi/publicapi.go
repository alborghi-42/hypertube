// Package publicapi is the public RESTful API required by the subject
// (section III.4), mounted at /api/v1. Clients authenticate with the OAuth2
// client-credentials grant (POST /api/v1/oauth/token) and then send
// "Authorization: Bearer <token>"; a token acts on behalf of the user who
// created the client. It never reads the session cookie and is not behind
// the cookie CSRF check: a bearer header cannot be forged cross-site.
//
// The routes live under /api/v1 instead of the subject's bare /users,
// /movies, /oauth/token because every non-/api path belongs to the Angular
// SSR app, which already owns /users/:username and /oauth/complete.
package publicapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/alborghi-42/hypertube/backend/internal/auth"
	"github.com/alborghi-42/hypertube/backend/internal/media"
	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

const (
	// Prefix is where the API is mounted (below BASE_PATH).
	Prefix = "/api/v1"

	tokenTTL = time.Hour
	userKey  = "publicapi.user"
)

// API serves /api/v1 and the client-management endpoints on /api/auth/clients.
type API struct {
	q         *repository.Queries
	profiles  *auth.ProfileHandler
	store     *media.Store
	providers media.Registry
	basePath  string // BASE_PATH, for Location headers
	log       *slog.Logger
}

func New(db repository.DBTX, basePath string, profiles *auth.ProfileHandler, store *media.Store, providers media.Registry, log *slog.Logger) *API {
	return &API{q: repository.New(db), profiles: profiles, store: store, providers: providers, basePath: basePath, log: log}
}

// RegisterRoutes mounts the bearer-token API on router (outside the
// cookie/CSRF /api group) and client management on authed (the
// session-protected /api group: a logged-in user creates their clients).
func (a *API) RegisterRoutes(router *gin.Engine, authed *gin.RouterGroup) {
	authed.GET("/auth/clients", a.listClients)
	authed.POST("/auth/clients", a.createClient)
	authed.DELETE("/auth/clients/:id", a.deleteClient)

	v1 := router.Group(Prefix, noStore)
	v1.POST("/oauth/token", a.token)

	// Subject: "Any user can access the website's front page".
	v1.GET("/movies", a.listMovies)

	bearer := v1.Group("", a.requireToken)
	bearer.GET("/users", a.listUsers)
	bearer.GET("/users/:id", a.getUser)
	bearer.PATCH("/users/:id", a.patchUser)

	bearer.GET("/movies/:id", a.getMovie)
	bearer.GET("/movies/:id/comments", a.listMovieComments)
	bearer.POST("/movies/:id/comments", a.postMovieComment)

	bearer.GET("/comments", a.listComments)
	bearer.POST("/comments", a.postComment)
	bearer.GET("/comments/:id", a.getComment)
	bearer.PATCH("/comments/:id", a.patchComment)
	bearer.DELETE("/comments/:id", a.deleteComment)
}

func noStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}

// fail writes the JSON error body used by the whole backend.
func fail(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}

func unauthorized(c *gin.Context, msg string) {
	c.Header("WWW-Authenticate", `Bearer realm="hypertube", error="invalid_token"`)
	fail(c, http.StatusUnauthorized, msg)
}

// requireToken resolves "Authorization: Bearer <token>" to the client's
// owner, or answers 401.
func (a *API) requireToken(c *gin.Context) {
	h := c.GetHeader("Authorization")
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		c.Header("WWW-Authenticate", `Bearer realm="hypertube"`)
		fail(c, http.StatusUnauthorized, "Bearer token required")
		return
	}
	u, err := a.q.GetAPITokenUser(c.Request.Context(), hashSecret(strings.TrimSpace(token)))
	if errors.Is(err, pgx.ErrNoRows) {
		unauthorized(c, "Invalid or expired token")
		return
	}
	if err != nil {
		a.log.Error("api token lookup failed", "error", err)
		fail(c, http.StatusInternalServerError, "Failed to verify token")
		return
	}
	c.Set(userKey, &u)
	c.Next()
}

func currentUser(c *gin.Context) *repository.User {
	u, _ := c.MustGet(userKey).(*repository.User)
	return u
}

// bindJSON decodes a JSON object body; unknown fields are ignored.
func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		fail(c, http.StatusBadRequest, "Invalid JSON body")
		return false
	}
	return true
}
