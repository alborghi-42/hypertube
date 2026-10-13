package media

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

// ErrNotFound is returned by Store when the row does not exist.
var ErrNotFound = errors.New("not found")

// Store adapts the sqlc-generated queries to the domain types used by the
// handlers and providers.
type Store struct {
	q *repository.Queries
}

func NewStore(db repository.DBTX) *Store {
	return &Store{q: repository.New(db)}
}

func toUUID(s string) (pgtype.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: u, Valid: true}, nil
}

func fromUUID(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func ptr[T any](v T) *T { return &v }

func mediaFromRow(r repository.Medium) *Media {
	return &Media{
		ID:              fromUUID(r.MediaID),
		ExternalID:      r.ExternalID,
		Provider:        r.Provider,
		Title:           r.Title,
		Description:     PlainText(deref(r.Description)),
		Creator:         deref(r.Creator),
		Date:            deref(r.Date),
		Year:            mediaYear(deref(r.Date)),
		ThumbnailURL:    deref(r.ThumbnailUrl),
		DurationSeconds: deref(r.DurationSeconds),
		DownloadStatus:  deref(r.DownloadStatus),
		LocalPath:       deref(r.LocalPath),
		PublicURL:       deref(r.PublicUrl),
		FileSize:        deref(r.FileSize),
		MimeType:        deref(r.MimeType),
		ImdbRating:      deref(r.ImdbRating),
		Actors:          r.Actors,
		CreatedAt:       r.CreatedAt.Time,
		UpdatedAt:       r.UpdatedAt.Time,
	}
}

// commentFromRow maps a bare comment row; username is the author's, known by
// the caller (writes are always made by the session user).
func commentFromRow(r repository.MediaComment, username string) *Comment {
	return &Comment{
		ID:        fromUUID(r.CommentID),
		MediaID:   fromUUID(r.MediaID),
		UserID:    fromUUID(r.UserID),
		Username:  username,
		Content:   r.Content,
		CreatedAt: r.CreatedAt.Time,
		UpdatedAt: r.UpdatedAt.Time,
	}
}

func (s *Store) GetMedia(ctx context.Context, id string) (*Media, error) {
	uid, err := toUUID(id)
	if err != nil {
		return nil, err
	}
	row, err := s.q.GetMediaByID(ctx, uid)
	if err != nil {
		return nil, notFound(err)
	}
	return mediaFromRow(row), nil
}

func (s *Store) CreateMedia(ctx context.Context, m *Media) (*Media, error) {
	uid, err := toUUID(m.ID)
	if err != nil {
		return nil, err
	}
	// Every column is written explicitly (empty strings, not NULL), matching
	// the original service's INSERT.
	row, err := s.q.CreateMedia(ctx, repository.CreateMediaParams{
		MediaID:         uid,
		ExternalID:      m.ExternalID,
		Provider:        m.Provider,
		Title:           m.Title,
		Description:     ptr(m.Description),
		Creator:         ptr(m.Creator),
		Date:            ptr(m.Date),
		ThumbnailUrl:    ptr(m.ThumbnailURL),
		DurationSeconds: ptr(m.DurationSeconds),
		DownloadStatus:  ptr(m.DownloadStatus),
		LocalPath:       ptr(m.LocalPath),
		PublicUrl:       ptr(m.PublicURL),
		FileSize:        ptr(m.FileSize),
		MimeType:        ptr(m.MimeType),
		ImdbRating:      ptr(m.ImdbRating),
		Actors:          m.Actors,
	})
	if err != nil {
		return nil, err
	}
	return mediaFromRow(row), nil
}

func (s *Store) UpdateMedia(ctx context.Context, m *Media) (*Media, error) {
	uid, err := toUUID(m.ID)
	if err != nil {
		return nil, err
	}
	row, err := s.q.UpdateMedia(ctx, repository.UpdateMediaParams{
		MediaID:         uid,
		ExternalID:      m.ExternalID,
		Provider:        m.Provider,
		Title:           m.Title,
		Description:     ptr(m.Description),
		Creator:         ptr(m.Creator),
		Date:            ptr(m.Date),
		ThumbnailUrl:    ptr(m.ThumbnailURL),
		DurationSeconds: ptr(m.DurationSeconds),
		DownloadStatus:  ptr(m.DownloadStatus),
		LocalPath:       ptr(m.LocalPath),
		PublicUrl:       ptr(m.PublicURL),
		FileSize:        ptr(m.FileSize),
		MimeType:        ptr(m.MimeType),
		ImdbRating:      ptr(m.ImdbRating),
		Actors:          m.Actors,
	})
	if err != nil {
		return nil, notFound(err)
	}
	return mediaFromRow(row), nil
}

func (s *Store) ListComments(ctx context.Context, mediaID string) ([]Comment, error) {
	uid, err := toUUID(mediaID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListCommentsByMedia(ctx, uid)
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

func (s *Store) GetComment(ctx context.Context, commentID string) (*Comment, error) {
	uid, err := toUUID(commentID)
	if err != nil {
		return nil, ErrNotFound
	}
	r, err := s.q.GetComment(ctx, uid)
	if err != nil {
		return nil, notFound(err)
	}
	return commentFromRow(repository.MediaComment{
		CommentID: r.CommentID, MediaID: r.MediaID, UserID: r.UserID,
		Content: r.Content, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, r.Username), nil
}

// CreateComment inserts c; c.Username is echoed back as the author's name.
func (s *Store) CreateComment(ctx context.Context, c *Comment) (*Comment, error) {
	cid, err := toUUID(c.ID)
	if err != nil {
		return nil, err
	}
	mid, err := toUUID(c.MediaID)
	if err != nil {
		return nil, err
	}
	uid, err := toUUID(c.UserID)
	if err != nil {
		return nil, err
	}
	row, err := s.q.CreateComment(ctx, repository.CreateCommentParams{
		CommentID: cid, MediaID: mid, UserID: uid, Content: c.Content,
	})
	if err != nil {
		return nil, err
	}
	return commentFromRow(row, c.Username), nil
}

// UpdateComment changes the content of a comment owned by author; it returns
// ErrNotFound when no such comment exists for that author.
func (s *Store) UpdateComment(ctx context.Context, commentID, mediaID string, author pgtype.UUID, authorName, content string) (*Comment, error) {
	cid, err := toUUID(commentID)
	if err != nil {
		return nil, err
	}
	mid, err := toUUID(mediaID)
	if err != nil {
		return nil, err
	}
	row, err := s.q.UpdateComment(ctx, repository.UpdateCommentParams{
		CommentID: cid, MediaID: mid, UserID: author, Content: content,
	})
	if err != nil {
		return nil, notFound(err)
	}
	return commentFromRow(row, authorName), nil
}

// DeleteComment removes a comment owned by author; ErrNotFound otherwise.
func (s *Store) DeleteComment(ctx context.Context, commentID, mediaID string, author pgtype.UUID) error {
	cid, err := toUUID(commentID)
	if err != nil {
		return err
	}
	mid, err := toUUID(mediaID)
	if err != nil {
		return err
	}
	n, err := s.q.DeleteComment(ctx, repository.DeleteCommentParams{CommentID: cid, MediaID: mid, UserID: author})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkWatched records (or refreshes) that the user played the media.
func (s *Store) MarkWatched(ctx context.Context, userID pgtype.UUID, mediaID string) error {
	mid, err := toUUID(mediaID)
	if err != nil {
		return err
	}
	return s.q.MarkWatched(ctx, repository.MarkWatchedParams{UserID: userID, MediaID: mid})
}

// WatchedSet returns which of mediaIDs the user has watched.
func (s *Store) WatchedSet(ctx context.Context, userID pgtype.UUID, mediaIDs []string) (map[string]bool, error) {
	ids := make([]pgtype.UUID, 0, len(mediaIDs))
	for _, id := range mediaIDs {
		if u, err := toUUID(id); err == nil {
			ids = append(ids, u)
		}
	}
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}
	rows, err := s.q.ListWatchedMediaIDs(ctx, repository.ListWatchedMediaIDsParams{UserID: userID, MediaIds: ids})
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(rows))
	for _, r := range rows {
		set[fromUUID(r)] = true
	}
	return set, nil
}

// SetDuration stores the runtime if the row has none yet.
func (s *Store) SetDuration(ctx context.Context, id string, seconds float64) error {
	uid, err := toUUID(id)
	if err != nil {
		return err
	}
	return s.q.SetMediaDuration(ctx, repository.SetMediaDurationParams{MediaID: uid, DurationSeconds: &seconds})
}
