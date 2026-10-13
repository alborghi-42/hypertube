package media

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SubtitleLanguages are the languages subtitles are fetched in: English
// always, plus the user's language (users.language) when the film is not
// originally in it.
var SubtitleLanguages = map[string]string{
	"en": "English",
	"fr": "Français",
	"it": "Italiano",
}

// omdbLanguageCodes maps OMDb "Language" names to SubtitleLanguages codes.
var omdbLanguageCodes = map[string]string{
	"english": "en",
	"french":  "fr",
	"italian": "it",
}

// Subtitle is one WebVTT track available for a media item.
type Subtitle struct {
	Lang  string `json:"lang"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

const (
	subtitleFetchTimeout = 90 * time.Second
	// A language OpenSubtitles had nothing for is not asked again before this
	// (the free API quota is small).
	subtitleMissTTL = 12 * time.Hour
)

// Subtitles fetches subtitles from OpenSubtitles in the background and
// caches them as DATA_DIR/subtitles/<mediaId>/<lang>.vtt. The files are the
// record: the list of a media item is the .vtt files in its directory.
type Subtitles struct {
	dir     string
	os      *openSubtitles // nil: lookups disabled, cached files still served
	omdbKey string
	log     *slog.Logger

	mu       sync.Mutex
	inflight map[string]bool      // mediaID -> fetch running
	misses   map[string]time.Time // mediaID/lang -> last failed lookup
}

// NewSubtitles stores files under dataDir/subtitles. With no API key
// nothing is fetched (a warning is logged once).
func NewSubtitles(dataDir string, cfg OpenSubtitlesConfig, omdbKey string, log *slog.Logger) *Subtitles {
	s := &Subtitles{
		dir:      filepath.Join(dataDir, "subtitles"),
		omdbKey:  omdbKey,
		log:      log,
		inflight: map[string]bool{},
		misses:   map[string]time.Time{},
	}
	if cfg.Enabled() {
		s.os = newOpenSubtitles(cfg)
	} else {
		log.Warn("OPENSUBTITLES_API_KEY not set: subtitles disabled")
	}
	return s
}

func validMediaID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil
}

// FilePath returns the cached .vtt path for mediaID/lang; ok is false for
// an invalid id or language (so nothing outside the directory is reachable).
func (s *Subtitles) FilePath(mediaID, lang string) (string, bool) {
	if s == nil || !validMediaID(mediaID) {
		return "", false
	}
	if _, ok := SubtitleLanguages[lang]; !ok {
		return "", false
	}
	return filepath.Join(s.dir, mediaID, lang+".vtt"), true
}

// SubtitleURL is the session-protected stream path of one track.
func SubtitleURL(mediaID, lang string) string {
	return publicURL(path.Join(StreamPrefix, mediaID, "subtitles", lang+".vtt"))
}

// List returns the cached tracks of mediaID, English first.
func (s *Subtitles) List(mediaID string) []Subtitle {
	out := []Subtitle{}
	for _, lang := range s.Languages(mediaID) {
		out = append(out, Subtitle{Lang: lang, Label: SubtitleLanguages[lang], URL: SubtitleURL(mediaID, lang)})
	}
	return out
}

// Languages returns the language codes cached for mediaID, English first.
func (s *Subtitles) Languages(mediaID string) []string {
	langs := []string{}
	if s == nil || !validMediaID(mediaID) {
		return langs
	}
	for lang := range SubtitleLanguages {
		if p, _ := s.FilePath(mediaID, lang); fileExists(p) {
			langs = append(langs, lang)
		}
	}
	sort.Slice(langs, func(i, j int) bool {
		if (langs[i] == "en") != (langs[j] == "en") {
			return langs[i] == "en"
		}
		return langs[i] < langs[j]
	})
	return langs
}

// Pending reports whether a background fetch is running for mediaID.
func (s *Subtitles) Pending(mediaID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inflight[mediaID]
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Ensure starts (at most one per media) a background fetch of the missing
// tracks for m and userLang. It never blocks playback.
func (s *Subtitles) Ensure(m Media, userLang string) {
	if s == nil || s.os == nil || !validMediaID(m.ID) {
		return
	}
	s.mu.Lock()
	if s.inflight[m.ID] || s.settledLocked(m.ID, userLang) {
		s.mu.Unlock()
		return
	}
	s.inflight[m.ID] = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.inflight, m.ID)
			s.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), subtitleFetchTimeout)
		defer cancel()
		s.fetch(ctx, m, userLang)
	}()
}

// settledLocked reports whether nothing could be fetched for mediaID: every
// language wanted at most (English plus userLang, before knowing the film's
// language) is cached or recently missed. Keeps polls from restarting fetches
// (and pending from staying true). Caller holds s.mu.
func (s *Subtitles) settledLocked(mediaID, userLang string) bool {
	now := time.Now()
	for _, lang := range wanted(userLang, "") {
		if p, _ := s.FilePath(mediaID, lang); fileExists(p) {
			continue
		}
		if t, ok := s.misses[mediaID+"/"+lang]; ok && now.Sub(t) < subtitleMissTTL {
			continue
		}
		return false
	}
	return true
}

// wanted returns the languages to fetch: English, plus userLang when it is
// supported, not English and not the film's original language.
func wanted(userLang string, original string) []string {
	langs := []string{"en"}
	if _, ok := SubtitleLanguages[userLang]; ok && userLang != "en" && userLang != original {
		langs = append(langs, userLang)
	}
	return langs
}

func (s *Subtitles) fetch(ctx context.Context, m Media, userLang string) {
	imdbID, original := m.ImdbID, ""
	info, err := omdbLookup(ctx, s.omdbKey, omdbQuery{ImdbID: m.ImdbID, Title: m.Title, Year: m.Year})
	if err == nil && info == nil && m.ImdbID == "" && m.Year != "" {
		// The provider year can be a re-release date; retry on the title.
		info, err = omdbLookup(ctx, s.omdbKey, omdbQuery{Title: m.Title})
	}
	if err == nil && info != nil {
		if imdbID == "" {
			imdbID = info.ImdbID
		}
		if len(info.Languages) > 0 {
			original = omdbLanguageCodes[strings.ToLower(info.Languages[0])]
		}
	}

	now := time.Now()
	var missing []string
	s.mu.Lock()
	if userLang != "en" && userLang == original {
		// Not needed (the film is in the user's language): settled, see settledLocked.
		s.misses[m.ID+"/"+userLang] = now
	}
	for _, lang := range wanted(userLang, original) {
		p, _ := s.FilePath(m.ID, lang)
		if fileExists(p) {
			continue
		}
		if t, ok := s.misses[m.ID+"/"+lang]; ok && now.Sub(t) < subtitleMissTTL {
			continue
		}
		missing = append(missing, lang)
	}
	s.mu.Unlock()
	if len(missing) == 0 {
		return
	}

	files, err := s.os.search(ctx, imdbID, m.Title, m.Year, missing)
	if err != nil && !errors.Is(err, errOpenSubtitlesNoMatch) {
		s.log.Warn("subtitle search failed", "media_id", m.ID, "error", err)
	}
	for _, lang := range missing {
		fileID, ok := files[lang]
		if !ok || s.save(ctx, m.ID, lang, fileID) != nil {
			s.mu.Lock()
			s.misses[m.ID+"/"+lang] = now
			s.mu.Unlock()
		}
	}
}

func (s *Subtitles) save(ctx context.Context, mediaID, lang string, fileID int) error {
	srt, err := s.os.download(ctx, fileID)
	if err == nil {
		var vtt []byte
		if vtt, err = SRTToVTT(srt); err == nil {
			err = writeFileAtomic(filepath.Join(s.dir, mediaID), lang+".vtt", vtt)
		}
	}
	if err != nil {
		s.log.Warn("subtitle download failed", "media_id", mediaID, "lang", lang, "file_id", fileID, "error", err)
		return err
	}
	s.log.Info("subtitle cached", "media_id", mediaID, "lang", lang)
	return nil
}

// writeFileAtomic writes dir/name via a temp file + rename, so a reader
// never sees a half-written track.
func writeFileAtomic(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, name+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
