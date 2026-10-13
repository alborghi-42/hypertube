package publicapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/alborghi-42/hypertube/backend/internal/auth"
	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

const clientNameMax = 50

// newSecret returns 256 random bits, base64url-encoded.
func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashSecret is SHA-256: client secrets and tokens are random 256-bit
// values, so a slow password hash (argon2id) would add nothing but a cheap
// CPU-exhaustion vector on the unauthenticated token endpoint.
func hashSecret(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

func uuidString(u pgtype.UUID) string { return uuid.UUID(u.Bytes).String() }

func parseUUID(s string) (pgtype.UUID, bool) {
	id, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, false
	}
	return pgtype.UUID{Bytes: id, Valid: true}, true
}

// --- client management (session + CSRF, /api/auth/clients) ---

type clientResponse struct {
	ClientID     string    `json:"client_id"`
	Name         string    `json:"name"`
	CreatedAt    time.Time `json:"created_at"`
	ClientSecret string    `json:"client_secret,omitempty"`
}

// POST /api/auth/clients {"name"} — creates a client for the logged-in
// user. The secret is only ever returned here.
func (a *API) createClient(c *gin.Context) {
	user := auth.CurrentUser(c)
	var req struct {
		Name string `json:"name"`
	}
	if !bindJSON(c, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > clientNameMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed", "fields": gin.H{"name": "Name must be 1 to 50 characters"}})
		return
	}
	secret, err := newSecret()
	if err != nil {
		fail(c, http.StatusInternalServerError, "Failed to create client")
		return
	}
	row, err := a.q.CreateAPIClient(c.Request.Context(), repository.CreateAPIClientParams{
		UserID: user.ID, Name: name, SecretHash: hashSecret(secret),
	})
	if err != nil {
		a.log.Error("create api client failed", "error", err)
		fail(c, http.StatusInternalServerError, "Failed to create client")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, clientResponse{
		ClientID: uuidString(row.ID), Name: row.Name, CreatedAt: row.CreatedAt.Time, ClientSecret: secret,
	})
}

// GET /api/auth/clients — the caller's clients, without secrets.
func (a *API) listClients(c *gin.Context) {
	rows, err := a.q.ListAPIClientsByUser(c.Request.Context(), auth.CurrentUser(c).ID)
	if err != nil {
		a.log.Error("list api clients failed", "error", err)
		fail(c, http.StatusInternalServerError, "Failed to load clients")
		return
	}
	out := make([]clientResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, clientResponse{ClientID: uuidString(r.ID), Name: r.Name, CreatedAt: r.CreatedAt.Time})
	}
	c.JSON(http.StatusOK, out)
}

// DELETE /api/auth/clients/:id — revokes a client and all its tokens.
func (a *API) deleteClient(c *gin.Context) {
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		fail(c, http.StatusNotFound, "Client not found")
		return
	}
	n, err := a.q.DeleteAPIClient(c.Request.Context(), repository.DeleteAPIClientParams{ID: id, UserID: auth.CurrentUser(c).ID})
	if err != nil {
		a.log.Error("delete api client failed", "error", err)
		fail(c, http.StatusInternalServerError, "Failed to delete client")
		return
	}
	if n == 0 {
		fail(c, http.StatusNotFound, "Client not found")
		return
	}
	c.Status(http.StatusNoContent)
}

// --- POST /api/v1/oauth/token (RFC 6749 section 4.4) ---

// oauthError writes an RFC 6749 section 5.2 error response.
func oauthError(c *gin.Context, status int, code, desc string) {
	if status == http.StatusUnauthorized {
		c.Header("WWW-Authenticate", `Basic realm="hypertube"`)
	}
	c.AbortWithStatusJSON(status, gin.H{"error": code, "error_description": desc})
}

// token exchanges client_id + client_secret for a bearer token. Credentials
// come from HTTP Basic auth or the body, which may be form-encoded (the
// RFC's format) or JSON.
func (a *API) token(c *gin.Context) {
	var req struct {
		GrantType    string `json:"grant_type" form:"grant_type"`
		ClientID     string `json:"client_id" form:"client_id"`
		ClientSecret string `json:"client_secret" form:"client_secret"`
	}
	if err := c.ShouldBind(&req); err != nil {
		oauthError(c, http.StatusBadRequest, "invalid_request", "Malformed request body")
		return
	}
	if id, secret, ok := c.Request.BasicAuth(); ok {
		req.ClientID, req.ClientSecret = id, secret
	}
	if req.GrantType != "client_credentials" {
		if req.GrantType == "" {
			oauthError(c, http.StatusBadRequest, "invalid_request", "grant_type is required")
		} else {
			oauthError(c, http.StatusBadRequest, "unsupported_grant_type", "Only client_credentials is supported")
		}
		return
	}
	if req.ClientID == "" || req.ClientSecret == "" {
		oauthError(c, http.StatusBadRequest, "invalid_request", "client_id and client_secret are required")
		return
	}

	ctx := c.Request.Context()
	id, ok := parseUUID(req.ClientID)
	var client repository.GetAPIClientSecretRow
	var err error
	if ok {
		client, err = a.q.GetAPIClientSecret(ctx, id)
	}
	if !ok || errors.Is(err, pgx.ErrNoRows) ||
		(err == nil && subtle.ConstantTimeCompare(client.SecretHash, hashSecret(req.ClientSecret)) != 1) {
		oauthError(c, http.StatusUnauthorized, "invalid_client", "Unknown client or wrong secret")
		return
	}
	if err != nil {
		a.log.Error("api client lookup failed", "error", err)
		oauthError(c, http.StatusInternalServerError, "server_error", "Failed to verify client")
		return
	}

	token, err := newSecret()
	if err != nil {
		oauthError(c, http.StatusInternalServerError, "server_error", "Failed to issue token")
		return
	}
	if err := a.q.CreateAPIToken(ctx, repository.CreateAPITokenParams{
		TokenHash: hashSecret(token),
		ClientID:  client.ID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(tokenTTL), Valid: true},
	}); err != nil {
		a.log.Error("create api token failed", "error", err)
		oauthError(c, http.StatusInternalServerError, "server_error", "Failed to issue token")
		return
	}
	if err := a.q.DeleteExpiredAPITokens(ctx); err != nil {
		a.log.Warn("prune expired api tokens failed", "error", err)
	}
	c.Header("Pragma", "no-cache")
	c.JSON(http.StatusOK, gin.H{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   int(tokenTTL.Seconds()),
	})
}
