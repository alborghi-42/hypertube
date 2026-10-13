package media

import "context"

// Provider is a source of legal media (archive.org today; legittorrents.info
// and others can be added by implementing this interface).
type Provider interface {
	ID() string
	FetchPopular(ctx context.Context, page, rows int) ([]Media, error)
	FetchByID(ctx context.Context, id string) (*Media, error)
	Search(ctx context.Context, query string, page, rows int) ([]Media, error)
	// TorrentSource returns the .torrent or magnet the item is downloaded
	// from; the Engine does the downloading, persistence and streaming.
	TorrentSource(ctx context.Context, id string) (*TorrentSource, error)
}

// Registry maps provider IDs to providers.
type Registry map[string]Provider

func NewRegistry(providers ...Provider) Registry {
	r := Registry{}
	for _, p := range providers {
		r[p.ID()] = p
	}
	return r
}
