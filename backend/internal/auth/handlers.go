package auth

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

// Login throttling: failed attempts per client IP and per username.
const (
	maxFailuresPerIP   = 20
	maxFailuresPerUser = 5
	failureWindow      = 15 * time.Minute
)

// Handler serves /api/auth/*.
type Handler struct {
	q        *repository.Queries
	sessions *Sessions
	log      *slog.Logger
	byIP     *failureLimiter
	byUser   *failureLimiter
}

func NewHandler(db repository.DBTX, sessions *Sessions, log *slog.Logger) *Handler {
	return &Handler{
		q:        repository.New(db),
		sessions: sessions,
		log:      log,
		byIP:     newFailureLimiter(maxFailuresPerIP, failureWindow),
		byUser:   newFailureLimiter(maxFailuresPerUser, failureWindow),
	}
}

// RegisterRoutes mounts the public auth endpoints on api and the
// session-only ones on authed (api + Sessions.Require).
// OAuth and password-reset endpoints belong on api as well (public).
func (h *Handler) RegisterRoutes(api, authed *gin.RouterGroup) {
	api.POST("/auth/register", h.register)
	api.POST("/auth/login", h.login)
	api.POST("/auth/logout", h.logout)
	authed.GET("/auth/me", h.me)
}

// userResponse is the private view of the current user (includes email).
type userResponse struct {
	ID        string  `json:"id"`
	Username  string  `json:"username"`
	Email     string  `json:"email"`
	FirstName string  `json:"first_name"`
	LastName  string  `json:"last_name"`
	AvatarURL *string `json:"avatar_url"`
	Language  string  `json:"language"`
}

func selfView(u *repository.User) userResponse {
	return userResponse{
		ID:        uuid.UUID(u.ID.Bytes).String(),
		Username:  u.Username,
		Email:     u.Email,
		FirstName: u.FirstName,
		LastName:  u.LastName,
		AvatarURL: u.AvatarUrl,
		Language:  u.Language,
	}
}

// POST /api/auth/register — creates the account and logs it in.
func (h *Handler) register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	req.normalize()
	if errs := req.validate(); len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed", "fields": errs})
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		h.log.Error("hash password failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create account"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	user, err := h.q.CreateUser(ctx, repository.CreateUserParams{
		Username:     req.Username,
		Email:        req.Email,
		FirstName:    req.FirstName,
		LastName:     req.LastName,
		PasswordHash: &hash,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			field, msg := "username", "Username is already taken"
			if strings.Contains(pgErr.ConstraintName, "email") {
				field, msg = "email", "Email is already registered"
			}
			c.JSON(http.StatusConflict, gin.H{"error": msg, "fields": fieldErrors{field: msg}})
			return
		}
		h.log.Error("create user failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create account"})
		return
	}
	if err := h.sessions.Start(c, user.ID); err != nil {
		h.log.Error("start session failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Account created but login failed"})
		return
	}
	c.JSON(http.StatusCreated, selfView(&user))
}

// POST /api/auth/login — username + password.
func (h *Handler) login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" || len(req.Password) > 4*passwordMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username and password are required"})
		return
	}

	ipKey := c.ClientIP()
	userKey := strings.ToLower(req.Username)
	if wait := max(h.byIP.blocked(ipKey), h.byUser.blocked(userKey)); wait > 0 {
		c.Header("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many failed attempts, try again later"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	user, err := h.q.GetUserByUsername(ctx, req.Username)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.log.Error("load user failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Login failed"})
		return
	}
	// Always run argon2 (against a dummy hash for unknown users or OAuth-only
	// accounts) so timing does not reveal which usernames exist.
	stored := dummyHash
	found := err == nil && user.PasswordHash != nil
	if found {
		stored = *user.PasswordHash
	}
	ok, verr := VerifyPassword(req.Password, stored)
	if verr != nil {
		h.log.Error("stored password hash is invalid", "error", verr)
	}
	if !found || !ok {
		h.byIP.fail(ipKey)
		h.byUser.fail(userKey)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
		return
	}
	h.byUser.reset(userKey)

	if err := h.sessions.Start(c, user.ID); err != nil {
		h.log.Error("start session failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Login failed"})
		return
	}
	// Opportunistic cleanup; failures are harmless.
	if err := h.q.DeleteExpiredSessions(ctx); err != nil {
		h.log.Warn("delete expired sessions failed", "error", err)
	}
	c.JSON(http.StatusOK, selfView(&user))
}

// POST /api/auth/logout — idempotent; always clears the cookie.
func (h *Handler) logout(c *gin.Context) {
	if err := h.sessions.End(c); err != nil {
		h.log.Warn("delete session failed", "error", err)
	}
	c.Status(http.StatusNoContent)
}

// GET /api/auth/me — the current user, including their own email.
func (h *Handler) me(c *gin.Context) {
	c.JSON(http.StatusOK, selfView(CurrentUser(c)))
}
