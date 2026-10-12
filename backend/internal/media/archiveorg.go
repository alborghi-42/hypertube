package media

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// PublicFilesPrefix is the URL prefix downloaded files are served under
// (see Handler.RegisterRoutes). Stored public_url values are relative to "/".
const PublicFilesPrefix = "stream/files"

// ArchiveOrg implements Provider for archive.org.
type ArchiveOrg struct {
	downloadDir string
	omdbKey     string
	log         *slog.Logger
}

func NewArchiveOrg(downloadDir, omdbKey string, log *slog.Logger) *ArchiveOrg {
	return &ArchiveOrg{downloadDir: downloadDir, omdbKey: omdbKey, log: log}
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

// Download picks the best MP4/h.264 file of the item and downloads it over
// HTTP into <downloadDir>/archive.org/<internal-id><ext>.
func (a *ArchiveOrg) Download(ctx context.Context, id string, progress chan<- int, store *Store) (string, error) {
	if progress != nil {
		defer close(progress)
	}

	metadata, err := a.fetchMetadata(ctx, id)
	if err != nil {
		return "", err
	}

	// Prefer original files over derived ones.
	var videoFileName string
	for _, file := range metadata.Files {
		format := strings.ToLower(file.Format)
		if strings.Contains(format, "mp4") || strings.Contains(format, "h.264") || strings.Contains(format, "mpeg4") {
			videoFileName = file.Name
			if file.Source == "original" {
				break
			}
		}
	}
	if videoFileName == "" {
		return "", fmt.Errorf("no suitable MP4 file found for identifier: %s", id)
	}

	downloadURL := fmt.Sprintf("https://archive.org/download/%s/%s", id, videoFileName)
	internalID := InternalID(id, a.ID())
	ext := filepath.Ext(videoFileName)
	if ext == "" {
		ext = ".mp4"
	}

	providerDir := filepath.Join(a.downloadDir, a.ID())
	if err := os.MkdirAll(providerDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create storage directory: %w", err)
	}
	fileName := internalID + ext
	localPath := filepath.Join(providerDir, fileName)
	relPath := path.Join(a.ID(), fileName)
	publicURL := path.Join(PublicFilesPrefix, relPath)

	rating, actors, err := omdbStats(ctx, a.omdbKey, metadata.Metadata.Title)
	if err != nil {
		a.log.Warn("OMDB stats unavailable", "title", metadata.Metadata.Title, "error", err)
	}

	state := func(status string) *Media {
		return &Media{
			ID:             internalID,
			ExternalID:     id,
			Provider:       a.ID(),
			Title:          metadata.Metadata.Title,
			Description:    metadata.Metadata.Description,
			Creator:        metadata.Metadata.Creator,
			Date:           metadata.Metadata.Date,
			ThumbnailURL:   thumbnailURL(id),
			LocalPath:      relPath,
			PublicURL:      publicURL,
			DownloadStatus: status,
			Actors:         actors,
			ImdbRating:     rating,
		}
	}
	persist := func(m *Media) {
		if _, err := store.UpdateMedia(ctx, m); err != nil {
			if _, err := store.CreateMedia(ctx, m); err != nil {
				a.log.Warn("failed to persist media download state", "media_id", internalID, "status", m.DownloadStatus, "error", err)
			}
		}
	}

	// Persist the initial state before downloading so /api/media/play can read it.
	persist(state("downloading"))

	a.log.Info("starting download", "url", downloadURL, "path", localPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download: status %d", resp.StatusCode)
	}

	out, err := os.Create(localPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	// ContentLength is -1 with chunked encoding (common on archive.org); then
	// no percentage is emitted and the final 100 below is the only signal.
	total := resp.ContentLength
	var downloaded int64
	buf := make([]byte, 32*1024)
	last := -1
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				return relPath, err
			}
			downloaded += int64(n)
			if total > 0 {
				pct := int(float64(downloaded) / float64(total) * 100)
				if pct > last {
					last = pct
					if progress != nil {
						select {
						case progress <- pct:
						default: // channel full: keep downloading
						}
					}
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return relPath, readErr
		}
	}

	// Always signal completion, so a chunked download is not reported as failed.
	if progress != nil {
		select {
		case progress <- 100:
		default:
		}
	}

	persist(state("downloaded"))
	a.log.Info("download completed", "external_id", id, "path", localPath)
	return relPath, nil
}
