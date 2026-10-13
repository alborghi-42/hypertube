package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OpenSubtitlesConfig configures the opensubtitles.com REST API client
// (https://opensubtitles.stoplight.io/docs/opensubtitles-api). APIKey and
// UserAgent are required; Username/Password log in for the download
// endpoint, which needs a user token on top of the API key.
type OpenSubtitlesConfig struct {
	APIKey    string
	UserAgent string
	Username  string
	Password  string
	// BaseURL defaults to https://api.opensubtitles.com/api/v1.
	BaseURL string
}

// Enabled reports whether subtitle lookups can run at all.
func (c OpenSubtitlesConfig) Enabled() bool { return c.APIKey != "" }

const (
	openSubtitlesDefaultBase = "https://api.opensubtitles.com/api/v1"
	// Subtitle files are small; anything larger is not an SRT.
	maxSubtitleBytes = 5 << 20
	// Tokens are valid 24h; renew a bit earlier.
	openSubtitlesTokenTTL = 23 * time.Hour
)

var errOpenSubtitlesNoMatch = errors.New("no subtitle found")

type openSubtitles struct {
	cfg    OpenSubtitlesConfig
	client *http.Client

	mu       sync.Mutex
	token    string
	tokenAt  time.Time
	loginURL string // base URL returned by /login (vip users get another host)
}

func newOpenSubtitles(cfg OpenSubtitlesConfig) *openSubtitles {
	if cfg.BaseURL == "" {
		cfg.BaseURL = openSubtitlesDefaultBase
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.UserAgent == "" {
		cfg.UserAgent = "Hypertube v1.0"
	}
	return &openSubtitles{cfg: cfg, client: &http.Client{Timeout: 20 * time.Second}}
}

func (o *openSubtitles) do(ctx context.Context, method, rawURL string, body any, auth bool, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Api-Key", o.cfg.APIKey)
	req.Header.Set("User-Agent", o.cfg.UserAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		if tok, _ := o.session(ctx); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized && auth {
		o.mu.Lock()
		o.token = "" // force a new login next time
		o.mu.Unlock()
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("opensubtitles %s %s: status %d: %.200s", method, req.URL.Path, resp.StatusCode, data)
	}
	return json.Unmarshal(data, out)
}

// session logs in once (when credentials are set) and returns the user
// token plus the API base to use with it.
func (o *openSubtitles) session(ctx context.Context) (string, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cfg.Username == "" || o.cfg.Password == "" {
		return "", o.cfg.BaseURL
	}
	if o.token != "" && time.Since(o.tokenAt) < openSubtitlesTokenTTL {
		return o.token, o.loginURL
	}
	var resp struct {
		Token   string `json:"token"`
		BaseURL string `json:"base_url"`
	}
	// No auth header on /login itself (auth=false avoids recursion).
	o.mu.Unlock()
	err := o.do(ctx, http.MethodPost, o.cfg.BaseURL+"/login",
		map[string]string{"username": o.cfg.Username, "password": o.cfg.Password}, false, &resp)
	o.mu.Lock()
	if err != nil || resp.Token == "" {
		return "", o.cfg.BaseURL
	}
	o.token, o.tokenAt = resp.Token, time.Now()
	o.loginURL = o.cfg.BaseURL
	if resp.BaseURL != "" {
		o.loginURL = "https://" + strings.TrimSuffix(strings.TrimPrefix(resp.BaseURL, "https://"), "/") + "/api/v1"
	}
	return o.token, o.loginURL
}

type osSearchResponse struct {
	Data []struct {
		Attributes struct {
			Language          string  `json:"language"`
			DownloadCount     int     `json:"download_count"`
			HearingImpaired   bool    `json:"hearing_impaired"`
			MachineTranslated bool    `json:"machine_translated"`
			AITranslated      bool    `json:"ai_translated"`
			FromTrusted       bool    `json:"from_trusted"`
			Ratings           float64 `json:"ratings"`
			Files             []struct {
				FileID int `json:"file_id"`
			} `json:"files"`
		} `json:"attributes"`
	} `json:"data"`
}

// imdbNumber strips "tt" and leading zeros: the API redirects any
// non-canonical query (zero-padded ids, unsorted or upper-case params).
func imdbNumber(id string) string {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(id), "tt"))
	if err != nil || n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// search returns the best file id per requested language (languages are
// ISO 639-1 codes). Matches on the IMDb id when known, else title + year.
func (o *openSubtitles) search(ctx context.Context, imdbID, title, year string, langs []string) (map[string]int, error) {
	v := url.Values{}
	sorted := append([]string(nil), langs...)
	sort.Strings(sorted)
	v.Set("languages", strings.Join(sorted, ","))
	if n := imdbNumber(imdbID); n != "" {
		v.Set("imdb_id", n)
	} else {
		if strings.TrimSpace(title) == "" {
			return nil, errOpenSubtitlesNoMatch
		}
		v.Set("query", strings.ToLower(strings.TrimSpace(title)))
		v.Set("type", "movie")
		if year != "" {
			v.Set("year", year)
		}
	}
	var resp osSearchResponse
	// Encode sorts the keys, as the API expects.
	if err := o.do(ctx, http.MethodGet, o.cfg.BaseURL+"/subtitles?"+v.Encode(), nil, false, &resp); err != nil {
		return nil, err
	}
	type pick struct {
		file  int
		score float64
	}
	best := map[string]pick{}
	for _, d := range resp.Data {
		a := d.Attributes
		lang := strings.ToLower(a.Language)
		if len(a.Files) == 0 || a.Files[0].FileID == 0 {
			continue
		}
		// Human translations first, then trusted uploaders, then popularity.
		score := float64(a.DownloadCount)
		if a.MachineTranslated || a.AITranslated {
			score -= 1e9
		}
		if a.FromTrusted {
			score += 1e6
		}
		if a.HearingImpaired {
			score -= 1e3
		}
		if cur, ok := best[lang]; !ok || score > cur.score {
			best[lang] = pick{file: a.Files[0].FileID, score: score}
		}
	}
	out := map[string]int{}
	for _, l := range langs {
		if p, ok := best[l]; ok {
			out[l] = p.file
		}
	}
	if len(out) == 0 {
		return nil, errOpenSubtitlesNoMatch
	}
	return out, nil
}

// download fetches a subtitle file (SRT) by file id.
func (o *openSubtitles) download(ctx context.Context, fileID int) ([]byte, error) {
	_, base := o.session(ctx)
	var link struct {
		Link string `json:"link"`
	}
	body := map[string]any{"file_id": fileID, "sub_format": "srt"}
	if err := o.do(ctx, http.MethodPost, base+"/download", body, true, &link); err != nil {
		return nil, err
	}
	if link.Link == "" {
		return nil, errors.New("opensubtitles download: empty link")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.Link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", o.cfg.UserAgent)
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subtitle file download: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSubtitleBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSubtitleBytes {
		return nil, errors.New("subtitle file too large")
	}
	return data, nil
}
