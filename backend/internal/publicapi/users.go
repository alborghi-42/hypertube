package publicapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/alborghi-42/hypertube/backend/internal/auth"
	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

type userResponse struct {
	ID        string  `json:"id"`
	Username  string  `json:"username"`
	AvatarURL *string `json:"avatar_url"`
	// Only for the profile owner (subject III.1: the email stays private).
	Email string `json:"email,omitempty"`
}

func userView(u *repository.User, self bool) userResponse {
	r := userResponse{ID: uuidString(u.ID), Username: u.Username, AvatarURL: u.AvatarUrl}
	if self {
		r.Email = u.Email
	}
	return r
}

// GET /api/v1/users — every user's id and username.
func (a *API) listUsers(c *gin.Context) {
	rows, err := a.q.ListUsers(c.Request.Context())
	if err != nil {
		a.log.Error("list users failed", "error", err)
		fail(c, http.StatusInternalServerError, "Failed to load users")
		return
	}
	type item struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	out := make([]item, 0, len(rows))
	for _, r := range rows {
		out = append(out, item{ID: uuidString(r.ID), Username: r.Username})
	}
	c.JSON(http.StatusOK, out)
}

// GET /api/v1/users/:id — username and profile picture URL; email for the owner.
func (a *API) getUser(c *gin.Context) {
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		fail(c, http.StatusNotFound, "User not found")
		return
	}
	u, err := a.q.GetUserByID(c.Request.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(c, http.StatusNotFound, "User not found")
		return
	}
	if err != nil {
		a.log.Error("load user failed", "error", err)
		fail(c, http.StatusInternalServerError, "Failed to load user")
		return
	}
	c.JSON(http.StatusOK, userView(&u, u.ID == currentUser(c).ID))
}

// PATCH /api/v1/users/:id {username, email, password, avatar_url} — partial
// update of the caller's own profile; 403 for any other user.
func (a *API) patchUser(c *gin.Context) {
	me := currentUser(c)
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		fail(c, http.StatusNotFound, "User not found")
		return
	}
	// Subject: any attempt on another profile is 403 (existing or not).
	if id != me.ID {
		fail(c, http.StatusForbidden, "You can only modify your own profile")
		return
	}
	var req struct {
		Username  *string `json:"username"`
		Email     *string `json:"email"`
		Password  *string `json:"password"`
		AvatarURL *string `json:"avatar_url"`
	}
	if !bindJSON(c, &req) {
		return
	}
	u, fields, err := a.profiles.UpdateAccount(c.Request.Context(), me, auth.AccountUpdate{
		Username: req.Username, Email: req.Email, Password: req.Password, AvatarURL: req.AvatarURL,
	})
	switch {
	case errors.Is(err, auth.ErrAccountConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "Username or email already in use", "fields": fields})
	case err != nil:
		a.log.Error("api profile update failed", "error", err)
		fail(c, http.StatusInternalServerError, "Failed to update profile")
	case fields != nil:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed", "fields": fields})
	default:
		c.JSON(http.StatusOK, userView(u, true))
	}
}
