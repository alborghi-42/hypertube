package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/sync/singleflight"
)

// StreamPrefix is the URL prefix media are streamed under (see
// Handler.RegisterRoutes). Stored public_url values are relative to "/".
const StreamPrefix = "stream/media"

// TorrentSource is where a provider's item is downloaded from: either a
// parsed .torrent (archive.org's <id>_archive.torrent, a legittorrents
// .torrent) or a magnet URI. Exactly one field is set.
type TorrentSource struct {
	MetaInfo *metainfo.MetaInfo
	Magnet   string
}

// TorrentConfig configures the shared BitTorrent client.
type TorrentConfig struct {
	DataDir    string // torrent data root (DATA_DIR/downloads)
	ListenPort int    // TCP+UDP peer port; 0 picks a random port
	NoUpload   bool   // never upload to peers
	Seed       bool   // keep uploading after a file completes
	// StallTimeout fails a download that made no progress for this long.
	// There is deliberately no overall deadline: big films on few peers are
	// slow, but a dead swarm must not stay "downloading" forever.
	StallTimeout time.Duration
}

// TorrentClient is the single process-wide BitTorrent client. Create it in
// main and Close it on shutdown.
type TorrentClient struct {
	cl      *torrent.Client
	storage storage.ClientImplCloser
	cfg     TorrentConfig
}

func NewTorrentClient(cfg TorrentConfig, log *slog.Logger) (*TorrentClient, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create torrent data dir: %w", err)
	}
	// Each torrent lives in <DataDir>/<infohash>/<torrent path>, so torrents
	// with the same name never collide and a failed one is removed whole.
	// No part files: the completed file keeps its final name, and piece
	// completion is persisted (bolt db in DataDir) so restarts resume.
	st := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: cfg.DataDir,
		TorrentDirMaker: func(base string, _ *metainfo.Info, ih metainfo.Hash) string {
			return filepath.Join(base, ih.HexString())
		},
		UsePartFiles: g.Some(false),
		Logger:       log,
	})
	tc := torrent.NewDefaultClientConfig()
	tc.DataDir = cfg.DataDir
	tc.DefaultStorage = st
	tc.ListenPort = cfg.ListenPort
	tc.NoUpload = cfg.NoUpload
	tc.Seed = cfg.Seed
	tc.Slogger = log.With("component", "torrent")
	cl, err := torrent.NewClient(tc)
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("start torrent client: %w", err)
	}
	return &TorrentClient{cl: cl, storage: st, cfg: cfg}, nil
}

func (c *TorrentClient) Close() {
	c.cl.Close()
	_ = c.storage.Close()
}

// PostProcessFunc runs once a file is fully downloaded (absPath on disk).
// Hook for on-the-fly/after-the-fact conversion of non browser-native
// containers (mkv, avi; ticket 42-16). It may update m before it is persisted.
type PostProcessFunc func(ctx context.Context, m *Media, absPath string) error

const (
	metadataTimeout = 3 * time.Minute
	readyBytes      = 16 << 20 // contiguous head bytes before /play says "ready"
	sequentialAhead = 32       // pieces raised to high priority past the head
	pollInterval    = time.Second
)

// Engine runs torrent downloads for media: one job per media, shared by
// concurrent /play calls, streamed while downloading.
type Engine struct {
	tc          *TorrentClient
	store       *Store
	downloadDir string
	log         *slog.Logger
	PostProcess PostProcessFunc

	ctx    context.Context
	cancel context.CancelFunc
	sf     singleflight.Group
	mu     sync.Mutex
	jobs   map[string]*Job
	inUse  *watchTracker // active streams/conversions + last_watched_at throttle (42-18)
}

func NewEngine(tc *TorrentClient, store *Store, log *slog.Logger) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		tc: tc, store: store, downloadDir: tc.cfg.DataDir, log: log,
		ctx: ctx, cancel: cancel, jobs: map[string]*Job{}, inUse: newWatchTracker(),
	}
}

// Close stops the job watchers (call before TorrentClient.Close). Unfinished
// downloads keep status "downloading" and resume on the next /play.
func (e *Engine) Close() { e.cancel() }

// Job is an active download of one file of a torrent.
type Job struct {
	MediaID  string
	RelPath  string // relative to the download dir
	MimeType string
	Length   int64

	t    *torrent.Torrent
	file *torrent.File

	ready chan struct{} // closed once readyBytes of the head are verified
	done  chan struct{} // closed on completion or failure
	err   error         // set before done is closed

	mu        sync.Mutex
	readers   int
	completed bool
	dropped   bool
}

func (j *Job) Ready() <-chan struct{} { return j.ready }
func (j *Job) Done() <-chan struct{}  { return j.done }

// Err is the failure reason; valid once Done is closed.
func (j *Job) Err() error { return j.err }

// Progress is the completed percentage of the file.
func (j *Job) Progress() int {
	if j.Length == 0 {
		return 100
	}
	return int(j.file.BytesCompleted() * 100 / j.Length)
}

// acquire registers a stream reader; false if the torrent is already gone.
func (j *Job) acquire() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.dropped {
		return false
	}
	j.readers++
	return true
}

func (j *Job) release() {
	j.mu.Lock()
	j.readers--
	drop := j.completed && j.readers == 0 && !j.dropped
	if drop {
		j.dropped = true
	}
	j.mu.Unlock()
	if drop {
		j.t.Drop()
	}
}

// Job returns the active download for mediaID, if any.
func (e *Engine) Job(mediaID string) *Job {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.jobs[mediaID]
}

func (e *Engine) removeJob(j *Job) {
	e.mu.Lock()
	if e.jobs[j.MediaID] == j {
		delete(e.jobs, j.MediaID)
	}
	e.mu.Unlock()
}

// Start returns the running download for m, starting it if needed.
// Concurrent callers for the same media share a single start.
func (e *Engine) Start(p Provider, m *Media) (*Job, error) {
	if j := e.Job(m.ID); j != nil {
		return j, nil
	}
	v, err, _ := e.sf.Do(m.ID, func() (any, error) {
		if j := e.Job(m.ID); j != nil {
			return j, nil
		}
		return e.start(p, m)
	})
	if err != nil {
		return nil, err
	}
	return v.(*Job), nil
}

func (e *Engine) start(p Provider, m *Media) (*Job, error) {
	ctx, cancel := context.WithTimeout(e.ctx, metadataTimeout)
	defer cancel()

	src, err := p.TorrentSource(ctx, m.ExternalID)
	if err != nil {
		return nil, fmt.Errorf("torrent source: %w", err)
	}
	var t *torrent.Torrent
	switch {
	case src.MetaInfo != nil:
		t, err = e.tc.cl.AddTorrent(src.MetaInfo)
	case src.Magnet != "":
		t, err = e.tc.cl.AddMagnet(src.Magnet)
	default:
		err = errors.New("empty torrent source")
	}
	if err != nil {
		return nil, fmt.Errorf("add torrent: %w", err)
	}
	select {
	case <-t.GotInfo():
	case <-ctx.Done():
		t.Drop()
		return nil, fmt.Errorf("torrent metadata: %w", ctx.Err())
	}

	f := pickVideoFile(t.Files())
	if f == nil {
		t.Drop()
		return nil, errors.New("no playable video file in torrent")
	}
	for _, other := range t.Files() {
		if other != f {
			other.SetPriority(torrent.PiecePriorityNone)
		}
	}
	f.SetPriority(torrent.PiecePriorityNormal)

	rel := filepath.ToSlash(filepath.Join(t.InfoHash().HexString(), filepath.FromSlash(f.Path())))
	j := &Job{
		MediaID:  m.ID,
		RelPath:  rel,
		MimeType: mimeForExt(path.Ext(f.Path())),
		Length:   f.Length(),
		t:        t,
		file:     f,
		ready:    make(chan struct{}),
		done:     make(chan struct{}),
	}

	row := *m
	row.DownloadStatus = "downloading"
	row.LocalPath = rel
	row.PublicURL = path.Join(StreamPrefix, m.ID)
	row.FileSize = f.Length()
	row.MimeType = j.MimeType
	e.persist(&row)

	e.mu.Lock()
	e.jobs[m.ID] = j
	e.mu.Unlock()
	e.log.Info("torrent download started", "media_id", m.ID, "infohash", t.InfoHash().HexString(), "file", f.Path(), "size", f.Length())
	go e.watch(j, row)
	return j, nil
}

// watch drives sequential piece priority, readiness, completion and stall
// detection for j until it finishes or the engine closes.
func (e *Engine) watch(j *Job, row Media) {
	t, f := j.t, j.file
	begin, end := f.BeginPieceIndex(), f.EndPieceIndex()
	raised := begin // pieces [begin, raised) already set to high priority
	lastBytes, lastProgress := int64(-1), time.Now()
	ready := false
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-t.Closed():
			e.fail(j, row, errors.New("torrent closed"))
			return
		case <-tick.C:
		}

		// Sequential download: the head (first incomplete piece) and a
		// window after it get high priority; readers' own readahead (seeks)
		// still preempts this with "now"/"readahead" priority.
		head := begin
		for head < end && t.PieceState(head).Complete {
			head++
		}
		for ; raised < end && raised < head+sequentialAhead; raised++ {
			t.Piece(raised).SetPriority(torrent.PiecePriorityHigh)
		}

		if !ready {
			info := t.Info()
			headBytes := int64(head)*info.PieceLength - f.Offset() // file bytes before the head
			if head == end || headBytes >= min(int64(readyBytes), j.Length) {
				ready = true
				close(j.ready)
			}
		}

		if head == end {
			e.complete(j, row)
			return
		}

		if n := f.BytesCompleted(); n != lastBytes {
			lastBytes, lastProgress = n, time.Now()
		} else if st := e.tc.cfg.StallTimeout; st > 0 && time.Since(lastProgress) > st {
			e.fail(j, row, fmt.Errorf("no progress for %s", st))
			return
		}
	}
}

func (e *Engine) complete(j *Job, row Media) {
	abs := filepath.Join(e.downloadDir, filepath.FromSlash(j.RelPath))
	if e.PostProcess != nil {
		if err := e.PostProcess(e.ctx, &row, abs); err != nil {
			e.log.Warn("post-process failed", "media_id", j.MediaID, "error", err)
		}
	}
	row.DownloadStatus = "downloaded"
	e.persist(&row)
	e.removeJob(j)
	close(j.done)
	e.log.Info("torrent download completed", "media_id", j.MediaID, "path", abs)

	// New requests are now served from disk; drop the torrent once the last
	// in-flight stream ends, unless we keep seeding.
	if e.tc.cfg.Seed {
		return
	}
	j.mu.Lock()
	j.completed = true
	drop := j.readers == 0 && !j.dropped
	if drop {
		j.dropped = true
	}
	j.mu.Unlock()
	if drop {
		j.t.Drop()
	}
}

func (e *Engine) fail(j *Job, row Media, err error) {
	e.log.Error("torrent download failed", "media_id", j.MediaID, "error", err)
	j.mu.Lock()
	j.dropped = true
	j.mu.Unlock()
	j.t.Drop()
	// Remove the partial data: the whole per-infohash directory.
	dir := filepath.Join(e.downloadDir, j.t.InfoHash().HexString())
	if rmErr := os.RemoveAll(dir); rmErr != nil {
		e.log.Warn("failed to remove partial download", "dir", dir, "error", rmErr)
	}
	row.DownloadStatus = "failed"
	row.LocalPath = ""
	row.PublicURL = ""
	e.persist(&row)
	e.removeJob(j)
	j.err = err
	close(j.done)
}

func (e *Engine) persist(m *Media) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := e.store.UpdateMedia(ctx, m); err != nil {
		if _, err := e.store.CreateMedia(ctx, m); err != nil {
			e.log.Warn("failed to persist media download state", "media_id", m.ID, "status", m.DownloadStatus, "error", err)
		}
	}
}

// AbsPath resolves a stored local_path inside the download dir; false if it
// would escape it.
func (e *Engine) AbsPath(rel string) (string, bool) {
	p := filepath.FromSlash(rel)
	if rel == "" || !filepath.IsLocal(p) {
		return "", false
	}
	return filepath.Join(e.downloadDir, p), true
}

// Browser-native containers first; the others need conversion (42-16).
var (
	nativeVideoExt  = map[string]bool{".mp4": true, ".m4v": true, ".webm": true, ".ogv": true}
	convertVideoExt = map[string]bool{".mkv": true, ".avi": true, ".mov": true, ".mpg": true, ".mpeg": true, ".wmv": true, ".flv": true, ".ts": true}
)

// NeedsConversion reports whether a file must be converted before browsers
// can play it.
func NeedsConversion(name string) bool {
	return convertVideoExt[strings.ToLower(path.Ext(name))]
}

// pickVideoFile returns the largest browser-native video, else the largest
// convertible one.
func pickVideoFile(files []*torrent.File) *torrent.File {
	var native, other *torrent.File
	for _, f := range files {
		ext := strings.ToLower(path.Ext(f.Path()))
		switch {
		case nativeVideoExt[ext]:
			if native == nil || f.Length() > native.Length() {
				native = f
			}
		case convertVideoExt[ext]:
			if other == nil || f.Length() > other.Length() {
				other = f
			}
		}
	}
	if native != nil {
		return native
	}
	return other
}

func mimeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".ogv":
		return "video/ogg"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".mov":
		return "video/quicktime"
	case ".mpg", ".mpeg":
		return "video/mpeg"
	case ".ts":
		return "video/mp2t"
	default:
		return "application/octet-stream"
	}
}
