package media

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

// archiveClient bounds every archive.org call so an outage cannot hang a request.
var archiveClient = &http.Client{Timeout: 20 * time.Second}

// ArchiveOrg implements Provider for archive.org.
type ArchiveOrg struct {
	omdbKey string
	log     *slog.Logger
}

func NewArchiveOrg(omdbKey string, log *slog.Logger) *ArchiveOrg {
	return &ArchiveOrg{omdbKey: omdbKey, log: log}
}

func (a *ArchiveOrg) ID() string { return "archive.org" }

func thumbnailURL(id string) string {
	return fmt.Sprintf("https://archive.org/services/img/%s?size=200", id)
}

// FetchPopular returns the most downloaded feature films.
func (a *ArchiveOrg) FetchPopular(ctx context.Context, page, rows int) ([]Media, error) {
	return a.search(ctx, "mediatype:movies AND collection:feature_films", "downloads desc", page, rows)
}

// luceneSpecial are the characters with meaning in archive.org's Lucene
// query syntax; user input escapes them so it is matched as plain text.
const luceneSpecial = `+-&|!(){}[]^"~*?:\\/`

func escapeLucene(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(luceneSpecial, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (a *ArchiveOrg) fetchMetadata(ctx context.Context, id string) (*archiveMetadata, error) {
	metaURL := fmt.Sprintf("https://archive.org/metadata/%s", id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := archiveClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive.org metadata returned status %d", resp.StatusCode)
	}
	var result archiveMetadata
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode metadata: %w", err)
	}
	return &result, nil
}

// Duration reads an item's runtime from its archive.org metadata (used when
// OMDB has no runtime for a cached row); 0 when unknown.
func (a *ArchiveOrg) Duration(ctx context.Context, id string) (float64, error) {
	result, err := a.fetchMetadata(ctx, id)
	if err != nil {
		return 0, err
	}
	return archiveDuration(result.Metadata.Runtime, result.Files), nil
}

// FetchByID returns a single item's metadata, enriched with OMDB stats.
func (a *ArchiveOrg) FetchByID(ctx context.Context, id string) (*Media, error) {
	result, err := a.fetchMetadata(ctx, id)
	if err != nil {
		a.log.Error("archive.org metadata fetch failed", "external_id", id, "error", err)
		return nil, err
	}

	md := result.Metadata
	// archive.org answers 200 {} for an unknown identifier.
	if md.Identifier == "" && md.Title == "" && len(result.Files) == 0 {
		return nil, nil
	}
	date := md.Date
	if date == "" {
		date = md.PublicDate
	}
	if date == "" {
		date = result.CreatedStr
	}

	externalID := md.Identifier
	if externalID == "" {
		externalID = id
	}
	title := md.Title
	if title == "" {
		title = externalID
	}

	now := time.Now()
	m := &Media{
		ID:           InternalID(externalID, a.ID()),
		ExternalID:   externalID,
		Provider:     a.ID(),
		Title:        title,
		Description:  PlainText(md.Description),
		Creator:      md.Creator,
		Date:         date,
		Year:         mediaYear(md.Date),
		ThumbnailURL: thumbnailURL(externalID),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	info, err := omdbLookup(ctx, a.omdbKey, omdbQuery{Title: md.Title, Year: m.Year})
	if err != nil {
		a.log.Warn("OMDB stats unavailable", "title", md.Title, "error", err)
	}
	applyOMDB(m, info)
	if m.DurationSeconds == 0 {
		m.DurationSeconds = archiveDuration(md.Runtime, result.Files)
	}
	return m, nil
}

// Search matches the user's words (escaped, all required) against
// torrent-backed films, sorted by title.
func (a *ArchiveOrg) Search(ctx context.Context, query string, page, rows int) ([]Media, error) {
	words := strings.Fields(query)
	for i, w := range words {
		words[i] = escapeLucene(w)
	}
	if len(words) == 0 {
		return []Media{}, nil
	}
	return a.search(ctx, "("+strings.Join(words, " AND ")+")", "titleSorter asc", page, rows)
}

// search runs a raw Lucene query against archive.org's advancedsearch,
// restricted to torrent-backed films.
func (a *ArchiveOrg) search(ctx context.Context, lucene, sortBy string, page, rows int) ([]Media, error) {
	query := lucene
	searchQuery := fmt.Sprintf("(%s) AND collection:moviesandfilms AND format:(\"Archive BitTorrent\")", lucene)
	q := url.Values{}
	q.Set("q", searchQuery)
	for _, f := range []string{"identifier", "title", "date", "publicdate"} {
		q.Add("fl[]", f)
	}
	q.Set("sort[]", sortBy)
	q.Set("output", "json")
	q.Set("rows", strconv.Itoa(rows))
	q.Set("page", strconv.Itoa(page))
	urlStr := "https://archive.org/advancedsearch.php?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	resp, err := archiveClient.Do(req)
	if err != nil {
		a.log.Error("archive.org search failed", "url", urlStr, "error", err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive.org search returned status %d", resp.StatusCode)
	}

	var result archiveResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	a.log.Info("searched archive.org", "count", len(result.Response.Items), "query", query)

	list := make([]Media, len(result.Response.Items))
	for i, item := range result.Response.Items {
		date := item.Date
		if date == "" {
			date = item.PublicDate
		}
		now := time.Now()
		list[i] = Media{
			ID:          InternalID(item.Identifier, a.ID()),
			ExternalID:  item.Identifier,
			Provider:    a.ID(),
			Title:       item.Title,
			Description: PlainText(item.Description),
			Creator:     item.Creator,
			Date:        date,
			// publicdate is the upload date, not the production year.
			Year:         mediaYear(item.Date),
			ThumbnailURL: thumbnailURL(item.Identifier),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
	}
	enrichOMDB(ctx, a.omdbKey, list, func(i int) omdbQuery {
		return omdbQuery{Title: list[i].Title, Year: list[i].Year}
	})
	return list, nil
}

// maxTorrentSize bounds the .torrent download (archive.org ones are KBs).
const maxTorrentSize = 16 << 20

// TorrentSource fetches archive.org's generated <id>_archive.torrent. It
// lists every file of the item (originals and derivatives) and carries
// archive.org web seeds, so it downloads even with no other peers.
func (a *ArchiveOrg) TorrentSource(ctx context.Context, id string) (*TorrentSource, error) {
	esc := url.PathEscape(id)
	torrentURL := fmt.Sprintf("https://archive.org/download/%s/%s_archive.torrent", esc, esc)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, torrentURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := archiveClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive.org torrent returned status %d", resp.StatusCode)
	}
	mi, err := metainfo.Load(io.LimitReader(resp.Body, maxTorrentSize))
	if err != nil {
		return nil, fmt.Errorf("parse archive.org torrent: %w", err)
	}
	return &TorrentSource{MetaInfo: mi}, nil
}
