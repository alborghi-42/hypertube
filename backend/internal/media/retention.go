package media

import (
	"context"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

// Retention (42-18): downloads nobody watched for MEDIA_RETENTION are
// deleted from disk and their row goes back to the never-downloaded state.

const (
	retentionInterval   = time.Hour
	retentionFirstDelay = 2 * time.Minute
	touchThrottle       = time.Minute
)

// watchTracker counts in-flight uses of a media (streams, background mp4
// conversions) and throttles last_watched_at writes.
type watchTracker struct {
	mu      sync.Mutex
	active  map[string]int
	touched map[string]time.Time
}

func newWatchTracker() *watchTracker {
	return &watchTracker{active: map[string]int{}, touched: map[string]time.Time{}}
}

// begin marks id in use; call the returned func when done.
func (w *watchTracker) begin(id string) func() {
	w.mu.Lock()
	w.active[id]++
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		if w.active[id]--; w.active[id] <= 0 {
			delete(w.active, id)
		}
		w.mu.Unlock()
	}
}

func (w *watchTracker) busy(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.active[id] > 0
}

// shouldTouch reports whether a last_watched_at write is due for id, and
// records it as done at now if so.
func (w *watchTracker) shouldTouch(id string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if last, ok := w.touched[id]; ok && now.Sub(last) < touchThrottle {
		return false
	}
	w.touched[id] = now
	return true
}

// prune forgets throttle entries older than the throttle window.
func (w *watchTracker) prune(now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, t := range w.touched {
		if now.Sub(t) >= touchThrottle {
			delete(w.touched, id)
		}
	}
}

// TouchWatched bumps last_watched_at for mediaID in the background, at most
// once per minute per media.
func (e *Engine) TouchWatched(mediaID string) {
	uid, err := toUUID(mediaID)
	if err != nil || !e.inUse.shouldTouch(mediaID, time.Now()) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.store.q.TouchMediaWatched(ctx, uid); err != nil {
			e.log.Warn("failed to record last watched", "media_id", mediaID, "error", err)
		}
	}()
}

// RunRetention deletes downloads unwatched for maxAge: once shortly after
// startup, then hourly, until ctx ends. maxAge <= 0 disables it.
func (e *Engine) RunRetention(ctx context.Context, maxAge time.Duration) {
	if maxAge <= 0 {
		e.log.Info("media retention disabled")
		return
	}
	timer := time.NewTimer(retentionFirstDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		e.inUse.prune(time.Now())
		e.cleanupExpired(ctx, time.Now().Add(-maxAge))
		timer.Reset(retentionInterval)
	}
}

func (e *Engine) cleanupExpired(ctx context.Context, cutoff time.Time) {
	ts := pgtype.Timestamptz{Time: cutoff, Valid: true}
	rows, err := e.store.q.ListExpiredMedia(ctx, ts)
	if err != nil {
		e.log.Error("media retention: list expired failed", "error", err)
		return
	}
	deleted := 0
	for _, r := range rows {
		if ctx.Err() != nil {
			return
		}
		if e.expire(ctx, r, ts) {
			deleted++
		}
	}
	if len(rows) > 0 {
		e.log.Info("media retention run done", "candidates", len(rows), "deleted", deleted, "cutoff", cutoff)
	}
}

// expire removes one expired media's files and resets its row; false if it
// was skipped (in use, watched again meanwhile, or an error).
func (e *Engine) expire(ctx context.Context, r repository.Medium, cutoff pgtype.Timestamptz) bool {
	m := mediaFromRow(r)
	if e.Job(m.ID) != nil || e.inUse.busy(m.ID) {
		e.log.Info("media retention: skipping media in use", "media_id", m.ID)
		return false
	}
	// Conditional on still being expired, so a play that just touched the
	// row wins.
	n, err := e.store.q.ResetExpiredMedia(ctx, repository.ResetExpiredMediaParams{MediaID: r.MediaID, Cutoff: cutoff})
	if err != nil {
		e.log.Error("media retention: reset row failed", "media_id", m.ID, "error", err)
		return false
	}
	if n == 0 {
		return false
	}

	ih, ok := infohashDir(m.LocalPath)
	if !ok {
		// Nothing on disk to remove (e.g. failed rows already cleaned).
		e.log.Info("media retention: reset media", "media_id", m.ID, "title", m.Title, "status", m.DownloadStatus)
		return true
	}
	// A seeding (or not yet dropped) torrent still holds the files open.
	var h metainfo.Hash
	if err := h.FromHexString(ih); err == nil {
		if t, ok := e.tc.cl.Torrent(h); ok && e.Job(m.ID) == nil {
			t.Drop()
		}
	}
	dir, ok := e.AbsPath(ih)
	if !ok {
		return true
	}
	// The per-infohash dir holds the torrent file and any cached .mp4.
	if err := os.RemoveAll(dir); err != nil {
		e.log.Warn("media retention: remove files failed", "media_id", m.ID, "dir", dir, "error", err)
		return true
	}
	e.log.Info("media retention: deleted unwatched media",
		"media_id", m.ID, "title", m.Title, "status", m.DownloadStatus,
		"dir", dir, "size", m.FileSize, "last_watched_at", r.LastWatchedAt.Time)
	return true
}

// infohashDir returns the first segment of a stored local_path
// ("<infohash>/<torrent path>") if it is a 40-char hex infohash.
func infohashDir(localPath string) (string, bool) {
	first, _, _ := strings.Cut(localPath, "/")
	if len(first) != 40 {
		return "", false
	}
	if _, err := hex.DecodeString(first); err != nil {
		return "", false
	}
	return first, true
}
