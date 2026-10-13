package media

import (
	"slices"
	"strings"
	"testing"
)

func argAfter(args []string, flag string) (string, bool) {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return "", false
	}
	return args[i+1], true
}

func TestFFmpegArgsScaled(t *testing.T) {
	args := ffmpegArgs("pipe:0", "pipe:1", 90, probeResult{Video: "h264", Audio: "aac", Height: 1080}, true, 720, 4)
	joined := strings.Join(args, " ")
	if v, _ := argAfter(args, "-vf"); v != "scale=-2:'min(720,ih)'" {
		t.Fatalf("scale filter = %q", v)
	}
	if v, _ := argAfter(args, "-c:v"); v != "libx264" {
		t.Fatalf("scaled stream must re-encode, got -c:v %q", v)
	}
	if v, _ := argAfter(args, "-c:a"); v != "copy" {
		t.Fatalf("aac audio should be copied, got %q", v)
	}
	if v, _ := argAfter(args, "-threads"); v != "4" {
		t.Fatalf("threads = %q", v)
	}
	if v, _ := argAfter(args, "-ss"); v != "90.000" {
		t.Fatalf("-ss = %q", v)
	}
	if v, _ := argAfter(args, "-output_ts_offset"); v != "90.000" {
		t.Fatalf("-output_ts_offset = %q", v)
	}
	if !strings.Contains(joined, "frag_keyframe+empty_moov+default_base_moof") {
		t.Fatalf("missing fragmented movflags: %s", joined)
	}
	if slices.Index(args, "-ss") > slices.Index(args, "-i") {
		t.Fatal("-ss must be an input option")
	}
}

func TestFFmpegArgsOriginal(t *testing.T) {
	args := ffmpegArgs("in.mkv", "pipe:1", 0, probeResult{Video: "h264", Audio: "ac3"}, true, 0, 4)
	if slices.Contains(args, "-vf") || slices.Contains(args, "-threads") {
		t.Fatalf("original must not scale: %v", args)
	}
	if slices.Contains(args, "-ss") || slices.Contains(args, "-output_ts_offset") {
		t.Fatalf("no offset expected at t=0: %v", args)
	}
	if v, _ := argAfter(args, "-c:v"); v != "copy" {
		t.Fatalf("h264 original should be copied, got %q", v)
	}
	if v, _ := argAfter(args, "-c:a"); v != "aac" {
		t.Fatalf("ac3 must be re-encoded to aac, got %q", v)
	}
	// The cached full conversion is not fragmented: no ts offset even with a start.
	conv := ffmpegArgs("in.mkv", "out.mp4", 10, probeResult{}, false, 0, 0)
	if slices.Contains(conv, "-output_ts_offset") {
		t.Fatalf("faststart output must not be offset: %v", conv)
	}
}

func TestQualities(t *testing.T) {
	cases := []struct {
		height int
		want   []string
	}{
		{0, []string{"original", "720p", "480p", "360p"}},
		{1080, []string{"original", "720p", "480p", "360p"}},
		{720, []string{"original", "480p", "360p"}},
		{480, []string{"original", "360p"}},
		{360, []string{"original"}},
	}
	for _, c := range cases {
		if got := qualitiesFor(c.height); !slices.Equal(got, c.want) {
			t.Errorf("qualitiesFor(%d) = %v, want %v", c.height, got, c.want)
		}
	}
	for q, want := range map[string]int{"": 0, "original": 0, "720p": 720, "480p": 480, "360p": 360} {
		if h, ok := QualityHeight(q); !ok || h != want {
			t.Errorf("QualityHeight(%q) = %d, %v", q, h, ok)
		}
	}
	for _, bad := range []string{"1080p", "720", "-vf", "original "} {
		if _, ok := QualityHeight(bad); ok {
			t.Errorf("QualityHeight(%q) accepted", bad)
		}
	}
}
