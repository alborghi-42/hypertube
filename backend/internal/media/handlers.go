package media

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
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
	store     *Store
	providers Registry
	engine    *Engine
	tx        *Transcoder
	log       *slog.Logger
	omdbKey   string
	subs      *Subtitles
}

// NewHandler also sets up ffmpeg conversion (42-16) and, unless one is
// already set, the engine's PostProcess hook caching a seekable .mp4.
// subs may be nil (no subtitles).
func NewHandler(store *Store, providers Registry, engine *Engine, subs *Subtitles, log *slog.Logger) *Handler {
	tx := NewTranscoderFromEnv(log)
	if engine.PostProcess == nil {
		engine.PostProcess = tx.CachePostProcess(engine)
	}
	return &Handler{store: store, providers: providers, engine: engine, tx: tx, log: log, omdbKey: os.Getenv("OMDB_API_KEY"), subs: subs}
}

// RegisterRoutes mounts the public movie GETs on api (the /api group, with
// Sessions.Optional so lists can flag the user's watched films), the
// rest of the JSON API on authed (/api behind a session check) and the
// video streams on stream (the /stream group, also session-only).
func (h *Handler) RegisterRoutes(api, authed, stream *gin.RouterGroup) {
	api.GET("/media", h.fetchMedia)
	api.GET("/media/search", h.searchMedia)
	api.GET("/media/details", h.getMediaDetails)

	// Starts a server-side download: logged-in users only.
	authed.GET("/media/play", h.playMedia)
	// Subtitle tracks of a media item; fetched lazily after /media/play.
	authed.GET("/media/subtitles", h.listSubtitles)

	authed.GET("/media/:mediaId/comments", h.getComments)
	authed.GET("/media/:mediaId/comments/:commentId", h.getComment)
	authed.POST("/media/:mediaId/comments", h.postComment)
	authed.PATCH("/media/:mediaId/comments/:commentId", h.patchComment)
	authed.DELETE("/media/:mediaId/comments/:commentId", h.deleteComment)

	// Range-capable video stream: from the torrent while downloading, from
	// disk once complete (see streamMedia).
	stream.GET("/media/:mediaId", h.streamMedia)
	stream.HEAD("/media/:mediaId", h.streamMedia)
	stream.GET("/media/:mediaId/subtitles/:file", h.serveSubtitle)
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

// providerTimeout bounds each provider's share of a merged list/search, so
// a slow or dead source only drops its own results.
const providerTimeout = 25 * time.Second

// collect runs fn on the requested provider, or on every provider in
// parallel when the provider param is absent. Failing providers are logged
// and skipped; ok is false (and a response written) only if all failed or
// the provider param is invalid.
func (h *Handler) collect(c *gin.Context, op string, fn func(context.Context, Provider) ([]Media, error)) (lists [][]Media, ok bool) {
	ids := h.providers.IDs()
	if name, set := c.GetQuery("provider"); set {
		if _, exists := h.providers[name]; !exists {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid provider"})
			return nil, false
		}
		ids = []string{name}
	}
	lists = make([][]Media, len(ids))
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(c.Request.Context(), providerTimeout)
			defer cancel()
			lists[i], errs[i] = fn(ctx, h.providers[id])
			if errs[i] != nil {
				h.log.Error(op+" failed", "provider", id, "error", errs[i])
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err == nil {
			return lists, true
		}
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to fetch media from provider"})
	return nil, false
}

// GET /api/media?page=&rows=&provider= — popular media from every provider
// (or just ?provider=), each in its own popularity order, deduplicated.
func (h *Handler) fetchMedia(c *gin.Context) {
	page, rows, ok := pagination(c)
	if !ok {
		return
	}
	lists, ok := h.collect(c, "fetch popular", func(ctx context.Context, p Provider) ([]Media, error) {
		return p.FetchPopular(ctx, page, rows)
	})
	if !ok {
		return
	}
	c.JSON(http.StatusOK, h.withWatched(c, mergeResults(lists, false)))
}

// GET /api/media/search?q=&page=&rows=&provider= — results from every
// provider (or just ?provider=), deduplicated by title+year, sorted by name.
// Each provider is paged independently, so a page holds up to rows results
// per provider.
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
	lists, ok := h.collect(c, "search", func(ctx context.Context, p Provider) ([]Media, error) {
		return p.Search(ctx, query, page, rows)
	})
	if !ok {
		return
	}
	c.JSON(http.StatusOK, h.withWatched(c, mergeResults(lists, true)))
}

// withWatched flags the results the session user has played. Anonymous
// requests (or a failed lookup) leave every item unwatched.
func (h *Handler) withWatched(c *gin.Context, list []Media) []Media {
	u := auth.CurrentUser(c)
	if u == nil || !u.ID.Valid || len(list) == 0 {
		return list
	}
	ids := make([]string, len(list))
	for i, m := range list {
		ids[i] = m.ID
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	watched, err := h.store.WatchedSet(ctx, u.ID, ids)
	if err != nil {
		h.log.Error("watch history lookup failed", "error", err)
		return list
	}
	for i := range list {
		list[i].Watched = watched[list[i].ID]
	}
	return list
}

// GET /api/media/details?external_id=&provider= — cached metadata, fetched
// from the provider and stored on first access, then completed with the
// OMDB credits (director, writers, production, genres, runtime) used by the
// movie page. A runtime found on OMDB is persisted as duration_seconds.
func (h *Handler) getMediaDetails(c *gin.Context) {
	externalID := c.Query("external_id")
	if externalID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing external media identifier"})
		return
	}
	name := c.DefaultQuery("provider", defaultProvider)
	internalID := InternalID(externalID, name)
	ctx := c.Request.Context()

	m, err := h.store.GetMedia(ctx, internalID)
	if err != nil {
		p, _, ok := h.provider(c)
		if !ok {
			return
		}
		fetched, ferr := p.FetchByID(ctx, externalID)
		if ferr != nil {
			h.log.Error("fetch by id failed", "provider", name, "external_id", externalID, "error", ferr)
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to fetch media from provider"})
			return
		}
		if fetched == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Media not found"})
			return
		}
		fetched.ID = internalID
		if m, err = h.store.CreateMedia(ctx, fetched); err != nil {
			h.log.Error("cache media failed", "media_id", internalID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cache media"})
			return
		}
	}

	EnrichDetails(ctx, h.store, h.omdbKey, h.log, m)
	c.JSON(http.StatusOK, m)
}

// EnrichDetails adds the OMDB fields that are not stored on the media row
// (served from the shared OMDB cache) and persists a missing runtime.
// Failures are logged and leave m as stored.
func EnrichDetails(ctx context.Context, store *Store, omdbKey string, log *slog.Logger, m *Media) {
	hadDuration := m.DurationSeconds > 0
	info, err := omdbLookup(ctx, omdbKey, omdbQuery{Title: m.Title, Year: m.Year})
	if err != nil {
		log.Warn("OMDB details unavailable", "media_id", m.ID, "error", err)
		return
	}
	if info == nil && m.Year != "" {
		// The provider year can be a re-release date; retry on the title.
		info, _ = omdbLookup(ctx, omdbKey, omdbQuery{Title: m.Title})
	}
	if info == nil {
		return
	}
	stored := *m
	applyOMDB(m, info)
	// The stored rating/actors come from the provider's own match; keep them.
	if stored.ImdbRating > 0 {
		m.ImdbRating = stored.ImdbRating
	}
	if len(stored.Actors) > 0 {
		m.Actors = stored.Actors
	}
	if !hadDuration && m.DurationSeconds > 0 {
		if err := store.SetDuration(ctx, m.ID, m.DurationSeconds); err != nil {
			log.Error("store duration failed", "media_id", m.ID, "error", err)
		}
	}
}

// GET /api/media/play?external_id=&provider= — makes the media playable.
// Fully downloaded media answer 200 "available" at once. Otherwise the
// torrent download is started (or joined: concurrent calls share one job)
// in the background and the response waits up to 15s for enough of the
// file head to start playback: 200 "ready", else 202 "downloading". In
// every case url streams the video (blocking on missing pieces).
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
	transcoded := false // set from the file being served, before resp
	resp := func(status int, state, msg string) {
		c.JSON(status, MediaStream{
			Transcoded: transcoded,
			InternalID: internalID,
			ExternalID: externalID,
			Provider:   name,
			Status:     state,
			URL:        publicURL(path.Join(StreamPrefix, internalID)),
			Message:    msg,
			Subtitles:  h.subs.List(internalID),
		})
	}

	m, err := h.store.GetMedia(ctx, internalID)
	if errors.Is(err, ErrNotFound) {
		m, err = p.FetchByID(ctx, externalID)
		if err == nil && m == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Media not found"})
			return
		}
		if err == nil {
			m.ID = internalID
			m, err = h.store.CreateMedia(ctx, m)
		}
	}
	if err != nil {
		h.log.Error("media lookup failed", "media_id", internalID, "error", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to load media"})
		return
	}
	h.engine.TouchWatched(internalID)
	// Subtitles are fetched in the background; the player polls
	// /api/media/subtitles for tracks that arrive after this response.
	h.subs.Ensure(*m, userLanguage(c))

	// The media row now exists: record the play for the session user
	// (42-13). Best-effort, playback does not depend on it.
	if u := auth.CurrentUser(c); u != nil && u.ID.Valid {
		if err := h.store.MarkWatched(ctx, u.ID, internalID); err != nil {
			h.log.Error("mark watched failed", "media_id", internalID, "error", err)
		}
	}

	// Only a completed download whose file is still on disk is "available";
	// "failed" or a vanished file start a fresh download below.
	if h.engine.Job(internalID) == nil && m.DownloadStatus == "downloaded" {
		if abs, ok := h.engine.AbsPath(m.LocalPath); ok {
			if _, err := os.Stat(abs); err == nil {
				transcoded = NeedsConversion(m.LocalPath)
				resp(http.StatusOK, "available", "Media already downloaded")
				return
			}
		}
	}

	job, err := h.engine.Start(p, m)
	if err != nil {
		h.log.Error("torrent start failed", "media_id", internalID, "error", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to start download", "internal_id": internalID})
		return
	}

	transcoded = NeedsConversion(job.RelPath)
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-job.Ready():
		resp(http.StatusOK, "ready", "Enough data downloaded to start playback")
	case <-job.Done():
		if job.Err() != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Download failed", "internal_id": internalID})
			return
		}
		resp(http.StatusOK, "available", "Media downloaded")
	case <-timer.C:
		resp(http.StatusAccepted, "downloading", "Download in progress")
	case <-ctx.Done():
	}
}

// GET /stream/media/:mediaId — the video bytes, with Range support. While
// the torrent is downloading, reads come from the torrent file reader, which
// prioritises the requested range and blocks until its pieces arrive, so
// playback (and seeking) works before the download completes.
func (h *Handler) streamMedia(c *gin.Context) {
	id := c.Param("mediaId")
	// Marks the media as in use (the retention job skips it) and bumps
	// last_watched_at, at most once a minute per media.
	defer h.engine.inUse.begin(id)()
	h.engine.TouchWatched(id)
	if j := h.engine.Job(id); j != nil && j.acquire() {
		defer j.release()
		r := j.file.NewReader()
		defer r.Close()
		r.SetContext(c.Request.Context())
		r.SetReadahead(8 << 20)
		if NeedsConversion(j.RelPath) {
			h.tx.Stream(c, "", r, func() io.ReadCloser {
				hr := j.file.NewReader()
				hr.SetContext(c.Request.Context())
				return hr
			})
			return
		}
		c.Header("Content-Type", j.MimeType)
		http.ServeContent(c.Writer, c.Request, path.Base(j.RelPath), time.Time{}, r)
		return
	}

	m, err := h.store.GetMedia(c.Request.Context(), id)
	if err != nil || m.DownloadStatus != "downloaded" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Media not available; start it with /api/media/play"})
		return
	}
	abs, ok := h.engine.AbsPath(m.LocalPath)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Media not available"})
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Media file missing; start it again with /api/media/play"})
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "Media not available"})
		return
	}
	if NeedsConversion(abs) {
		h.tx.Stream(c, abs, nil, nil)
		return
	}
	if m.MimeType != "" {
		c.Header("Content-Type", m.MimeType)
	}
	http.ServeContent(c.Writer, c.Request, st.Name(), st.ModTime(), f)
}

// --- subtitles ---

// userLanguage is the session user's preferred language ("" if anonymous).
func userLanguage(c *gin.Context) string {
	if u := auth.CurrentUser(c); u != nil {
		return u.Language
	}
	return ""
}

// GET /api/media/subtitles?media_id= — the cached tracks plus pending=true
// while a background fetch runs. Also (re)starts the fetch for the
// caller's language, so a user whose language was not fetched yet gets it.
func (h *Handler) listSubtitles(c *gin.Context) {
	id := c.Query("media_id")
	if !validMediaID(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid media identifier"})
		return
	}
	m, err := h.store.GetMedia(c.Request.Context(), id)
	if errors.Is(err, ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Media not found"})
		return
	}
	if err != nil {
		h.log.Error("media lookup failed", "media_id", id, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load media"})
		return
	}
	h.subs.Ensure(*m, userLanguage(c))
	c.JSON(http.StatusOK, gin.H{"media_id": id, "subtitles": h.subs.List(id), "pending": h.subs.Pending(id)})
}

// GET /stream/media/:mediaId/subtitles/:lang.vtt — one cached WebVTT track.
func (h *Handler) serveSubtitle(c *gin.Context) {
	lang, ok := strings.CutSuffix(c.Param("file"), ".vtt")
	p, valid := h.subs.FilePath(c.Param("mediaId"), lang)
	if !ok || !valid || !fileExists(p) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Subtitle not found"})
		return
	}
	c.Header("Content-Type", "text/vtt; charset=utf-8")
	c.Header("Cache-Control", "private, max-age=3600")
	c.File(p)
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
