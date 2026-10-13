package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// omdbClient bounds every OMDB call.
var omdbClient = &http.Client{Timeout: 10 * time.Second}

const (
	// omdbWorkers bounds concurrent OMDB lookups per result page.
	omdbWorkers = 8
	// omdbCacheTTL keeps hits (and misses, so unknown titles are not
	// re-queried on every scroll) for a while; ratings change slowly.
	omdbCacheTTL = 24 * time.Hour
	// omdbCacheMax caps the cache; when full it is simply reset.
	omdbCacheMax = 10000
)

type omdbResponse struct {
	Actors     string `json:"Actors"`
	Director   string `json:"Director"`
	Writer     string `json:"Writer"`
	Production string `json:"Production"`
	Runtime    string `json:"Runtime"`
	Plot       string `json:"Plot"`
	Poster     string `json:"Poster"`
	Genre      string `json:"Genre"`
	Year       string `json:"Year"`
	ImdbID     string `json:"imdbID"`
	ImdbRating string `json:"imdbRating"`
	Response   string `json:"Response"`
}

// omdbInfo is the subset of an OMDB movie used for enrichment.
type omdbInfo struct {
	ImdbID string
	Rating float64 // 0 when OMDB has no rating ("N/A")
	Actors []string
	Genres []string
	Year   string

	Directors  []string
	Writers    []string
	Production string  // production company; "" when OMDB has none
	Runtime    float64 // seconds; 0 when unknown
	Plot       string
	Poster     string
}

type omdbEntry struct {
	info    *omdbInfo // nil: OMDB has no match
	fetched time.Time
}

// omdbCache is shared by every provider: the same film is often listed by
// several of them and requested again on each scroll or search keystroke.
var omdbCache = struct {
	sync.Mutex
	m map[string]omdbEntry
}{m: map[string]omdbEntry{}}

// omdbQuery is what a lookup matches on. An IMDb id is exact; otherwise the
// title is matched together with the year when the provider knows it, so a
// homonymous remake is not picked up.
type omdbQuery struct {
	ImdbID string
	Title  string
	Year   string
}

func (q omdbQuery) key() string {
	if q.ImdbID != "" {
		return "i:" + q.ImdbID
	}
	return "t:" + titleKey(q.Title) + "\x00" + q.Year
}

func (q omdbQuery) empty() bool { return q.ImdbID == "" && titleKey(q.Title) == "" }

// omdbLookup returns the OMDB movie for q, cached. With no API key it
// returns an error, which callers treat as non-fatal; (nil, nil) means OMDB
// has no such movie.
func omdbLookup(ctx context.Context, apiKey string, q omdbQuery) (*omdbInfo, error) {
	if apiKey == "" {
		return nil, errors.New("OMDB_API_KEY is not set")
	}
	if q.empty() {
		return nil, nil
	}
	key := q.key()
	omdbCache.Lock()
	e, ok := omdbCache.m[key]
	omdbCache.Unlock()
	if ok && time.Since(e.fetched) < omdbCacheTTL {
		return e.info, nil
	}

	info, err := omdbFetch(ctx, apiKey, q)
	if err != nil {
		return nil, err
	}
	omdbCache.Lock()
	if len(omdbCache.m) >= omdbCacheMax {
		omdbCache.m = map[string]omdbEntry{}
	}
	omdbCache.m[key] = omdbEntry{info: info, fetched: time.Now()}
	omdbCache.Unlock()
	return info, nil
}

func omdbFetch(ctx context.Context, apiKey string, q omdbQuery) (*omdbInfo, error) {
	v := url.Values{}
	v.Set("apikey", apiKey)
	v.Set("type", "movie")
	v.Set("plot", "short")
	if q.ImdbID != "" {
		v.Set("i", q.ImdbID)
	} else {
		v.Set("t", q.Title)
		if q.Year != "" {
			v.Set("y", q.Year)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.omdbapi.com/?"+v.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build OMDB request: %w", err)
	}
	resp, err := omdbClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OMDB data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OMDB API returned status code %d", resp.StatusCode)
	}

	var body omdbResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("failed to decode OMDB response: %w", err)
	}
	if body.Response != "True" {
		return nil, nil
	}

	info := &omdbInfo{
		ImdbID: body.ImdbID,
		Actors: splitOMDBList(body.Actors),
		Genres: splitOMDBList(body.Genre),
		Year:   mediaYear(body.Year),

		Directors:  splitOMDBList(body.Director),
		Writers:    splitOMDBList(body.Writer),
		Production: omdbString(body.Production),
		Runtime:    parseRuntime(body.Runtime),
		Plot:       omdbString(body.Plot),
		Poster:     omdbString(body.Poster),
	}
	if r, err := strconv.ParseFloat(body.ImdbRating, 64); err == nil {
		info.Rating = r
	}
	return info, nil
}

// omdbString maps OMDB's "N/A" placeholder to "".
func omdbString(s string) string {
	s = strings.TrimSpace(s)
	if s == "N/A" {
		return ""
	}
	return s
}

// parseRuntime turns OMDB's "Runtime" ("94 min") into seconds; 0 if unknown.
func parseRuntime(s string) float64 {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	n, err := strconv.Atoi(f[0])
	if err != nil || n <= 0 {
		return 0
	}
	return float64(n * 60)
}

// splitOMDBList splits OMDB's "A, B, C" fields; "N/A" is empty.
func splitOMDBList(s string) []string {
	if s == "" || s == "N/A" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// applyOMDB copies the lookup result onto m. The provider's own year wins:
// OMDB only fills it in when the provider has none.
func applyOMDB(m *Media, info *omdbInfo) {
	if info == nil {
		return
	}
	m.ImdbID = info.ImdbID
	m.ImdbRating = info.Rating
	m.Actors = info.Actors
	m.Genres = info.Genres
	if m.Year == "" {
		m.Year = info.Year
	}
	m.Directors = info.Directors
	m.Writers = info.Writers
	m.Production = info.Production
	if m.DurationSeconds <= 0 {
		m.DurationSeconds = info.Runtime
	}
	if m.Description == "" {
		m.Description = info.Plot
	}
	m.PosterURL = info.Poster
}

// enrichOMDB looks every item up concurrently (bounded by omdbWorkers),
// queryOf giving each item's lookup. Failures only leave that item bare.
func enrichOMDB(ctx context.Context, apiKey string, list []Media, queryOf func(i int) omdbQuery) {
	if apiKey == "" {
		return
	}
	sem := make(chan struct{}, omdbWorkers)
	var wg sync.WaitGroup
	for i := range list {
		q := queryOf(i)
		if q.empty() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info, err := omdbLookup(ctx, apiKey, q)
			if err != nil {
				return
			}
			applyOMDB(&list[i], info)
		}()
	}
	wg.Wait()
}
