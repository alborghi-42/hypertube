package publicapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/alborghi-42/hypertube/backend/internal/media"
)

const (
	maxRows        = 100
	latestComments = 50
)

func intQuery(c *gin.Context, key string, def, min, max int) (int, bool) {
	v, err := strconv.Atoi(c.DefaultQuery(key, strconv.Itoa(def)))
	if err != nil || v < min || v > max {
		fail(c, http.StatusBadRequest, "Invalid "+key+" parameter")
		return 0, false
	}
	return v, true
}

type movieListItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GET /api/v1/movies?page=&rows= — the front page (popular movies). Listed
// movies are cached so their id resolves on GET /movies/:id.
func (a *API) listMovies(c *gin.Context) {
	page, ok := intQuery(c, "page", 1, 1, 1000)
	if !ok {
		return
	}
	rows, ok := intQuery(c, "rows", 20, 1, maxRows)
	if !ok {
		return
	}
	list, err := media.FrontPage(c.Request.Context(), a.providers, page, rows, a.log)
	if err != nil {
		fail(c, http.StatusBadGateway, "Failed to fetch movies from providers")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	out := make([]movieListItem, 0, len(list))
	for i := range list {
		if err := a.store.EnsureMedia(ctx, &list[i]); err != nil {
			a.log.Warn("cache listed movie failed", "media_id", list[i].ID, "error", err)
		}
		out = append(out, movieListItem{ID: list[i].ID, Name: list[i].Title})
	}
	c.JSON(http.StatusOK, out)
}

type movieResponse struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	ImdbRating     *float64 `json:"imdb_rating"`
	ProductionYear *int     `json:"production_year"`
	Length         *int     `json:"length"`    // seconds
	Subtitles      []string `json:"subtitles"` // cached languages (ISO 639-1), English first
	CommentsCount  int64    `json:"comments_count"`
	// OMDb credits, as on the movie page; empty when OMDb has no match.
	Directors  []string `json:"directors"`
	Writers    []string `json:"writers"`
	Production string   `json:"production"`
	PosterURL  string   `json:"poster_url"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// loadMovie answers 404 itself when the movie is unknown.
func (a *API) loadMovie(c *gin.Context, id string) (*media.Media, bool) {
	if _, err := uuid.Parse(id); err != nil {
		fail(c, http.StatusNotFound, "Movie not found")
		return nil, false
	}
	m, err := a.store.GetMedia(c.Request.Context(), id)
	if errors.Is(err, media.ErrNotFound) {
		fail(c, http.StatusNotFound, "Movie not found")
		return nil, false
	}
	if err != nil {
		a.log.Error("load movie failed", "media_id", id, "error", err)
		fail(c, http.StatusInternalServerError, "Failed to load movie")
		return nil, false
	}
	return m, true
}

// GET /api/v1/movies/:id — what has been collected about the movie.
func (a *API) getMovie(c *gin.Context) {
	m, ok := a.loadMovie(c, c.Param("id"))
	if !ok {
		return
	}
	count, err := a.store.CountComments(c.Request.Context(), m.ID)
	if err != nil {
		a.log.Error("count comments failed", "media_id", m.ID, "error", err)
		fail(c, http.StatusInternalServerError, "Failed to load movie")
		return
	}
	ectx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	media.EnrichDetails(ectx, a.store, os.Getenv("OMDB_API_KEY"), a.log, m)
	resp := movieResponse{
		ID:            m.ID,
		Name:          m.Title,
		Subtitles:     a.subs.Languages(m.ID),
		CommentsCount: count,
		Directors:     nonNil(m.Directors),
		Writers:       nonNil(m.Writers),
		Production:    m.Production,
		PosterURL:     m.PosterURL,
	}
	if m.ImdbRating > 0 {
		resp.ImdbRating = &m.ImdbRating
	}
	if y, err := strconv.Atoi(m.Year); err == nil {
		resp.ProductionYear = &y
	}
	if m.DurationSeconds > 0 {
		l := int(m.DurationSeconds)
		resp.Length = &l
	}
	c.JSON(http.StatusOK, resp)
}
