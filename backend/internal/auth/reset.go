package auth

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/alborghi-42/hypertube/backend/internal/mail"
	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

const (
	resetTokenTTL = time.Hour

	// Forgot-password throttling: every request counts (not only failures),
	// per client IP and per target email.
	maxForgotPerIP    = 10
	maxForgotPerEmail = 3
	forgotWindow      = time.Hour

	// Invalid reset tokens per client IP.
	maxResetFailuresPerIP = 20
	resetFailureWindow    = 15 * time.Minute
)

// TxDB is a database that can also open transactions (e.g. *pgxpool.Pool).
type TxDB interface {
	repository.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ResetHandler serves /api/auth/password/{forgot,reset}.
type ResetHandler struct {
	db          TxDB
	q           *repository.Queries
	sessions    *Sessions
	mailer      mail.Sender
	resetURL    string // absolute URL of the Angular /reset-password page
	log         *slog.Logger
	forgotIP    *failureLimiter
	forgotEmail *failureLimiter
	resetIP     *failureLimiter
}

// NewResetHandler builds the reset flow; publicURL + basePath is where the
// app is reachable from the user's browser (e.g. https://host + /hypertube).
func NewResetHandler(db TxDB, sessions *Sessions, mailer mail.Sender, publicURL, basePath string, log *slog.Logger) *ResetHandler {
	return &ResetHandler{
		db:          db,
		q:           repository.New(db),
		sessions:    sessions,
		mailer:      mailer,
		resetURL:    strings.TrimRight(publicURL, "/") + basePath + "/reset-password",
		log:         log,
		forgotIP:    newFailureLimiter(maxForgotPerIP, forgotWindow),
		forgotEmail: newFailureLimiter(maxForgotPerEmail, forgotWindow),
		resetIP:     newFailureLimiter(maxResetFailuresPerIP, resetFailureWindow),
	}
}

// RegisterRoutes mounts the public endpoints on api (not authed).
func (h *ResetHandler) RegisterRoutes(api *gin.RouterGroup) {
	api.POST("/auth/password/forgot", h.forgot)
	api.POST("/auth/password/reset", h.reset)
}

type forgotRequest struct {
	Email string `json:"email"`
}

type resetRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

func tooMany(c *gin.Context, wait time.Duration, msg string) {
	c.Header("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	c.JSON(http.StatusTooManyRequests, gin.H{"error": msg})
}

// POST /api/auth/password/forgot — always 204 for a well-formed email, so the
// response does not reveal whether an account exists. The email is sent in
// the background for the same reason (SMTP latency would be observable).
func (h *ResetHandler) forgot(c *gin.Context) {
	var req forgotRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if msg := validateEmail(email); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg, "fields": fieldErrors{"email": msg}})
		return
	}

	ipKey := c.ClientIP()
	if wait := max(h.forgotIP.blocked(ipKey), h.forgotEmail.blocked(email)); wait > 0 {
		tooMany(c, wait, "Too many reset requests, try again later")
		return
	}
	h.forgotIP.fail(ipKey)
	h.forgotEmail.fail(email)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	user, err := h.q.GetUserByEmail(ctx, email)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		c.Status(http.StatusNoContent)
		return
	case err != nil:
		h.log.Error("load user failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to request a password reset"})
		return
	}

	token, err := newToken()
	if err != nil {
		h.log.Error("generate reset token failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to request a password reset"})
		return
	}
	if err := h.q.CreatePasswordReset(ctx, repository.CreatePasswordResetParams{
		TokenHash: hashToken(token),
		UserID:    user.ID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(resetTokenTTL), Valid: true},
	}); err != nil {
		h.log.Error("create password reset failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to request a password reset"})
		return
	}
	// Opportunistic cleanup; failures are harmless.
	if err := h.q.DeleteExpiredPasswordResets(ctx); err != nil {
		h.log.Warn("delete expired password resets failed", "error", err)
	}

	link := h.resetURL + "?token=" + url.QueryEscape(token)
	go h.sendResetEmail(user.Email, user.FirstName, link)
	c.Status(http.StatusNoContent)
}

func (h *ResetHandler) sendResetEmail(to, firstName, link string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body := "Hi " + firstName + ",\n\n" +
		"Someone asked to reset the password of your Hypertube account.\n" +
		"Open this link within one hour to choose a new password:\n\n" +
		link + "\n\n" +
		"If it was not you, ignore this email: your password stays the same.\n"
	if err := h.mailer.Send(ctx, to, "Reset your Hypertube password", body); err != nil {
		h.log.Error("send password reset email failed", "error", err)
	}
}

// POST /api/auth/password/reset — consumes the token, sets the new password
// and logs the user out everywhere. The user must log in again afterwards.
func (h *ResetHandler) reset(c *gin.Context) {
	var req resetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	ipKey := c.ClientIP()
	if wait := h.resetIP.blocked(ipKey); wait > 0 {
		tooMany(c, wait, "Too many failed attempts, try again later")
		return
	}
	if req.Token == "" || len(req.Token) > 128 {
		h.resetIP.fail(ipKey)
		c.JSON(http.StatusBadRequest, gin.H{"error": "This reset link is invalid or has expired"})
		return
	}
	if msg := ValidatePassword(req.Password); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed", "fields": fieldErrors{"password": msg}})
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		h.log.Error("hash password failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset password"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	userID, err := h.applyReset(ctx, hashToken(req.Token), hash)
	if errors.Is(err, pgx.ErrNoRows) {
		h.resetIP.fail(ipKey)
		c.JSON(http.StatusBadRequest, gin.H{"error": "This reset link is invalid or has expired"})
		return
	}
	if err != nil {
		h.log.Error("reset password failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset password"})
		return
	}
	if err := h.sessions.RevokeAll(ctx, userID); err != nil {
		// The password is already changed; report it so the user knows old
		// sessions may still be alive.
		h.log.Error("revoke sessions after password reset failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Password changed but other sessions could not be logged out"})
		return
	}
	// Also drop this browser's cookie (its session, if any, was just revoked).
	h.sessions.opts.set(c, SessionCookie, "", 0, true)
	c.Status(http.StatusNoContent)
}

// applyReset marks the token used, updates the password and invalidates the
// user's other pending tokens in one transaction.
func (h *ResetHandler) applyReset(ctx context.Context, tokenHash []byte, passwordHash string) (pgtype.UUID, error) {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return pgtype.UUID{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.q.WithTx(tx)
	userID, err := q.ConsumePasswordReset(ctx, tokenHash)
	if err != nil {
		return pgtype.UUID{}, err
	}
	if err := q.UpdateUserPassword(ctx, repository.UpdateUserPasswordParams{ID: userID, PasswordHash: &passwordHash}); err != nil {
		return pgtype.UUID{}, err
	}
	if err := q.InvalidateUserPasswordResets(ctx, userID); err != nil {
		return pgtype.UUID{}, err
	}
	return userID, tx.Commit(ctx)
}
