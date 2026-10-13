package media

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

// Helpers for the public API's extra routes (42-27): search, add/delete
// movies and watch history.

// SearchAll runs query on every provider in parallel, merged and sorted by
// name like GET /api/media/search; failing providers are skipped.
func SearchAll(ctx context.Context, providers Registry, query string, page, rows int, log *slog.Logger) ([]Media, error) {
	ids := providers.IDs()
	lists := make([][]Media, len(ids))
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, providerTimeout)
			defer cancel()
			lists[i], errs[i] = providers[id].Search(pctx, query, page, rows)
			if errs[i] != nil {
				log.Error("search failed", "provider", id, "error", errs[i])
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err == nil {
			return mergeResults(lists, true), nil
		}
	}
	return nil, ErrAllProvidersFailed
}

var (
	// ErrMediaInUse: the media is downloading, streaming or converting.
	ErrMediaInUse = errors.New("media in use")
	// ErrMediaShared: other users have commented on the media.
	ErrMediaShared = errors.New("media commented by other users")
)

// DeleteMedia removes a media row (its comments and watch history cascade)
// and its downloaded files, on behalf of userID. Media are shared, so it is
// refused with ErrMediaInUse while a download job, stream or conversion is
// active, and with ErrMediaShared when another user has commented on it.
func (e *Engine) DeleteMedia(ctx context.Context, mediaID string, userID pgtype.UUID) error {
	m, err := e.store.GetMedia(ctx, mediaID)
	if err != nil {
		return notFound(err)
	}
	if e.Job(m.ID) != nil || e.inUse.busy(m.ID) {
		return ErrMediaInUse
	}
	uid, err := toUUID(m.ID)
	if err != nil {
		return ErrNotFound
	}
	n, err := e.store.q.DeleteMediaUnlessOthersCommented(ctx, repository.DeleteMediaUnlessOthersCommentedParams{MediaID: uid, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		if _, err := e.store.GetMedia(ctx, m.ID); errors.Is(err, ErrNotFound) {
			return ErrNotFound // deleted concurrently
		}
		return ErrMediaShared
	}
	// A play that raced the delete keeps its files (its row writes now fail).
	if e.Job(m.ID) != nil || e.inUse.busy(m.ID) {
		e.log.Warn("media deleted while a play started; files kept", "media_id", m.ID)
		return nil
	}
	if dir, _, err := e.removeFiles(m); err != nil {
		e.log.Warn("delete media: remove files failed", "media_id", m.ID, "dir", dir, "error", err)
	}
	e.log.Info("media deleted via API", "media_id", m.ID, "title", m.Title, "status", m.DownloadStatus)
	return nil
}

// WatchEntry is one item of a user's watch history.
type WatchEntry struct {
	MediaID   string
	Title     string
	WatchedAt time.Time
}

// WatchHistory returns the user's watch history, most recent first.
func (s *Store) WatchHistory(ctx context.Context, userID pgtype.UUID, limit, offset int32) ([]WatchEntry, error) {
	rows, err := s.q.ListWatchHistory(ctx, repository.ListWatchHistoryParams{UserID: userID, RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, err
	}
	out := make([]WatchEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, WatchEntry{MediaID: fromUUID(r.MediaID), Title: r.Title, WatchedAt: r.WatchedAt.Time})
	}
	return out, nil
}

// UnmarkWatched removes mediaID from the user's history; ErrNotFound if it
// was not there.
func (s *Store) UnmarkWatched(ctx context.Context, userID pgtype.UUID, mediaID string) error {
	mid, err := toUUID(mediaID)
	if err != nil {
		return ErrNotFound
	}
	n, err := s.q.UnmarkWatched(ctx, repository.UnmarkWatchedParams{UserID: userID, MediaID: mid})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
