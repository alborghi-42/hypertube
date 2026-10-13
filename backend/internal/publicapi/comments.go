package publicapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/alborghi-42/hypertube/backend/internal/media"
)

type commentResponse struct {
	ID        string    `json:"id"`
	Comment   string    `json:"comment"`
	Username  string    `json:"username"`
	MovieID   string    `json:"movie_id"`
	Date      time.Time `json:"date"`
	UpdatedAt time.Time `json:"updated_at"`
}

func commentView(cm *media.Comment) commentResponse {
	return commentResponse{
		ID: cm.ID, Comment: cm.Content, Username: cm.Username, MovieID: cm.MediaID,
		Date: cm.CreatedAt, UpdatedAt: cm.UpdatedAt,
	}
}

func commentList(list []media.Comment) []commentResponse {
	out := make([]commentResponse, 0, len(list))
	for i := range list {
		out = append(out, commentView(&list[i]))
	}
	return out
}

func timeout(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), 5*time.Second)
}

// GET /api/v1/comments — the latest comments across all movies.
func (a *API) listComments(c *gin.Context) {
	ctx, cancel := timeout(c)
	defer cancel()
	list, err := a.store.LatestComments(ctx, latestComments)
	if err != nil {
		a.log.Error("list latest comments failed", "error", err)
		fail(c, http.StatusInternalServerError, "Failed to load comments")
		return
	}
	c.JSON(http.StatusOK, commentList(list))
}

// GET /api/v1/movies/:id/comments
func (a *API) listMovieComments(c *gin.Context) {
	m, ok := a.loadMovie(c, c.Param("id"))
	if !ok {
		return
	}
	ctx, cancel := timeout(c)
	defer cancel()
	list, err := a.store.ListComments(ctx, m.ID)
	if err != nil {
		a.log.Error("list comments failed", "media_id", m.ID, "error", err)
		fail(c, http.StatusInternalServerError, "Failed to load comments")
		return
	}
	c.JSON(http.StatusOK, commentList(list))
}

type commentRequest struct {
	Comment *string `json:"comment"`
	MovieID string  `json:"movie_id"`
	// Accepted (subject: "comment, username") but never trusted: the author
	// is always the token's user.
	Username string `json:"username"`
}

func (r *commentRequest) content(c *gin.Context) (string, bool) {
	if r.Comment == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed", "fields": gin.H{"comment": "Comment is required"}})
		return "", false
	}
	text, msg := media.NormalizeComment(*r.Comment)
	if msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed", "fields": gin.H{"comment": msg}})
		return "", false
	}
	return text, true
}

// POST /api/v1/comments {comment, movie_id}
func (a *API) postComment(c *gin.Context) {
	var req commentRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.MovieID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed", "fields": gin.H{"movie_id": "movie_id is required"}})
		return
	}
	if _, err := uuid.Parse(req.MovieID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed", "fields": gin.H{"movie_id": "movie_id must be a movie id"}})
		return
	}
	a.createComment(c, req.MovieID, &req)
}

// POST /api/v1/movies/:id/comments {comment}
func (a *API) postMovieComment(c *gin.Context) {
	var req commentRequest
	if !bindJSON(c, &req) {
		return
	}
	a.createComment(c, c.Param("id"), &req)
}

func (a *API) createComment(c *gin.Context, movieID string, req *commentRequest) {
	text, ok := req.content(c)
	if !ok {
		return
	}
	m, ok := a.loadMovie(c, movieID)
	if !ok {
		return
	}
	u := currentUser(c)
	ctx, cancel := timeout(c)
	defer cancel()
	created, err := a.store.CreateComment(ctx, &media.Comment{
		ID: uuid.NewString(), MediaID: m.ID, UserID: uuidString(u.ID), Username: u.Username, Content: text,
	})
	if err != nil {
		a.log.Error("create comment failed", "media_id", m.ID, "error", err)
		fail(c, http.StatusInternalServerError, "Failed to create comment")
		return
	}
	c.Header("Location", a.basePath+Prefix+"/comments/"+created.ID)
	c.JSON(http.StatusCreated, commentView(created))
}

// loadComment answers 404 itself; with own it also answers 403 when the
// comment belongs to someone else.
func (a *API) loadComment(ctx context.Context, c *gin.Context, own bool) (*media.Comment, bool) {
	cm, err := a.store.GetComment(ctx, c.Param("id"))
	if errors.Is(err, media.ErrNotFound) {
		fail(c, http.StatusNotFound, "Comment not found")
		return nil, false
	}
	if err != nil {
		a.log.Error("load comment failed", "comment_id", c.Param("id"), "error", err)
		fail(c, http.StatusInternalServerError, "Failed to load comment")
		return nil, false
	}
	if own && cm.UserID != uuidString(currentUser(c).ID) {
		fail(c, http.StatusForbidden, "You can only modify your own comments")
		return nil, false
	}
	return cm, true
}

// GET /api/v1/comments/:id
func (a *API) getComment(c *gin.Context) {
	ctx, cancel := timeout(c)
	defer cancel()
	if cm, ok := a.loadComment(ctx, c, false); ok {
		c.JSON(http.StatusOK, commentView(cm))
	}
}

// PATCH /api/v1/comments/:id {comment} — author only (403 otherwise).
func (a *API) patchComment(c *gin.Context) {
	var req commentRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx, cancel := timeout(c)
	defer cancel()
	cm, ok := a.loadComment(ctx, c, true)
	if !ok {
		return
	}
	text, ok := req.content(c)
	if !ok {
		return
	}
	u := currentUser(c)
	updated, err := a.store.UpdateComment(ctx, cm.ID, cm.MediaID, u.ID, u.Username, text)
	if errors.Is(err, media.ErrNotFound) {
		fail(c, http.StatusNotFound, "Comment not found")
		return
	}
	if err != nil {
		a.log.Error("update comment failed", "comment_id", cm.ID, "error", err)
		fail(c, http.StatusInternalServerError, "Failed to update comment")
		return
	}
	c.JSON(http.StatusOK, commentView(updated))
}

// DELETE /api/v1/comments/:id — author only; 204.
func (a *API) deleteComment(c *gin.Context) {
	ctx, cancel := timeout(c)
	defer cancel()
	cm, ok := a.loadComment(ctx, c, true)
	if !ok {
		return
	}
	err := a.store.DeleteComment(ctx, cm.ID, cm.MediaID, currentUser(c).ID)
	if errors.Is(err, media.ErrNotFound) {
		fail(c, http.StatusNotFound, "Comment not found")
		return
	}
	if err != nil {
		a.log.Error("delete comment failed", "comment_id", cm.ID, "error", err)
		fail(c, http.StatusInternalServerError, "Failed to delete comment")
		return
	}
	c.Status(http.StatusNoContent)
}
