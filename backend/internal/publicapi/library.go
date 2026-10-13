package publicapi

// Extra routes (42-27, bonus): search, add and delete movies, watch history.

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/alborghi-42/hypertube/backend/internal/media"
)

// externalIDPattern bounds what reaches a provider URL: archive.org
// identifiers and publicdomaintorrents ids are plain [A-Za-z0-9._-].
var externalIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)

func listItem(m *media.Media) movieListItem {
	return movieListItem{ID: m.ID, Name: m.Title, Provider: m.Provider, ExternalID: m.ExternalID}
}

// searchMovies is GET /api/v1/movies?q=: every provider's results, sorted by
// name. Results are not cached: an id 404s on /movies/:id until the movie is
// added with POST /movies (or listed on the front page).
func (a *API) searchMovies(c *gin.Context, query string, page, rows int) {
	list, err := media.SearchAll(c.Request.Context(), a.providers, query, page, rows, a.log)
	if err != nil {
		fail(c, http.StatusBadGateway, "Failed to fetch movies from providers")
		return
	}
	out := make([]movieListItem, 0, len(list))
	for i := range list {
		out = append(out, listItem(&list[i]))
	}
	c.JSON(http.StatusOK, out)
}

// POST /api/v1/movies {provider, external_id} — adds a provider's movie to
// the library: 201 + Location when new, 200 + Location when it was already
// there (the id is deterministic, so the call is idempotent).
func (a *API) addMovie(c *gin.Context) {
	var req struct {
		Provider   string `json:"provider"`
		ExternalID string `json:"external_id"`
	}
	if !bindJSON(c, &req) {
		return
	}
	fields := gin.H{}
	p, known := a.providers[req.Provider]
	switch {
	case req.Provider == "":
		fields["provider"] = "provider is required"
	case !known:
		fields["provider"] = "provider must be one of: " + strings.Join(a.providers.IDs(), ", ")
	}
	switch {
	case req.ExternalID == "":
		fields["external_id"] = "external_id is required"
	case !externalIDPattern.MatchString(req.ExternalID):
		fields["external_id"] = "external_id is not a valid provider identifier"
	}
	if len(fields) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed", "fields": fields})
		return
	}

	id := media.InternalID(req.ExternalID, req.Provider)
	ctx := c.Request.Context()
	location := a.basePath + Prefix + "/movies/" + id
	if m, err := a.store.GetMedia(ctx, id); err == nil {
		c.Header("Location", location)
		c.JSON(http.StatusOK, listItem(m))
		return
	} else if !errors.Is(err, media.ErrNotFound) {
		a.log.Error("load movie failed", "media_id", id, "error", err)
		fail(c, http.StatusInternalServerError, "Failed to load movie")
		return
	}

	pctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	m, err := p.FetchByID(pctx, req.ExternalID)
	if err != nil {
		a.log.Error("api add movie: provider fetch failed", "provider", req.Provider, "external_id", req.ExternalID, "error", err)
		fail(c, http.StatusBadGateway, "Failed to fetch movie from provider")
		return
	}
	if m == nil {
		fail(c, http.StatusNotFound, "Movie not found at provider")
		return
	}
	m.ID, m.Provider, m.ExternalID = id, req.Provider, req.ExternalID
	created, err := a.store.CreateMedia(ctx, m)
	if err != nil {
		// Lost a race with another add/list/play of the same movie.
		if existing, gerr := a.store.GetMedia(ctx, id); gerr == nil {
			c.Header("Location", location)
			c.JSON(http.StatusOK, listItem(existing))
			return
		}
		a.log.Error("api add movie: store failed", "media_id", id, "error", err)
		fail(c, http.StatusInternalServerError, "Failed to add movie")
		return
	}
	c.Header("Location", location)
	c.JSON(http.StatusCreated, listItem(created))
}

// DELETE /api/v1/movies/:id — removes the movie's downloaded files and its
// row (its comments and watch history go with it). Movies are shared, so
// this is 409 while it is downloading/streaming/converting, or when another
// user has commented on it. 204 on success.
func (a *API) deleteMovie(c *gin.Context) {
	m, ok := a.loadMovie(c, c.Param("id"))
	if !ok {
		return
	}
	ctx, cancel := timeout(c)
	defer cancel()
	err := a.engine.DeleteMedia(ctx, m.ID, currentUser(c).ID)
	switch {
	case err == nil:
		c.Status(http.StatusNoContent)
	case errors.Is(err, media.ErrNotFound):
		fail(c, http.StatusNotFound, "Movie not found")
	case errors.Is(err, media.ErrMediaInUse):
		fail(c, http.StatusConflict, "Movie is being downloaded or watched")
	case errors.Is(err, media.ErrMediaShared):
		fail(c, http.StatusConflict, "Other users have commented on this movie")
	default:
		a.log.Error("api delete movie failed", "media_id", m.ID, "error", err)
		fail(c, http.StatusInternalServerError, "Failed to delete movie")
	}
}

type watchedResponse struct {
	MovieID   string    `json:"movie_id"`
	Name      string    `json:"name"`
	WatchedAt time.Time `json:"watched_at"`
}

// GET /api/v1/users/:id/watched?page=&rows= — the token owner's watch
// history, most recent first; 403 for any other user (like PATCH).
func (a *API) listWatched(c *gin.Context) {
	me := currentUser(c)
	id, ok := parseUUID(c.Param("id"))
	if !ok {
		fail(c, http.StatusNotFound, "User not found")
		return
	}
	if id != me.ID {
		fail(c, http.StatusForbidden, "You can only see your own watch history")
		return
	}
	page, ok := intQuery(c, "page", 1, 1, 1000)
	if !ok {
		return
	}
	rows, ok := intQuery(c, "rows", 20, 1, maxRows)
	if !ok {
		return
	}
	ctx, cancel := timeout(c)
	defer cancel()
	list, err := a.store.WatchHistory(ctx, me.ID, int32(rows), int32((page-1)*rows))
	if err != nil {
		a.log.Error("list watch history failed", "error", err)
		fail(c, http.StatusInternalServerError, "Failed to load watch history")
		return
	}
	out := make([]watchedResponse, 0, len(list))
	for _, w := range list {
		out = append(out, watchedResponse{MovieID: w.MediaID, Name: w.Title, WatchedAt: w.WatchedAt})
	}
	c.JSON(http.StatusOK, out)
}

// POST /api/v1/movies/:id/watched — marks the movie watched by the token
// owner (refreshing watched_at if it already was); 204.
func (a *API) markWatched(c *gin.Context) {
	m, ok := a.loadMovie(c, c.Param("id"))
	if !ok {
		return
	}
	ctx, cancel := timeout(c)
	defer cancel()
	if err := a.store.MarkWatched(ctx, currentUser(c).ID, m.ID); err != nil {
		a.log.Error("api mark watched failed", "media_id", m.ID, "error", err)
		fail(c, http.StatusInternalServerError, "Failed to mark as watched")
		return
	}
	c.Status(http.StatusNoContent)
}

// DELETE /api/v1/movies/:id/watched — removes it from the token owner's
// history; 204, or 404 when it was not there.
func (a *API) unmarkWatched(c *gin.Context) {
	m, ok := a.loadMovie(c, c.Param("id"))
	if !ok {
		return
	}
	ctx, cancel := timeout(c)
	defer cancel()
	err := a.store.UnmarkWatched(ctx, currentUser(c).ID, m.ID)
	switch {
	case err == nil:
		c.Status(http.StatusNoContent)
	case errors.Is(err, media.ErrNotFound):
		fail(c, http.StatusNotFound, "Movie is not in your watch history")
	default:
		a.log.Error("api unmark watched failed", "media_id", m.ID, "error", err)
		fail(c, http.StatusInternalServerError, "Failed to unmark as watched")
	}
}
