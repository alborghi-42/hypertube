package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// On-the-fly conversion of non browser-native containers (mkv, avi, ...;
// ticket 42-16). ffmpeg writes fragmented MP4 to stdout, which is copied to
// the HTTP response while it is produced. A pipe is not seekable, so these
// responses carry no Content-Length / Accept-Ranges; clients seek by
// reloading the stream with ?t=<seconds> (ffmpeg -ss).

const (
	defaultMaxTranscodes = 2
	probeHeadBytes       = 16 << 20 // head of a downloading file fed to ffprobe
	probeTimeout         = 30 * time.Second
	stderrKeep           = 4 << 10
	// Threads per downscaled encode (42-25), so a few viewers cannot take
	// every core of the shared box. Override with SCALE_THREADS.
	defaultScaleThreads = 4
)

// QualityOriginal is the untouched path of ?quality= (42-25): range
// streaming, or the plain transcode for mkv & co.
const QualityOriginal = "original"

// qualityHeights is the allow-list of downscaled renditions, highest first.
var qualityHeights = []struct {
	Name   string
	Height int
}{{"720p", 720}, {"480p", 480}, {"360p", 360}}

// QualityHeight returns the target height of a ?quality= value: 0 for
// original (or empty), false for anything not in the allow-list.
func QualityHeight(q string) (int, bool) {
	if q == "" || q == QualityOriginal {
		return 0, true
	}
	for _, v := range qualityHeights {
		if v.Name == q {
			return v.Height, true
		}
	}
	return 0, false
}

// qualitiesFor lists original plus the renditions strictly lower than the
// source height. With an unknown height (0) every rendition is offered:
// the scale filter never upscales, so a too-high pick just re-encodes at
// the source size.
func qualitiesFor(height int) []string {
	out := []string{QualityOriginal}
	for _, v := range qualityHeights {
		if height == 0 || v.Height < height {
			out = append(out, v.Name)
		}
	}
	return out
}

// Transcoder runs ffprobe/ffmpeg with a cap on concurrent live transcodes.
type Transcoder struct {
	ffmpeg  string
	ffprobe string
	slots   chan struct{} // live streams
	cache   chan struct{} // background full conversions (one at a time)
	threads int           // -threads for downscaled encodes
	heights sync.Map      // media id -> source video height (int), probed once
	probing sync.Map      // media id -> struct{}: background head probe running
	log     *slog.Logger
}

// NewTranscoderFromEnv reads FFMPEG_PATH, FFPROBE_PATH (default: from PATH)
// and MAX_TRANSCODES (default 2).
func NewTranscoderFromEnv(log *slog.Logger) *Transcoder {
	ffmpeg := envOr("FFMPEG_PATH", "ffmpeg")
	ffprobe := envOr("FFPROBE_PATH", "ffprobe")
	if p, err := exec.LookPath(ffmpeg); err == nil {
		ffmpeg = p
	} else {
		log.Warn("ffmpeg not found; conversion of mkv/avi streams will fail", "ffmpeg", ffmpeg, "error", err)
	}
	if p, err := exec.LookPath(ffprobe); err == nil {
		ffprobe = p
	}
	n := defaultMaxTranscodes
	if v, err := strconv.Atoi(os.Getenv("MAX_TRANSCODES")); err == nil && v > 0 {
		n = v
	}
	threads := defaultScaleThreads
	if v, err := strconv.Atoi(os.Getenv("SCALE_THREADS")); err == nil && v >= 0 {
		threads = v
	}
	return &Transcoder{
		ffmpeg: ffmpeg, ffprobe: ffprobe, log: log, threads: threads,
		slots: make(chan struct{}, n), cache: make(chan struct{}, 1),
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// probeResult holds the codecs of the first video and audio streams and
// the height of the video one (0 if unknown).
type probeResult struct {
	Video  string
	Audio  string
	Height int
}

// probe runs ffprobe on a file path, or on stdin fed from head when path is "".
// A failed probe is not fatal: callers fall back to a full re-encode.
func (t *Transcoder) probe(ctx context.Context, file string, head io.Reader) (probeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	in := file
	if in == "" {
		in = "pipe:0"
	}
	cmd := exec.CommandContext(ctx, t.ffprobe, "-v", "error",
		"-show_entries", "stream=codec_type,codec_name,height", "-of", "json", "-i", in)
	if file == "" {
		cmd.Stdin = head
	}
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	var res probeResult
	var parsed struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	// ffprobe on a truncated head may exit non-zero but still print streams.
	if jerr := json.Unmarshal(out, &parsed); jerr != nil {
		if err == nil {
			err = jerr
		}
		return res, err
	}
	for _, s := range parsed.Streams {
		switch {
		case s.CodecType == "video" && res.Video == "":
			res.Video = s.CodecName
			res.Height = s.Height
		case s.CodecType == "audio" && res.Audio == "":
			res.Audio = s.CodecName
		}
	}
	return res, nil
}

// ffmpegArgs builds the conversion command line. in is a path or "pipe:0";
// out is a path or "pipe:1". height > 0 downscales to that height (42-25,
// never upscaling) with threads encoder threads (0 = ffmpeg default).
//
// With a start offset the output timestamps are shifted forward by start
// (-output_ts_offset), so the fragmented stream carries the film's absolute
// timeline and subtitle cues line up; the player still checks what the
// browser exposes and shifts the cues itself if it rebased to 0.
func ffmpegArgs(in, out string, start float64, pr probeResult, fragmented bool, height, threads int) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	ss := ""
	if start > 0 {
		ss = strconv.FormatFloat(start, 'f', 3, 64)
		args = append(args, "-ss", ss)
	}
	args = append(args, "-i", in, "-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn")
	switch {
	case height > 0:
		args = append(args,
			"-vf", fmt.Sprintf("scale=-2:'min(%d,ih)'", height),
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p")
		if threads > 0 {
			args = append(args, "-threads", strconv.Itoa(threads))
		}
	case pr.Video == "h264":
		args = append(args, "-c:v", "copy")
	default:
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p")
	}
	switch pr.Audio {
	case "aac", "mp3":
		args = append(args, "-c:a", "copy")
	default:
		args = append(args, "-c:a", "aac", "-b:a", "160k", "-ac", "2")
	}
	if ss != "" && fragmented {
		args = append(args, "-output_ts_offset", ss)
	}
	if fragmented {
		args = append(args, "-movflags", "frag_keyframe+empty_moov+default_base_moof")
	} else {
		args = append(args, "-movflags", "+faststart")
	}
	return append(args, "-f", "mp4", "-y", out)
}

// Stream transcodes to fragmented MP4 on the response. Input is the file on
// disk (file != "") or src, a reader that blocks until torrent pieces arrive.
// openHead returns a fresh reader on the source head for probing (nil when
// file is set). ffmpeg is killed when the request context ends. height > 0
// downscales (42-25); mediaID keys the cached probe result.
func (t *Transcoder) Stream(c *gin.Context, mediaID string, height int, file string, src io.Reader, openHead func() io.ReadCloser) {
	start := 0.0
	if v := c.Query("t"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid t parameter"})
			return
		}
		start = f
	}

	h := c.Writer.Header()
	h.Set("Content-Type", "video/mp4")
	h.Set("Cache-Control", "no-store")
	h.Set("Accept-Ranges", "none")
	h.Set("X-Transcoded", "1")
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}

	select {
	case t.slots <- struct{}{}:
		defer func() { <-t.slots }()
	default:
		h.Del("Content-Type")
		c.Header("Retry-After", "5")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Too many conversions in progress, retry shortly"})
		return
	}

	ctx := c.Request.Context()
	var pr probeResult
	var err error
	if file != "" {
		pr, err = t.probe(ctx, file, nil)
	} else if openHead != nil {
		head := openHead()
		pr, err = t.probe(ctx, "", io.LimitReader(head, probeHeadBytes))
		head.Close()
	}
	if err != nil {
		t.log.Warn("ffprobe failed; re-encoding everything", "error", err)
	} else if pr.Height > 0 && mediaID != "" {
		t.heights.Store(mediaID, pr.Height)
	}

	in := file
	if in == "" {
		in = "pipe:0"
	}
	cmd := exec.CommandContext(ctx, t.ffmpeg, ffmpegArgs(in, "pipe:1", start, pr, true, height, t.threads)...)
	if file == "" {
		cmd.Stdin = src
	}
	cmd.Stdout = c.Writer
	stderr := &headBuffer{max: stderrKeep}
	cmd.Stderr = stderr
	cmd.WaitDelay = 5 * time.Second

	c.Status(http.StatusOK)
	began := time.Now()
	err = cmd.Run()
	switch {
	case ctx.Err() != nil:
		t.log.Debug("transcode stopped: client gone", "after", time.Since(began))
	case err != nil:
		t.log.Warn("transcode failed", "error", err, "stderr", strings.TrimSpace(stderr.String()))
	}
}

// Qualities returns the ?quality= values worth offering for a media item,
// from the cached source height. Unknown height: a downloaded file (file
// != "") is probed now (local disk, fast); a download in progress gets a
// background probe of its head (openHead) and this answer offers every
// rendition. Renditions are never pre-encoded nor cached on disk: each is
// produced live on request (CPU on a shared box).
func (t *Transcoder) Qualities(ctx context.Context, mediaID, file string, openHead func() io.ReadCloser) []string {
	if v, ok := t.heights.Load(mediaID); ok {
		return qualitiesFor(v.(int))
	}
	if file != "" {
		pr, err := t.probe(ctx, file, nil)
		if err == nil && pr.Height > 0 {
			t.heights.Store(mediaID, pr.Height)
		}
		return qualitiesFor(pr.Height)
	}
	if openHead != nil {
		if _, busy := t.probing.LoadOrStore(mediaID, struct{}{}); !busy {
			go func() {
				defer t.probing.Delete(mediaID)
				head := openHead()
				defer head.Close()
				pr, err := t.probe(context.Background(), "", io.LimitReader(head, probeHeadBytes))
				if err == nil && pr.Height > 0 {
					t.heights.Store(mediaID, pr.Height)
				}
			}()
		}
	}
	return qualitiesFor(0)
}

// Convert writes a complete, seekable (faststart) MP4 of src to dst,
// atomically: a temp file in dst's directory renamed on success.
func (t *Transcoder) Convert(ctx context.Context, src, dst string) error {
	t.cache <- struct{}{}
	defer func() { <-t.cache }()

	pr, err := t.probe(ctx, src, nil)
	if err != nil {
		t.log.Warn("ffprobe failed; re-encoding everything", "file", src, "error", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".transcode-*.mp4")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	tmp.Close()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpName)
		}
	}()

	cmd := exec.CommandContext(ctx, t.ffmpeg, ffmpegArgs(src, tmpName, 0, pr, false, 0, 0)...)
	stderr := &headBuffer{max: stderrKeep}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	ok = true
	return nil
}

// CachePostProcess returns an Engine.PostProcess hook that, for files that
// need conversion, starts a background full conversion to <name>.mp4 next
// to the original and then points the media row at it (local_path,
// mime_type, file_size), so later plays are plain range requests.
func (t *Transcoder) CachePostProcess(e *Engine) PostProcessFunc {
	return func(ctx context.Context, m *Media, absPath string) error {
		if !NeedsConversion(absPath) {
			return nil
		}
		id := m.ID
		dst := strings.TrimSuffix(absPath, filepath.Ext(absPath)) + ".mp4"
		go func() {
			defer e.inUse.begin(id)() // keeps the retention job off this media
			began := time.Now()
			if err := t.Convert(ctx, absPath, dst); err != nil {
				t.log.Warn("background mp4 conversion failed", "media_id", id, "error", err)
				return
			}
			rel, err := filepath.Rel(e.downloadDir, dst)
			if err != nil {
				return
			}
			st, err := os.Stat(dst)
			if err != nil {
				return
			}
			dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			row, err := e.store.GetMedia(dbCtx, id)
			if err != nil || row.DownloadStatus != "downloaded" {
				return
			}
			row.LocalPath = filepath.ToSlash(rel)
			row.MimeType = mimeForExt(path.Ext(dst))
			row.FileSize = st.Size()
			if _, err := e.store.UpdateMedia(dbCtx, row); err != nil {
				t.log.Warn("failed to record converted mp4", "media_id", id, "error", err)
				return
			}
			t.log.Info("background mp4 conversion done", "media_id", id, "path", dst, "took", time.Since(began))
		}()
		return nil
	}
}

// cancelReadCloser releases a context together with its reader.
type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r cancelReadCloser) Close() error {
	r.cancel()
	return r.ReadCloser.Close()
}

// headBuffer keeps the first max bytes written (ffmpeg error output).
type headBuffer struct {
	bytes.Buffer
	max int
}

func (b *headBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}
