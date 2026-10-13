package media

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

// Exported helpers for the public REST API (internal/publicapi), which has
// no gin session context and speaks its own payload shapes.

// ErrAllProvidersFailed is returned by FrontPage when no provider answered.
var ErrAllProvidersFailed = errors.New("all providers failed")

// FrontPage returns the popular list shown on the library front page: every
// provider's popular items, merged and deduplicated like GET /api/media.
func FrontPage(ctx context.Context, providers Registry, page, rows int, log *slog.Logger) ([]Media, error) {
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
			lists[i], errs[i] = providers[id].FetchPopular(pctx, page, rows)
			if errs[i] != nil {
				log.Error("fetch popular failed", "provider", id, "error", errs[i])
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err == nil {
			return mergeResults(lists, false), nil
		}
	}
	return nil, ErrAllProvidersFailed
}

// EnsureMedia caches m (as listed by a provider) unless its row already
// exists, so ids handed out by the API resolve with GetMedia.
func (s *Store) EnsureMedia(ctx context.Context, m *Media) error {
	if _, err := s.GetMedia(ctx, m.ID); err == nil || !errors.Is(err, ErrNotFound) {
		return err
	}
	_, err := s.CreateMedia(ctx, m)
	return err
}

// CountComments is the number of comments on a media item.
func (s *Store) CountComments(ctx context.Context, mediaID string) (int64, error) {
	uid, err := toUUID(mediaID)
	if err != nil {
		return 0, ErrNotFound
	}
	return s.q.CountCommentsByMedia(ctx, uid)
}

// LatestComments returns the most recent comments across all media.
func (s *Store) LatestComments(ctx context.Context, limit int32) ([]Comment, error) {
	rows, err := s.q.ListLatestComments(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Comment, 0, len(rows))
	for _, r := range rows {
		out = append(out, *commentFromRow(repository.MediaComment{
			CommentID: r.CommentID, MediaID: r.MediaID, UserID: r.UserID,
			Content: r.Content, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}, r.Username))
	}
	return out, nil
}

// NormalizeComment trims content and checks it like bindComment: it returns
// the cleaned text, or a non-empty message describing why it is invalid.
func NormalizeComment(content string) (string, string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", "Comment cannot be empty"
	}
	if utf8.RuneCountInString(content) > MaxCommentLength {
		return "", "Comment is too long"
	}
	return content, ""
}
