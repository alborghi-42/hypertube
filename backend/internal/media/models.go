// Package media ports the archive.org media feature (search, details,
// background download + playback, comments) from the deprecated Clashboard
// media-service into the standalone Hypertube backend.
package media

import (
	"time"

	"github.com/google/uuid"
)

// Media is a media item with metadata from the provider plus OMDB enrichment.
type Media struct {
	ID         string `json:"id"`
	ExternalID string `json:"external_id"`
	Provider   string `json:"provider"`

	Title       string `json:"title"`
	Description string `json:"description"`
	Creator     string `json:"creator"`
	Date        string `json:"date"`

	ThumbnailURL    string  `json:"thumbnail_url"`
	DurationSeconds float64 `json:"duration_seconds"`

	DownloadStatus string `json:"download_status"`
	LocalPath      string `json:"local_path"`
	PublicURL      string `json:"public_url"`
	FileSize       int64  `json:"file_size"`
	MimeType       string `json:"mime_type"`

	// Year is the production year (provider date, else OMDB); "" if unknown.
	Year       string   `json:"year"`
	Actors     []string `json:"actors"`
	Genres     []string `json:"genres"`
	ImdbID     string   `json:"imdb_id,omitempty"`
	ImdbRating float64  `json:"imdb_rating"`

	// Credits from OMDB (details only; not persisted, re-read from the
	// OMDB cache). OMDB has no producer field: Production is the production
	// company and Writers the screenplay credits.
	Directors  []string `json:"directors,omitempty"`
	Writers    []string `json:"writers,omitempty"`
	Production string   `json:"production,omitempty"`
	PosterURL  string   `json:"poster_url,omitempty"`

	// Watched is set on list/search results for the session user (42-13).
	Watched bool `json:"watched"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MediaStream describes the stream state and URL returned by /api/media/play.
type MediaStream struct {
	InternalID string `json:"internal_id"`
	ExternalID string `json:"external_id,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Status     string `json:"status"`
	URL        string `json:"url,omitempty"`
	Message    string `json:"message,omitempty"`
	// Transcoded streams are converted on the fly (fragmented MP4): no
	// byte-range seeking; reload the url with ?t=<seconds> to seek.
	Transcoded bool `json:"transcoded"`
	// Subtitles already cached; more may follow (GET /api/media/subtitles).
	Subtitles []Subtitle `json:"subtitles"`
}

// Comment is a user comment on a media item.
type Comment struct {
	ID        string    `json:"id"`
	MediaID   string    `json:"media_id"`
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MaxCommentLength is the longest comment accepted, in characters (runes),
// counted after trimming surrounding whitespace.
const MaxCommentLength = 1000

type commentRequest struct {
	Content string `json:"content" binding:"required"`
}

// archive.org API payloads.

type archiveItem struct {
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Creator     string `json:"creator"`
	Date        string `json:"date"`
	PublicDate  string `json:"publicdate"`
}

type archiveResponse struct {
	Response struct {
		NumFound int           `json:"numFound"`
		Items    []archiveItem `json:"docs"`
	} `json:"response"`
}

type archiveMetadataFile struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Source string `json:"source"`
	Length string `json:"length"` // seconds ("713.62") or a clock ("12:03")
}

type archiveMetadata struct {
	Files      []archiveMetadataFile `json:"files"`
	CreatedStr string                `json:"created_str"`
	Metadata   struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Creator     string `json:"creator"`
		Date        string `json:"date"`
		PublicDate  string `json:"publicdate"`
		Identifier  string `json:"identifier"`
		Runtime     string `json:"runtime"`
	} `json:"metadata"`
}

// InternalID returns a deterministic UUID for provider + externalID (SHA-1
// based), so the same item always maps to the same media row.
func InternalID(externalID, provider string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(provider+":"+externalID)).String()
}
