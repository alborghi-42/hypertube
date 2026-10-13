package media

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

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

// FetchPopular returns popular feature films.
func (a *ArchiveOrg) FetchPopular(ctx context.Context, page, rows int) ([]Media, error) {
	return a.Search(ctx, "mediatype:movies AND collection:feature_films", page, rows)
}

func (a *ArchiveOrg) fetchMetadata(ctx context.Context, id string) (*archiveMetadata, error) {
	metaURL := fmt.Sprintf("https://archive.org/metadata/%s", id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
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

// FetchByID returns a single item's metadata, enriched with OMDB stats.
func (a *ArchiveOrg) FetchByID(ctx context.Context, id string) (*Media, error) {
	result, err := a.fetchMetadata(ctx, id)
	if err != nil {
		a.log.Error("archive.org metadata fetch failed", "external_id", id, "error", err)
		return nil, err
	}

	md := result.Metadata
	date := md.Date
	if date == "" {
		date = md.PublicDate
	}
	if date == "" {
		date = result.CreatedStr
	}

	rating, actors, err := omdbStats(ctx, a.omdbKey, md.Title)
	if err != nil {
		a.log.Warn("OMDB stats unavailable", "title", md.Title, "error", err)
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
	return &Media{
		ID:           InternalID(externalID, a.ID()),
		ExternalID:   externalID,
		Provider:     a.ID(),
		Title:        title,
		Description:  md.Description,
		Creator:      md.Creator,
		Date:         date,
		ThumbnailURL: thumbnailURL(externalID),
		CreatedAt:    now,
		UpdatedAt:    now,
		Actors:       actors,
		ImdbRating:   rating,
	}, nil
}

// Search queries archive.org's advancedsearch for torrent-backed films.
func (a *ArchiveOrg) Search(ctx context.Context, query string, page, rows int) ([]Media, error) {
	searchQuery := fmt.Sprintf("(%s) AND collection:moviesandfilms AND format:(\"Archive BitTorrent\")", query)
	urlStr := fmt.Sprintf("https://archive.org/advancedsearch.php?q=%s&fl[]=identifier&fl[]=title&fl[]=date&fl[]=publicdate&sort[]=downloads+desc&output=json&rows=%d&page=%d",
		url.QueryEscape(searchQuery), rows, page)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
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
		rating, _, err := omdbStats(ctx, a.omdbKey, item.Title)
		if err != nil {
			a.log.Debug("OMDB stats unavailable for search result", "title", item.Title, "error", err)
		}
		now := time.Now()
		list[i] = Media{
			ID:           InternalID(item.Identifier, a.ID()),
			ExternalID:   item.Identifier,
			Provider:     a.ID(),
			Title:        item.Title,
			Description:  item.Description,
			Creator:      item.Creator,
			Date:         date,
			ThumbnailURL: thumbnailURL(item.Identifier),
			CreatedAt:    now,
			UpdatedAt:    now,
			ImdbRating:   rating,
		}
	}
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
	resp, err := http.DefaultClient.Do(req)
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
