package media

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Provider is a source of legal media (archive.org, publicdomaintorrents;
// more can be added by implementing this interface and registering it).
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

// IDs returns the provider IDs with defaultProvider first, then by name, so
// merged results and dedupe preference are deterministic.
func (r Registry) IDs() []string {
	ids := make([]string, 0, len(r))
	for id := range r {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if (ids[i] == defaultProvider) != (ids[j] == defaultProvider) {
			return ids[i] == defaultProvider
		}
		return ids[i] < ids[j]
	})
	return ids
}

var nonAlnum = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// titleKey is the case-, punctuation- and spacing-insensitive form of a
// title, used for sorting and dedupe. Leading articles are kept, matching
// archive.org's titleSorter.
func titleKey(title string) string {
	return strings.TrimSpace(nonAlnum.ReplaceAllString(strings.ToLower(title), " "))
}

// mediaYear is the 4-digit year at the start of a provider date, if any.
func mediaYear(date string) string {
	if len(date) >= 4 {
		if _, err := strconv.Atoi(date[:4]); err == nil {
			return date[:4]
		}
	}
	return ""
}

// mergeResults concatenates per-provider results (in provider order),
// dropping later duplicates by normalized title+year. A result with no year
// also collides with a same-title result that has one. With byName the
// output is sorted by title (search); otherwise provider order is kept
// (popular: each provider's own ranking).
func mergeResults(lists [][]Media, byName bool) []Media {
	out := []Media{}
	seen := map[string]bool{}
	for _, list := range lists {
		for _, m := range list {
			key := titleKey(m.Title)
			if key == "" {
				key = m.Provider + "\x00" + m.ExternalID
			}
			year := mediaYear(m.Date)
			if seen[key+"\x00"+year] || (year == "" && seen[key+"\x00*"]) || (year != "" && seen[key+"\x00"]) {
				continue
			}
			seen[key+"\x00"+year] = true
			seen[key+"\x00*"] = true
			out = append(out, m)
		}
	}
	if byName {
		sort.SliceStable(out, func(i, j int) bool {
			ki, kj := titleKey(out[i].Title), titleKey(out[j].Title)
			if ki != kj {
				return ki < kj
			}
			return out[i].Date < out[j].Date
		})
	}
	return out
}
