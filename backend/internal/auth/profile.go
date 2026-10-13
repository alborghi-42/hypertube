package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

// Languages a user can pick as preferred language (subject: defaults to English).
// Mirrored by LANGUAGES in frontend/src/app/shell/language.service.ts: the UI is translated
// into exactly these.
var supportedLanguages = map[string]bool{"en": true, "fr": true, "it": true}

// ProfileHandler serves /api/users/* (profiles) and /api/avatars/* (avatar files).
type ProfileHandler struct {
	q       *repository.Queries
	avatars *avatarStore
	log     *slog.Logger
}

// NewProfileHandler stores uploaded avatars under dataDir/avatars.
func NewProfileHandler(db repository.DBTX, dataDir string, log *slog.Logger) *ProfileHandler {
	return &ProfileHandler{q: repository.New(db), avatars: newAvatarStore(dataDir), log: log}
}

// RegisterRoutes mounts the avatar files on api (public, like any image URL)
// and every profile endpoint on authed (api + Sessions.Require).
func (h *ProfileHandler) RegisterRoutes(api, authed *gin.RouterGroup) {
	api.GET("/avatars/:file", h.serveAvatar)
	authed.GET("/users", h.list)
	authed.GET("/users/me", h.getMe)
	authed.PATCH("/users/me", h.updateMe)
	authed.POST("/users/me/avatar", h.uploadAvatar)
	authed.GET("/users/:id", h.get)
	authed.PATCH("/users/:id", h.updateByID)
}

// publicUserResponse is what other users see: never the email.
type publicUserResponse struct {
	ID        string  `json:"id"`
	Username  string  `json:"username"`
	FirstName string  `json:"first_name"`
	LastName  string  `json:"last_name"`
	AvatarURL *string `json:"avatar_url"`
}

func publicView(u *repository.User) publicUserResponse {
	return publicUserResponse{
		ID:        uuid.UUID(u.ID.Bytes).String(),
		Username:  u.Username,
		FirstName: u.FirstName,
		LastName:  u.LastName,
		AvatarURL: u.AvatarUrl,
	}
}

// updateProfileRequest is a partial update: omitted fields keep their value.
type updateProfileRequest struct {
	Email     *string `json:"email"`
	Username  *string `json:"username"`
	FirstName *string `json:"first_name"`
	LastName  *string `json:"last_name"`
	Language  *string `json:"language"`
}

// GET /api/users — every user's id and username (subject API).
func (h *ProfileHandler) list(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	rows, err := h.q.ListUsers(ctx)
	if err != nil {
		h.log.Error("list users failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load users"})
		return
	}
	type item struct {
		ID        string  `json:"id"`
		Username  string  `json:"username"`
		AvatarURL *string `json:"avatar_url"`
	}
	out := make([]item, 0, len(rows))
	for _, r := range rows {
		out = append(out, item{ID: uuid.UUID(r.ID.Bytes).String(), Username: r.Username, AvatarURL: r.AvatarUrl})
	}
	c.JSON(http.StatusOK, out)
}

// GET /api/users/me — same as /api/auth/me.
func (h *ProfileHandler) getMe(c *gin.Context) {
	c.JSON(http.StatusOK, selfView(CurrentUser(c)))
}

// GET /api/users/:id — :id is a user UUID or a username (a username can never
// parse as a UUID: max 30 characters). The email is only included for the owner.
func (h *ProfileHandler) get(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	key := c.Param("id")
	var (
		user repository.User
		err  error
	)
	if id, perr := uuid.Parse(key); perr == nil {
		user, err = h.q.GetUserByID(ctx, pgtype.UUID{Bytes: id, Valid: true})
	} else {
		user, err = h.q.GetUserByUsername(ctx, key)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if err != nil {
		h.log.Error("load user failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user"})
		return
	}
	if user.ID == CurrentUser(c).ID {
		c.JSON(http.StatusOK, selfView(&user))
		return
	}
	c.JSON(http.StatusOK, publicView(&user))
}

// PATCH /api/users/:id — only the caller's own id is accepted (403 otherwise).
func (h *ProfileHandler) updateByID(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id != uuid.UUID(CurrentUser(c).ID.Bytes) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You can only modify your own profile"})
		return
	}
	h.updateMe(c)
}

// PATCH /api/users/me — partial update of the profile fields.
func (h *ProfileHandler) updateMe(c *gin.Context) {
	current := CurrentUser(c)
	var req updateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	// Merge onto the current values, then validate the result like registration.
	merged := registerRequest{
		Email:     pick(req.Email, current.Email),
		Username:  pick(req.Username, current.Username),
		FirstName: pick(req.FirstName, current.FirstName),
		LastName:  pick(req.LastName, current.LastName),
	}
	merged.normalize()
	language := strings.TrimSpace(pick(req.Language, current.Language))

	errs := fieldErrors{}
	if msg := validateUsername(merged.Username); msg != "" {
		errs["username"] = msg
	}
	if msg := validateEmail(merged.Email); msg != "" {
		errs["email"] = msg
	}
	if msg := validateName(merged.FirstName); msg != "" {
		errs["first_name"] = msg
	}
	if msg := validateName(merged.LastName); msg != "" {
		errs["last_name"] = msg
	}
	if !supportedLanguages[language] {
		errs["language"] = "Unsupported language"
	}
	if len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed", "fields": errs})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	// NOTE: email changes are applied directly (no confirmation mail); the
	// subject only requires that the address can be modified.
	user, err := h.q.UpdateUserProfile(ctx, repository.UpdateUserProfileParams{
		ID:        current.ID,
		Username:  merged.Username,
		Email:     merged.Email,
		FirstName: merged.FirstName,
		LastName:  merged.LastName,
		Language:  language,
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
		h.log.Error("update profile failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update profile"})
		return
	}
	c.JSON(http.StatusOK, selfView(&user))
}

func pick(v *string, fallback string) string {
	if v != nil {
		return *v
	}
	return fallback
}
