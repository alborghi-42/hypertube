package media

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/alborghi-42/hypertube/backend/internal/auth"
	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

const defaultProvider = "archive.org"

// Handler serves the media and comments API.
type Handler struct {
	store       *Store
	providers   Registry
	downloadDir string
	log         *slog.Logger
}

func NewHandler(store *Store, providers Registry, downloadDir string, log *slog.Logger) *Handler {
	return &Handler{store: store, providers: providers, downloadDir: downloadDir, log: log}
}

// RegisterRoutes mounts the public movie GETs on api (the /api group), the
// rest of the JSON API on authed (/api behind a session check) and the
// downloaded files on stream (the /stream group, also session-only).
func (h *Handler) RegisterRoutes(api, authed, stream *gin.RouterGroup) {
	api.GET("/media", h.fetchMedia)
	api.GET("/media/search", h.searchMedia)
	api.GET("/media/details", h.getMediaDetails)

	// Starts a server-side download: logged-in users only.
	authed.GET("/media/play", h.playMedia)

	authed.GET("/media/:mediaId/comments", h.getComments)
	authed.GET("/media/:mediaId/comments/:commentId", h.getComment)
	authed.POST("/media/:mediaId/comments", h.postComment)
	authed.PATCH("/media/:mediaId/comments/:commentId", h.patchComment)
	authed.DELETE("/media/:mediaId/comments/:commentId", h.deleteComment)

	// Downloaded files; http.FileServer handles Range requests for seeking.
	stream.Static("/files", h.downloadDir)
}

// publicURL turns a stored public_url ("stream/files/...") into a
// same-origin absolute path.
func publicURL(stored string) string {
	if stored == "" {
		return ""
	}
	return "/" + strings.TrimPrefix(stored, "/")
}

func (h *Handler) provider(c *gin.Context) (Provider, string, bool) {
	name := c.DefaultQuery("provider", defaultProvider)
	p, ok := h.providers[name]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid provider"})
	}
	return p, name, ok
}

func pagination(c *gin.Context) (page, rows int, ok bool) {
	rows, err := strconv.Atoi(c.DefaultQuery("rows", "50"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid rows parameter"})
		return 0, 0, false
	}
	page, err = strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid page parameter"})
		return 0, 0, false
	}
	return page, rows, true
}

// GET /api/media?page=&rows=&provider= — popular media.
func (h *Handler) fetchMedia(c *gin.Context) {
	page, rows, ok := pagination(c)
	if !ok {
		return
	}
	p, name, ok := h.provider(c)
	if !ok {
		return
	}
	result, err := p.FetchPopular(c.Request.Context(), page, rows)
	if err != nil {
		h.log.Error("fetch popular failed", "provider", name, "error", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to fetch media from provider"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// GET /api/media/search?q=&page=&rows=&provider=
func (h *Handler) searchMedia(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing search query"})
		return
	}
	page, rows, ok := pagination(c)
	if !ok {
		return
	}
	p, name, ok := h.provider(c)
	if !ok {
		return
	}
	result, err := p.Search(c.Request.Context(), query, page, rows)
	if err != nil {
		h.log.Error("search failed", "provider", name, "error", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to search media from provider"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// GET /api/media/details?external_id=&provider= — cached metadata, fetched
// from the provider and stored on first access.
func (h *Handler) getMediaDetails(c *gin.Context) {
	externalID := c.Query("external_id")
	if externalID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing external media identifier"})
		return
	}
	name := c.DefaultQuery("provider", defaultProvider)
	internalID := InternalID(externalID, name)

	if existing, err := h.store.GetMedia(c.Request.Context(), internalID); err == nil {
		c.JSON(http.StatusOK, existing)
		return
	}

	p, _, ok := h.provider(c)
	if !ok {
		return
	}
	m, err := p.FetchByID(c.Request.Context(), externalID)
	if err != nil {
		h.log.Error("fetch by id failed", "provider", name, "external_id", externalID, "error", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to fetch media from provider"})
		return
	}
	if m == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Media not found"})
		return
	}
	m.ID = internalID

	saved, err := h.store.CreateMedia(c.Request.Context(), m)
	if err != nil {
		h.log.Error("cache media failed", "media_id", internalID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cache media"})
		return
	}
	c.JSON(http.StatusOK, saved)
}

// GET /api/media/play?external_id=&provider= — starts a background download
// and holds the response until it reaches 5% (200, status "ready"), or
// answers 202 "downloading" after 15s. Already known media return 200
// "available" immediately.
func (h *Handler) playMedia(c *gin.Context) {
	externalID := c.Query("external_id")
	if externalID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing media identifier"})
		return
	}
	p, name, ok := h.provider(c)
	if !ok {
		return
	}
	internalID := InternalID(externalID, name)
	ctx := c.Request.Context()

	if m, err := h.store.GetMedia(ctx, internalID); err == nil {
		if m.DownloadStatus == "downloaded" || m.DownloadStatus == "downloading" || m.LocalPath != "" {
			c.JSON(http.StatusOK, MediaStream{
				InternalID: internalID,
				ExternalID: externalID,
				Provider:   name,
				Status:     "available",
				URL:        publicURL(m.PublicURL),
				Message:    "Media already available",
			})
			return
		}
	}

	progress := make(chan int, 100)

	// Detached from the request context so the download outlives the response.
	go func() {
		dlCtx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()

		filePath, err := p.Download(dlCtx, externalID, progress, h.store)
		bg := context.Background()
		m, getErr := h.store.GetMedia(bg, internalID)
		if err != nil {
			h.log.Error("background download failed", "external_id", externalID, "error", err)
			if getErr == nil {
				m.DownloadStatus = "failed"
				_, _ = h.store.UpdateMedia(bg, m)
			}
			return
		}
		if getErr == nil {
			m.DownloadStatus = "downloaded"
			m.LocalPath = filePath
			_, _ = h.store.UpdateMedia(bg, m)
		}
	}()

	const threshold = 5
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()

waitLoop:
	for {
		select {
		case pct, ok := <-progress:
			if !ok {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Download failed or discontinued before reaching stable threshold", "internal_id": internalID})
				return
			}
			if pct >= threshold {
				break waitLoop
			}
		case <-timer.C:
			m, err := h.store.GetMedia(ctx, internalID)
			if err != nil {
				h.log.Error("media status unavailable after timeout", "media_id", internalID, "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Download initiated but failed to retrieve media status after timeout", "internal_id": internalID})
				return
			}
			c.JSON(http.StatusAccepted, MediaStream{
				InternalID: internalID,
				ExternalID: externalID,
				Provider:   name,
				Status:     "downloading",
				URL:        publicURL(m.PublicURL),
				Message:    "Download initiated but progress is slow",
			})
			return
		case <-ctx.Done():
			return
		}
	}

	m, err := h.store.GetMedia(ctx, internalID)
	if err != nil {
		h.log.Error("media status unavailable after download start", "media_id", internalID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Download started but failed to retrieve media status", "internal_id": internalID})
		return
	}
	c.JSON(http.StatusOK, MediaStream{
		InternalID: internalID,
		ExternalID: externalID,
		Provider:   name,
		Status:     "ready",
		URL:        publicURL(m.PublicURL),
		Message:    "Media download reached stable threshold",
	})
}

// --- comments ---

func (h *Handler) commentError(c *gin.Context, err error, msg string) {
	if errors.Is(err, ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Comment not found"})
		return
	}
	h.log.Error(msg, "media_id", c.Param("mediaId"), "comment_id", c.Param("commentId"), "error", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": msg})
}

func (h *Handler) getComments(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	comments, err := h.store.ListComments(ctx, c.Param("mediaId"))
	if err != nil {
		h.commentError(c, err, "Failed to fetch comments")
		return
	}
	c.JSON(http.StatusOK, comments)
}

func (h *Handler) getComment(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	comment, err := h.store.GetComment(ctx, c.Param("commentId"))
	if err != nil {
		h.commentError(c, err, "Failed to fetch comment")
		return
	}
	c.JSON(http.StatusOK, comment)
}

// bindComment reads and validates the comment body: non-empty after trimming
// and at most MaxCommentLength characters. Writes the 400 itself.
func bindComment(c *gin.Context) (string, bool) {
	var req commentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return "", false
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Comment cannot be empty"})
		return "", false
	}
	if utf8.RuneCountInString(content) > MaxCommentLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Comment is too long", "max_length": MaxCommentLength})
		return "", false
	}
	return content, true
}

// sessionUser returns the logged-in user. The comment routes sit behind
// sessions.Require(), so a missing user is a wiring bug; answer 401 anyway.
func sessionUser(c *gin.Context) (*repository.User, bool) {
	u := auth.CurrentUser(c)
	if u == nil || !u.ID.Valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return nil, false
	}
	return u, true
}

// ownComment loads the comment in the URL and checks the caller wrote it:
// 404 if it does not exist on this media, 403 if it belongs to someone else.
func (h *Handler) ownComment(ctx context.Context, c *gin.Context, u *repository.User) bool {
	comment, err := h.store.GetComment(ctx, c.Param("commentId"))
	if err != nil {
		h.commentError(c, err, "Failed to fetch comment")
		return false
	}
	if comment.MediaID != c.Param("mediaId") {
		c.JSON(http.StatusNotFound, gin.H{"error": "Comment not found"})
		return false
	}
	if comment.UserID != fromUUID(u.ID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You can only modify your own comments"})
		return false
	}
	return true
}

func (h *Handler) postComment(c *gin.Context) {
	u, ok := sessionUser(c)
	if !ok {
		return
	}
	content, ok := bindComment(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	created, err := h.store.CreateComment(ctx, &Comment{
		ID:       uuid.NewString(),
		MediaID:  c.Param("mediaId"),
		UserID:   fromUUID(u.ID),
		Username: u.Username,
		Content:  content,
	})
	if err != nil {
		h.commentError(c, err, "Failed to create comment")
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (h *Handler) patchComment(c *gin.Context) {
	u, ok := sessionUser(c)
	if !ok {
		return
	}
	content, ok := bindComment(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if !h.ownComment(ctx, c, u) {
		return
	}
	updated, err := h.store.UpdateComment(ctx, c.Param("commentId"), c.Param("mediaId"), u.ID, u.Username, content)
	if err != nil {
		h.commentError(c, err, "Failed to update comment")
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (h *Handler) deleteComment(c *gin.Context) {
	u, ok := sessionUser(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if !h.ownComment(ctx, c, u) {
		return
	}
	if err := h.store.DeleteComment(ctx, c.Param("commentId"), c.Param("mediaId"), u.ID); err != nil {
		h.commentError(c, err, "Failed to delete comment")
		return
	}
	c.Status(http.StatusNoContent)
}
