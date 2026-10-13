package media

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

// PublicDomainTorrents implements Provider for publicdomaintorrents.info, a
// tracker of public-domain films (legittorrents.info, the subject's other
// example, shut down in 2023). The site has no API or search endpoint: the
// catalogue is its "SHOW ALL" HTML page, cached in memory, and each film's
// page carries its synopsis, screenshot, IMDb link and .torrent links.
type PublicDomainTorrents struct {
	omdbKey string
	log     *slog.Logger
	client  *http.Client

	mu        sync.Mutex
	catalog   []pdtEntry
	fetchedAt time.Time
	details   map[string]*pdtDetails
}

const (
	pdtBase       = "https://www.publicdomaintorrents.info"
	pdtCatalogTTL = 6 * time.Hour
	pdtMaxPage    = 4 << 20
	// pdtEnrichWorkers bounds concurrent film-page fetches per result page.
	pdtEnrichWorkers = 6
)

type pdtEntry struct {
	ID    string // the site's movieid
	Title string
}

type pdtDetails struct {
	Title       string
	Description string
	Thumbnail   string
	ImdbID      string
	Torrents    []string // .torrent file names, as linked on the page
}

var (
	pdtListRe    = regexp.MustCompile(`nshowmovie\.html\?movieid=(\d+)>([^<]+)</a>`)
	pdtTitleRe   = regexp.MustCompile(`(?s)<h3>(.*?)</h3>`)
	pdtDescRe    = regexp.MustCompile(`(?s)</tr>\s*<tr>\s*<td></td>\s*<td>\s*([^<]{20,}?)\s*</td>`)
	pdtGrabRe    = regexp.MustCompile(`src=(grabs/[^\s>]+\.(?i:jpe?g|png|gif))`)
	pdtImdbRe    = regexp.MustCompile(`imdb\.com/title/(tt\d+)`)
	pdtTorrentRe = regexp.MustCompile(`btdownload\.php\?type=torrent&(?:amp;)?file=([^\s>"]+\.torrent)`)
)

func NewPublicDomainTorrents(omdbKey string, log *slog.Logger) *PublicDomainTorrents {
	return &PublicDomainTorrents{
		omdbKey: omdbKey,
		log:     log,
		client:  &http.Client{Timeout: 20 * time.Second},
		details: map[string]*pdtDetails{},
	}
}

func (p *PublicDomainTorrents) ID() string { return "publicdomaintorrents" }

func (p *PublicDomainTorrents) get(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("publicdomaintorrents returned status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// loadCatalog returns the full film list sorted by title, refreshing the
// cache after pdtCatalogTTL. A failed refresh serves the stale copy.
func (p *PublicDomainTorrents) loadCatalog(ctx context.Context) ([]pdtEntry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.catalog != nil && time.Since(p.fetchedAt) < pdtCatalogTTL {
		return p.catalog, nil
	}
	body, err := p.get(ctx, pdtBase+"/nshowcat.html?category=ALL", pdtMaxPage)
	if err == nil {
		seen := map[string]bool{}
		var list []pdtEntry
		for _, m := range pdtListRe.FindAllSubmatch(body, -1) {
			id := string(m[1])
			title := strings.TrimSpace(html.UnescapeString(string(m[2])))
			if seen[id] || title == "" {
				continue
			}
			seen[id] = true
			list = append(list, pdtEntry{ID: id, Title: title})
		}
		if len(list) == 0 {
			err = fmt.Errorf("publicdomaintorrents catalogue page had no films")
		} else {
			sort.SliceStable(list, func(i, j int) bool { return titleKey(list[i].Title) < titleKey(list[j].Title) })
			p.catalog, p.fetchedAt = list, time.Now()
		}
	}
	if err != nil {
		if p.catalog != nil {
			p.log.Warn("publicdomaintorrents catalogue refresh failed, serving stale copy", "error", err)
			return p.catalog, nil
		}
		return nil, err
	}
	return p.catalog, nil
}

func (p *PublicDomainTorrents) fetchDetails(ctx context.Context, id string) (*pdtDetails, error) {
	p.mu.Lock()
	d, ok := p.details[id]
	p.mu.Unlock()
	if ok {
		return d, nil
	}
	body, err := p.get(ctx, pdtBase+"/nshowmovie.html?movieid="+url.QueryEscape(id), pdtMaxPage)
	if err != nil {
		return nil, err
	}
	page := string(body)
	d = &pdtDetails{}
	if m := pdtTitleRe.FindStringSubmatch(page); m != nil {
		d.Title = strings.TrimSpace(html.UnescapeString(m[1]))
	}
	if d.Title == "" {
		// Unknown ids render the template with empty fields.
		return nil, nil
	}
	if m := pdtDescRe.FindStringSubmatch(page); m != nil {
		d.Description = strings.TrimSpace(html.UnescapeString(m[1]))
	}
	if m := pdtGrabRe.FindStringSubmatch(page); m != nil {
		d.Thumbnail = pdtBase + "/" + m[1]
	}
	if m := pdtImdbRe.FindStringSubmatch(page); m != nil {
		d.ImdbID = m[1]
	}
	for _, m := range pdtTorrentRe.FindAllStringSubmatch(page, -1) {
		d.Torrents = append(d.Torrents, m[1])
	}
	p.mu.Lock()
	p.details[id] = d
	p.mu.Unlock()
	return d, nil
}

func (p *PublicDomainTorrents) toMedia(e pdtEntry, d *pdtDetails) Media {
	now := time.Now()
	m := Media{
		ID:         InternalID(e.ID, p.ID()),
		ExternalID: e.ID,
		Provider:   p.ID(),
		Title:      e.Title,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if d != nil {
		m.Description = d.Description
		m.ThumbnailURL = d.Thumbnail
	}
	return m
}

// page slices entries for 1-based page/rows and enriches them with their
// film pages (thumbnail, synopsis) concurrently; a failed film page only
// leaves that item without a thumbnail.
func (p *PublicDomainTorrents) page(ctx context.Context, entries []pdtEntry, page, rows int) []Media {
	if page < 1 {
		page = 1
	}
	if rows < 1 {
		rows = 50
	}
	start := (page - 1) * rows
	if start >= len(entries) {
		return []Media{}
	}
	entries = entries[start:min(start+rows, len(entries))]

	list := make([]Media, len(entries))
	sem := make(chan struct{}, pdtEnrichWorkers)
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			d, err := p.fetchDetails(ctx, e.ID)
			if err != nil {
				p.log.Debug("publicdomaintorrents film page unavailable", "movieid", e.ID, "error", err)
			}
			list[i] = p.toMedia(e, d)
			if p.omdbKey != "" {
				if rating, _, err := omdbStats(ctx, p.omdbKey, e.Title); err == nil {
					list[i].ImdbRating = rating
				}
			}
		}()
	}
	wg.Wait()
	return list
}

// FetchPopular lists the catalogue by name: the site exposes no reliable
// popularity ranking (its "top seeded" page times out).
func (p *PublicDomainTorrents) FetchPopular(ctx context.Context, page, rows int) ([]Media, error) {
	entries, err := p.loadCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return p.page(ctx, entries, page, rows), nil
}

// Search matches every query word against the cached catalogue titles.
func (p *PublicDomainTorrents) Search(ctx context.Context, query string, page, rows int) ([]Media, error) {
	entries, err := p.loadCatalog(ctx)
	if err != nil {
		return nil, err
	}
	words := strings.Fields(strings.ToLower(query))
	var matched []pdtEntry
	for _, e := range entries {
		title := strings.ToLower(e.Title)
		ok := true
		for _, w := range words {
			if !strings.Contains(title, w) {
				ok = false
				break
			}
		}
		if ok {
			matched = append(matched, e)
		}
	}
	return p.page(ctx, matched, page, rows), nil
}

func (p *PublicDomainTorrents) FetchByID(ctx context.Context, id string) (*Media, error) {
	d, err := p.fetchDetails(ctx, id)
	if err != nil || d == nil {
		return nil, err
	}
	m := p.toMedia(pdtEntry{ID: id, Title: d.Title}, d)
	rating, actors, err := omdbStats(ctx, p.omdbKey, d.Title)
	if err != nil {
		p.log.Warn("OMDB stats unavailable", "title", d.Title, "error", err)
	}
	m.ImdbRating, m.Actors = rating, actors
	return &m, nil
}

// TorrentSource downloads the film's .torrent, preferring the MP4 encode
// (plays natively in browsers) over the DivX AVI.
func (p *PublicDomainTorrents) TorrentSource(ctx context.Context, id string) (*TorrentSource, error) {
	d, err := p.fetchDetails(ctx, id)
	if err != nil {
		return nil, err
	}
	if d == nil || len(d.Torrents) == 0 {
		return nil, fmt.Errorf("publicdomaintorrents film %s has no torrent", id)
	}
	file := d.Torrents[0]
	for _, t := range d.Torrents {
		if strings.HasSuffix(strings.ToLower(t), ".mp4.torrent") {
			file = t
			break
		}
	}
	body, err := p.get(ctx, pdtBase+"/bt/btdownload.php?type=torrent&file="+url.QueryEscape(file), maxTorrentSize)
	if err != nil {
		return nil, err
	}
	mi, err := metainfo.Load(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse publicdomaintorrents torrent: %w", err)
	}
	return &TorrentSource{MetaInfo: mi}, nil
}
