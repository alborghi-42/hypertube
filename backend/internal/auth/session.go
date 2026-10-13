package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

const (
	// SessionCookie holds the opaque session token (httpOnly).
	SessionCookie = "ht_session"
	// CSRFCookie / CSRFHeader implement the double-submit check; the names
	// are Angular HttpClient's XSRF defaults.
	CSRFCookie = "XSRF-TOKEN"
	CSRFHeader = "X-XSRF-TOKEN"

	sessionTTL = 7 * 24 * time.Hour
	csrfTTL    = sessionTTL
	userKey    = "auth.user"
)

// CookieOptions controls the attributes shared by the session and CSRF cookies.
type CookieOptions struct {
	Path   string // BASE_PATH, or "/" when served at the root
	Secure bool   // false only for local plain-http development
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func (o CookieOptions) set(c *gin.Context, name, value string, maxAge time.Duration, httpOnly bool) {
	cookie := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     o.Path,
		HttpOnly: httpOnly,
		Secure:   o.Secure,
		SameSite: http.SameSiteLaxMode,
	}
	if maxAge > 0 {
		cookie.MaxAge = int(maxAge.Seconds())
	} else {
		cookie.MaxAge = -1
	}
	http.SetCookie(c.Writer, cookie)
}

// CSRF issues the XSRF-TOKEN cookie when missing and, for unsafe methods,
// requires the X-XSRF-TOKEN header to match it (double-submit cookie). A
// cross-site attacker can make the browser send the cookie but cannot read it
// to forge the header. Mount it on every /api route.
func CSRF(opts CookieOptions) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, _ := c.Cookie(CSRFCookie)
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			if cookie == "" {
				if token, err := newToken(); err == nil {
					opts.set(c, CSRFCookie, token, csrfTTL, false)
				}
			}
			c.Next()
			return
		}
		header := c.GetHeader(CSRFHeader)
		if cookie == "" || subtle.ConstantTimeCompare([]byte(cookie), []byte(header)) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Invalid CSRF token"})
			return
		}
		c.Next()
	}
}

// Sessions creates, resolves and revokes server-side sessions.
type Sessions struct {
	q    *repository.Queries
	opts CookieOptions
}

func NewSessions(db repository.DBTX, opts CookieOptions) *Sessions {
	return &Sessions{q: repository.New(db), opts: opts}
}

// Start creates a session for userID and sets the cookies. It also rotates the
// CSRF token so a pre-login token cannot be reused. Hook for OAuth callbacks.
func (s *Sessions) Start(c *gin.Context, userID pgtype.UUID) error {
	token, err := newToken()
	if err != nil {
		return err
	}
	expires := time.Now().Add(sessionTTL)
	if err := s.q.CreateSession(c.Request.Context(), repository.CreateSessionParams{
		TokenHash: hashToken(token),
		UserID:    userID,
		ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
	}); err != nil {
		return err
	}
	s.opts.set(c, SessionCookie, token, sessionTTL, true)
	if csrf, err := newToken(); err == nil {
		s.opts.set(c, CSRFCookie, csrf, csrfTTL, false)
	}
	return nil
}

// End revokes the current session (if any) and clears the cookie.
func (s *Sessions) End(c *gin.Context) error {
	var err error
	if token, _ := c.Cookie(SessionCookie); token != "" {
		err = s.q.DeleteSession(c.Request.Context(), hashToken(token))
	}
	s.opts.set(c, SessionCookie, "", 0, true)
	return err
}

// RevokeAll deletes every session of a user. Hook for password reset / email change.
func (s *Sessions) RevokeAll(ctx context.Context, userID pgtype.UUID) error {
	return s.q.DeleteUserSessions(ctx, userID)
}

func (s *Sessions) lookup(c *gin.Context) (*repository.User, error) {
	token, _ := c.Cookie(SessionCookie)
	if token == "" {
		return nil, nil
	}
	u, err := s.q.GetSessionUser(c.Request.Context(), hashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// Optional attaches the user to the context when a valid session exists but
// never rejects the request.
func (s *Sessions) Optional() gin.HandlerFunc {
	return func(c *gin.Context) {
		if u, err := s.lookup(c); err == nil && u != nil {
			c.Set(userKey, u)
		}
		c.Next()
	}
}

// Require rejects requests without a valid session with 401.
func (s *Sessions) Require() gin.HandlerFunc {
	return func(c *gin.Context) {
		u, err := s.lookup(c)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to load session"})
			return
		}
		if u == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
			return
		}
		c.Set(userKey, u)
		c.Next()
	}
}

// CurrentUser returns the authenticated user set by Require/Optional, or nil.
func CurrentUser(c *gin.Context) *repository.User {
	if v, ok := c.Get(userKey); ok {
		if u, ok := v.(*repository.User); ok {
			return u
		}
	}
	return nil
}
